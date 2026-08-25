// Package resellers implements the multi-tenant commercial layer.
//
// It sits entirely above subjects: a reseller provisioning a user is an
// ordinary subject creation that happens to be paid for and owned. Nothing
// here touches node-side code, gRPC, or desired-state reconciliation, so the
// commercial layer cannot destabilise the control plane. Publishing the
// resulting document remains the caller's job through CommitNodeChange, which
// is still the only path allowed to move a node revision.
//
// The organising decision is that credit is an append-only LEDGER. Balance is
// always SUM(delta) and is never stored, because a cached balance cannot
// explain itself, cannot be audited, and corrupts permanently under a lost
// update.
package resellers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/panel/subjects"
)

// Reason values for a ledger movement.
const (
	ReasonTopup      = "topup"
	ReasonProvision  = "provision"
	ReasonRenew      = "renew"
	ReasonRefund     = "refund"
	ReasonAdjustment = "adjustment"
)

// Sentinel errors. Callers map these to HTTP status codes; the handler must
// not re-derive them by matching on strings.
var (
	// ErrInsufficientCredit means the debit would take the reseller below
	// their credit floor. It is a business outcome, not a failure.
	ErrInsufficientCredit = errors.New("insufficient credit")
	// ErrLimitExceeded means a hard ceiling (subject count, quota) would be
	// breached. Distinct from credit: topping up does not fix it.
	ErrLimitExceeded = errors.New("reseller limit exceeded")
	// ErrDisabled means the reseller exists but may not transact.
	ErrDisabled = errors.New("reseller is disabled")
	// ErrNotFound means no such reseller, or none visible to this caller.
	ErrNotFound = errors.New("reseller not found")
)

// Store owns reseller persistence.
type Store struct {
	db   *store.Store
	subj subjectCreator
	now  func() time.Time
}

// subjectCreator is the slice of subjects.Store this package needs. Depending
// on an interface rather than the concrete store keeps the commercial layer
// testable without a secret box, and documents exactly how far into subject
// management this package reaches.
type subjectCreator interface {
	Create(ctx context.Context, tx *sql.Tx, in subjects.CreateInput) (int64, error)
	NodeIDsFor(ctx context.Context, tx *sql.Tx, subjectID int64) ([]int64, error)
}

// NewStore returns a reseller store. subj is the subject store the commercial
// layer provisions through; it must be the real one in production so
// credentials are sealed by the same path as every other subject.
func NewStore(db *store.Store, subj subjectCreator, now func() time.Time) *Store {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Store{db: db, subj: subj, now: now}
}

// Reseller is a commercial tenant.
type Reseller struct {
	ID            int64
	AdminID       int64
	DisplayName   string
	Enabled       bool
	MaxSubjects   *int64
	MaxQuotaBytes *int64
	CreditFloor   int64
	CreatedAt     int64
	UpdatedAt     int64
}

// Balance returns the reseller's current credit as the sum of their ledger.
//
// It is deliberately computed rather than read from a column. A stored balance
// and a ledger that disagree is a class of bug with no safe resolution: you
// cannot tell which one is wrong. Summing on read makes the ledger the single
// source of truth by construction, and the (reseller_id, id) index keeps it a
// covered scan.
//
// Runs inside the caller's transaction so a debit can read the balance and
// write the movement atomically. A balance read outside a transaction is
// advisory only, and must never gate a charge.
func (s *Store) Balance(ctx context.Context, tx *sql.Tx, resellerID int64) (int64, error) {
	var balance sql.NullInt64
	err := tx.QueryRowContext(ctx,
		`SELECT sum(delta) FROM reseller_credit_ledger WHERE reseller_id = ?`,
		resellerID).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("read balance for reseller %d: %w", resellerID, err)
	}
	// No rows means no movements, which is a balance of zero rather than an
	// error: a reseller created a moment ago has an empty ledger.
	if !balance.Valid {
		return 0, nil
	}
	return balance.Int64, nil
}

// BalanceRead is Balance for display purposes, outside any transaction.
//
// Named differently on purpose. The value is stale the instant it is returned,
// so it must not be used to decide whether a charge is affordable — that
// decision belongs inside the same transaction as the debit.
func (s *Store) BalanceRead(ctx context.Context, resellerID int64) (int64, error) {
	var balance sql.NullInt64
	err := s.db.Read().QueryRowContext(ctx,
		`SELECT sum(delta) FROM reseller_credit_ledger WHERE reseller_id = ?`,
		resellerID).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("read balance for reseller %d: %w", resellerID, err)
	}
	if !balance.Valid {
		return 0, nil
	}
	return balance.Int64, nil
}

// Get loads a reseller.
func (s *Store) Get(ctx context.Context, tx *sql.Tx, id int64) (Reseller, error) {
	var r Reseller
	var maxSubjects, maxQuota sql.NullInt64
	var enabled int
	err := tx.QueryRowContext(ctx,
		`SELECT id, admin_id, display_name, enabled, max_subjects, max_quota_bytes,
		        credit_floor, created_at, updated_at
		   FROM resellers WHERE id = ?`, id).
		Scan(&r.ID, &r.AdminID, &r.DisplayName, &enabled, &maxSubjects, &maxQuota,
			&r.CreditFloor, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Reseller{}, ErrNotFound
	}
	if err != nil {
		return Reseller{}, fmt.Errorf("read reseller %d: %w", id, err)
	}
	r.Enabled = enabled == 1
	if maxSubjects.Valid {
		r.MaxSubjects = &maxSubjects.Int64
	}
	if maxQuota.Valid {
		r.MaxQuotaBytes = &maxQuota.Int64
	}
	return r, nil
}

// CreditInput describes one ledger movement.
type CreditInput struct {
	ResellerID int64
	// Delta is signed and must not be zero. The schema enforces that too;
	// a zero movement is always a caller bug rather than a real event.
	Delta  int64
	Reason string
	Note   string
	// SubjectID links the movement to what it paid for, when applicable.
	SubjectID *int64
	// ActorAdminID is who caused it.
	ActorAdminID *int64
	// IdempotencyKey makes a retry safe. Required: a credit movement with no
	// key cannot be retried safely, so the API must not allow one.
	IdempotencyKey string
}

// Credit appends one movement to the ledger.
//
// Idempotent by (reseller_id, idempotency_key). A repeat returns the existing
// movement's id and applies nothing, so a client that retries after an
// ambiguous network failure cannot double-credit.
func (s *Store) Credit(ctx context.Context, tx *sql.Tx, in CreditInput) (int64, error) {
	if in.Delta == 0 {
		return 0, errors.New("a credit movement of zero is meaningless")
	}
	if strings.TrimSpace(in.IdempotencyKey) == "" {
		return 0, errors.New("idempotency key is required; without one a retry double-credits")
	}
	switch in.Reason {
	case ReasonTopup, ReasonProvision, ReasonRenew, ReasonRefund, ReasonAdjustment:
	default:
		return 0, fmt.Errorf("unknown ledger reason %q", in.Reason)
	}

	// Existing movement wins. Checked before inserting rather than relying on
	// the constraint violation, so the caller gets the original id back.
	var existing int64
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM reseller_credit_ledger
		  WHERE reseller_id = ? AND idempotency_key = ?`,
		in.ResellerID, in.IdempotencyKey).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("check idempotency: %w", err)
	}

	var subjectID, actorID any
	if in.SubjectID != nil {
		subjectID = *in.SubjectID
	}
	if in.ActorAdminID != nil {
		actorID = *in.ActorAdminID
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO reseller_credit_ledger
		   (reseller_id, delta, reason, subject_id, actor_admin_id, note,
		    idempotency_key, at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		in.ResellerID, in.Delta, in.Reason, subjectID, actorID,
		in.Note, in.IdempotencyKey, s.now().UTC().Unix())
	if err != nil {
		return 0, fmt.Errorf("append ledger movement: %w", err)
	}
	return res.LastInsertId()
}

// ProvisionInput describes a reseller creating a customer.
type ProvisionInput struct {
	ResellerID int64
	// Cost is what the reseller is charged. Non-negative; the debit is
	// applied as -Cost.
	Cost int64
	// Subject is the customer to create, passed through to subjects.Create so
	// credentials are sealed by the same path as any other subject.
	Subject subjects.CreateInput
	// QuotaBytes counts against the reseller's max_quota_bytes ceiling.
	QuotaBytes int64
	// ActorAdminID is the acting admin, recorded in both ledger and audit.
	ActorAdminID *int64
	// IdempotencyKey makes the whole provision retry-safe.
	IdempotencyKey string
	// RequestID threads through to the audit record.
	RequestID string
	// Actor is the audit actor.
	Actor audit.Actor
}

// ProvisionResult reports what a provision did.
type ProvisionResult struct {
	SubjectID int64
	LedgerID  int64
	// Balance after the debit.
	Balance int64
	// NodeIDs the caller must republish through CommitNodeChange. This package
	// deliberately does not publish: only CommitNodeChange may move a node
	// revision, and calling it from inside this transaction would nest writes.
	NodeIDs []int64
}

// ProvisionSubject debits the reseller and creates the customer in ONE
// transaction.
//
// This atomicity is the whole point of the method. The two failure modes it
// exists to prevent are a customer who exists but was never paid for, and a
// reseller who was charged for a customer who does not exist. Both are
// unrecoverable without manual reconciliation, and both are trivially
// reachable if the debit and the create are separate transactions.
//
// The caller MUST republish result.NodeIDs through CommitNodeChange, or the
// customer will exist in the database and on no node.
func (s *Store) ProvisionSubject(
	ctx context.Context, tx *sql.Tx, in ProvisionInput,
) (ProvisionResult, error) {
	if s.subj == nil {
		return ProvisionResult{}, errors.New("no subject store configured")
	}
	if in.Cost < 0 {
		return ProvisionResult{}, errors.New("cost must not be negative; use a refund movement")
	}
	if strings.TrimSpace(in.IdempotencyKey) == "" {
		return ProvisionResult{}, errors.New(
			"idempotency key is required; without one a retry both double-charges and duplicates the customer")
	}

	r, err := s.Get(ctx, tx, in.ResellerID)
	if err != nil {
		return ProvisionResult{}, err
	}
	if !r.Enabled {
		return ProvisionResult{}, ErrDisabled
	}

	// A repeat of an already-applied provision must return the original
	// outcome rather than charging again. Checked before any limit or credit
	// test, because a retry of a successful call must succeed even if the
	// reseller has since run out of credit or hit a ceiling.
	var priorLedger int64
	var priorSubject sql.NullInt64
	err = tx.QueryRowContext(ctx,
		`SELECT id, subject_id FROM reseller_credit_ledger
		  WHERE reseller_id = ? AND idempotency_key = ?`,
		in.ResellerID, in.IdempotencyKey).Scan(&priorLedger, &priorSubject)
	if err == nil {
		balance, berr := s.Balance(ctx, tx, in.ResellerID)
		if berr != nil {
			return ProvisionResult{}, berr
		}
		out := ProvisionResult{LedgerID: priorLedger, Balance: balance}
		if priorSubject.Valid {
			out.SubjectID = priorSubject.Int64
			if ids, ierr := s.subj.NodeIDsFor(ctx, tx, out.SubjectID); ierr == nil {
				out.NodeIDs = ids
			}
		}
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ProvisionResult{}, fmt.Errorf("check provision idempotency: %w", err)
	}

	// Hard ceilings first. These are not fixable by topping up, so failing on
	// them before the credit check gives the caller the more useful error.
	if err := s.checkLimits(ctx, tx, r, in.QuotaBytes); err != nil {
		return ProvisionResult{}, err
	}

	// Credit check inside the same transaction as the debit. Reading the
	// balance in an earlier transaction and charging in a later one is the
	// classic oversell: two concurrent provisions both see enough credit.
	balance, err := s.Balance(ctx, tx, in.ResellerID)
	if err != nil {
		return ProvisionResult{}, err
	}
	if balance-in.Cost < r.CreditFloor {
		return ProvisionResult{}, fmt.Errorf(
			"%w: balance %d, cost %d, floor %d",
			ErrInsufficientCredit, balance, in.Cost, r.CreditFloor)
	}

	// Create the customer through the ordinary subject path, so credentials
	// are sealed and services granted exactly as for a platform-owned user.
	subjectID, err := s.subj.Create(ctx, tx, in.Subject)
	if err != nil {
		return ProvisionResult{}, fmt.Errorf("create subject: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO reseller_subjects (subject_id, reseller_id, cost, created_at)
		 VALUES (?,?,?,?)`,
		subjectID, in.ResellerID, in.Cost, s.now().UTC().Unix()); err != nil {
		return ProvisionResult{}, fmt.Errorf("record ownership: %w", err)
	}

	// Record the quota that checkLimits just measured this request against.
	//
	// Without this the ceiling is decorative: checkLimits sums
	// subjects.quota_bytes across the reseller's customers, and if provisioning
	// never writes it that sum stays zero forever. Every request would then be
	// checked against an empty allocation and max_quota_bytes could never trip,
	// however many customers a tenant provisioned. The value was already
	// recorded in the audit entry, which is what made the gap easy to miss --
	// the number appeared in the record of the decision without ever reaching
	// the state the decision is made from.
	if in.QuotaBytes > 0 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE subjects SET quota_bytes = ? WHERE id = ?`,
			in.QuotaBytes, subjectID); err != nil {
			return ProvisionResult{}, fmt.Errorf("record allocated quota: %w", err)
		}
	}

	ledgerID := int64(0)
	if in.Cost > 0 {
		ledgerID, err = s.Credit(ctx, tx, CreditInput{
			ResellerID:     in.ResellerID,
			Delta:          -in.Cost,
			Reason:         ReasonProvision,
			SubjectID:      &subjectID,
			ActorAdminID:   in.ActorAdminID,
			IdempotencyKey: in.IdempotencyKey,
		})
		if err != nil {
			return ProvisionResult{}, err
		}
	}

	// audit.InTx, never BestEffort: this call already holds the store's single
	// write connection, and BestEffort would block on it until its own timeout
	// and drop the record.
	if err := audit.InTx(ctx, tx, in.RequestID, in.Actor, audit.Record{
		Action:     "reseller.provision",
		TargetType: "subject",
		TargetID:   sql.NullInt64{Int64: subjectID, Valid: true},
		// Cost and reseller only. Never the credentials the subject was
		// created with.
		After: map[string]any{
			"reseller_id": in.ResellerID,
			"cost":        in.Cost,
			"quota_bytes": in.QuotaBytes,
		},
		Result: "ok",
	}); err != nil {
		return ProvisionResult{}, err
	}

	nodeIDs, err := s.subj.NodeIDsFor(ctx, tx, subjectID)
	if err != nil {
		return ProvisionResult{}, fmt.Errorf("resolve affected nodes: %w", err)
	}

	return ProvisionResult{
		SubjectID: subjectID,
		LedgerID:  ledgerID,
		Balance:   balance - in.Cost,
		NodeIDs:   nodeIDs,
	}, nil
}

// checkLimits enforces the hard ceilings that credit does not cover.
func (s *Store) checkLimits(
	ctx context.Context, tx *sql.Tx, r Reseller, addedQuota int64,
) error {
	if r.MaxSubjects != nil {
		var count int64
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM reseller_subjects WHERE reseller_id = ?`,
			r.ID).Scan(&count); err != nil {
			return fmt.Errorf("count subjects: %w", err)
		}
		if count+1 > *r.MaxSubjects {
			return fmt.Errorf("%w: %d of %d subjects already provisioned",
				ErrLimitExceeded, count, *r.MaxSubjects)
		}
	}

	if r.MaxQuotaBytes != nil && addedQuota > 0 {
		// Sum the quota already committed to this reseller's customers. The
		// ceiling is on total allocation, not on any single customer, because
		// the resource being protected is the node's capacity.
		var allocated sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			`SELECT sum(s.quota_bytes)
			   FROM reseller_subjects rs
			   JOIN subjects s ON s.id = rs.subject_id
			  WHERE rs.reseller_id = ? AND s.quota_bytes IS NOT NULL`,
			r.ID).Scan(&allocated); err != nil {
			return fmt.Errorf("sum allocated quota: %w", err)
		}
		if allocated.Int64+addedQuota > *r.MaxQuotaBytes {
			return fmt.Errorf("%w: %d of %d bytes already allocated, %d requested",
				ErrLimitExceeded, allocated.Int64, *r.MaxQuotaBytes, addedQuota)
		}
	}
	return nil
}

// OwnerOf returns the reseller owning a subject, or ErrNotFound when the
// subject is platform-owned.
//
// This is the read behind the scope predicate: a reseller may only see and
// mutate subjects this returns them for.
func (s *Store) OwnerOf(ctx context.Context, tx *sql.Tx, subjectID int64) (int64, error) {
	var resellerID int64
	err := tx.QueryRowContext(ctx,
		`SELECT reseller_id FROM reseller_subjects WHERE subject_id = ?`,
		subjectID).Scan(&resellerID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("read owner of subject %d: %w", subjectID, err)
	}
	return resellerID, nil
}

// CreateInput describes a new tenant.
type CreateInput struct {
	// AdminID is the panel user who operates this tenant. UNIQUE in the
	// schema: one admin is one reseller, which is what makes "my reseller"
	// resolvable from a session without a second identifier.
	AdminID     int64
	DisplayName string
	// Limits. Nil means unlimited, which is why they are pointers rather than
	// zero values -- zero is a real limit meaning "may create nothing".
	MaxSubjects   *int64
	MaxQuotaBytes *int64
	// CreditFloor is how far below zero the balance may go. Usually zero;
	// a negative floor extends credit on trust.
	CreditFloor int64
}

// Create inserts a reseller.
//
// Deliberately does NOT grant opening credit. A balance is the sum of its
// ledger, so opening credit is a ledger movement like any other and must carry
// its own reason, actor and idempotency key. Folding it in here would create
// value with no audit trail behind it.
func (s *Store) Create(ctx context.Context, tx *sql.Tx, in CreateInput) (int64, error) {
	name := strings.TrimSpace(in.DisplayName)
	if name == "" {
		return 0, errors.New("display name is required")
	}
	if in.AdminID <= 0 {
		return 0, errors.New("admin id is required; a tenant is operated by a panel user")
	}
	now := s.now().UTC().Unix()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO resellers
		   (admin_id, display_name, enabled, max_subjects, max_quota_bytes,
		    credit_floor, created_at, updated_at)
		 VALUES (?,?,1,?,?,?,?,?)`,
		in.AdminID, name, in.MaxSubjects, in.MaxQuotaBytes, in.CreditFloor, now, now)
	if err != nil {
		return 0, fmt.Errorf("insert reseller: %w", err)
	}
	return res.LastInsertId()
}

// UpdateInput is a partial change. Nil fields are left alone.
//
// MaxSubjects and MaxQuotaBytes are double pointers so "set to unlimited" is
// expressible: the outer nil means "do not touch", the inner nil means "no
// limit". A single pointer could not tell those apart.
type UpdateInput struct {
	DisplayName   *string
	Enabled       *bool
	MaxSubjects   **int64
	MaxQuotaBytes **int64
	CreditFloor   *int64
}

// Update applies a partial change to a reseller.
func (s *Store) Update(ctx context.Context, tx *sql.Tx, id int64, in UpdateInput) error {
	var exists int
	if err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM resellers WHERE id = ?`, id).Scan(&exists); err != nil {
		return err // sql.ErrNoRows reaches the handler as a 404
	}

	if in.DisplayName != nil {
		name := strings.TrimSpace(*in.DisplayName)
		if name == "" {
			return errors.New("display name must not be empty")
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE resellers SET display_name = ? WHERE id = ?`, name, id); err != nil {
			return fmt.Errorf("update display name: %w", err)
		}
	}
	if in.Enabled != nil {
		// Disabling stops provisioning -- ProvisionSubject checks it -- but
		// does not touch existing customers. Cutting them off is a separate
		// decision an operator makes deliberately.
		if _, err := tx.ExecContext(ctx,
			`UPDATE resellers SET enabled = ? WHERE id = ?`, boolToInt(*in.Enabled), id); err != nil {
			return fmt.Errorf("update enabled: %w", err)
		}
	}
	if in.MaxSubjects != nil {
		if _, err := tx.ExecContext(ctx,
			`UPDATE resellers SET max_subjects = ? WHERE id = ?`, *in.MaxSubjects, id); err != nil {
			return fmt.Errorf("update max subjects: %w", err)
		}
	}
	if in.MaxQuotaBytes != nil {
		if _, err := tx.ExecContext(ctx,
			`UPDATE resellers SET max_quota_bytes = ? WHERE id = ?`, *in.MaxQuotaBytes, id); err != nil {
			return fmt.Errorf("update max quota: %w", err)
		}
	}
	if in.CreditFloor != nil {
		if _, err := tx.ExecContext(ctx,
			`UPDATE resellers SET credit_floor = ? WHERE id = ?`, *in.CreditFloor, id); err != nil {
			return fmt.Errorf("update credit floor: %w", err)
		}
	}

	_, err := tx.ExecContext(ctx,
		`UPDATE resellers SET updated_at = ? WHERE id = ?`, s.now().UTC().Unix(), id)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ErrHasCustomers means the reseller still owns subjects and so may not be
// deleted.
//
// The schema enforces this: reseller_subjects.reseller_id is ON DELETE
// RESTRICT, deliberately, because cascading would silently delete a tenant's
// live customers along with the tenant. This is the typed form of that refusal
// so a caller can report it as a conflict rather than a constraint failure the
// operator has to decode.
var ErrHasCustomers = errors.New("reseller still owns customers")

// Delete removes a reseller.
//
// Refused while they own customers. Deactivating (enabled = false) is the
// reversible option and is what an operator usually wants: it stops
// provisioning immediately without touching anybody already connected.
//
// The credit ledger cascades. That is correct rather than a loss: a ledger
// belongs to a tenant that no longer exists, and the audit log retains the
// movements independently.
func (s *Store) Delete(ctx context.Context, tx *sql.Tx, id int64) error {
	var owned int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM reseller_subjects WHERE reseller_id = ?`, id).Scan(&owned); err != nil {
		return fmt.Errorf("count owned subjects: %w", err)
	}
	if owned > 0 {
		// Checked before the DELETE so the message can say how many, which the
		// bare foreign-key error cannot.
		return fmt.Errorf("%w: %d still provisioned", ErrHasCustomers, owned)
	}

	res, err := tx.ExecContext(ctx, `DELETE FROM resellers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete reseller: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

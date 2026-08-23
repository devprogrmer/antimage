package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/amyrm/antimage/internal/panel/rbac"
)

// SubjectRow is the subject shape the reseller-scoped queries return.
type SubjectRow struct {
	ID             int64
	Name           string
	Enabled        bool
	ExpiresAt      sql.NullInt64
	QuotaBytes     sql.NullInt64
	QuotaUsedBytes int64
	FrozenAt       sql.NullInt64
	// ResellerID is 0 for a platform-owned subject.
	ResellerID sql.NullInt64
}

// ResellerRow is the tenant shape the scoped queries return. Balance is
// deliberately absent: it is derived from the ledger, and a row struct that
// carried it would invite callers to treat a snapshot as authoritative.
type ResellerRow struct {
	ID            int64
	AdminID       int64
	DisplayName   string
	Enabled       bool
	MaxSubjects   sql.NullInt64
	MaxQuotaBytes sql.NullInt64
	CreditFloor   int64
	CreatedAt     int64
	UpdatedAt     int64
}

// subjectScopePredicate is the second enforcement layer for the reseller
// engine, mirroring scopePredicate in nodes_query.go.
//
// It is a static SQL fragment, never built at runtime. The caller supplies
// is_super and admin_id as bound parameters, so there is no path by which a
// caller can widen the filter -- not a handler bug, not a crafted query
// string, not a forgotten WHERE clause. That is the entire point: the first
// layer (rbac.Check) decides whether an operation is permitted at all, and
// this layer decides which rows exist as far as this caller is concerned.
//
// The rule it encodes:
//
//	a super admin sees everything;
//	anyone else sees only subjects owned by the reseller whose admin_id is
//	theirs.
//
// A platform-owned subject -- one with no reseller_subjects row -- is
// therefore invisible to every non-super caller. That is deliberate and is
// the fail-closed direction: a subject with no recorded owner must not
// default to being everybody's.
const subjectScopePredicate = `
  (? = 1 OR subjects.id IN (
      SELECT rs.subject_id
        FROM reseller_subjects rs
        JOIN resellers r ON r.id = rs.reseller_id
       WHERE r.admin_id = ?))`

// resellerScopePredicate restricts the tenant records themselves.
//
// A non-super caller can only ever match their own reseller row. This is what
// backs the /me route without granting reseller:read, and it means that even
// if a handler forgot its permission check, a reseller could still only load
// themselves.
const resellerScopePredicate = `
  (? = 1 OR resellers.admin_id = ?)`

// ListSubjectsScoped returns the subjects visible to this caller.
//
// There is no unscoped variant of this function on purpose. A caller that
// needs every subject passes a super scope explicitly, which is greppable in
// review; an unscoped helper sitting next to a scoped one is an accident
// waiting to be committed.
func (s *Store) ListSubjectsScoped(ctx context.Context, sc rbac.Scope) ([]SubjectRow, error) {
	rows, err := s.Read().QueryContext(ctx,
		`SELECT subjects.id, subjects.name, subjects.enabled, subjects.expires_at,
		        subjects.quota_bytes, subjects.quota_used_bytes, subjects.frozen_at,
		        (SELECT reseller_id FROM reseller_subjects
		          WHERE subject_id = subjects.id)
		   FROM subjects
		  WHERE `+subjectScopePredicate+`
		  ORDER BY subjects.name`,
		boolToInt(sc.IsSuper), sc.AdminID)
	if err != nil {
		return nil, fmt.Errorf("list subjects: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []SubjectRow
	for rows.Next() {
		var r SubjectRow
		var enabled int
		if err := rows.Scan(&r.ID, &r.Name, &enabled, &r.ExpiresAt,
			&r.QuotaBytes, &r.QuotaUsedBytes, &r.FrozenAt, &r.ResellerID); err != nil {
			return nil, fmt.Errorf("scan subject: %w", err)
		}
		r.Enabled = enabled == 1
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate subjects: %w", err)
	}
	return out, nil
}

// GetSubjectScoped returns sql.ErrNoRows for both a missing subject and one
// belonging to another reseller.
//
// The indistinguishability is the security property, not an accident of
// implementation. If out-of-scope returned a distinct error -- 403 where
// missing returns 404 -- a reseller could walk the id space and learn exactly
// how many customers their competitors have, and which ids are real. Every
// probe must look identical to a probe for something that does not exist.
func (s *Store) GetSubjectScoped(
	ctx context.Context, sc rbac.Scope, id int64,
) (*SubjectRow, error) {
	var r SubjectRow
	var enabled int
	err := s.Read().QueryRowContext(ctx,
		`SELECT subjects.id, subjects.name, subjects.enabled, subjects.expires_at,
		        subjects.quota_bytes, subjects.quota_used_bytes, subjects.frozen_at,
		        (SELECT reseller_id FROM reseller_subjects
		          WHERE subject_id = subjects.id)
		   FROM subjects
		  WHERE subjects.id = ? AND `+subjectScopePredicate,
		id, boolToInt(sc.IsSuper), sc.AdminID,
	).Scan(&r.ID, &r.Name, &enabled, &r.ExpiresAt,
		&r.QuotaBytes, &r.QuotaUsedBytes, &r.FrozenAt, &r.ResellerID)
	if err != nil {
		return nil, err // sql.ErrNoRows passes through unwrapped by design
	}
	r.Enabled = enabled == 1
	return &r, nil
}

// SubjectInScope reports whether this caller may act on a subject.
//
// Mutations call this before writing. It exists because a write path cannot
// simply append the predicate to an UPDATE and check RowsAffected: zero rows
// affected is ambiguous between "not yours" and "already in that state", and
// resolving that ambiguity after the fact is how tenant checks get skipped.
func (s *Store) SubjectInScope(ctx context.Context, sc rbac.Scope, id int64) (bool, error) {
	var one int
	err := s.Read().QueryRowContext(ctx,
		`SELECT 1 FROM subjects WHERE subjects.id = ? AND `+subjectScopePredicate,
		id, boolToInt(sc.IsSuper), sc.AdminID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check subject scope: %w", err)
	}
	return true, nil
}

// ListResellersScoped returns the tenant records visible to this caller.
func (s *Store) ListResellersScoped(ctx context.Context, sc rbac.Scope) ([]ResellerRow, error) {
	rows, err := s.Read().QueryContext(ctx,
		`SELECT resellers.id, resellers.admin_id, resellers.display_name,
		        resellers.enabled, resellers.max_subjects, resellers.max_quota_bytes,
		        resellers.credit_floor, resellers.created_at, resellers.updated_at
		   FROM resellers
		  WHERE `+resellerScopePredicate+`
		  ORDER BY resellers.display_name`,
		boolToInt(sc.IsSuper), sc.AdminID)
	if err != nil {
		return nil, fmt.Errorf("list resellers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ResellerRow
	for rows.Next() {
		var r ResellerRow
		var enabled int
		if err := rows.Scan(&r.ID, &r.AdminID, &r.DisplayName, &enabled,
			&r.MaxSubjects, &r.MaxQuotaBytes, &r.CreditFloor,
			&r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan reseller: %w", err)
		}
		r.Enabled = enabled == 1
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate resellers: %w", err)
	}
	return out, nil
}

// GetResellerScoped returns sql.ErrNoRows for a missing reseller and for one
// belonging to somebody else, for the same reason as GetSubjectScoped.
func (s *Store) GetResellerScoped(
	ctx context.Context, sc rbac.Scope, id int64,
) (*ResellerRow, error) {
	var r ResellerRow
	var enabled int
	err := s.Read().QueryRowContext(ctx,
		`SELECT resellers.id, resellers.admin_id, resellers.display_name,
		        resellers.enabled, resellers.max_subjects, resellers.max_quota_bytes,
		        resellers.credit_floor, resellers.created_at, resellers.updated_at
		   FROM resellers
		  WHERE resellers.id = ? AND `+resellerScopePredicate,
		id, boolToInt(sc.IsSuper), sc.AdminID,
	).Scan(&r.ID, &r.AdminID, &r.DisplayName, &enabled,
		&r.MaxSubjects, &r.MaxQuotaBytes, &r.CreditFloor, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	r.Enabled = enabled == 1
	return &r, nil
}

// ListLedgerScoped returns credit movements for a reseller, scoped.
//
// The scope is applied to the RESELLER, not to the ledger rows: a movement is
// visible exactly when its reseller is. Filtering the movements directly would
// mean a caller who guessed a ledger id could read one row of somebody else's
// billing history.
func (s *Store) ListLedgerScoped(
	ctx context.Context, sc rbac.Scope, resellerID int64, limit int,
) ([]LedgerRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.Read().QueryContext(ctx,
		`SELECT l.id, l.delta, l.reason, l.subject_id, l.note, l.at
		   FROM reseller_credit_ledger l
		   JOIN resellers ON resellers.id = l.reseller_id
		  WHERE l.reseller_id = ? AND `+resellerScopePredicate+`
		  ORDER BY l.id DESC
		  LIMIT ?`,
		resellerID, boolToInt(sc.IsSuper), sc.AdminID, limit)
	if err != nil {
		return nil, fmt.Errorf("list ledger: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []LedgerRow
	for rows.Next() {
		var l LedgerRow
		if err := rows.Scan(&l.ID, &l.Delta, &l.Reason, &l.SubjectID,
			&l.Note, &l.At); err != nil {
			return nil, fmt.Errorf("scan ledger row: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ledger: %w", err)
	}
	return out, nil
}

// LedgerRow is one credit movement as returned to a caller.
type LedgerRow struct {
	ID        int64
	Delta     int64
	Reason    string
	SubjectID sql.NullInt64
	Note      string
	At        int64
}

// BalanceScoped returns a reseller's balance, or sql.ErrNoRows if this caller
// may not see that reseller.
//
// Scoped rather than open because a balance is commercially sensitive: it
// reveals how much business a competitor is doing.
func (s *Store) BalanceScoped(
	ctx context.Context, sc rbac.Scope, resellerID int64,
) (int64, error) {
	// Existence-and-scope first, so an out-of-scope reseller is
	// indistinguishable from a missing one rather than returning a bare zero.
	var one int
	err := s.Read().QueryRowContext(ctx,
		`SELECT 1 FROM resellers WHERE resellers.id = ? AND `+resellerScopePredicate,
		resellerID, boolToInt(sc.IsSuper), sc.AdminID).Scan(&one)
	if err != nil {
		return 0, err
	}

	var balance sql.NullInt64
	if err := s.Read().QueryRowContext(ctx,
		`SELECT sum(delta) FROM reseller_credit_ledger WHERE reseller_id = ?`,
		resellerID).Scan(&balance); err != nil {
		return 0, fmt.Errorf("sum ledger: %w", err)
	}
	return balance.Int64, nil
}

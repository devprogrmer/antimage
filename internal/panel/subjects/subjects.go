// Package subjects owns the people a node serves: their credentials, the
// services they may use, and their expiry.
//
// Credential material is sealed with AES-256-GCM under the master key before
// it reaches the database and is unsealed only when a desired document is
// assembled or a subscription is rendered. It is never logged, never audited,
// and never returned by a list endpoint. See
// docs/superpowers/specs/2026-08-18-sp2-design-decisions.md, decision 1.
package subjects

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/shared/secrets"
)

// CredentialKind is what a protocol needs to identify a user.
type CredentialKind string

const (
	// KindUUID is VLESS and VMess.
	KindUUID CredentialKind = "uuid"
	// KindPassword is Trojan and Shadowsocks.
	KindPassword CredentialKind = "password"
)

// ErrUnknownKind is returned for a credential kind no protocol uses.
var ErrUnknownKind = errors.New("unknown credential kind")

// ErrNameTaken means another subject already holds that name.
var ErrNameTaken = errors.New("a subject with that name already exists")

// passwordBytes is the entropy of a generated Trojan/Shadowsocks password.
// 32 bytes base64url-encoded, which is well past any brute-force concern and
// short enough to paste.
const passwordBytes = 32

// Subject is a person or device, as the panel stores it. Credentials are
// deliberately absent: they are fetched separately so a list can never leak
// them.
type Subject struct {
	ID        int64
	Name      string
	Enabled   bool
	ExpiresAt *time.Time
	ExpiredAt *time.Time
	CreatedAt time.Time
	Note      string
}

// Expired reports whether the subject has passed its expiry at the given time.
// A subject with no expiry never expires.
func (s Subject) Expired(at time.Time) bool {
	return s.ExpiresAt != nil && !at.Before(*s.ExpiresAt)
}

// Active reports whether the subject should appear in a desired document.
// This is the single predicate the document builder and the expiry sweeper
// both consult, so they cannot disagree about who is entitled to service.
func (s Subject) Active(at time.Time) bool {
	return s.Enabled && !s.Expired(at)
}

// GenerateCredential mints new credential material for a kind.
//
// UUIDs are v4 rather than derived: a credential must be importable from an
// existing deployment so operators can migrate without every client
// reconfiguring, which a pure function of subject id could not represent.
func GenerateCredential(kind CredentialKind) (string, error) {
	switch kind {
	case KindUUID:
		u, err := uuid.NewRandom()
		if err != nil {
			return "", fmt.Errorf("generate uuid: %w", err)
		}
		return u.String(), nil
	case KindPassword:
		raw := make([]byte, passwordBytes)
		if _, err := rand.Read(raw); err != nil {
			return "", fmt.Errorf("generate password: %w", err)
		}
		return base64.RawURLEncoding.EncodeToString(raw), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownKind, kind)
	}
}

// ValidateCredential accepts material an operator is importing from another
// panel. A UUID must parse; a password must be long enough to be worth having.
func ValidateCredential(kind CredentialKind, value string) error {
	switch kind {
	case KindUUID:
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("not a valid uuid: %w", err)
		}
		return nil
	case KindPassword:
		if len(value) < 16 {
			return errors.New("password must be at least 16 characters")
		}
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrUnknownKind, kind)
	}
}

// Store reads and writes subjects. It holds the secret box because credential
// material must never cross this boundary in plaintext.
type Store struct {
	db  *store.Store
	box *secrets.Box
	now func() time.Time
}

func NewStore(db *store.Store, box *secrets.Box, now func() time.Time) *Store {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Store{db: db, box: box, now: now}
}

// CreateInput describes a new subject. An empty Credentials map means "mint
// the defaults", which is what the UI sends.
type CreateInput struct {
	Name      string
	Note      string
	ExpiresAt *time.Time
	// ServiceIDs the subject may use.
	ServiceIDs []int64
	// Credentials to import. Absent kinds are generated.
	Credentials map[CredentialKind]string
}

// Create inserts a subject, seals its credentials, and grants its services in
// one transaction. It does NOT bump any node revision: the caller wraps this
// in CommitNodeChange per affected node, because that is the only path allowed
// to touch desired_revision.
func (s *Store) Create(ctx context.Context, tx *sql.Tx, in CreateInput) (int64, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return 0, errors.New("subject name is required")
	}
	if s.box == nil {
		// Fail closed: storing credential material unsealed would put it in
		// every backup in plaintext.
		return 0, errors.New("no secret box configured; refusing to store credentials unsealed")
	}

	now := s.now().UTC()
	var expires any
	if in.ExpiresAt != nil {
		expires = in.ExpiresAt.UTC().Unix()
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO subjects (name, enabled, expires_at, created_at, note)
		 VALUES (?, 1, ?, ?, ?)`,
		name, expires, now.Unix(), in.Note)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, fmt.Errorf("%w: %q", ErrNameTaken, name)
		}
		return 0, fmt.Errorf("insert subject: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	// Both kinds are minted by default so a subject can be attached to any
	// inbound later without a second round trip.
	for _, kind := range []CredentialKind{KindUUID, KindPassword} {
		value, supplied := in.Credentials[kind]
		if supplied {
			if err := ValidateCredential(kind, value); err != nil {
				return 0, fmt.Errorf("credential %s: %w", kind, err)
			}
		} else {
			value, err = GenerateCredential(kind)
			if err != nil {
				return 0, err
			}
		}
		if err := s.putCredential(ctx, tx, id, kind, value, now); err != nil {
			return 0, err
		}
	}

	for _, svcID := range in.ServiceIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO subject_services (subject_id, service_id) VALUES (?,?)`,
			id, svcID); err != nil {
			return 0, fmt.Errorf("grant service %d: %w", svcID, err)
		}
	}
	return id, nil
}

func (s *Store) putCredential(
	ctx context.Context, tx *sql.Tx, subjectID int64, kind CredentialKind, value string, now time.Time,
) error {
	sealed, err := s.box.Seal([]byte(value))
	if err != nil {
		return fmt.Errorf("seal %s credential: %w", kind, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO subject_credentials (subject_id, kind, value_enc, rotation, created_at)
		 VALUES (?,?,?,0,?)
		 ON CONFLICT(subject_id, kind) DO UPDATE SET
		   value_enc = excluded.value_enc,
		   rotation  = subject_credentials.rotation + 1,
		   created_at = excluded.created_at`,
		subjectID, string(kind), sealed, now.Unix()); err != nil {
		return fmt.Errorf("store %s credential: %w", kind, err)
	}
	return nil
}

// Rotate replaces one credential without touching any other, which is the
// capability that ruled out deriving credentials from a single seed.
func (s *Store) Rotate(ctx context.Context, tx *sql.Tx, subjectID int64, kind CredentialKind) (string, error) {
	value, err := GenerateCredential(kind)
	if err != nil {
		return "", err
	}
	if err := s.putCredential(ctx, tx, subjectID, kind, value, s.now().UTC()); err != nil {
		return "", err
	}
	return value, nil
}

// Credential unseals one credential. Callers must not log the result.
func (s *Store) Credential(ctx context.Context, subjectID int64, kind CredentialKind) (string, error) {
	if s.box == nil {
		return "", errors.New("no secret box configured")
	}
	var sealed []byte
	err := s.db.Read().QueryRowContext(ctx,
		`SELECT value_enc FROM subject_credentials WHERE subject_id = ? AND kind = ?`,
		subjectID, string(kind)).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("subject %d has no %s credential", subjectID, kind)
	}
	if err != nil {
		return "", fmt.Errorf("read credential: %w", err)
	}
	plain, err := s.box.Open(sealed)
	if err != nil {
		return "", fmt.Errorf("unseal credential (wrong master key?): %w", err)
	}
	return string(plain), nil
}

// Get reads one subject without its credentials.
func (s *Store) Get(ctx context.Context, id int64) (*Subject, error) {
	row := s.db.Read().QueryRowContext(ctx,
		`SELECT id, name, enabled, expires_at, expired_at, created_at, note
		   FROM subjects WHERE id = ?`, id)
	return scanSubject(row)
}

// List returns every subject, newest first. Credentials are deliberately not
// included: a list endpoint must not be able to leak them.
func (s *Store) List(ctx context.Context) ([]Subject, error) {
	rows, err := s.db.Read().QueryContext(ctx,
		`SELECT id, name, enabled, expires_at, expired_at, created_at, note
		   FROM subjects ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list subjects: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Subject
	for rows.Next() {
		var (
			s         Subject
			enabled   int
			expiresAt sql.NullInt64
			expiredAt sql.NullInt64
			createdAt int64
		)
		if err := rows.Scan(&s.ID, &s.Name, &enabled, &expiresAt, &expiredAt, &createdAt, &s.Note); err != nil {
			return nil, fmt.Errorf("scan subject: %w", err)
		}
		s.Enabled = enabled == 1
		s.CreatedAt = time.Unix(createdAt, 0).UTC()
		if expiresAt.Valid {
			t := time.Unix(expiresAt.Int64, 0).UTC()
			s.ExpiresAt = &t
		}
		if expiredAt.Valid {
			t := time.Unix(expiredAt.Int64, 0).UTC()
			s.ExpiredAt = &t
		}
		out = append(out, s)
	}
	// Without this a mid-iteration failure is served as a complete list.
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSubject(row rowScanner) (*Subject, error) {
	var (
		s         Subject
		enabled   int
		expiresAt sql.NullInt64
		expiredAt sql.NullInt64
		createdAt int64
	)
	err := row.Scan(&s.ID, &s.Name, &enabled, &expiresAt, &expiredAt, &createdAt, &s.Note)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("scan subject: %w", err)
	}
	s.Enabled = enabled == 1
	s.CreatedAt = time.Unix(createdAt, 0).UTC()
	if expiresAt.Valid {
		t := time.Unix(expiresAt.Int64, 0).UTC()
		s.ExpiresAt = &t
	}
	if expiredAt.Valid {
		t := time.Unix(expiredAt.Int64, 0).UTC()
		s.ExpiredAt = &t
	}
	return &s, nil
}

// NodeIDsFor returns every node a subject is provisioned on, which is the set
// whose revisions must bump when the subject changes.
func (s *Store) NodeIDsFor(ctx context.Context, tx *sql.Tx, subjectID int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT DISTINCT sv.node_id
		   FROM subject_services ss
		   JOIN services sv ON sv.id = ss.service_id
		  WHERE ss.subject_id = ?
		  ORDER BY sv.node_id`, subjectID)
	if err != nil {
		return nil, fmt.Errorf("find nodes for subject %d: %w", subjectID, err)
	}
	defer func() { _ = rows.Close() }()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "UNIQUE CONSTRAINT FAILED")
}

// UpdateInput describes a partial change. Nil fields are left alone, which is
// what lets a caller toggle `enabled` without resending the whole subject.
type UpdateInput struct {
	Name        *string
	Note        *string
	Enabled     *bool
	ExpiresAt   *time.Time
	ClearExpiry bool
	ServiceIDs  *[]int64
}

// Update applies a partial change.
//
// Re-enabling a subject clears expired_at, because leaving it set would make a
// re-enabled account look permanently expired in every listing.
func (s *Store) Update(ctx context.Context, tx *sql.Tx, id int64, in UpdateInput) error {
	var exists int
	if err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM subjects WHERE id = ?`, id).Scan(&exists); err != nil {
		return err // sql.ErrNoRows reaches the handler as a 404
	}

	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return errors.New("subject name cannot be empty")
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE subjects SET name = ? WHERE id = ?`, name, id); err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("%w: %q", ErrNameTaken, name)
			}
			return fmt.Errorf("rename subject: %w", err)
		}
	}
	if in.Note != nil {
		if _, err := tx.ExecContext(ctx,
			`UPDATE subjects SET note = ? WHERE id = ?`, *in.Note, id); err != nil {
			return fmt.Errorf("update note: %w", err)
		}
	}
	if in.Enabled != nil {
		enabled := 0
		if *in.Enabled {
			enabled = 1
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE subjects SET enabled = ? WHERE id = ?`, enabled, id); err != nil {
			return fmt.Errorf("update enabled: %w", err)
		}
		if *in.Enabled {
			if _, err := tx.ExecContext(ctx,
				`UPDATE subjects SET expired_at = NULL WHERE id = ?`, id); err != nil {
				return fmt.Errorf("clear expired_at: %w", err)
			}
		}
	}
	switch {
	case in.ClearExpiry:
		if _, err := tx.ExecContext(ctx,
			`UPDATE subjects SET expires_at = NULL, expired_at = NULL WHERE id = ?`, id); err != nil {
			return fmt.Errorf("clear expiry: %w", err)
		}
	case in.ExpiresAt != nil:
		if _, err := tx.ExecContext(ctx,
			`UPDATE subjects SET expires_at = ?, expired_at = NULL WHERE id = ?`,
			in.ExpiresAt.UTC().Unix(), id); err != nil {
			return fmt.Errorf("set expiry: %w", err)
		}
	}
	if in.ServiceIDs != nil {
		// Replace wholesale: computing a delta would leave a window where the
		// subject is granted neither the old nor the new set.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM subject_services WHERE subject_id = ?`, id); err != nil {
			return fmt.Errorf("clear service grants: %w", err)
		}
		for _, svcID := range *in.ServiceIDs {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO subject_services (subject_id, service_id) VALUES (?,?)`,
				id, svcID); err != nil {
				return fmt.Errorf("grant service %d: %w", svcID, err)
			}
		}
	}
	return nil
}

// Delete removes a subject. The schema cascades its credentials and grants, so
// the credential stops working rather than merely disappearing from the panel.
func (s *Store) Delete(ctx context.Context, tx *sql.Tx, id int64) error {
	res, err := tx.ExecContext(ctx, `DELETE FROM subjects WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete subject: %w", err)
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

// NodeIDsForRead is NodeIDsFor without a transaction, for callers that need the
// node set before opening one.
func (s *Store) NodeIDsForRead(ctx context.Context, subjectID int64) ([]int64, error) {
	rows, err := s.db.Read().QueryContext(ctx,
		`SELECT DISTINCT sv.node_id
		   FROM subject_services ss
		   JOIN services sv ON sv.id = ss.service_id
		  WHERE ss.subject_id = ?
		  ORDER BY sv.node_id`, subjectID)
	if err != nil {
		return nil, fmt.Errorf("find nodes for subject %d: %w", subjectID, err)
	}
	defer func() { _ = rows.Close() }()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Freeze freezes a subject, preventing access until unfrozen.
// Sets frozen_at timestamp and reason. Typically used for quota enforcement or violations.
func (s *Store) Freeze(ctx context.Context, tx *sql.Tx, subjectID int64, reason string) error {
	now := s.now().UTC().Unix()
	res, err := tx.ExecContext(ctx,
		`UPDATE subjects SET frozen_at = ?, frozen_reason = ? WHERE id = ?`,
		now, reason, subjectID)
	if err != nil {
		return fmt.Errorf("freeze subject: %w", err)
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

// Unfreeze unfreezes a subject, restoring access.
// Clears frozen_at and frozen_reason.
func (s *Store) Unfreeze(ctx context.Context, tx *sql.Tx, subjectID int64) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE subjects SET frozen_at = NULL, frozen_reason = NULL WHERE id = ?`,
		subjectID)
	if err != nil {
		return fmt.Errorf("unfreeze subject: %w", err)
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

// Disable disables a subject, setting enabled = 0.
// This is different from freeze: disable is manual admin action, freeze is automatic quota enforcement.
func (s *Store) Disable(ctx context.Context, tx *sql.Tx, subjectID int64) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE subjects SET enabled = 0 WHERE id = ?`,
		subjectID)
	if err != nil {
		return fmt.Errorf("disable subject: %w", err)
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

// Enable enables a subject, setting enabled = 1 and clearing expired_at.
func (s *Store) Enable(ctx context.Context, tx *sql.Tx, subjectID int64) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE subjects SET enabled = 1, expired_at = NULL WHERE id = ?`,
		subjectID)
	if err != nil {
		return fmt.Errorf("enable subject: %w", err)
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


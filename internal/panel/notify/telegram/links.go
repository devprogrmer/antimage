// Package telegram owns the binding between a Telegram account and a panel
// admin, and nothing else.
//
// It deliberately contains no command handling and no Telegram API client: the
// identity question ("who is this chat, and may they act?") is the security
// boundary, and keeping it in its own package means it can be reasoned about
// and tested without a bot running.
//
// The rule the rest of the bot depends on: this package resolves a Telegram id
// to an admin id, and NOTHING ELSE resolves it. Every command then loads a real
// rbac.Actor from that admin id and calls the same scoped store methods the
// HTTP layer calls, so tenant isolation is inherited rather than reimplemented.
package telegram

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

// codeTTL bounds how long a link code is usable.
//
// A code is read off a screen and typed into a chat, which takes under a
// minute. Anything longer is a window with no legitimate use, and link codes
// are the one credential in the system that travels through a third party's
// infrastructure.
const codeTTL = 10 * time.Minute

// codeBytes is the entropy behind a link code. 10 bytes base32-encodes to 16
// characters, which is short enough to retype and far past brute force even
// before rate limiting.
const codeBytes = 10

var (
	// ErrNotLinked means this Telegram account is not bound to any admin, or
	// its binding was revoked. The bot must treat both identically: telling a
	// stranger that an account "was revoked" confirms it once existed.
	ErrNotLinked = errors.New("telegram account is not linked")
	// ErrBadCode covers wrong, expired, and already-consumed codes. One error
	// for all three on purpose -- distinguishing them tells an attacker
	// whether a guess was structurally valid.
	ErrBadCode = errors.New("invalid or expired link code")
	// ErrAlreadyLinked means the admin already has a Telegram account bound.
	ErrAlreadyLinked = errors.New("this admin already has a linked telegram account")
)

// Link is a binding as shown in the panel's linked-accounts list.
type Link struct {
	TelegramID int64
	AdminID    int64
	Username   string
	LinkedAt   int64
	LastSeenAt int64
	RevokedAt  *int64
}

// Store owns link persistence.
type Store struct {
	db  *store.Store
	now func() time.Time
}

func NewStore(db *store.Store, now func() time.Time) *Store {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Store{db: db, now: now}
}

// hashCode is the one-way transform applied before storage.
//
// Mirrors auth.hashToken: the database holds only the hash, so read access to
// a backup does not yield working link codes.
func hashCode(code string) []byte {
	sum := sha256.Sum256([]byte(normalise(code)))
	return sum[:]
}

// normalise makes codes forgiving to retype without weakening them.
//
// Users read these off a screen, so case and stray spaces or dashes are
// mistakes rather than attacks. Normalisation happens before hashing so the
// stored hash is of the canonical form.
func normalise(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	code = strings.ReplaceAll(code, "-", "")
	code = strings.ReplaceAll(code, " ", "")
	return code
}

// IssueCode mints a one-time link code for an admin.
//
// Returns the PLAINTEXT, which is the only moment it exists in this process.
// The caller shows it once to an already-authenticated admin; it is never
// logged and never audited by value.
//
// Any unconsumed codes the admin already holds are invalidated first, so a
// second "link" click cannot leave two live codes outstanding.
func (s *Store) IssueCode(ctx context.Context, tx *sql.Tx, adminID int64) (string, error) {
	raw := make([]byte, codeBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate link code: %w", err)
	}
	code := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)

	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx,
		`UPDATE telegram_link_codes SET consumed_at = ?
		  WHERE admin_id = ? AND consumed_at IS NULL`,
		now.Unix(), adminID); err != nil {
		return "", fmt.Errorf("invalidate previous codes: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO telegram_link_codes (code_hash, admin_id, expires_at, created_at)
		 VALUES (?,?,?,?)`,
		hashCode(code), adminID, now.Add(codeTTL).Unix(), now.Unix()); err != nil {
		return "", fmt.Errorf("store link code: %w", err)
	}
	return code, nil
}

// Redeem binds a Telegram account to whichever admin issued the code.
//
// Single use and time bounded. The consumed marker is set rather than the row
// deleted, so a replayed code is distinguishable in the audit trail from one
// that never existed.
func (s *Store) Redeem(
	ctx context.Context, tx *sql.Tx, telegramID int64, username, code string,
) (int64, error) {
	now := s.now().UTC()

	var adminID, expiresAt int64
	var consumedAt sql.NullInt64
	err := tx.QueryRowContext(ctx,
		`SELECT admin_id, expires_at, consumed_at
		   FROM telegram_link_codes WHERE code_hash = ?`, hashCode(code)).
		Scan(&adminID, &expiresAt, &consumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrBadCode
	}
	if err != nil {
		return 0, fmt.Errorf("read link code: %w", err)
	}
	if consumedAt.Valid || now.Unix() > expiresAt {
		return 0, ErrBadCode
	}

	// An admin may hold only one Telegram account. A second binding would make
	// every audit record ambiguous about which human acted.
	var existing int64
	switch err := tx.QueryRowContext(ctx,
		`SELECT telegram_id FROM telegram_links
		  WHERE admin_id = ? AND revoked_at IS NULL`, adminID).Scan(&existing); {
	case err == nil && existing != telegramID:
		return 0, ErrAlreadyLinked
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return 0, fmt.Errorf("check existing link: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE telegram_link_codes SET consumed_at = ?, consumed_by = ?
		  WHERE code_hash = ?`, now.Unix(), telegramID, hashCode(code)); err != nil {
		return 0, fmt.Errorf("consume link code: %w", err)
	}

	// Re-linking the same Telegram account revives the row rather than
	// stacking rows, and clears any previous revocation.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO telegram_links
		   (telegram_id, admin_id, username, linked_at, last_seen_at, revoked_at)
		 VALUES (?,?,?,?,?,NULL)
		 ON CONFLICT(telegram_id) DO UPDATE SET
		   admin_id = excluded.admin_id,
		   username = excluded.username,
		   linked_at = excluded.linked_at,
		   last_seen_at = excluded.last_seen_at,
		   revoked_at = NULL`,
		telegramID, adminID, username, now.Unix(), now.Unix()); err != nil {
		return 0, fmt.Errorf("bind telegram account: %w", err)
	}
	return adminID, nil
}

// AdminFor resolves a Telegram id to an admin id.
//
// This is the ONLY path from a chat to an identity. It is called on every
// command rather than cached, because a revocation has to take effect
// immediately -- a cached identity is a credential that outlives its own
// revocation.
//
// A revoked link and an absent link both return ErrNotLinked: telling a
// stranger that their account "was revoked" confirms it once existed.
func (s *Store) AdminFor(ctx context.Context, telegramID int64) (int64, error) {
	var adminID int64
	err := s.db.Read().QueryRowContext(ctx,
		`SELECT admin_id FROM telegram_links
		  WHERE telegram_id = ? AND revoked_at IS NULL`, telegramID).Scan(&adminID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotLinked
	}
	if err != nil {
		return 0, fmt.Errorf("resolve telegram link: %w", err)
	}
	return adminID, nil
}

// Touch records activity, so the panel's linked-accounts list can show a
// last-seen time and an operator can spot a dormant or hijacked binding.
//
// Best effort by design: failing to update a timestamp must never fail the
// command the user actually asked for.
func (s *Store) Touch(ctx context.Context, telegramID int64) {
	_ = s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE telegram_links SET last_seen_at = ? WHERE telegram_id = ?`,
			s.now().UTC().Unix(), telegramID)
		return err
	})
}

// Revoke cuts off a Telegram account.
//
// Used both by an admin unlinking their own account and by a super admin
// cutting off someone else's. The row survives so an incident review can see
// that the binding existed and when it ended.
func (s *Store) Revoke(ctx context.Context, tx *sql.Tx, telegramID int64) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE telegram_links SET revoked_at = ?
		  WHERE telegram_id = ? AND revoked_at IS NULL`,
		s.now().UTC().Unix(), telegramID)
	if err != nil {
		return fmt.Errorf("revoke telegram link: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotLinked
	}
	return nil
}

// RevokeByAdmin cuts off whichever account is bound to an admin.
func (s *Store) RevokeByAdmin(ctx context.Context, tx *sql.Tx, adminID int64) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE telegram_links SET revoked_at = ?
		  WHERE admin_id = ? AND revoked_at IS NULL`,
		s.now().UTC().Unix(), adminID)
	if err != nil {
		return fmt.Errorf("revoke telegram link: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotLinked
	}
	return nil
}

// ForAdmin returns the active link for one admin, for the panel UI.
func (s *Store) ForAdmin(ctx context.Context, adminID int64) (Link, error) {
	var l Link
	var revoked sql.NullInt64
	err := s.db.Read().QueryRowContext(ctx,
		`SELECT telegram_id, admin_id, username, linked_at, last_seen_at, revoked_at
		   FROM telegram_links WHERE admin_id = ? AND revoked_at IS NULL`, adminID).
		Scan(&l.TelegramID, &l.AdminID, &l.Username, &l.LinkedAt, &l.LastSeenAt, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return Link{}, ErrNotLinked
	}
	if err != nil {
		return Link{}, fmt.Errorf("read link for admin %d: %w", adminID, err)
	}
	if revoked.Valid {
		l.RevokedAt = &revoked.Int64
	}
	return l, nil
}

// SameCode reports whether two codes match, in constant time.
//
// Exported for callers that compare a supplied code against one they already
// hold. Redeem does not need it -- it looks the hash up directly, which is
// already constant-time with respect to the code -- but any future comparison
// path must not leak a prefix through timing.
func SameCode(a, b string) bool {
	return subtle.ConstantTimeCompare(hashCode(a), hashCode(b)) == 1
}

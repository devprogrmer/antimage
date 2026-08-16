package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

const (
	// IdleTimeout and AbsoluteLifetime come from the spec's global constraints.
	IdleTimeout      = 4 * time.Hour
	AbsoluteLifetime = 7 * 24 * time.Hour
	CookieName       = "antimage_session"
	tokenBytes       = 32
)

// ErrSessionInvalid covers every rejection reason. Callers must not
// distinguish "unknown", "revoked", and "expired" in responses.
var ErrSessionInvalid = errors.New("session invalid")

type Session struct {
	ID         int64
	AdminID    int64
	IP         string
	UserAgent  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt time.Time
}

type Sessions struct {
	store *store.Store
	now   func() time.Time
}

func NewSessions(s *store.Store, now func() time.Time) *Sessions {
	if now == nil {
		now = time.Now
	}
	return &Sessions{store: s, now: now}
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// Create mints an opaque 32-byte token, persists only its SHA-256, and
// returns the raw token to be set as a cookie exactly once.
func (s *Sessions) Create(ctx context.Context, adminID int64, ip, ua string) (string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	now := s.now().UTC()
	err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO sessions
			   (admin_id, token_hash, ip, user_agent, created_at, expires_at, last_used_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			adminID, hashToken(token), ip, ua,
			now.Unix(), now.Add(AbsoluteLifetime).Unix(), now.Unix())
		return err
	})
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return token, nil
}

// Lookup validates a token and refreshes last_used_at. It enforces both the
// idle window and the absolute lifetime, and rejects revoked sessions.
// Every rejection reason collapses to ErrSessionInvalid so callers cannot
// tell an attacker whether a token ever existed.
func (s *Sessions) Lookup(ctx context.Context, token string) (*Session, error) {
	if token == "" {
		return nil, ErrSessionInvalid
	}
	now := s.now().UTC()

	var (
		sess                             Session
		createdAt, expiresAt, lastUsedAt int64
		revokedAt                        sql.NullInt64
	)
	err := s.store.Read().QueryRowContext(ctx,
		`SELECT id, admin_id, ip, user_agent, created_at, expires_at, last_used_at, revoked_at
		   FROM sessions WHERE token_hash = ?`, hashToken(token),
	).Scan(&sess.ID, &sess.AdminID, &sess.IP, &sess.UserAgent,
		&createdAt, &expiresAt, &lastUsedAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSessionInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("lookup session: %w", err)
	}
	if revokedAt.Valid {
		return nil, ErrSessionInvalid
	}
	// Absolute lifetime: fixed at Create time, never extended by activity.
	// Strictly greater-than so a session stays valid through the full
	// AbsoluteLifetime window and expires only once it is exceeded.
	if now.Unix() > expiresAt {
		return nil, ErrSessionInvalid
	}
	// Idle timeout: measured from the last successful lookup, so continuous
	// use restarts the window even though the absolute deadline above does not.
	if now.Sub(time.Unix(lastUsedAt, 0).UTC()) >= IdleTimeout {
		return nil, ErrSessionInvalid
	}

	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE sessions SET last_used_at = ? WHERE id = ?`, now.Unix(), sess.ID)
		return err
	}); err != nil {
		return nil, fmt.Errorf("refresh session: %w", err)
	}

	sess.CreatedAt = time.Unix(createdAt, 0).UTC()
	sess.ExpiresAt = time.Unix(expiresAt, 0).UTC()
	sess.LastUsedAt = now
	return &sess, nil
}

func (s *Sessions) Revoke(ctx context.Context, sessionID int64) error {
	now := s.now().UTC().Unix()
	return s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE sessions SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
			now, sessionID)
		return err
	})
}

// RevokeAllForAdmin is called on password change, privilege change, and
// administrative suspension.
func (s *Sessions) RevokeAllForAdmin(ctx context.Context, adminID int64) error {
	now := s.now().UTC().Unix()
	return s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE sessions SET revoked_at = ? WHERE admin_id = ? AND revoked_at IS NULL`,
			now, adminID)
		return err
	})
}

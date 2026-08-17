package nodes

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

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/store"
)

// EnrollTokenTTL is deliberately short: the token travels in a curl one-liner
// and grants the right to become a node.
const EnrollTokenTTL = 30 * time.Minute

// ErrTokenInvalid covers unknown, expired, and already-used tokens. Callers
// must not distinguish them.
var ErrTokenInvalid = errors.New("enrollment token invalid")

func hashEnrollToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func IssueEnrollToken(
	ctx context.Context, s *store.Store, nodeID int64,
	actor audit.Actor, requestID string, now time.Time,
) (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("generate enrollment token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	err := s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO enroll_tokens (token_hash, node_id, expires_at, created_at)
			 VALUES (?,?,?,?)`,
			hashEnrollToken(token), nodeID,
			now.Add(EnrollTokenTTL).Unix(), now.Unix()); err != nil {
			return fmt.Errorf("insert enrollment token: %w", err)
		}
		return audit.InTx(ctx, tx, requestID, actor, audit.Record{
			Action:     "node.enroll_token",
			TargetType: "node",
			TargetID:   sql.NullInt64{Int64: nodeID, Valid: true},
			After:      map[string]any{"expires_at": now.Add(EnrollTokenTTL).Unix()},
			Result:     "ok",
		})
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// RedeemEnrollToken burns the token and returns the node it was bound to.
// The update is conditional, so two concurrent redemptions cannot both win.
func RedeemEnrollToken(ctx context.Context, s *store.Store, token string, now time.Time) (int64, error) {
	if token == "" {
		return 0, ErrTokenInvalid
	}
	var nodeID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE enroll_tokens SET used_at = ?
			  WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?`,
			now.Unix(), hashEnrollToken(token), now.Unix())
		if err != nil {
			return fmt.Errorf("burn enrollment token: %w", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("rows affected: %w", err)
		}
		if affected == 0 {
			return ErrTokenInvalid
		}
		return tx.QueryRowContext(ctx,
			`SELECT node_id FROM enroll_tokens WHERE token_hash = ?`,
			hashEnrollToken(token)).Scan(&nodeID)
	})
	if err != nil {
		return 0, err
	}
	return nodeID, nil
}

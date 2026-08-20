package subjects

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/amyrm/antimage/internal/panel/store"
)

var (
	// ErrTokenNotFound is returned when a subscription token doesn't exist.
	ErrTokenNotFound = errors.New("subscription token not found")
)

// GenerateToken creates a cryptographically random subscription token.
// Returns base64url-encoded 32 bytes (~43 characters, URL-safe).
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// EnsureToken returns the subject's subscription token, generating one if empty.
// Lazy initialization: existing subjects get tokens on first access.
func EnsureToken(ctx context.Context, st *store.Store, subjectID int64) (string, error) {
	var token string
	row := st.Read().QueryRowContext(ctx, `SELECT subscription_token FROM subjects WHERE id = ?`, subjectID)
	if err := row.Scan(&token); err != nil {
		return "", fmt.Errorf("query subject token: %w", err)
	}

	// If token already exists, return it (idempotent).
	if token != "" {
		return token, nil
	}

	// Generate and store new token.
	newToken, err := GenerateToken()
	if err != nil {
		return "", err
	}

	err = st.Write(ctx, func(tx *sql.Tx) error {
		// Re-check token hasn't been set by concurrent call.
		row := tx.QueryRowContext(ctx, `SELECT subscription_token FROM subjects WHERE id = ?`, subjectID)
		var current string
		if err := row.Scan(&current); err != nil {
			return err
		}
		if current != "" {
			// Another goroutine set it, use theirs.
			token = current
			return nil
		}

		// Set the new token.
		_, err := tx.ExecContext(ctx, `UPDATE subjects SET subscription_token = ? WHERE id = ?`, newToken, subjectID)
		if err != nil {
			return fmt.Errorf("update subject token: %w", err)
		}
		token = newToken
		return nil
	})
	if err != nil {
		return "", err
	}

	return token, nil
}

// RevokeToken regenerates a subject's subscription token, invalidating the old one.
// Returns the new token.
func RevokeToken(ctx context.Context, st *store.Store, subjectID int64) (string, error) {
	newToken, err := GenerateToken()
	if err != nil {
		return "", err
	}

	err = st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE subjects SET subscription_token = ? WHERE id = ?`, newToken, subjectID)
		if err != nil {
			return fmt.Errorf("revoke subject token: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	return newToken, nil
}

// LookupByToken finds a subject ID by subscription token.
// Returns (subjectID, nil) if found and enabled.
// Returns (0, ErrTokenNotFound) if token doesn't exist or subject is disabled.
func LookupByToken(ctx context.Context, st *store.Store, token string) (int64, error) {
	if token == "" {
		return 0, ErrTokenNotFound
	}

	var subjectID int64
	var enabled bool
	row := st.Read().QueryRowContext(ctx,
		`SELECT id, enabled FROM subjects WHERE subscription_token = ?`,
		token)
	err := row.Scan(&subjectID, &enabled)
	if err == sql.ErrNoRows {
		return 0, ErrTokenNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("lookup subject by token: %w", err)
	}

	if !enabled {
		return 0, ErrTokenNotFound
	}

	return subjectID, nil
}

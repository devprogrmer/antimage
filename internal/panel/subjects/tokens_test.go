package subjects

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amyrm/antimage/internal/panel/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestGenerateToken(t *testing.T) {
	// Generate multiple tokens and verify properties.
	tokens := make(map[string]bool)
	for i := 0; i < 100; i++ {
		token, err := GenerateToken()
		if err != nil {
			t.Fatalf("GenerateToken failed: %v", err)
		}

		// Check length (~43 characters for 32 bytes base64url).
		if len(token) < 40 || len(token) > 50 {
			t.Errorf("token length %d out of expected range [40, 50]", len(token))
		}

		// Check URL-safe characters (no +, /, or =).
		if strings.ContainsAny(token, "+/=") {
			t.Errorf("token contains non-URL-safe characters: %q", token)
		}

		// Check uniqueness.
		if tokens[token] {
			t.Errorf("duplicate token generated: %q", token)
		}
		tokens[token] = true
	}
}

func TestEnsureToken(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	// Create a subject.
	var subjectID int64
	st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO subjects (name, enabled, created_at) VALUES (?, ?, ?)`,
			"test@example.com", true, 1234567890)
		if err != nil {
			return err
		}
		subjectID, _ = res.LastInsertId()
		return nil
	})

	// First call generates token.
	token1, err := EnsureToken(ctx, st, subjectID)
	if err != nil {
		t.Fatalf("EnsureToken failed: %v", err)
	}
	if token1 == "" {
		t.Fatal("EnsureToken returned empty token")
	}

	// Second call returns same token (idempotent).
	token2, err := EnsureToken(ctx, st, subjectID)
	if err != nil {
		t.Fatalf("EnsureToken second call failed: %v", err)
	}
	if token1 != token2 {
		t.Errorf("EnsureToken not idempotent: %q != %q", token1, token2)
	}

	// Verify token is stored in DB.
	var dbToken string
	row := st.Read().QueryRowContext(ctx, `SELECT subscription_token FROM subjects WHERE id = ?`, subjectID)
	if err := row.Scan(&dbToken); err != nil {
		t.Fatalf("query token from DB: %v", err)
	}
	if dbToken != token1 {
		t.Errorf("DB token %q != returned token %q", dbToken, token1)
	}
}

func TestRevokeToken(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	// Create a subject with a token.
	var subjectID int64
	st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO subjects (name, enabled, subscription_token, created_at) VALUES (?, ?, ?, ?)`,
			"test@example.com", true, "old-token-12345", 1234567890)
		if err != nil {
			return err
		}
		subjectID, _ = res.LastInsertId()
		return nil
	})

	// Revoke generates new token.
	newToken, err := RevokeToken(ctx, st, subjectID)
	if err != nil {
		t.Fatalf("RevokeToken failed: %v", err)
	}
	if newToken == "" {
		t.Fatal("RevokeToken returned empty token")
	}
	if newToken == "old-token-12345" {
		t.Error("RevokeToken did not change the token")
	}

	// Old token is invalid.
	_, err = LookupByToken(ctx, st, "old-token-12345")
	if !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("old token should be invalid, got error: %v", err)
	}

	// New token is valid.
	foundID, err := LookupByToken(ctx, st, newToken)
	if err != nil {
		t.Fatalf("new token lookup failed: %v", err)
	}
	if foundID != subjectID {
		t.Errorf("new token lookup returned wrong ID: %d != %d", foundID, subjectID)
	}
}

func TestLookupByToken(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	// Create enabled and disabled subjects.
	var enabledID int64
	st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO subjects (name, enabled, subscription_token, created_at) VALUES (?, ?, ?, ?)`,
			"enabled@example.com", true, "valid-token-abc", 1234567890)
		if err != nil {
			return err
		}
		enabledID, _ = res.LastInsertId()

		_, err = tx.ExecContext(ctx, `
			INSERT INTO subjects (name, enabled, subscription_token, created_at) VALUES (?, ?, ?, ?)`,
			"disabled@example.com", false, "disabled-token-xyz", 1234567890)
		if err != nil {
			return err
		}
		return nil
	})

	// Valid token returns subject ID.
	foundID, err := LookupByToken(ctx, st, "valid-token-abc")
	if err != nil {
		t.Fatalf("LookupByToken failed for valid token: %v", err)
	}
	if foundID != enabledID {
		t.Errorf("LookupByToken returned wrong ID: %d != %d", foundID, enabledID)
	}

	// Disabled subject returns ErrTokenNotFound.
	_, err = LookupByToken(ctx, st, "disabled-token-xyz")
	if !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("disabled subject should return ErrTokenNotFound, got: %v", err)
	}

	// Non-existent token returns ErrTokenNotFound.
	_, err = LookupByToken(ctx, st, "nonexistent-token")
	if !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("nonexistent token should return ErrTokenNotFound, got: %v", err)
	}

	// Empty token returns ErrTokenNotFound.
	_, err = LookupByToken(ctx, st, "")
	if !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("empty token should return ErrTokenNotFound, got: %v", err)
	}
}

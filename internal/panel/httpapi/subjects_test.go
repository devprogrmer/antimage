package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestHandleGetSubject(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Seed an admin and get session token.
	env.seedAdmin(t, "admin", "password", "super_admin")
	token := env.login(t, "admin", "password")

	// Seed a subject with token.
	var subjectID int64
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO subjects (name, enabled, subscription_token, created_at, note)
			VALUES (?, ?, ?, ?, ?)`,
			"user@example.com", 1, "test-token-abc", time.Now().Unix(), "Test user")
		if err != nil {
			return err
		}
		subjectID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed subject: %v", err)
	}

	// Get subject details.
	rec := env.get(t, "/api/v1/subjects/"+itoa64(subjectID), token)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp SubjectJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.ID != subjectID {
		t.Errorf("wrong ID: %d", resp.ID)
	}
	if resp.Name != "user@example.com" {
		t.Errorf("wrong name: %s", resp.Name)
	}
	if !resp.Enabled {
		t.Error("subject should be enabled")
	}
	if resp.SubscriptionURL == "" {
		t.Error("missing subscription URL")
	}
	if resp.Note != "Test user" {
		t.Errorf("wrong note: %s", resp.Note)
	}

	// Verify subscription URL format.
	expectedURL := "http://panel.local/api/v1/subscribe/test-token-abc"
	if resp.SubscriptionURL != expectedURL {
		t.Errorf("wrong subscription URL: %s, expected: %s", resp.SubscriptionURL, expectedURL)
	}
}

func TestHandleGetSubject_LazyTokenInitialization(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Seed an admin.
	env.seedAdmin(t, "admin", "password", "super_admin")
	token := env.login(t, "admin", "password")

	// Seed a subject WITHOUT a token (empty string).
	var subjectID int64
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO subjects (name, enabled, subscription_token, created_at)
			VALUES (?, ?, ?, ?)`,
			"notoken@example.com", 1, "", time.Now().Unix())
		if err != nil {
			return err
		}
		subjectID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed subject: %v", err)
	}

	// Get subject - should trigger lazy token generation.
	rec := env.get(t, "/api/v1/subjects/"+itoa64(subjectID), token)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp SubjectJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.SubscriptionURL == "" {
		t.Error("subscription URL should be generated lazily")
	}

	// Verify token was actually stored in DB.
	var storedToken string
	row := env.store.Read().QueryRowContext(ctx,
		`SELECT subscription_token FROM subjects WHERE id = ?`, subjectID)
	if err := row.Scan(&storedToken); err != nil {
		t.Fatalf("query stored token: %v", err)
	}
	if storedToken == "" {
		t.Error("token should be stored in DB after lazy initialization")
	}
}

func TestHandleGetSubject_NotFound(t *testing.T) {
	env := newTestEnv(t)

	env.seedAdmin(t, "admin", "password", "super_admin")
	token := env.login(t, "admin", "password")

	rec := env.get(t, "/api/v1/subjects/99999", token)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleRevokeToken(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	env.seedAdmin(t, "admin", "password", "super_admin")
	token := env.login(t, "admin", "password")

	// Seed a subject with token.
	oldToken := "old-token-xyz"
	var subjectID int64
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO subjects (name, enabled, subscription_token, created_at)
			VALUES (?, ?, ?, ?)`,
			"revoke@example.com", 1, oldToken, time.Now().Unix())
		if err != nil {
			return err
		}
		subjectID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed subject: %v", err)
	}

	// Revoke token.
	rec := env.post(t, "/api/v1/subjects/"+itoa64(subjectID)+"/revoke-token", "", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp["subscription_url"] == "" {
		t.Error("missing new subscription URL")
	}
	if resp["message"] == "" {
		t.Error("missing message")
	}

	// Verify old token is invalid.
	var storedToken string
	row := env.store.Read().QueryRowContext(ctx,
		`SELECT subscription_token FROM subjects WHERE id = ?`, subjectID)
	if err := row.Scan(&storedToken); err != nil {
		t.Fatalf("query stored token: %v", err)
	}
	if storedToken == oldToken {
		t.Error("token should have been changed")
	}
	if storedToken == "" {
		t.Error("new token should not be empty")
	}

	// Verify old subscription URL doesn't contain old token.
	if resp["subscription_url"] == "http://panel.local/api/v1/subscribe/"+oldToken {
		t.Error("new subscription URL should not contain old token")
	}
}

func TestHandleRevokeToken_InvalidID(t *testing.T) {
	env := newTestEnv(t)

	env.seedAdmin(t, "admin", "password", "super_admin")
	token := env.login(t, "admin", "password")

	rec := env.post(t, "/api/v1/subjects/invalid/revoke-token", "", token)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid ID, got %d", rec.Code)
	}
}

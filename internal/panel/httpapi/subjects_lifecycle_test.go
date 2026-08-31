package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"testing"
)

func TestSubjectFreezeUnfreeze(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "admin", "password", "super_admin")
	token := env.login(t, "admin", "password")

	// Create a subject
	ctx := context.Background()
	var subjectID int64
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO subjects (name, enabled, created_at) VALUES (?, 1, ?)`,
			"testuser", 1700000000)
		if err != nil {
			return err
		}
		subjectID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("failed to create subject: %v", err)
	}

	// Test freeze
	t.Run("freeze subject", func(t *testing.T) {
		w := env.post(t, fmt.Sprintf("/api/v1/subjects/%d/freeze", subjectID),
			`{"reason":"quota exceeded"}`, token)

		if w.Code != http.StatusNoContent {
			t.Errorf("expected 204 No Content, got %d: %s", w.Code, w.Body.String())
		}

		// Verify frozen_at and frozen_reason are set
		var frozenAt sql.NullInt64
		var frozenReason sql.NullString
		err := env.store.Read().QueryRow(
			`SELECT frozen_at, frozen_reason FROM subjects WHERE id = ?`, subjectID).
			Scan(&frozenAt, &frozenReason)
		if err != nil {
			t.Fatalf("failed to query subject: %v", err)
		}

		if !frozenAt.Valid {
			t.Error("expected frozen_at to be set")
		}
		if frozenReason.String != "quota exceeded" {
			t.Errorf("expected frozen_reason 'quota exceeded', got %q", frozenReason.String)
		}
	})

	// Test unfreeze
	t.Run("unfreeze subject", func(t *testing.T) {
		w := env.post(t, fmt.Sprintf("/api/v1/subjects/%d/unfreeze", subjectID), "", token)

		if w.Code != http.StatusNoContent {
			t.Errorf("expected 204 No Content, got %d: %s", w.Code, w.Body.String())
		}

		// Verify frozen_at and frozen_reason are cleared
		var frozenAt sql.NullInt64
		var frozenReason sql.NullString
		err := env.store.Read().QueryRow(
			`SELECT frozen_at, frozen_reason FROM subjects WHERE id = ?`, subjectID).
			Scan(&frozenAt, &frozenReason)
		if err != nil {
			t.Fatalf("failed to query subject: %v", err)
		}

		if frozenAt.Valid {
			t.Error("expected frozen_at to be NULL")
		}
		if frozenReason.Valid {
			t.Error("expected frozen_reason to be NULL")
		}
	})

	// Test freeze requires permission
	t.Run("freeze requires permission", func(t *testing.T) {
		env.seedAdmin(t, "readonly", "password", "readonly")
		readonlyToken := env.login(t, "readonly", "password")

		w := env.post(t, fmt.Sprintf("/api/v1/subjects/%d/freeze", subjectID),
			`{"reason":"test"}`, readonlyToken)

		if w.Code != http.StatusForbidden {
			t.Errorf("expected 403 Forbidden, got %d", w.Code)
		}
	})
}

func TestSubjectDisableEnable(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "admin", "password", "super_admin")
	token := env.login(t, "admin", "password")

	// Create a subject
	ctx := context.Background()
	var subjectID int64
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO subjects (name, enabled, created_at) VALUES (?, 1, ?)`,
			"testuser2", 1700000000)
		if err != nil {
			return err
		}
		subjectID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("failed to create subject: %v", err)
	}

	// Test disable
	t.Run("disable subject", func(t *testing.T) {
		w := env.post(t, fmt.Sprintf("/api/v1/subjects/%d/disable", subjectID), "", token)

		if w.Code != http.StatusNoContent {
			t.Errorf("expected 204 No Content, got %d: %s", w.Code, w.Body.String())
		}

		// Verify enabled is 0
		var enabled int
		err := env.store.Read().QueryRow(
			`SELECT enabled FROM subjects WHERE id = ?`, subjectID).Scan(&enabled)
		if err != nil {
			t.Fatalf("failed to query subject: %v", err)
		}

		if enabled != 0 {
			t.Errorf("expected enabled = 0, got %d", enabled)
		}
	})

	// Test enable
	t.Run("enable subject", func(t *testing.T) {
		w := env.post(t, fmt.Sprintf("/api/v1/subjects/%d/enable", subjectID), "", token)

		if w.Code != http.StatusNoContent {
			t.Errorf("expected 204 No Content, got %d: %s", w.Code, w.Body.String())
		}

		// Verify enabled is 1
		var enabled int
		err := env.store.Read().QueryRow(
			`SELECT enabled FROM subjects WHERE id = ?`, subjectID).Scan(&enabled)
		if err != nil {
			t.Fatalf("failed to query subject: %v", err)
		}

		if enabled != 1 {
			t.Errorf("expected enabled = 1, got %d", enabled)
		}
	})

	// Test enable clears expired_at
	t.Run("enable clears expired_at", func(t *testing.T) {
		// Set expired_at
		err := env.store.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE subjects SET expired_at = ? WHERE id = ?`,
				1700000000, subjectID)
			return err
		})
		if err != nil {
			t.Fatalf("failed to set expired_at: %v", err)
		}

		w := env.post(t, fmt.Sprintf("/api/v1/subjects/%d/enable", subjectID), "", token)

		if w.Code != http.StatusNoContent {
			t.Errorf("expected 204 No Content, got %d: %s", w.Code, w.Body.String())
		}

		// Verify expired_at is NULL
		var expiredAt sql.NullInt64
		err = env.store.Read().QueryRow(
			`SELECT expired_at FROM subjects WHERE id = ?`, subjectID).Scan(&expiredAt)
		if err != nil {
			t.Fatalf("failed to query subject: %v", err)
		}

		if expiredAt.Valid {
			t.Error("expected expired_at to be NULL after enable")
		}
	})

	// Test disable requires permission
	t.Run("disable requires permission", func(t *testing.T) {
		env.seedAdmin(t, "readonly2", "password", "readonly")
		readonlyToken := env.login(t, "readonly2", "password")

		w := env.post(t, fmt.Sprintf("/api/v1/subjects/%d/disable", subjectID), "", readonlyToken)

		if w.Code != http.StatusForbidden {
			t.Errorf("expected 403 Forbidden, got %d", w.Code)
		}
	})
}

func TestSubjectLifecycleNotFound(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "admin", "password", "super_admin")
	token := env.login(t, "admin", "password")

	nonexistentID := int64(99999)

	tests := []struct {
		name string
		path string
		body string
	}{
		{"freeze", fmt.Sprintf("/api/v1/subjects/%d/freeze", nonexistentID), `{"reason":"test"}`},
		{"unfreeze", fmt.Sprintf("/api/v1/subjects/%d/unfreeze", nonexistentID), ""},
		{"disable", fmt.Sprintf("/api/v1/subjects/%d/disable", nonexistentID), ""},
		{"enable", fmt.Sprintf("/api/v1/subjects/%d/enable", nonexistentID), ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := env.post(t, tt.path, tt.body, token)

			if w.Code != http.StatusNotFound {
				t.Errorf("expected 404 Not Found, got %d", w.Code)
			}
		})
	}
}

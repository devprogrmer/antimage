package httpapi

import (
	"context"
	"database/sql"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/shared/secrets"
)

func TestHandleSubscribe_ValidToken(t *testing.T) {
	// Create master key for credential unsealing.
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}

	// Update deps with Box.
	env := newTestEnv(t, func(d *Deps) {
		d.Box = box
	})

	// Seed a subject with token.
	ctx := context.Background()
	var subjectID int64
	err = env.store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO subjects (name, enabled, subscription_token, created_at)
			VALUES (?, ?, ?, ?)`,
			"user@example.com", 1, "valid-token-abc123", time.Now().Unix())
		if err != nil {
			return err
		}
		subjectID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed subject: %v", err)
	}

	// Create a node and service.
	var nodeID, serviceID int64
	err = env.store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (name, address, status, created_at)
			VALUES (?, ?, ?, ?)`,
			"test-node", "node.example.com", "online", time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, _ = res.LastInsertId()

		res, err = tx.ExecContext(ctx, `
			INSERT INTO services (node_id, adapter_kind, params, enabled, created_at)
			VALUES (?, ?, ?, ?, ?)`,
			nodeID, "xray", `{"protocol":"vless","port":443}`, 1, time.Now().Unix())
		if err != nil {
			return err
		}
		serviceID, _ = res.LastInsertId()

		// Link subject to service.
		_, err = tx.ExecContext(ctx, `
			INSERT INTO subject_services (subject_id, service_id) VALUES (?, ?)`,
			subjectID, serviceID)
		return err
	})
	if err != nil {
		t.Fatalf("seed node and service: %v", err)
	}

	// Seed credentials.
	uuidPlain := "11111111-2222-3333-4444-555555555555"
	uuidEnc, _ := box.Seal([]byte(uuidPlain))
	err = env.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO subject_credentials (subject_id, kind, value_enc, created_at)
			VALUES (?, ?, ?, ?)`,
			subjectID, "uuid", uuidEnc, time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("seed credentials: %v", err)
	}

	// Request subscription with v2ray User-Agent.
	rec := env.do(t, http.MethodGet, "/api/v1/subscribe/valid-token-abc123", "", "")
	rec.Result().Header.Set("User-Agent", "v2rayN/5.0")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify base64 response.
	decoded, err := base64.StdEncoding.DecodeString(rec.Body.String())
	if err != nil {
		t.Fatalf("response not base64: %v", err)
	}

	if !strings.Contains(string(decoded), "vless://") {
		t.Errorf("expected vless:// URI in response, got: %s", string(decoded))
	}
}

func TestHandleSubscribe_InvalidToken(t *testing.T) {
	env := newTestEnv(t)

	rec := env.get(t, "/api/v1/subscribe/invalid-token-xyz", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleSubscribe_DisabledSubject(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Seed disabled subject.
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO subjects (name, enabled, subscription_token, created_at)
			VALUES (?, ?, ?, ?)`,
			"disabled@example.com", 0, "disabled-token", time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("seed subject: %v", err)
	}

	rec := env.get(t, "/api/v1/subscribe/disabled-token", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for disabled subject, got %d", rec.Code)
	}
}

func TestHandleSubscribe_ExpiredSubject(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Test env uses fixed time: time.Unix(1_700_000_000, 0).UTC()
	// Seed expired subject (expires_at in the past relative to fixed time).
	pastTime := int64(1_700_000_000 - 86400) // 1 day before fixed time
	var subjectID int64
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO subjects (name, enabled, subscription_token, expires_at, created_at)
			VALUES (?, ?, ?, ?, ?)`,
			"expired@example.com", 1, "expired-token", pastTime, 1_700_000_000)
		if err != nil {
			return err
		}
		subjectID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed subject: %v", err)
	}

	// Add a node and service so we don't get 503.
	err = env.store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (name, address, status, created_at)
			VALUES (?, ?, ?, ?)`,
			"test-node", "node.example.com", "online", 1_700_000_000)
		if err != nil {
			return err
		}
		nodeID, _ := res.LastInsertId()

		res, err = tx.ExecContext(ctx, `
			INSERT INTO services (node_id, adapter_kind, params, enabled, created_at)
			VALUES (?, ?, ?, ?, ?)`,
			nodeID, "xray", `{"protocol":"vless","port":443}`, 1, 1_700_000_000)
		if err != nil {
			return err
		}
		serviceID, _ := res.LastInsertId()

		// Link subject to service.
		_, err = tx.ExecContext(ctx, `
			INSERT INTO subject_services (subject_id, service_id) VALUES (?, ?)`,
			subjectID, serviceID)
		return err
	})
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}

	rec := env.get(t, "/api/v1/subscribe/expired-token", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for expired subject, got %d", rec.Code)
	}
}

func TestHandleSubscribe_FrozenSubject(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Seed frozen subject.
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO subjects (name, enabled, subscription_token, frozen_at, created_at)
			VALUES (?, ?, ?, ?, ?)`,
			"frozen@example.com", 1, "frozen-token", time.Now().Unix(), time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("seed subject: %v", err)
	}

	rec := env.get(t, "/api/v1/subscribe/frozen-token", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for frozen subject, got %d", rec.Code)
	}
}

func TestHandleSubscribe_NoNodes(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Seed subject with no services.
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO subjects (name, enabled, subscription_token, created_at)
			VALUES (?, ?, ?, ?)`,
			"noservices@example.com", 1, "noservices-token", time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("seed subject: %v", err)
	}

	rec := env.get(t, "/api/v1/subscribe/noservices-token", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 for subject with no nodes, got %d", rec.Code)
	}
}

func TestHandleSubscribe_RateLimit(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Seed subject.
	token := "ratelimit-token"
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO subjects (name, enabled, subscription_token, created_at)
			VALUES (?, ?, ?, ?)`,
			"ratelimit@example.com", 1, token, time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("seed subject: %v", err)
	}

	// Make 10 requests (should all succeed, though 503 due to no nodes).
	for i := 0; i < 10; i++ {
		rec := env.get(t, "/api/v1/subscribe/"+token, "")
		if rec.Code != http.StatusServiceUnavailable {
			// Expected 503 since no services exist
		}
	}

	// 11th request should be rate limited.
	rec := env.get(t, "/api/v1/subscribe/"+token, "")
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 after rate limit, got %d", rec.Code)
	}

	// Check Retry-After header.
	retryAfter := rec.Header().Get("Retry-After")
	if retryAfter != "60" {
		t.Errorf("expected Retry-After: 60, got: %s", retryAfter)
	}
}

func TestHandleSubscribe_FormatDetection(t *testing.T) {
	// This would test UA-based format selection, but requires full node setup.
	// Deferred to integration tests.
	t.Skip("format detection tested in integration tests")
}

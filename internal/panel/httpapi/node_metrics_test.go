package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/nodes"
)

func TestHandleNodeMetrics(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Seed an admin and get session token
	env.seedAdmin(t, "admin", "password", "super_admin")
	token := env.login(t, "admin", "password")

	// Seed a node with metrics
	var nodeID int64
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (name, address, created_at, reconnect_count, last_reconcile_duration_ms, failed_reconcile_streak)
			VALUES (?, ?, ?, ?, ?, ?)`,
			"test-node", "test.example.com", time.Now().Unix(), 3, 1500, 1)
		if err != nil {
			return err
		}
		nodeID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}

	// Seed RTT samples
	now := time.Unix(1_700_000_000, 0).UTC()
	rtts := []int64{20, 25, 30, 35, 40}
	for i, rtt := range rtts {
		err = nodes.RecordRTT(ctx, env.store, nodeID, rtt, now.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatalf("RecordRTT: %v", err)
		}
	}

	// Get metrics
	rec := env.get(t, "/api/v1/nodes/"+itoa64(nodeID)+"/metrics", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp NodeMetricsJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.ReconnectCount != 3 {
		t.Errorf("ReconnectCount = %d, want 3", resp.ReconnectCount)
	}
	if resp.LastReconcileDurationMs == nil || *resp.LastReconcileDurationMs != 1500 {
		t.Errorf("LastReconcileDurationMs = %v, want 1500", resp.LastReconcileDurationMs)
	}
	if resp.FailedReconcileStreak != 1 {
		t.Errorf("FailedReconcileStreak = %d, want 1", resp.FailedReconcileStreak)
	}
	// Average of 20, 25, 30, 35, 40 = 30
	if resp.AvgRTTMs == nil || *resp.AvgRTTMs != 30 {
		t.Errorf("AvgRTTMs = %v, want 30", resp.AvgRTTMs)
	}
}

func TestHandleNodeMetrics_NoRTTData(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Seed an admin and get session token
	env.seedAdmin(t, "admin", "password", "super_admin")
	token := env.login(t, "admin", "password")

	// Seed a node with no RTT data
	var nodeID int64
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (name, address, created_at)
			VALUES (?, ?, ?)`,
			"test-node", "test.example.com", time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}

	// Get metrics
	rec := env.get(t, "/api/v1/nodes/"+itoa64(nodeID)+"/metrics", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp NodeMetricsJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.ReconnectCount != 0 {
		t.Errorf("ReconnectCount = %d, want 0", resp.ReconnectCount)
	}
	if resp.LastReconcileDurationMs != nil {
		t.Errorf("LastReconcileDurationMs = %v, want nil", resp.LastReconcileDurationMs)
	}
	if resp.AvgRTTMs != nil {
		t.Errorf("AvgRTTMs = %v, want nil (no samples)", resp.AvgRTTMs)
	}
}

func TestHandleNodeMetrics_InvalidNodeID(t *testing.T) {
	env := newTestEnv(t)

	env.seedAdmin(t, "admin", "password", "super_admin")
	token := env.login(t, "admin", "password")

	rec := env.get(t, "/api/v1/nodes/invalid/metrics", token)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid node ID, got %d", rec.Code)
	}
}

func TestHandleNodeMetrics_RequiresAuth(t *testing.T) {
	env := newTestEnv(t)

	rec := env.get(t, "/api/v1/nodes/1/metrics", "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d", rec.Code)
	}
}

func TestHandleNodeMetrics_NullValues(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Seed an admin and get session token
	env.seedAdmin(t, "admin", "password", "super_admin")
	token := env.login(t, "admin", "password")

	// Seed a node with NULL last_reconcile_duration_ms
	var nodeID int64
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (name, address, created_at, reconnect_count, last_reconcile_duration_ms, failed_reconcile_streak)
			VALUES (?, ?, ?, ?, NULL, ?)`,
			"test-node", "test.example.com", time.Now().Unix(), 0, 0)
		if err != nil {
			return err
		}
		nodeID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}

	// Get metrics
	rec := env.get(t, "/api/v1/nodes/"+itoa64(nodeID)+"/metrics", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify JSON encoding handles null correctly
	body := rec.Body.String()
	if body == "" {
		t.Fatal("empty response body")
	}

	var resp NodeMetricsJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.LastReconcileDurationMs != nil {
		t.Errorf("LastReconcileDurationMs = %v, want nil", resp.LastReconcileDurationMs)
	}
}

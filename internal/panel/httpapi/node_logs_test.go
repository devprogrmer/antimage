package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// TestGetNodeLogs_ThreeSources proves the timeline the browser reads at
// `/api/v1/nodes/{id}/logs` combines the three things the panel actually
// knows about a node: its current last_error, apply-run steps that failed
// (with the step's stderr), and audit records targeting this node.
// If any of those channels stops being reported, an operator investigating a
// node during an incident silently loses one of the three views they need.
func TestGetNodeLogs_ThreeSources(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	nodeID := int64(42)
	ctx := context.Background()

	base := time.Now().Add(-time.Hour)
	err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (id, name, address, status, last_error, last_seen_at, created_at)
			 VALUES (?, 'nyc-1', '10.0.0.7', 'degraded', 'panel-side: adapter refused', ?, ?)`,
			nodeID, base.Add(50*time.Minute).Unix(), base.Unix())
		if err != nil {
			return err
		}
		// A failing apply-run step -- the stderr the operator wants to read.
		res, err := tx.ExecContext(ctx,
			`INSERT INTO node_apply_runs (node_id, target_revision, started_at, outcome)
			 VALUES (?, 5, ?, 'failed')`, nodeID, base.Add(30*time.Minute).Unix())
		if err != nil {
			return err
		}
		runID, _ := res.LastInsertId()
		_, err = tx.ExecContext(ctx,
			`INSERT INTO node_apply_steps (run_id, seq, step_kind, disruption, outcome, error, duration_ms)
			 VALUES (?, 1, 'reload', 'reload', 'failed', 'systemctl reload xray: exit 1', 220)`,
			runID)
		if err != nil {
			return err
		}
		// A converged step MUST NOT appear -- an operator does not need every
		// successful reload cluttering the timeline of the one that broke.
		_, err = tx.ExecContext(ctx,
			`INSERT INTO node_apply_steps (run_id, seq, step_kind, disruption, outcome, error, duration_ms)
			 VALUES (?, 2, 'reload', 'reload', 'ok', '', 40)`, runID)
		if err != nil {
			return err
		}
		// An audit row against this node.
		_, err = tx.ExecContext(ctx,
			`INSERT INTO audit_log (at, actor_type, actor_admin_id, actor_label, action,
			                        target_type, target_id, after_json, result)
			 VALUES (?, 'admin', 1, 'testadmin', 'node.disable', 'node', ?, ?, 'ok')`,
			base.Add(20*time.Minute).Unix(), nodeID, `{"reason":"planned maintenance"}`)
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/nodes/42/logs?limit=50", nil)
	req = req.WithContext(withActor(req.Context(), actor))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "42")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleGetNodeLogs(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	var got struct {
		Logs []nodeLogEntry `json:"logs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("parse: %v", err)
	}
	sources := map[string]int{}
	for _, l := range got.Logs {
		sources[l.Source]++
	}
	if sources["agent"] < 1 {
		t.Errorf("no agent log line for last_error: %+v", got.Logs)
	}
	if sources["apply"] < 1 {
		t.Errorf("no apply-run stderr line: %+v", got.Logs)
	}
	if sources["audit"] < 1 {
		t.Errorf("no audit line: %+v", got.Logs)
	}

	// The converged step's message must not show up. Any test that just
	// counts three lines would pass while the noise-suppression it depends
	// on is broken.
	for _, l := range got.Logs {
		if l.Source == "apply" && l.Level != "error" {
			t.Errorf("apply line at non-error level: %+v", l)
		}
	}

	// Timestamps must sort newest-first across sources. An audit row from 20
	// minutes ago has to come after last_error from 10 minutes ago.
	for i := 1; i < len(got.Logs); i++ {
		if got.Logs[i-1].Timestamp < got.Logs[i].Timestamp {
			t.Errorf("timeline not descending at %d: %d before %d",
				i, got.Logs[i-1].Timestamp, got.Logs[i].Timestamp)
		}
	}

	// The audit line's rendered message has to include the reason from
	// after_json -- "node.disable" alone doesn't say why, and the UI won't
	// render the raw JSON here.
	var sawReason bool
	for _, l := range got.Logs {
		if l.Source == "audit" && contains(l.Message, "planned maintenance") {
			sawReason = true
		}
	}
	if !sawReason {
		t.Errorf("audit reason not surfaced: %+v", got.Logs)
	}
}

// TestGetNodeLogs_NotFound_MissingNode covers the case an operator would
// otherwise hit by mistyping a node id -- must be a real 404, not an empty
// list that reads as "this node is quiet".
func TestGetNodeLogs_NotFound_MissingNode(t *testing.T) {
	deps, _, actor := setupTestDeps(t)
	req := httptest.NewRequest("GET", "/api/v1/nodes/999/logs", nil)
	req = req.WithContext(withActor(req.Context(), actor))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "999")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleGetNodeLogs(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", w.Code)
	}
}

// TestGetNodeLogs_LimitClamped defends the audit_log against a caller asking
// for every row in it against one node id. 500 is the hard ceiling and the
// browser never asks for that many, but nothing in HTTP stops a caller from
// trying.
func TestGetNodeLogs_LimitClamped(t *testing.T) {
	if got := parseLimit("100000", 50, 500); got != 500 {
		t.Errorf("parseLimit clamp: got %d, want 500", got)
	}
	if got := parseLimit("", 50, 500); got != 50 {
		t.Errorf("parseLimit default: got %d, want 50", got)
	}
	if got := parseLimit("-5", 50, 500); got != 50 {
		t.Errorf("parseLimit rejects negative: got %d, want 50", got)
	}
}

func contains(s, needle string) bool {
	for i := 0; i+len(needle) <= len(s); i++ {
		if s[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/amyrm/antimage/internal/panel/rbac"
)

// TestSubjectActivity_UnionAndSort proves the two sources merge into one
// timeline sorted newest-first. Without this, ActivityTimeline would render
// either half of the truth, and an operator investigating a customer would
// have to know to piece two views together to see what happened.
func TestSubjectActivity_UnionAndSort(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	actor.Perms[rbac.PermSubjectRead] = struct{}{}

	subjectID := int64(21)
	ctx := context.Background()
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO subjects (id, name, enabled, created_at)
			 VALUES (?, 'customer-21', 1, ?)`,
			subjectID, time.Now().Unix()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (id, name, address, status, created_at)
			 VALUES (5, 'nyc-1', '10.0.0.7', 'online', ?)`, time.Now().Unix()); err != nil {
			return err
		}
		// A connection at t=100.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO connection_audit_log (subject_id, node_id, event_type, source_ip, timestamp)
			 VALUES (?, 5, 'connect', '1.2.3.4', 100)`, subjectID); err != nil {
			return err
		}
		// A newer admin action at t=200.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO audit_log (at, actor_type, actor_admin_id, actor_label, action,
			                        target_type, target_id, after_json, result)
			 VALUES (200, 'admin', 1, 'op', 'subject.disable', 'subject', ?,
			         '{"reason":"quota exceeded"}', 'ok')`, subjectID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/subjects/21/activity", nil)
	req = req.WithContext(withActor(req.Context(), actor))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "21")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleGetSubjectActivity(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Activities []subjectActivity `json:"activities"`
		HasMore    bool              `json:"has_more"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got.Activities) != 2 {
		t.Fatalf("got %d, want 2 (union of both sources)", len(got.Activities))
	}
	// Newest first: audit at 200 before connection at 100. If a caller only
	// reads the first entry, they must read the more recent one.
	if got.Activities[0].EventType != "disabled" || got.Activities[0].Timestamp != 200 {
		t.Errorf("first entry should be the admin action; got %+v", got.Activities[0])
	}
	if got.Activities[1].EventType != "connection_start" || got.Activities[1].Timestamp != 100 {
		t.Errorf("second entry should be the connection; got %+v", got.Activities[1])
	}
	// The audit reason has to be surfaced in the details -- "subject.disable"
	// alone doesn't explain why.
	if !strings.Contains(got.Activities[0].Details, "quota exceeded") {
		t.Errorf("audit reason missing: %q", got.Activities[0].Details)
	}
}

// TestSubjectActivity_FilterOnlyConnection proves the client-side filter
// name for connection events reaches SQL, so a filter for "connection_start"
// does not pull the audit table too.
func TestSubjectActivity_FilterOnlyConnection(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	actor.Perms[rbac.PermSubjectRead] = struct{}{}

	subjectID := int64(31)
	ctx := context.Background()
	_ = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO subjects (id, name, enabled, created_at)
			 VALUES (?, 'customer-31', 1, ?)`,
			subjectID, time.Now().Unix())
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO nodes (id, name, address, status, created_at) VALUES (9, 'n', 'x', 'online', ?)`,
			time.Now().Unix())
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO connection_audit_log (subject_id, node_id, event_type, source_ip, timestamp)
			 VALUES (?, 9, 'connect', '1.1.1.1', 100),
			        (?, 9, 'disconnect', '1.1.1.1', 200)`,
			subjectID, subjectID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO audit_log (at, actor_type, actor_admin_id, actor_label, action,
			                        target_type, target_id, result)
			 VALUES (150, 'admin', 1, 'op', 'subject.disable', 'subject', ?, 'ok')`, subjectID)
		return err
	})

	req := httptest.NewRequest("GET",
		"/api/v1/subjects/31/activity?event_type=connection_start", nil)
	req = req.WithContext(withActor(req.Context(), actor))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "31")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleGetSubjectActivity(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var got struct {
		Activities []subjectActivity `json:"activities"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	for _, a := range got.Activities {
		if a.EventType != "connection_start" {
			t.Errorf("filter leaked %q into a connection_start-only query", a.EventType)
		}
	}
	if len(got.Activities) != 1 {
		t.Errorf("got %d connection_start rows, want 1", len(got.Activities))
	}
}

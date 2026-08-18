package nodes

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

func setSeen(t *testing.T, s *store.Store, nodeID int64, status string, seen time.Time) {
	t.Helper()
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE nodes SET status = ?, last_seen_at = ? WHERE id = ?`,
			status, seen.Unix(), nodeID)
		return err
	})
	if err != nil {
		t.Fatalf("setSeen: %v", err)
	}
}

func statusOf(t *testing.T, s *store.Store, nodeID int64) string {
	t.Helper()
	var status string
	if err := s.Read().QueryRow(
		`SELECT status FROM nodes WHERE id = ?`, nodeID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	return status
}

func TestStaleOnlineNodeBecomesOffline(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	setSeen(t, s, nodeID, "online", now.Add(-OfflineAfter-time.Second))

	sw := NewSweeper(s, func() time.Time { return now })
	marked, err := sw.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if marked != 1 {
		t.Errorf("marked = %d, want 1", marked)
	}
	if got := statusOf(t, s, nodeID); got != "offline" {
		t.Errorf("status = %q, want offline", got)
	}
}

func TestFreshNodeIsLeftAlone(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	setSeen(t, s, nodeID, "online", now.Add(-10*time.Second))

	sw := NewSweeper(s, func() time.Time { return now })
	marked, err := sw.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if marked != 0 {
		t.Errorf("marked = %d, want 0", marked)
	}
	if got := statusOf(t, s, nodeID); got != "online" {
		t.Errorf("status = %q, want online", got)
	}
}

// A node the admin disabled, or one that never enrolled, must not be
// relabelled by a timer.
func TestDisabledAndPendingNodesAreNotSwept(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	for _, status := range []string{"disabled", "pending", "enrolling"} {
		s, nodeID := newNodeFixture(t)
		setSeen(t, s, nodeID, status, now.Add(-24*time.Hour))

		sw := NewSweeper(s, func() time.Time { return now })
		marked, err := sw.Sweep(context.Background())
		if err != nil {
			t.Fatalf("Sweep: %v", err)
		}
		if marked != 0 {
			t.Errorf("%s node: marked = %d, want 0", status, marked)
		}
		if got := statusOf(t, s, nodeID); got != status {
			t.Errorf("%s node became %q", status, got)
		}
	}
}

// Integrity is a fault that needs an operator, so a heartbeat gap must not
// downgrade it to the ordinary offline state and hide it.
func TestIntegrityStateSurvivesTheSweep(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	setSeen(t, s, nodeID, "integrity", now.Add(-24*time.Hour))

	sw := NewSweeper(s, func() time.Time { return now })
	marked, err := sw.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if marked != 0 {
		t.Errorf("marked = %d, want 0", marked)
	}
	if got := statusOf(t, s, nodeID); got != "integrity" {
		t.Errorf("status = %q, want integrity preserved", got)
	}
}

// A degraded node is still a node that was reporting, so a heartbeat gap is
// news about it and the sweep applies.
func TestDegradedNodeIsSwept(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	setSeen(t, s, nodeID, "degraded", now.Add(-OfflineAfter-time.Second))

	sw := NewSweeper(s, func() time.Time { return now })
	if _, err := sw.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if got := statusOf(t, s, nodeID); got != "offline" {
		t.Errorf("status = %q, want offline", got)
	}
}

func TestTransitionIsAudited(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	setSeen(t, s, nodeID, "online", now.Add(-OfflineAfter-time.Second))

	sw := NewSweeper(s, func() time.Time { return now })
	if _, err := sw.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	var (
		action, actorType, label string
		targetID                 sql.NullInt64
	)
	if err := s.Read().QueryRow(
		`SELECT action, actor_type, actor_label, target_id
		   FROM audit_log ORDER BY id DESC LIMIT 1`,
	).Scan(&action, &actorType, &label, &targetID); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if action != "node.offline" || actorType != "system" || label != "sweeper" {
		t.Errorf("audit = %s/%s/%s, want node.offline/system/sweeper", action, actorType, label)
	}
	if !targetID.Valid || targetID.Int64 != nodeID {
		t.Errorf("audit target_id = %v, want %d", targetID, nodeID)
	}
}

// A node that never reported a heartbeat but did reach 'online' still has a
// NULL last_seen_at; the sweep must treat that as stale rather than skip it.
func TestNodeWithNoHeartbeatEverIsSwept(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`UPDATE nodes SET status = 'online', last_seen_at = NULL WHERE id = ?`, nodeID)
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	sw := NewSweeper(s, func() time.Time { return now })
	if marked, err := sw.Sweep(context.Background()); err != nil || marked != 1 {
		t.Fatalf("Sweep: marked = %d, err = %v, want 1, nil", marked, err)
	}
	if got := statusOf(t, s, nodeID); got != "offline" {
		t.Errorf("status = %q, want offline", got)
	}
}

// WithThreshold exists so the acceptance suite can exercise the real sweep
// path without a 90-second wall-clock wait. It must actually change the
// cutoff, and a non-positive value must fall back to the production default
// rather than sweeping everything immediately.
func TestSweeperThresholdIsConfigurableAndDefaultsSafely(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0).UTC()

	setSeen(t, s, nodeID, "online", base.Add(-10*time.Second))

	now := func() time.Time { return base }

	// Ten seconds of silence is far inside the 90s default: nothing swept.
	n, err := NewSweeper(s, now).Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n != 0 {
		t.Fatalf("default threshold swept %d nodes after 10s of silence", n)
	}

	// A non-positive threshold must NOT mean "sweep everything now".
	n, err = NewSweeper(s, now).WithThreshold(0).Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n != 0 {
		t.Fatalf("WithThreshold(0) swept %d nodes; zero must select the default", n)
	}

	// A short threshold sweeps the same node.
	n, err = NewSweeper(s, now).WithThreshold(5 * time.Second).Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("WithThreshold(5s) swept %d nodes, want 1", n)
	}
	if got := statusOf(t, s, nodeID); got != "offline" {
		t.Errorf("status = %q, want offline", got)
	}
}

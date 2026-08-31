package observability

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// checkNodeHealth and checkEnforcementFailures both selected columns that exist
// in no migration -- last_reconcile_at in both, plus last_reconcile_error in the
// second. Every call returned "no such column", sweep() swallowed it into a log
// line, and so neither the node-offline nor the enforcement-failure alert had
// ever fired in the product's life.
//
// A query against a column that does not exist only fails at runtime, so the
// compiler cannot catch a regression here and these tests must. They assert the
// error return, not just the alert, because that is the signal sweep() discards.

func nodeWithStaleHeartbeat(t *testing.T, s interface {
	Write(context.Context, func(*sql.Tx) error) error
}, name, status string, lastSeen int64, streak int) int64 {
	t.Helper()
	var id int64
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			INSERT INTO nodes (name, address, status, last_seen_at,
			                   failed_reconcile_streak, last_error, created_at)
			VALUES (?, '127.0.0.1', ?, ?, ?, 'apply failed: exit 1', 1000)`,
			name, status, lastSeen, streak)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}
	return id
}

func alertCount(t *testing.T, s *sql.DB, alertType string, nodeID int64) int {
	t.Helper()
	var n int
	if err := s.QueryRow(
		`SELECT COUNT(*) FROM alerts
		  WHERE target_type = 'node' AND target_id = ? AND alert_type = ?`,
		nodeID, alertType).Scan(&n); err != nil {
		t.Fatalf("count alerts: %v", err)
	}
	return n
}

func TestNodeOfflineAlertActuallyFires(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()

	// Last seen well past the offline threshold.
	stale := now.Add(-2 * NodeOfflineThreshold).Unix()
	nodeID := nodeWithStaleHeartbeat(t, s, "stale-node", "online", stale, 0)

	if err := NewSweeper(s).checkNodeHealth(ctx, now); err != nil {
		t.Fatalf("checkNodeHealth returned an error, so the alert never fires "+
			"and sweep() would hide it in a log line: %v", err)
	}

	if got := alertCount(t, s.Read(), "node_offline", nodeID); got != 1 {
		t.Errorf("node_offline alerts for node %d = %d, want 1", nodeID, got)
	}
}

func TestEnforcementFailureAlertActuallyFires(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()

	nodeID := nodeWithStaleHeartbeat(t, s, "failing-node", "degraded",
		now.Add(-time.Minute).Unix(), EnforcementFailureThreshold+2)

	if err := NewSweeper(s).checkEnforcementFailures(ctx, now); err != nil {
		t.Fatalf("checkEnforcementFailures returned an error, so the alert never "+
			"fires and sweep() would hide it in a log line: %v", err)
	}

	if got := alertCount(t, s.Read(), "enforcement_failure", nodeID); got != 1 {
		t.Errorf("enforcement_failure alerts for node %d = %d, want 1", nodeID, got)
	}
}

// A node that has never checked in has a NULL last_seen_at. Scanning that into
// a plain int64 fails the row, and reporting it as time.Unix(0,0) would date the
// alert to 1970.
func TestEnforcementFailureHandlesANodeNeverSeen(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()

	var nodeID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			INSERT INTO nodes (name, address, status, failed_reconcile_streak, created_at)
			VALUES ('never-seen', '127.0.0.1', 'degraded', ?, 1000)`,
			EnforcementFailureThreshold+1)
		if err != nil {
			return err
		}
		nodeID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}

	if err := NewSweeper(s).checkEnforcementFailures(ctx, now); err != nil {
		t.Fatalf("checkEnforcementFailures: %v", err)
	}
	if got := alertCount(t, s.Read(), "enforcement_failure", nodeID); got != 1 {
		t.Errorf("a node that has never reported still has a failure streak "+
			"worth alerting on; alerts = %d, want 1", got)
	}
}

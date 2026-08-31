package nodes_test

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/shared/secrets"
	"github.com/amyrm/antimage/internal/testutil/storetest"
)

// TestSP3AccountingEndToEnd verifies the complete accounting flow:
// usage ingestion → quota tracking → enforcement → reset.
func TestSP3AccountingEndToEnd(t *testing.T) {
	st := mustOpenStore(t)
	defer st.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC).Unix()

	// Create a node, subject with quota, and service.
	var nodeID, subjectID, serviceID int64
	err := st.Write(ctx, func(tx *sql.Tx) error {
		// Create node.
		result, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (name, address, created_at) VALUES ('test-node', '10.0.0.1', ?)`, now)
		if err != nil {
			return err
		}
		nodeID, _ = result.LastInsertId()

		// Create subject with 1KB quota, reset time in future.
		result, err = tx.ExecContext(ctx, `
			INSERT INTO subjects (name, enabled, quota_bytes, quota_used_bytes, quota_reset_at, created_at)
			VALUES ('alice', 1, 1024, 0, ?, ?)`, now+86400, now)
		if err != nil {
			return err
		}
		subjectID, _ = result.LastInsertId()

		// Create a service so the subject appears in desired documents.
		result, err = tx.ExecContext(ctx, `
			INSERT INTO services (node_id, adapter_kind, enabled, params, created_at)
			VALUES (?, 'xray', 1, '{"protocol":"vless","port":443}', ?)`, nodeID, now)
		if err != nil {
			return err
		}
		serviceID, _ = result.LastInsertId()

		// Grant subject to service.
		_, err = tx.ExecContext(ctx, `
			INSERT INTO subject_services (subject_id, service_id) VALUES (?, ?)`, subjectID, serviceID)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Step 1: Ingest usage below quota (500 bytes).
	samples := []nodes.UsageDelta{{SubjectID: subjectID, UplinkBytes: 250, DownlinkBytes: 250}}
	if err := nodes.IngestUsageReport(ctx, st, nodeID, 1, samples, now+10); err != nil {
		t.Fatalf("ingest first report: %v", err)
	}

	// Verify usage updated.
	var usedBytes int64
	err = st.Read().QueryRowContext(ctx, `SELECT quota_used_bytes FROM subjects WHERE id = ?`, subjectID).Scan(&usedBytes)
	if err != nil {
		t.Fatalf("query usage: %v", err)
	}
	if usedBytes != 500 {
		t.Errorf("after first report: quota_used_bytes = %d, want 500", usedBytes)
	}

	// Step 2: Ingest more usage to exceed quota (600 bytes, total 1100).
	samples2 := []nodes.UsageDelta{{SubjectID: subjectID, UplinkBytes: 300, DownlinkBytes: 300}}
	if err := nodes.IngestUsageReport(ctx, st, nodeID, 2, samples2, now+20); err != nil {
		t.Fatalf("ingest second report: %v", err)
	}

	// Verify usage exceeded quota.
	err = st.Read().QueryRowContext(ctx, `SELECT quota_used_bytes FROM subjects WHERE id = ?`, subjectID).Scan(&usedBytes)
	if err != nil {
		t.Fatalf("query usage after exceed: %v", err)
	}
	if usedBytes != 1100 {
		t.Errorf("after exceeding quota: quota_used_bytes = %d, want 1100", usedBytes)
	}

	// Subject should still be enabled (sweeper hasn't run yet).
	var enabled int
	err = st.Read().QueryRowContext(ctx, `SELECT enabled FROM subjects WHERE id = ?`, subjectID).Scan(&enabled)
	if err != nil {
		t.Fatalf("query enabled before sweep: %v", err)
	}
	if enabled != 1 {
		t.Errorf("before sweep: enabled = %d, want 1", enabled)
	}

	// Step 3: Run quota enforcement sweeper.
	box := mustCreateBox(t)
	enforcer := &nodes.QuotaEnforcementSweeper{
		Store: st,
		Log:   slog.Default(),
		CommitFunc: func(ctx context.Context, nodeID int64, actor, reason string) error {
			_, err := nodes.CommitNodeChange(ctx, st, nodeID, audit.SystemActor(actor), "", reason, nil, nodes.WithUnsealer(box))
			return err
		},
	}
	if err := enforcer.Run(ctx, now+30); err != nil {
		t.Fatalf("quota enforcement sweep: %v", err)
	}

	// Verify subject frozen.
	var frozenAt sql.NullInt64
	var frozenReason sql.NullString
	err = st.Read().QueryRowContext(ctx,
		`SELECT enabled, frozen_at, frozen_reason FROM subjects WHERE id = ?`, subjectID).
		Scan(&enabled, &frozenAt, &frozenReason)
	if err != nil {
		t.Fatalf("query after freeze: %v", err)
	}
	if enabled != 0 {
		t.Errorf("after freeze: enabled = %d, want 0", enabled)
	}
	if !frozenAt.Valid || frozenAt.Int64 != now+30 {
		t.Errorf("after freeze: frozen_at = %v, want %d", frozenAt, now+30)
	}
	if !frozenReason.Valid || frozenReason.String != "quota_exceeded" {
		t.Errorf("after freeze: frozen_reason = %v, want 'quota_exceeded'", frozenReason)
	}

	// Verify node revision bumped (freeze triggers document change).
	var desiredRevision int64
	err = st.Read().QueryRowContext(ctx, `SELECT desired_revision FROM nodes WHERE id = ?`, nodeID).Scan(&desiredRevision)
	if err != nil {
		t.Fatalf("query revision after freeze: %v", err)
	}
	if desiredRevision == 0 {
		t.Errorf("after freeze: desired_revision = 0, expected > 0 (document changed)")
	}

	// Step 4: Simulate time advancing to reset time.
	resetTime := now + 86400

	// Run quota reset sweeper.
	resetter := &nodes.QuotaResetSweeper{
		Store: st,
		Log:   slog.Default(),
		CommitFunc: func(ctx context.Context, nodeID int64, actor, reason string) error {
			_, err := nodes.CommitNodeChange(ctx, st, nodeID, audit.SystemActor(actor), "", reason, nil, nodes.WithUnsealer(box))
			return err
		},
	}
	if err := resetter.Run(ctx, resetTime); err != nil {
		t.Fatalf("quota reset sweep: %v", err)
	}

	// Verify usage reset and subject unfrozen.
	var quotaResetAt int64
	err = st.Read().QueryRowContext(ctx,
		`SELECT enabled, quota_used_bytes, quota_reset_at, frozen_at FROM subjects WHERE id = ?`,
		subjectID).Scan(&enabled, &usedBytes, &quotaResetAt, &frozenAt)
	if err != nil {
		t.Fatalf("query after reset: %v", err)
	}
	if enabled != 1 {
		t.Errorf("after reset: enabled = %d, want 1 (unfrozen)", enabled)
	}
	if usedBytes != 0 {
		t.Errorf("after reset: quota_used_bytes = %d, want 0", usedBytes)
	}
	if frozenAt.Valid {
		// frozen_at should be NULL after unfreezing.
		t.Errorf("after reset: frozen_at should be NULL, got %v", frozenAt.Int64)
	}
	// quota_reset_at should advance by period (30 days).
	expectedNextReset := resetTime + (30 * 24 * 60 * 60)
	if quotaResetAt != expectedNextReset {
		t.Errorf("after reset: quota_reset_at = %d, want %d", quotaResetAt, expectedNextReset)
	}
}

// TestSP3IdempotencyAcrossRetries verifies that duplicate sequence numbers
// are correctly ignored (at-least-once delivery).
func TestSP3IdempotencyAcrossRetries(t *testing.T) {
	st := mustOpenStore(t)
	defer st.Close()
	ctx := context.Background()
	now := time.Now().Unix()

	var nodeID, subjectID int64
	err := st.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (name, address, created_at) VALUES ('node-1', '10.0.0.2', ?)`, now)
		if err != nil {
			return err
		}
		nodeID, _ = result.LastInsertId()

		result, err = tx.ExecContext(ctx, `
			INSERT INTO subjects (name, enabled, quota_used_bytes, created_at)
			VALUES ('bob', 1, 0, ?)`, now)
		if err != nil {
			return err
		}
		subjectID, _ = result.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Ingest usage: 100 bytes.
	samples := []nodes.UsageDelta{{SubjectID: subjectID, UplinkBytes: 50, DownlinkBytes: 50}}
	if err := nodes.IngestUsageReport(ctx, st, nodeID, 1, samples, now+1); err != nil {
		t.Fatalf("ingest seq=1: %v", err)
	}

	// Verify 100 bytes recorded.
	var usedBytes int64
	err = st.Read().QueryRowContext(ctx, `SELECT quota_used_bytes FROM subjects WHERE id = ?`, subjectID).Scan(&usedBytes)
	if err != nil {
		t.Fatalf("query usage: %v", err)
	}
	if usedBytes != 100 {
		t.Errorf("after seq=1: quota_used_bytes = %d, want 100", usedBytes)
	}

	// Re-ingest same sequence (simulated retry): 200 bytes but same seq.
	samples2 := []nodes.UsageDelta{{SubjectID: subjectID, UplinkBytes: 100, DownlinkBytes: 100}}
	if err := nodes.IngestUsageReport(ctx, st, nodeID, 1, samples2, now+2); err != nil {
		t.Fatalf("re-ingest seq=1: %v", err)
	}

	// Verify still 100 bytes (idempotent, second report ignored).
	err = st.Read().QueryRowContext(ctx, `SELECT quota_used_bytes FROM subjects WHERE id = ?`, subjectID).Scan(&usedBytes)
	if err != nil {
		t.Fatalf("query usage after retry: %v", err)
	}
	if usedBytes != 100 {
		t.Errorf("after retry seq=1: quota_used_bytes = %d, want 100 (idempotent)", usedBytes)
	}

	// Verify only one delta row exists.
	var deltaCount int
	err = st.Read().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM usage_deltas WHERE node_id = ? AND sequence = 1`, nodeID).Scan(&deltaCount)
	if err != nil {
		t.Fatalf("query delta count: %v", err)
	}
	if deltaCount != 1 {
		t.Errorf("delta count for seq=1: %d, want 1 (idempotent)", deltaCount)
	}

	// Ingest new sequence: should apply.
	samples3 := []nodes.UsageDelta{{SubjectID: subjectID, UplinkBytes: 75, DownlinkBytes: 75}}
	if err := nodes.IngestUsageReport(ctx, st, nodeID, 2, samples3, now+3); err != nil {
		t.Fatalf("ingest seq=2: %v", err)
	}

	// Verify 250 bytes total.
	err = st.Read().QueryRowContext(ctx, `SELECT quota_used_bytes FROM subjects WHERE id = ?`, subjectID).Scan(&usedBytes)
	if err != nil {
		t.Fatalf("query usage after seq=2: %v", err)
	}
	if usedBytes != 250 {
		t.Errorf("after seq=2: quota_used_bytes = %d, want 250", usedBytes)
	}
}

// TestSP3RollupAggregation verifies hourly and daily rollups aggregate correctly.
func TestSP3RollupAggregation(t *testing.T) {
	st := mustOpenStore(t)
	defer st.Close()
	ctx := context.Background()

	var nodeID, subjectID int64
	baseTime := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC).Unix() // 10:00 UTC

	err := st.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (name, address, created_at) VALUES ('node-rollup', '10.0.0.3', ?)`, baseTime)
		if err != nil {
			return err
		}
		nodeID, _ = result.LastInsertId()

		result, err = tx.ExecContext(ctx, `
			INSERT INTO subjects (name, enabled, quota_used_bytes, created_at)
			VALUES ('charlie', 1, 0, ?)`, baseTime)
		if err != nil {
			return err
		}
		subjectID, _ = result.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Ingest multiple deltas in the same hour.
	samples1 := []nodes.UsageDelta{{SubjectID: subjectID, UplinkBytes: 100, DownlinkBytes: 100}}
	if err := nodes.IngestUsageReport(ctx, st, nodeID, 1, samples1, baseTime+600); err != nil { // 10:10
		t.Fatalf("ingest 1: %v", err)
	}

	samples2 := []nodes.UsageDelta{{SubjectID: subjectID, UplinkBytes: 150, DownlinkBytes: 150}}
	if err := nodes.IngestUsageReport(ctx, st, nodeID, 2, samples2, baseTime+1200); err != nil { // 10:20
		t.Fatalf("ingest 2: %v", err)
	}

	samples3 := []nodes.UsageDelta{{SubjectID: subjectID, UplinkBytes: 50, DownlinkBytes: 50}}
	if err := nodes.IngestUsageReport(ctx, st, nodeID, 3, samples3, baseTime+1800); err != nil { // 10:30
		t.Fatalf("ingest 3: %v", err)
	}

	// Run hourly rollup (at 11:00, covering 10:xx hour).
	rollupTime := baseTime + 3600 // 11:00
	if err := nodes.RollupHourly(ctx, st, rollupTime); err != nil {
		t.Fatalf("hourly rollup: %v", err)
	}

	// Verify hourly rollup aggregated all three deltas.
	var uplink, downlink int64
	hourStart := (baseTime / 3600) * 3600 // Hour boundary for 10:00
	err = st.Read().QueryRowContext(ctx,
		`SELECT uplink_bytes, downlink_bytes FROM usage_rollups_hourly
		 WHERE subject_id = ? AND hour_start = ?`, subjectID, hourStart).Scan(&uplink, &downlink)
	if err != nil {
		t.Fatalf("query hourly rollup: %v", err)
	}
	if uplink != 300 || downlink != 300 {
		t.Errorf("hourly rollup: uplink=%d, downlink=%d, want 300/300", uplink, downlink)
	}

	// Run daily rollup (at end of day).
	dayStart := (baseTime / 86400) * 86400
	dailyRollupTime := dayStart + 86400 + 1800 // Next day at 00:30
	if err := nodes.RollupDaily(ctx, st, dailyRollupTime); err != nil {
		t.Fatalf("daily rollup: %v", err)
	}

	// Verify daily rollup.
	err = st.Read().QueryRowContext(ctx,
		`SELECT uplink_bytes, downlink_bytes FROM usage_rollups_daily
		 WHERE subject_id = ? AND day_start = ?`, subjectID, dayStart).Scan(&uplink, &downlink)
	if err != nil {
		t.Fatalf("query daily rollup: %v", err)
	}
	if uplink != 300 || downlink != 300 {
		t.Errorf("daily rollup: uplink=%d, downlink=%d, want 300/300", uplink, downlink)
	}
}

// TestSP3SubjectFrozenOmittedFromDocument verifies that frozen subjects
// do not appear in desired documents (enforcement by omission).
func TestSP3SubjectFrozenOmittedFromDocument(t *testing.T) {
	st := mustOpenStore(t)
	defer st.Close()
	ctx := context.Background()
	box := mustCreateBox(t)
	now := time.Now().Unix()

	var nodeID, enabledSubjectID, frozenSubjectID, serviceID int64
	err := st.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (name, address, created_at) VALUES ('node-omit', '10.0.0.4', ?)`, now)
		if err != nil {
			return err
		}
		nodeID, _ = result.LastInsertId()

		// Enabled subject.
		result, err = tx.ExecContext(ctx, `
			INSERT INTO subjects (name, enabled, quota_used_bytes, created_at)
			VALUES ('enabled-user', 1, 0, ?)`, now)
		if err != nil {
			return err
		}
		enabledSubjectID, _ = result.LastInsertId()

		// Frozen subject.
		result, err = tx.ExecContext(ctx, `
			INSERT INTO subjects (name, enabled, frozen_at, frozen_reason, quota_used_bytes, created_at)
			VALUES ('frozen-user', 0, ?, 'quota_exceeded', 0, ?)`, now, now)
		if err != nil {
			return err
		}
		frozenSubjectID, _ = result.LastInsertId()

		// Service.
		result, err = tx.ExecContext(ctx, `
			INSERT INTO services (node_id, adapter_kind, enabled, params, created_at)
			VALUES (?, 'xray', 1, '{"protocol":"vless","port":443}', ?)`, nodeID, now)
		if err != nil {
			return err
		}
		serviceID, _ = result.LastInsertId()

		// Grant both subjects to service.
		_, err = tx.ExecContext(ctx, `
			INSERT INTO subject_services (subject_id, service_id) VALUES (?, ?), (?, ?)`,
			enabledSubjectID, serviceID, frozenSubjectID, serviceID)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Build desired snapshot.
	var snap *nodes.Snapshot
	err = st.Write(ctx, func(tx *sql.Tx) error {
		var err error
		snap, err = nodes.BuildDesiredSnapshot(ctx, tx, nodeID, nodes.WithUnsealer(box))
		return err
	})
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}

	// Verify only enabled subject appears in document.
	if len(snap.Document.Subjects) != 1 {
		t.Fatalf("document subjects count = %d, want 1 (only enabled)", len(snap.Document.Subjects))
	}
	if snap.Document.Subjects[0].ID != enabledSubjectID {
		t.Errorf("document subject ID = %d, want %d (enabled subject)", snap.Document.Subjects[0].ID, enabledSubjectID)
	}
}

func mustOpenStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := storetest.OpenCopy(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func mustCreateBox(t *testing.T) *secrets.Box {
	t.Helper()
	key := make([]byte, secrets.KeySize)
	for i := range key {
		key[i] = 0x42 // Arbitrary test key
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		t.Fatalf("create box: %v", err)
	}
	return box
}

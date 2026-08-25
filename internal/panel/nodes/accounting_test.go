package nodes

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/testutil/storetest"
)

func TestIngestUsageReportAppliesDeltasOnce(t *testing.T) {
	st := mustOpen(t)
	defer st.Close()
	ctx := context.Background()

	// Create a node and a subject to receive usage.
	var nodeID, subjectID int64
	err := st.Write(ctx, func(tx *sql.Tx) error {
		// Create node.
		result, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (name, address, created_at) VALUES ('test-node', '127.0.0.1', 1000)`)
		if err != nil {
			return err
		}
		nodeID, _ = result.LastInsertId()

		// Create subject.
		result, err = tx.ExecContext(ctx, `
			INSERT INTO subjects (name, enabled, quota_used_bytes, created_at)
			VALUES ('test-user', 1, 0, 1000)`)
		if err != nil {
			return err
		}
		subjectID, _ = result.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("create node and subject: %v", err)
	}

	// Ingest a report with one sample.
	samples := []UsageDelta{{SubjectID: subjectID, UplinkBytes: 100, DownlinkBytes: 200}}
	if err := IngestUsageReport(ctx, st, nodeID, 1, samples, 1000); err != nil {
		t.Fatalf("ingest first report: %v", err)
	}

	// Check the delta was recorded and usage was updated.
	var count int64
	var usedBytes int64
	err = st.Read().QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(uplink_bytes + downlink_bytes), 0) FROM usage_deltas`).Scan(&count, &usedBytes)
	if err != nil {
		t.Fatalf("query deltas: %v", err)
	}
	if count != 1 {
		t.Errorf("delta count = %d, want 1", count)
	}
	if usedBytes != 300 {
		t.Errorf("total delta bytes = %d, want 300", usedBytes)
	}

	err = st.Read().QueryRowContext(ctx,
		`SELECT quota_used_bytes FROM subjects WHERE id = ?`, subjectID).Scan(&usedBytes)
	if err != nil {
		t.Fatalf("query subject: %v", err)
	}
	if usedBytes != 300 {
		t.Errorf("subject quota_used_bytes = %d, want 300", usedBytes)
	}

	// Re-ingest the same report (same node_id, sequence). Should be idempotent.
	if err := IngestUsageReport(ctx, st, nodeID, 1, samples, 1001); err != nil {
		t.Fatalf("ingest duplicate report: %v", err)
	}

	// Delta count should still be 1, usage unchanged.
	err = st.Read().QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(uplink_bytes + downlink_bytes), 0) FROM usage_deltas`).Scan(&count, &usedBytes)
	if err != nil {
		t.Fatalf("query deltas after duplicate: %v", err)
	}
	if count != 1 {
		t.Errorf("after duplicate: delta count = %d, want 1", count)
	}

	err = st.Read().QueryRowContext(ctx,
		`SELECT quota_used_bytes FROM subjects WHERE id = ?`, subjectID).Scan(&usedBytes)
	if err != nil {
		t.Fatalf("query subject after duplicate: %v", err)
	}
	if usedBytes != 300 {
		t.Errorf("after duplicate: subject quota_used_bytes = %d, want 300", usedBytes)
	}

	// Ingest a new sequence. Should apply.
	samples2 := []UsageDelta{{SubjectID: subjectID, UplinkBytes: 50, DownlinkBytes: 50}}
	if err := IngestUsageReport(ctx, st, nodeID, 2, samples2, 2000); err != nil {
		t.Fatalf("ingest second report: %v", err)
	}

	err = st.Read().QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(uplink_bytes + downlink_bytes), 0) FROM usage_deltas`).Scan(&count, &usedBytes)
	if err != nil {
		t.Fatalf("query deltas after second: %v", err)
	}
	if count != 2 {
		t.Errorf("after second report: delta count = %d, want 2", count)
	}
	if usedBytes != 400 {
		t.Errorf("after second report: total delta bytes = %d, want 400", usedBytes)
	}

	err = st.Read().QueryRowContext(ctx,
		`SELECT quota_used_bytes FROM subjects WHERE id = ?`, subjectID).Scan(&usedBytes)
	if err != nil {
		t.Fatalf("query subject after second: %v", err)
	}
	if usedBytes != 400 {
		t.Errorf("after second report: subject quota_used_bytes = %d, want 400", usedBytes)
	}
}

func TestPruneUsageDeltasRemovesOldRows(t *testing.T) {
	st := mustOpen(t)
	defer st.Close()
	ctx := context.Background()

	// Create a node and a subject.
	var nodeID, subjectID int64
	err := st.Write(ctx, func(tx *sql.Tx) error {
		// Create node.
		result, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (name, address, created_at) VALUES ('test-node-2', '127.0.0.2', 1000)`)
		if err != nil {
			return err
		}
		nodeID, _ = result.LastInsertId()

		// Create subject.
		result, err = tx.ExecContext(ctx, `
			INSERT INTO subjects (name, enabled, quota_used_bytes, created_at)
			VALUES ('test-user-2', 1, 0, 1000)`)
		if err != nil {
			return err
		}
		subjectID, _ = result.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("create node and subject: %v", err)
	}

	// Insert old and new deltas.
	samples := []UsageDelta{{SubjectID: subjectID, UplinkBytes: 100, DownlinkBytes: 100}}
	if err := IngestUsageReport(ctx, st, nodeID, 1, samples, 1000); err != nil {
		t.Fatalf("ingest old: %v", err)
	}
	if err := IngestUsageReport(ctx, st, nodeID, 2, samples, 10000); err != nil {
		t.Fatalf("ingest new: %v", err)
	}

	// Prune deltas older than 5000 seconds (cutoff at now=10000 - retention=5000).
	deleted, err := PruneUsageDeltas(ctx, st, 5000, 10000)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}

	// Only the new delta should remain.
	var count int64
	err = st.Read().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM usage_deltas`).Scan(&count)
	if err != nil {
		t.Fatalf("count after prune: %v", err)
	}
	if count != 1 {
		t.Errorf("after prune: delta count = %d, want 1", count)
	}
}

func mustOpen(t *testing.T) *store.Store {
	t.Helper()
	st, err := storetest.OpenCopy(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return st
}

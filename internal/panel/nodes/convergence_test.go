package nodes

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/store"
)

func bumpTo(t *testing.T, s *store.Store, nodeID int64, port int) *CommitResult {
	t.Helper()
	res, err := CommitNodeChange(context.Background(), s, nodeID,
		audit.SystemActor("test"), "req", "seed",
		func(tx *sql.Tx) error {
			_, err := tx.Exec(
				`INSERT INTO services (node_id, adapter_kind, params, enabled, created_at)
				 VALUES (?, 'stub', ?, 1, ?)`,
				nodeID, `{"port":`+itoa(port)+`}`, time.Now().Unix())
			return err
		})
	if err != nil {
		t.Fatalf("CommitNodeChange: %v", err)
	}
	return res
}

func TestConvergedRunAdvancesAppliedRevision(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	ctx := context.Background()
	commit := bumpTo(t, s, nodeID, 443)

	out, err := RecordApplyRun(ctx, s, ApplyRunInput{
		NodeID: nodeID, TargetRevision: commit.Revision,
		Converged: true, DocSHA256: commit.SHA256,
		Steps: []StepOutcome{{Seq: 1, Kind: "write_service", Disruption: "restart", OK: true}},
		Now:   time.Unix(1_700_000_000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("RecordApplyRun: %v", err)
	}
	if out.Status != "online" {
		t.Errorf("status = %q, want online", out.Status)
	}
	if out.AppliedRevision != commit.Revision {
		t.Errorf("applied_revision = %d, want %d", out.AppliedRevision, commit.Revision)
	}
}

// Invariant 7: partial application must NOT advance applied_revision.
func TestPartialRunLeavesAppliedRevisionBehind(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	ctx := context.Background()
	commit := bumpTo(t, s, nodeID, 443)

	out, err := RecordApplyRun(ctx, s, ApplyRunInput{
		NodeID: nodeID, TargetRevision: commit.Revision,
		Converged: false, Err: "step 2 failed", DocSHA256: commit.SHA256,
		Steps: []StepOutcome{
			{Seq: 1, Kind: "write_service", Disruption: "restart", OK: true},
			{Seq: 2, Kind: "write_service", Disruption: "restart", OK: false, Err: "permission denied"},
		},
		Now: time.Unix(1_700_000_000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("RecordApplyRun: %v", err)
	}
	if out.Status != "degraded" {
		t.Errorf("status = %q, want degraded", out.Status)
	}
	if out.AppliedRevision == commit.Revision {
		t.Fatal("applied_revision advanced on a partial apply — invariant 7 broken")
	}

	// The failure must remain inspectable per step.
	var stepErr string
	if err := s.Read().QueryRow(
		`SELECT error FROM node_apply_steps WHERE seq = 2`).Scan(&stepErr); err != nil {
		t.Fatalf("read step: %v", err)
	}
	if stepErr != "permission denied" {
		t.Errorf("step error = %q, want it preserved for the UI", stepErr)
	}
}

// Invariant 6: matching revision but mismatched hash is an integrity fault,
// never convergence.
func TestRevisionMatchWithHashMismatchIsIntegrity(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	ctx := context.Background()
	commit := bumpTo(t, s, nodeID, 443)

	out, err := RecordApplyRun(ctx, s, ApplyRunInput{
		NodeID: nodeID, TargetRevision: commit.Revision,
		Converged: true,
		DocSHA256: "0000000000000000000000000000000000000000000000000000000000000000",
		Now:       time.Unix(1_700_000_000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("RecordApplyRun: %v", err)
	}
	if !out.Integrity {
		t.Fatal("hash mismatch at a matching revision was not flagged as an integrity fault")
	}
	if out.Status != "integrity" {
		t.Errorf("status = %q, want integrity", out.Status)
	}
	if out.AppliedRevision == commit.Revision {
		t.Error("applied_revision advanced despite an integrity fault")
	}
}

func TestDeferredRunIsRecordedWithoutAdvancing(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	ctx := context.Background()
	commit := bumpTo(t, s, nodeID, 443)

	out, err := RecordApplyRun(ctx, s, ApplyRunInput{
		NodeID: nodeID, TargetRevision: commit.Revision,
		Converged: false, Deferred: true, DocSHA256: commit.SHA256,
		Now: time.Unix(1_700_000_000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("RecordApplyRun: %v", err)
	}
	if out.AppliedRevision == commit.Revision {
		t.Error("deferred work advanced applied_revision")
	}
	var outcome string
	if err := s.Read().QueryRow(
		`SELECT outcome FROM node_apply_runs ORDER BY id DESC LIMIT 1`).Scan(&outcome); err != nil {
		t.Fatalf("read run: %v", err)
	}
	if outcome != "deferred" {
		t.Errorf("outcome = %q, want deferred", outcome)
	}
}

func TestHeartbeatUpdatesLastSeenAndHealth(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()

	if err := RecordHeartbeat(ctx, s, nodeID, HealthSample{
		Load1: 0.5, MemUsed: 1 << 20, UptimeS: 3600,
		Adapters: []AdapterHealthSample{{Kind: "stub", OK: true, Detail: "ready"}},
	}, now); err != nil {
		t.Fatalf("RecordHeartbeat: %v", err)
	}
	var lastSeen sql.NullInt64
	if err := s.Read().QueryRow(
		`SELECT last_seen_at FROM nodes WHERE id = ?`, nodeID).Scan(&lastSeen); err != nil {
		t.Fatalf("read node: %v", err)
	}
	if !lastSeen.Valid || lastSeen.Int64 != now.Unix() {
		t.Errorf("last_seen_at = %v, want %d", lastSeen, now.Unix())
	}
	var n int
	_ = s.Read().QueryRow(`SELECT count(*) FROM node_health`).Scan(&n)
	if n != 1 {
		t.Errorf("node_health rows = %d, want 1", n)
	}
}

func TestHelloRecordsAdapterKindsWithoutBumpingRevision(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	ctx := context.Background()
	commit := bumpTo(t, s, nodeID, 443)

	if err := RecordHello(ctx, s, nodeID,
		[]AdapterInfo{{Kind: "stub", Version: "1"}}, 0, "", time.Unix(1_700_000_000, 0).UTC()); err != nil {
		t.Fatalf("RecordHello: %v", err)
	}

	var kinds string
	var desired int64
	if err := s.Read().QueryRow(
		`SELECT adapter_kinds, desired_revision FROM nodes WHERE id = ?`, nodeID,
	).Scan(&kinds, &desired); err != nil {
		t.Fatalf("read node: %v", err)
	}
	if kinds != `["stub"]` {
		t.Errorf("adapter_kinds = %s, want [\"stub\"]", kinds)
	}
	// adapter_kinds is observed data and must never enter the desired
	// document, or every agent restart would bump the revision.
	if desired != commit.Revision {
		t.Errorf("desired_revision moved from %d to %d on Hello", commit.Revision, desired)
	}
}

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

// A revision the panel never issued must not be accepted as convergence.
// Reachable when the panel DB is restored from a backup that predates the
// revision the node already applied, or when revision history is pruned.
// The nodes CHECK (applied_revision <= desired_revision) blocks the variant
// above desired_revision, but only by failing the whole transaction; at or
// below desired_revision nothing in the schema objects.
func TestUnissuedRevisionIsIntegrityNotConvergence(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	ctx := context.Background()
	bumpTo(t, s, nodeID, 443)
	bumpTo(t, s, nodeID, 444)
	bumpTo(t, s, nodeID, 445)

	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`DELETE FROM node_revisions WHERE node_id = ? AND revision = 2`, nodeID)
		return err
	}); err != nil {
		t.Fatalf("prune revision 2: %v", err)
	}

	out, err := RecordApplyRun(ctx, s, ApplyRunInput{
		NodeID: nodeID, TargetRevision: 2,
		Converged: true, DocSHA256: "a-hash-this-panel-never-issued",
		Now:       time.Unix(1_700_000_000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("RecordApplyRun: %v", err)
	}
	if !out.Integrity {
		t.Fatal("a hash the panel never issued was accepted as convergence")
	}
	if out.Status != "integrity" {
		t.Errorf("status = %q, want integrity", out.Status)
	}
	if out.AppliedRevision == 2 {
		t.Error("applied_revision advanced to an unissued revision")
	}

	var reason string
	if err := s.Read().QueryRow(
		`SELECT json_extract(after_json, '$.reason') FROM audit_log
		  WHERE action = 'node.integrity_fault' ORDER BY id DESC LIMIT 1`).Scan(&reason); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if reason != "unissued_revision" {
		t.Errorf("audit reason = %q, want unissued_revision", reason)
	}
}

// Revision 0 is the state of a node that has never been given a desired
// document. It has no node_revisions row by construction, and must not be
// mistaken for an unissued revision — that would fault every fresh node on
// its first report.
func TestConvergedOnRevisionZeroIsNotIntegrity(t *testing.T) {
	s, nodeID := newNodeFixture(t)

	out, err := RecordApplyRun(context.Background(), s, ApplyRunInput{
		NodeID: nodeID, TargetRevision: 0, Converged: true, DocSHA256: "",
		Now: time.Unix(1_700_000_000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("RecordApplyRun: %v", err)
	}
	if out.Integrity {
		t.Fatal("a fresh node converging on revision 0 was flagged as an integrity fault")
	}
	if out.Status != "online" {
		t.Errorf("status = %q, want online", out.Status)
	}
}

// An integrity fault must survive routine contact. Both Hello and Heartbeat
// run on every reconnect and every ~30s respectively; if either reset status
// unconditionally, the fault an operator needs to see would disappear before
// they saw it and the panel would report green on a node it disagrees with.
func TestContactDoesNotClearIntegrityStatus(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()

	for _, tc := range []struct {
		name    string
		status  string
		contact func(*store.Store, int64) error
	}{
		{"hello/integrity", "integrity", func(s *store.Store, id int64) error {
			return RecordHello(ctx, s, id, []AdapterInfo{{Kind: "stub", Version: "1"}}, 0, "", now)
		}},
		{"heartbeat/integrity", "integrity", func(s *store.Store, id int64) error {
			return RecordHeartbeat(ctx, s, id, HealthSample{Load1: 0.5}, now)
		}},
		{"hello/disabled", "disabled", func(s *store.Store, id int64) error {
			return RecordHello(ctx, s, id, []AdapterInfo{{Kind: "stub", Version: "1"}}, 0, "", now)
		}},
		{"heartbeat/disabled", "disabled", func(s *store.Store, id int64) error {
			return RecordHeartbeat(ctx, s, id, HealthSample{Load1: 0.5}, now)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, nodeID := newNodeFixture(t)
			if err := s.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec(`UPDATE nodes SET status = ? WHERE id = ?`, tc.status, nodeID)
				return err
			}); err != nil {
				t.Fatalf("set status: %v", err)
			}

			if err := tc.contact(s, nodeID); err != nil {
				t.Fatalf("contact: %v", err)
			}

			var got string
			if err := s.Read().QueryRow(
				`SELECT status FROM nodes WHERE id = ?`, nodeID).Scan(&got); err != nil {
				t.Fatalf("read status: %v", err)
			}
			if got != tc.status {
				t.Errorf("status = %q after contact, want %q preserved", got, tc.status)
			}
		})
	}
}

// The same paths must still bring a node back from offline, or a node that
// reconnects would stay marked offline forever.
func TestContactClearsOfflineStatus(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()

	for _, tc := range []struct {
		name    string
		contact func(*store.Store, int64) error
	}{
		{"hello", func(s *store.Store, id int64) error {
			return RecordHello(ctx, s, id, []AdapterInfo{{Kind: "stub", Version: "1"}}, 0, "", now)
		}},
		{"heartbeat", func(s *store.Store, id int64) error {
			return RecordHeartbeat(ctx, s, id, HealthSample{Load1: 0.5}, now)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, nodeID := newNodeFixture(t)
			if err := s.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec(`UPDATE nodes SET status = 'offline' WHERE id = ?`, nodeID)
				return err
			}); err != nil {
				t.Fatalf("set status: %v", err)
			}
			if err := tc.contact(s, nodeID); err != nil {
				t.Fatalf("contact: %v", err)
			}
			var got string
			_ = s.Read().QueryRow(`SELECT status FROM nodes WHERE id = ?`, nodeID).Scan(&got)
			if got != "online" {
				t.Errorf("status = %q, want online after contact resumed", got)
			}
		})
	}
}

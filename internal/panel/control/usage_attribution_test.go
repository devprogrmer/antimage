package control

import (
	"context"
	"database/sql"
	"testing"
	"time"

	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
)

// The wire hop, which nothing covered.
//
// C2's attribution crosses four boundaries: the Xray tag, the adapter's
// UsageSample, the protobuf UsageSample, and the stored row. The adapter tests
// cover the first two and the nodes tests cover the last, but the protobuf ->
// UsageDelta conversion sat between them untested -- so dropping ServiceId here
// would have silently NULLed every attribution on the platform while every
// other test stayed green. That is exactly the shape of failure this field
// exists to prevent, one layer up.
func TestUsageReportCarriesTheServiceOffTheWire(t *testing.T) {
	st, nodeID, _, _ := enrolledNodeFixture(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()

	// A subject and a service on this node, so the id resolves.
	var subjectID, serviceID int64
	err := st.Write(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx,
			`INSERT INTO subjects (name, enabled, quota_used_bytes, created_at)
			 VALUES ('alice', 1, 0, 1000)`)
		if err != nil {
			return err
		}
		subjectID, _ = r.LastInsertId()
		r, err = tx.ExecContext(ctx,
			`INSERT INTO services (node_id, adapter_kind, params, enabled, created_at)
			 VALUES (?, 'xray', '{}', 1, 1000)`, nodeID)
		if err != nil {
			return err
		}
		serviceID, _ = r.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	svc := NewControlService(depsFor(t, st, now))
	if err := svc.onUsageReport(ctx, nodeID, &pb.UsageReport{
		NodeId:   nodeID,
		Sequence: 1,
		Samples: []*pb.UsageSample{{
			SubjectId:     subjectID,
			ServiceId:     serviceID,
			UplinkBytes:   100,
			DownlinkBytes: 200,
		}},
	}); err != nil {
		t.Fatalf("onUsageReport: %v", err)
	}

	var stored sql.NullInt64
	if err := st.Read().QueryRow(
		`SELECT service_id FROM usage_deltas WHERE subject_id = ?`, subjectID,
	).Scan(&stored); err != nil {
		t.Fatalf("read delta: %v", err)
	}
	if !stored.Valid {
		t.Fatal("service_id is NULL: the attribution the agent put on the wire " +
			"was dropped converting the protobuf sample into a UsageDelta")
	}
	if stored.Int64 != serviceID {
		t.Errorf("service_id = %d, want %d", stored.Int64, serviceID)
	}
}

// An agent built before this field sends nothing, which decodes as zero. It
// must keep accounting, unattributed.
func TestUsageReportWithoutAServiceStillIngests(t *testing.T) {
	st, nodeID, _, _ := enrolledNodeFixture(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()

	var subjectID int64
	err := st.Write(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx,
			`INSERT INTO subjects (name, enabled, quota_used_bytes, created_at)
			 VALUES ('alice', 1, 0, 1000)`)
		if err != nil {
			return err
		}
		subjectID, _ = r.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	svc := NewControlService(depsFor(t, st, now))
	if err := svc.onUsageReport(ctx, nodeID, &pb.UsageReport{
		NodeId:   nodeID,
		Sequence: 1,
		// No ServiceId: what an older agent puts on the wire.
		Samples: []*pb.UsageSample{{
			SubjectId: subjectID, UplinkBytes: 100, DownlinkBytes: 200,
		}},
	}); err != nil {
		t.Fatalf("a report from an agent that does not send service ids was "+
			"rejected: %v", err)
	}

	var used int64
	if err := st.Read().QueryRow(
		`SELECT quota_used_bytes FROM subjects WHERE id = ?`, subjectID).Scan(&used); err != nil {
		t.Fatalf("read usage: %v", err)
	}
	if used != 300 {
		t.Errorf("usage = %d, want 300; an older agent's traffic was discarded", used)
	}
}

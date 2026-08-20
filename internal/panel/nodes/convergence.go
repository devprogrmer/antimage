package nodes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/store"
)

type StepOutcome struct {
	Seq        int32
	Kind       string
	Disruption string
	OK         bool
	Err        string
	DurationMS int64
}

type ApplyRunInput struct {
	NodeID         int64
	TargetRevision int64
	Converged      bool
	Deferred       bool
	Err            string
	DocSHA256      string
	Steps          []StepOutcome
	Now            time.Time
}

type Outcome struct {
	Status          string
	AppliedRevision int64
	Integrity       bool
}

// RecordApplyRun persists a convergence attempt and decides the node's state.
//
// It implements invariants 6 and 7:
//
//   - applied_revision advances only when the agent reports Converged AND the
//     hash it applied matches the hash the panel recorded for that revision.
//   - a revision match with a hash mismatch is an integrity fault, never
//     convergence.
func RecordApplyRun(ctx context.Context, s *store.Store, in ApplyRunInput) (Outcome, error) {
	var out Outcome

	err := s.Write(ctx, func(tx *sql.Tx) error {
		var expectedSHA string
		err := tx.QueryRowContext(ctx,
			`SELECT doc_sha256 FROM node_revisions WHERE node_id = ? AND revision = ?`,
			in.NodeID, in.TargetRevision).Scan(&expectedSHA)
		known := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read expected hash: %w", err)
		}

		// Revision 0 is the pre-commit state: a node that has never had a
		// desired document legitimately converges on nothing, and no
		// node_revisions row exists for it (revision has CHECK (revision > 0)).
		// Above 0, a missing row means the agent applied a revision this panel
		// never issued — a restored backup, a pruned history, or a node talking
		// to the wrong panel. Trusting it would advance applied_revision to a
		// document whose hash was never verified against anything.
		unissued := in.Converged && in.TargetRevision > 0 && !known
		mismatch := in.Converged && known && in.DocSHA256 != expectedSHA
		integrity := unissued || mismatch

		var (
			runOutcome string
			status     string
			advance    bool
		)
		switch {
		case integrity:
			runOutcome, status = "integrity", "integrity"
		case in.Deferred:
			runOutcome, status = "deferred", "online"
		case in.Converged:
			runOutcome, status, advance = "converged", "online", true
		case in.Err != "":
			runOutcome, status = "failed", "degraded"
		default:
			runOutcome, status = "partial", "degraded"
		}

		res, err := tx.ExecContext(ctx,
			`INSERT INTO node_apply_runs (node_id, target_revision, started_at, finished_at, outcome)
			 VALUES (?,?,?,?,?)`,
			in.NodeID, in.TargetRevision, in.Now.Unix(), in.Now.Unix(), runOutcome)
		if err != nil {
			return fmt.Errorf("insert apply run: %w", err)
		}
		runID, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("run id: %w", err)
		}

		for _, st := range in.Steps {
			stepOutcome := "ok"
			if !st.OK {
				stepOutcome = "failed"
			}
			disruption := st.Disruption
			switch disruption {
			case "none", "reload", "restart":
			default:
				disruption = "unknown"
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO node_apply_steps
				   (run_id, seq, step_kind, disruption, outcome, error, duration_ms)
				 VALUES (?,?,?,?,?,?,?)`,
				runID, st.Seq, st.Kind, disruption, stepOutcome, st.Err, st.DurationMS); err != nil {
				return fmt.Errorf("insert apply step %d: %w", st.Seq, err)
			}
		}

		if advance {
			if _, err := tx.ExecContext(ctx,
				`UPDATE nodes SET applied_revision = ?, status = ?, last_error = '' WHERE id = ?`,
				in.TargetRevision, status, in.NodeID); err != nil {
				return fmt.Errorf("advance applied_revision: %w", err)
			}
		} else {
			if _, err := tx.ExecContext(ctx,
				`UPDATE nodes SET status = ?, last_error = ? WHERE id = ?`,
				status, in.Err, in.NodeID); err != nil {
				return fmt.Errorf("update node status: %w", err)
			}
		}

		if integrity {
			reason := "hash_mismatch"
			if unissued {
				reason = "unissued_revision"
			}
			if err := audit.InTx(ctx, tx, "", audit.SystemActor("reconciler"), audit.Record{
				Action:     "node.integrity_fault",
				TargetType: "node",
				TargetID:   sql.NullInt64{Int64: in.NodeID, Valid: true},
				After: map[string]any{
					"reason":   reason,
					"revision": in.TargetRevision,
					"expected": expectedSHA,
					"reported": in.DocSHA256,
				},
				Result: "failed",
			}); err != nil {
				return err
			}
		}

		var applied int64
		if err := tx.QueryRowContext(ctx,
			`SELECT applied_revision FROM nodes WHERE id = ?`, in.NodeID).Scan(&applied); err != nil {
			return fmt.Errorf("read applied_revision: %w", err)
		}
		out = Outcome{Status: status, AppliedRevision: applied, Integrity: integrity}
		return nil
	})
	if err != nil {
		return Outcome{}, err
	}
	return out, nil
}

type AdapterInfo struct {
	Kind         string
	Version      string
	Capabilities []string // SP5: adapter capabilities
}

// RecordHello caches the adapter kinds the agent reports.
//
// adapter_kinds is observed data, not configuration: it never enters the
// desired document, so an agent restart cannot bump a revision.
func RecordHello(
	ctx context.Context, s *store.Store, nodeID int64,
	adapters []AdapterInfo, appliedRevision int64, docSHA string, now time.Time,
) error {
	kinds := make([]string, 0, len(adapters))
	for _, a := range adapters {
		kinds = append(kinds, a.Kind)
	}
	encoded, err := json.Marshal(kinds)
	if err != nil {
		return fmt.Errorf("encode adapter kinds: %w", err)
	}

	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE nodes SET adapter_kinds = ?, last_seen_at = ?,
			        status = CASE WHEN status IN ('pending','enrolling','offline')
			                      THEN 'online' ELSE status END
			  WHERE id = ?`,
			string(encoded), now.Unix(), nodeID)
		return err
	})
	if err != nil {
		return err
	}

	// SP5: Update adapter registry with version and capabilities
	for _, a := range adapters {
		if err := UpsertAdapter(ctx, s, nodeID, a.Kind, a.Version, a.Capabilities, now); err != nil {
			return fmt.Errorf("upsert adapter %s: %w", a.Kind, err)
		}
	}

	return nil
}

type AdapterHealthSample struct {
	Kind   string `json:"kind"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type HealthSample struct {
	Load1    float64
	MemUsed  uint64
	UptimeS  uint64
	RTTMs    int64
	Adapters []AdapterHealthSample
}

func RecordHeartbeat(ctx context.Context, s *store.Store, nodeID int64, h HealthSample, now time.Time) error {
	adapters, err := json.Marshal(h.Adapters)
	if err != nil {
		return fmt.Errorf("encode adapter health: %w", err)
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO node_health
			   (node_id, at, load1, mem_used, uptime_s, rtt_ms, adapter_status)
			 VALUES (?,?,?,?,?,?,?)`,
			nodeID, now.Unix(), h.Load1, int64(h.MemUsed), int64(h.UptimeS),
			h.RTTMs, string(adapters)); err != nil {
			return fmt.Errorf("insert health sample: %w", err)
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE nodes SET last_seen_at = ?,
			        status = CASE WHEN status = 'offline' THEN 'online' ELSE status END
			  WHERE id = ?`, now.Unix(), nodeID)
		return err
	})
}

package nodes

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/amyrm/antimage/internal/panel/store"
)

// Verification and repair for the accounting tables.
//
// Two operations, with different jobs.
//
// Checksum answers "did anything change?". It is taken before a migration and
// again after, and the two are compared. The coefficient migration adds columns
// with behaviour-preserving defaults and is supposed to change no figure
// anywhere; a checksum is how that stops being a claim and becomes a check.
//
// Repair answers "how much of the damage can be undone?". The rollup inflation
// bug means hourly rows written before the fix are wrong by an unknown factor.
// Where the underlying deltas survive, the correct figure can be recomputed.
// Where they have been pruned, it cannot, and this says so rather than
// producing a confident wrong answer.

// Checksum is a fingerprint of every accounting figure that a bill is derived
// from, plus the counts and coefficients that would change one.
type Checksum struct {
	Digest string

	DeltaRows       int64
	DeltaBytes      int64
	HourlyRows      int64
	HourlyBytes     int64
	DailyRows       int64
	DailyBytes      int64
	SubjectUsed     int64
	RollupWatermark int64

	// NonUnityCoefficients counts coefficients an operator has moved off x1.0.
	// While it is zero, the coefficient migration's Down is lossless; past that
	// it discards configuration and the recovery path is a restore from backup.
	NonUnityCoefficients int64
}

// String renders the checksum for an operator, one figure per line.
func (c Checksum) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "digest              %s\n", c.Digest)
	fmt.Fprintf(&b, "usage_deltas        %d rows, %d bytes\n", c.DeltaRows, c.DeltaBytes)
	fmt.Fprintf(&b, "rollups_hourly      %d rows, %d bytes\n", c.HourlyRows, c.HourlyBytes)
	fmt.Fprintf(&b, "rollups_daily       %d rows, %d bytes\n", c.DailyRows, c.DailyBytes)
	fmt.Fprintf(&b, "subjects.used       %d bytes\n", c.SubjectUsed)
	fmt.Fprintf(&b, "rollup watermark    %d\n", c.RollupWatermark)
	fmt.Fprintf(&b, "coefficients off x1 %d\n", c.NonUnityCoefficients)
	return b.String()
}

// TakeChecksum fingerprints the accounting tables.
//
// The digest covers the aggregates rather than every row, which is what keeps
// it cheap enough to run either side of a migration on a live database. That is
// the right trade here: the question it answers is "did this migration move a
// number", and a migration that moved one row's bytes without moving any total
// is not a failure mode any of these migrations have.
//
// Coefficient columns are read through a tolerant helper because this runs both
// before and after the migration that adds them.
func TakeChecksum(ctx context.Context, st *store.Store) (Checksum, error) {
	var c Checksum
	db := st.Read()

	scan := func(query string, dest ...any) error {
		return db.QueryRowContext(ctx, query).Scan(dest...)
	}

	if err := scan(`SELECT COUNT(*), COALESCE(SUM(uplink_bytes + downlink_bytes), 0)
	                  FROM usage_deltas`, &c.DeltaRows, &c.DeltaBytes); err != nil {
		return c, fmt.Errorf("checksum deltas: %w", err)
	}
	if err := scan(`SELECT COUNT(*), COALESCE(SUM(uplink_bytes + downlink_bytes), 0)
	                  FROM usage_rollups_hourly`, &c.HourlyRows, &c.HourlyBytes); err != nil {
		return c, fmt.Errorf("checksum hourly: %w", err)
	}
	if err := scan(`SELECT COUNT(*), COALESCE(SUM(uplink_bytes + downlink_bytes), 0)
	                  FROM usage_rollups_daily`, &c.DailyRows, &c.DailyBytes); err != nil {
		return c, fmt.Errorf("checksum daily: %w", err)
	}
	if err := scan(`SELECT COALESCE(SUM(quota_used_bytes), 0) FROM subjects`,
		&c.SubjectUsed); err != nil {
		return c, fmt.Errorf("checksum subject usage: %w", err)
	}

	// Both of these are absent before their own migration. A missing table is
	// reported as zero rather than as an error, so the same checksum can be
	// taken on either side of the upgrade and compared.
	_ = scan(`SELECT last_delta_id FROM usage_rollup_state WHERE name = 'hourly'`,
		&c.RollupWatermark)
	c.NonUnityCoefficients = countNonUnityCoefficients(ctx, db)

	c.Digest = digestOf(
		c.DeltaRows, c.DeltaBytes, c.HourlyRows, c.HourlyBytes,
		c.DailyRows, c.DailyBytes, c.SubjectUsed, c.NonUnityCoefficients,
	)
	return c, nil
}

// countNonUnityCoefficients tolerates the columns not existing yet.
func countNonUnityCoefficients(ctx context.Context, db *sql.DB) int64 {
	var total int64
	for _, table := range []string{"nodes", "services", "subjects", "resellers"} {
		var n int64
		err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM `+table+` WHERE usage_coefficient <> 10000`).Scan(&n)
		if err != nil {
			continue // column not present on this schema version
		}
		total += n
	}
	return total
}

// digestOf hashes the figures in a fixed order.
//
// The watermark is deliberately NOT in the digest, because the digest means
// "the billed figures", and how far the rollup has progressed is bookkeeping
// rather than a bill. This is a definitional choice and not a load-bearing one:
// the watermark only advances when new deltas are folded, and those move
// DeltaRows and HourlyBytes, which ARE in the digest. So including it would
// raise no false positives either. It is excluded so that the digest answers
// one question, and the tests do not pin this -- a mutation that folds the
// watermark in is caught by nothing.
func digestOf(values ...int64) string {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, strconv.FormatInt(v, 10))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, ":")))
	return hex.EncodeToString(sum[:])
}

// Divergence lists the fields in which two checksums differ, in a form an
// operator can act on. Empty means identical.
func (c Checksum) Divergence(other Checksum) []string {
	var out []string
	compare := func(name string, before, after int64) {
		if before != after {
			out = append(out, fmt.Sprintf("%s: %d -> %d (%+d)", name, before, after, after-before))
		}
	}
	compare("usage_deltas rows", c.DeltaRows, other.DeltaRows)
	compare("usage_deltas bytes", c.DeltaBytes, other.DeltaBytes)
	compare("rollups_hourly rows", c.HourlyRows, other.HourlyRows)
	compare("rollups_hourly bytes", c.HourlyBytes, other.HourlyBytes)
	compare("rollups_daily rows", c.DailyRows, other.DailyRows)
	compare("rollups_daily bytes", c.DailyBytes, other.DailyBytes)
	compare("subjects.quota_used_bytes", c.SubjectUsed, other.SubjectUsed)
	sort.Strings(out)
	return out
}

// RepairReport describes what recomputing the hourly rollups would do, or did.
type RepairReport struct {
	DryRun bool

	// RecoverableHours are (subject, hour) buckets whose deltas all survive, so
	// the true figure can be recomputed.
	RecoverableHours int64
	// UnrecoverableHours are buckets with no surviving deltas. Their rows are
	// left exactly as they are: an inflated figure is wrong, and replacing it
	// with zero would be wrong AND would look deliberate.
	UnrecoverableHours int64

	BytesBefore int64
	BytesAfter  int64
}

// ProjectedDelta is the change repair would make to the recoverable buckets.
// Negative is the expected direction: the bug inflated.
func (r RepairReport) ProjectedDelta() int64 { return r.BytesAfter - r.BytesBefore }

func (r RepairReport) String() string {
	verb := "would change"
	if !r.DryRun {
		verb = "changed"
	}
	return fmt.Sprintf(
		"repair (dry_run=%t): %d recoverable buckets, %d unrecoverable left untouched; "+
			"%s %d -> %d bytes (%+d)",
		r.DryRun, r.RecoverableHours, r.UnrecoverableHours, verb,
		r.BytesBefore, r.BytesAfter, r.ProjectedDelta())
}

// RepairHourlyRollups recomputes hourly buckets from surviving raw deltas.
//
// Rows written before the watermark existed were inflated by however many times
// the sweeper ran while their deltas were still present. Where those deltas
// survive the retention window, the true figure is recoverable by recomputing;
// where they do not, it is not recoverable at all and this leaves the row alone.
//
// Leaving a known-wrong row in place is the deliberate choice. The alternatives
// are to delete it, which destroys the only surviving record that the traffic
// happened, or to write a zero, which is equally wrong and looks intentional.
// An inflated figure that is reported as inflated is more use than either.
//
// dryRun performs the whole computation and reports what it would write without
// writing it. The read and the write happen in one transaction either way, so
// the projection an operator approves is the projection that gets applied --
// a dry run that read outside the transaction could be overtaken by the
// sweeper between the report and the apply.
//
// Idempotent: recomputing from the same deltas produces the same rows, so a
// second run reports a projected delta of zero.
func RepairHourlyRollups(ctx context.Context, st *store.Store, dryRun bool) (RepairReport, error) {
	report := RepairReport{DryRun: dryRun}

	err := st.Write(ctx, func(tx *sql.Tx) error {
		// Buckets that surviving deltas can account for, and what they should
		// hold. Bucketing here must match RollupHourly exactly or repair would
		// invent a difference where there is none.
		//
		// Grouped by (subject, node, service, hour) since C3, and that is not
		// cosmetic: a rollup row is now per node and service, so matching on
		// (subject, hour) alone would pair EVERY group's row with the whole
		// subject-hour total and set each one to it. Repairing an inflation
		// defect by multiplying the traffic by the number of groups is a
		// spectacular way to make it worse.
		const recomputed = `
			SELECT subject_id,
			       node_id,
			       service_id,
			       (created_at / 3600) * 3600 AS hour_start,
			       SUM(uplink_bytes)   AS up,
			       SUM(downlink_bytes) AS down
			  FROM usage_deltas
			 GROUP BY subject_id, node_id, service_id, hour_start`

		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*), COALESCE(SUM(h.uplink_bytes + h.downlink_bytes), 0),
			       COALESCE(SUM(r.up + r.down), 0)
			  FROM usage_rollups_hourly h
			  JOIN (`+recomputed+`) r
			    ON r.subject_id = h.subject_id AND r.hour_start = h.hour_start
			   AND COALESCE(r.node_id, 0)    = COALESCE(h.node_id, 0)
			   AND COALESCE(r.service_id, 0) = COALESCE(h.service_id, 0)`,
		).Scan(&report.RecoverableHours, &report.BytesBefore, &report.BytesAfter); err != nil {
			return fmt.Errorf("project repair: %w", err)
		}

		// COALESCE on both sides throughout: NULL = NULL is false in SQL, so
		// without it an unattributed bucket would never match its own deltas
		// and would be reported unrecoverable while sitting next to the rows
		// that account for it.
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*)
			  FROM usage_rollups_hourly h
			 WHERE NOT EXISTS (
			       SELECT 1 FROM usage_deltas d
			        WHERE d.subject_id = h.subject_id
			          AND (d.created_at / 3600) * 3600 = h.hour_start
			          AND COALESCE(d.node_id, 0)    = COALESCE(h.node_id, 0)
			          AND COALESCE(d.service_id, 0) = COALESCE(h.service_id, 0))`,
		).Scan(&report.UnrecoverableHours); err != nil {
			return fmt.Errorf("count unrecoverable: %w", err)
		}

		if dryRun {
			// Returning an error is what rolls the transaction back, so the
			// projection above is guaranteed to have written nothing.
			return errDryRun
		}

		if _, err := tx.ExecContext(ctx, `
			UPDATE usage_rollups_hourly AS h
			   SET uplink_bytes = (
			         SELECT SUM(d.uplink_bytes) FROM usage_deltas d
			          WHERE d.subject_id = h.subject_id
			            AND (d.created_at / 3600) * 3600 = h.hour_start
			            AND COALESCE(d.node_id, 0)    = COALESCE(h.node_id, 0)
			            AND COALESCE(d.service_id, 0) = COALESCE(h.service_id, 0)),
			       downlink_bytes = (
			         SELECT SUM(d.downlink_bytes) FROM usage_deltas d
			          WHERE d.subject_id = h.subject_id
			            AND (d.created_at / 3600) * 3600 = h.hour_start
			            AND COALESCE(d.node_id, 0)    = COALESCE(h.node_id, 0)
			            AND COALESCE(d.service_id, 0) = COALESCE(h.service_id, 0))
			 WHERE EXISTS (
			       SELECT 1 FROM usage_deltas d
			        WHERE d.subject_id = h.subject_id
			          AND (d.created_at / 3600) * 3600 = h.hour_start
			          AND COALESCE(d.node_id, 0)    = COALESCE(h.node_id, 0)
			          AND COALESCE(d.service_id, 0) = COALESCE(h.service_id, 0))`); err != nil {
			return fmt.Errorf("repair hourly rollups: %w", err)
		}
		return nil
	})

	if err != nil && !errors.Is(err, errDryRun) {
		return RepairReport{}, err
	}
	return report, nil
}

// errDryRun aborts the repair transaction after the projection is computed. It
// never escapes RepairHourlyRollups.
var errDryRun = errors.New("dry run")

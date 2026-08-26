package nodes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/amyrm/antimage/internal/panel/store"
)

// BillableGroup is one (node, service, outbound) combination's contribution to a bill.
//
// The groups are returned, not just the total, because section 11 requires the
// calculation be shown rather than hidden, and section 3.1 forbids the frontend
// recomputing authoritative state. The UI renders this; it does not derive it.
type BillableGroup struct {
	// Nil when the traffic was never attributed -- an adapter that cannot
	// account per inbound, or usage recorded before attribution existed.
	NodeID     *int64
	ServiceID  *int64
	OutboundID *int64
	RawBytes   int64
	Factors    Factors
	Billable   int64
}

// BillableReport is one subject's bill for a period, with its derivation.
type BillableReport struct {
	SubjectID int64
	From      int64
	To        int64
	RawBytes  int64
	Billable  int64
	Groups    []BillableGroup
}

// BillableForSubject computes what a subject owes for [from, to).
//
//	billable = raw * node_coef * service_coef * subject_coef * reseller_coef * outbound_coef
//
// Summed PER (node, service) group rather than applied once to a total, and
// that is the whole reason the rollups gained those dimensions. A subject on a
// x2.0 node and a x1.0 node has no single node coefficient: applying any one of
// them to their combined raw bytes gives a number that is wrong for both halves
// of their traffic.
//
// Reads the permanent hourly rollups plus the deltas not yet folded into them,
// so the answer is current rather than as-of-the-last-sweep. The two cannot
// double count: the fold advances a watermark inside the same transaction that
// writes the rows, so a delta is either below the watermark and in a rollup, or
// above it and counted here.
func BillableForSubject(
	ctx context.Context, st *store.Store, subjectID, from, to int64,
) (BillableReport, error) {
	report := BillableReport{SubjectID: subjectID, From: from, To: to}

	subjectCoef, resellerCoef, err := subjectCoefficients(ctx, st, subjectID)
	if err != nil {
		return BillableReport{}, err
	}

	rows, err := st.Read().QueryContext(ctx, `
		WITH raw AS (
			SELECT node_id, service_id, uplink_bytes + downlink_bytes AS bytes
			  FROM usage_rollups_hourly
			 WHERE subject_id = ? AND hour_start >= ? AND hour_start < ?
			UNION ALL
			SELECT node_id, service_id, uplink_bytes + downlink_bytes
			  FROM usage_deltas
			 WHERE subject_id = ? AND created_at >= ? AND created_at < ?
			   AND id > COALESCE(
			       (SELECT last_delta_id FROM usage_rollup_state WHERE name = 'hourly'), 0)
		)
		SELECT raw.node_id, raw.service_id, SUM(raw.bytes),
		       COALESCE(n.usage_coefficient, ?),
		       COALESCE(sv.usage_coefficient, ?)
		  FROM raw
		  LEFT JOIN nodes    n  ON n.id  = raw.node_id
		  LEFT JOIN services sv ON sv.id = raw.service_id
		 GROUP BY COALESCE(raw.node_id, 0), COALESCE(raw.service_id, 0)
		 ORDER BY COALESCE(raw.node_id, 0), COALESCE(raw.service_id, 0)`,
		subjectID, from, to,
		subjectID, from, to,
		CoefficientUnit, CoefficientUnit,
	)
	if err != nil {
		return BillableReport{}, fmt.Errorf("read billable groups: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			nodeID, serviceID sql.NullInt64
			bytes             int64
			nodeCoef, svcCoef int64
		)
		if err := rows.Scan(&nodeID, &serviceID, &bytes, &nodeCoef, &svcCoef); err != nil {
			return BillableReport{}, fmt.Errorf("scan billable group: %w", err)
		}

		g := BillableGroup{
			RawBytes: bytes,
			Factors: Factors{
				Node: nodeCoef, Service: svcCoef,
				Subject: subjectCoef, Reseller: resellerCoef,
				Outbound: CoefficientUnit, // TODO: add outbound_id attribution to usage_deltas
			},
		}
		if nodeID.Valid {
			id := nodeID.Int64
			g.NodeID = &id
		}
		if serviceID.Valid {
			id := serviceID.Int64
			g.ServiceID = &id
		}

		g.Billable, err = Billable(g.RawBytes, g.Factors)
		if err != nil {
			// Reported, never swallowed. A group that cannot be computed is a
			// bill that cannot be trusted, and returning a partial total that
			// looks plausible is how an overflow becomes an undercharge nobody
			// investigates.
			return BillableReport{}, fmt.Errorf(
				"subject %d, node %v, service %v: %w", subjectID, nodeID, serviceID, err)
		}

		report.RawBytes += g.RawBytes
		report.Billable += g.Billable
		report.Groups = append(report.Groups, g)
	}
	if err := rows.Err(); err != nil {
		return BillableReport{}, fmt.Errorf("iterate billable groups: %w", err)
	}
	if report.Groups == nil {
		// An empty period is a real answer and callers marshal this straight to
		// JSON; a nil slice would render as null and make the UI branch on it.
		report.Groups = []BillableGroup{}
	}
	return report, nil
}

// subjectCoefficients reads the two factors that apply to a subject wherever
// their traffic went.
//
// A subject with no reseller is platform-owned and bills at x1.0. That is not a
// fallback for a missing row -- there is genuinely no reseller margin on a
// subject nobody resells.
func subjectCoefficients(
	ctx context.Context, st *store.Store, subjectID int64,
) (subjectCoef, resellerCoef int64, err error) {
	err = st.Read().QueryRowContext(ctx, `
		SELECT s.usage_coefficient, COALESCE(r.usage_coefficient, ?)
		  FROM subjects s
		  LEFT JOIN reseller_subjects rs ON rs.subject_id = s.id
		  LEFT JOIN resellers r          ON r.id = rs.reseller_id
		 WHERE s.id = ?`,
		CoefficientUnit, subjectID,
	).Scan(&subjectCoef, &resellerCoef)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, 0, fmt.Errorf("subject %d does not exist", subjectID)
	case err != nil:
		return 0, 0, fmt.Errorf("read subject coefficients: %w", err)
	}
	return subjectCoef, resellerCoef, nil
}

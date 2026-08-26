package nodes

import (
	"context"
	"fmt"

	"github.com/amyrm/antimage/internal/panel/store"
)

// Quota enforcement on BILLABLE bytes (C4, implementing AD-2's "quota should be
// what the operator sold").
//
// The stored quota_used_bytes counter is RAW and stays that way. Comparing it
// against a quota the operator set in billable terms would compare two
// different quantities, and coefficients would have no effect on enforcement --
// the exact class of bug that made max_quota_bytes decorative in the reseller
// engine, where a number was recorded somewhere other than the state the
// decision reads from.
//
// AD-2's own suggestion, an adjusted THRESHOLD (quota x 10000^4 / k1k2k3k4),
// cannot be used: it presumes one set of coefficients per subject, and a
// subject on a x2.0 node and a x1.0 node has no single node coefficient. The
// bill is a sum over groups, so the comparison has to be too.

// overQuota is one subject the sweeper has decided to freeze.
type overQuota struct {
	SubjectID int64
	Billable  int64
	Quota     int64
}

// findSubjectsOverQuota returns the subjects whose billable usage for the
// current period has reached their quota.
//
// One query for every subject's groups rather than one query per subject: a
// panel with thousands of subjects would otherwise issue thousands of reads per
// sweep. The rows arrive ordered by subject so they can be folded in a single
// pass, and the arithmetic stays in Go where overflow is an error rather than a
// silently wrapped bill.
//
// A subject with no recorded period start falls back to their creation time,
// and that fallback is not a convenience -- it closes an enforcement hole.
// Skipping such a subject would mean a quota that was set but never enforced,
// which is the decorative-number failure this whole change exists to remove.
// created_at is also the RIGHT window for them: a subject with no reset
// schedule has one period that began when they did, which is exactly what the
// counter this replaces measured.
func findSubjectsOverQuota(ctx context.Context, st *store.Store, now int64) ([]overQuota, error) {
	rows, err := st.Read().QueryContext(ctx, `
		WITH eligible AS (
			SELECT s.id, s.quota_bytes,
			       COALESCE(s.quota_period_start, s.created_at) AS quota_period_start,
			       s.usage_coefficient AS subject_coef,
			       COALESCE(r.usage_coefficient, ?) AS reseller_coef
			  FROM subjects s
			  LEFT JOIN reseller_subjects rs ON rs.subject_id = s.id
			  LEFT JOIN resellers r          ON r.id = rs.reseller_id
			 WHERE s.quota_bytes IS NOT NULL
			   AND s.frozen_at IS NULL
			   AND s.enabled = 1
		),
		raw AS (
			SELECT e.id AS subject_id, h.node_id, h.service_id,
			       h.uplink_bytes + h.downlink_bytes AS bytes
			  FROM eligible e
			  JOIN usage_rollups_hourly h ON h.subject_id = e.id
			 WHERE h.hour_start >= e.quota_period_start AND h.hour_start < ?
			UNION ALL
			-- Deltas not yet folded, so enforcement is current rather than
			-- as-of-the-last-sweep. The fold advances its watermark in the same
			-- transaction that writes the rows, so a delta is in exactly one of
			-- these two branches.
			SELECT e.id, d.node_id, d.service_id,
			       d.uplink_bytes + d.downlink_bytes
			  FROM eligible e
			  JOIN usage_deltas d ON d.subject_id = e.id
			 WHERE d.created_at >= e.quota_period_start AND d.created_at < ?
			   AND d.id > COALESCE(
			       (SELECT last_delta_id FROM usage_rollup_state WHERE name = 'hourly'), 0)
		)
		SELECT e.id, e.quota_bytes, e.subject_coef, e.reseller_coef,
		       COALESCE(n.usage_coefficient, ?)  AS node_coef,
		       COALESCE(sv.usage_coefficient, ?) AS service_coef,
		       SUM(raw.bytes)
		  FROM raw
		  JOIN eligible e   ON e.id = raw.subject_id
		  LEFT JOIN nodes    n  ON n.id  = raw.node_id
		  LEFT JOIN services sv ON sv.id = raw.service_id
		 GROUP BY e.id, COALESCE(raw.node_id, 0), COALESCE(raw.service_id, 0)
		 ORDER BY e.id`,
		CoefficientUnit, now, now, CoefficientUnit, CoefficientUnit,
	)
	if err != nil {
		return nil, fmt.Errorf("query billable usage: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var (
		out     []overQuota
		current overQuota
		haveOne bool
	)
	flush := func() {
		if haveOne && current.Billable >= current.Quota {
			out = append(out, current)
		}
	}

	for rows.Next() {
		var (
			subjectID, quota          int64
			subjectCoef, resellerCoef int64
			nodeCoef, serviceCoef     int64
			bytes                     int64
		)
		if err := rows.Scan(&subjectID, &quota, &subjectCoef, &resellerCoef,
			&nodeCoef, &serviceCoef, &bytes); err != nil {
			return nil, fmt.Errorf("scan billable group: %w", err)
		}

		if !haveOne || current.SubjectID != subjectID {
			flush()
			current = overQuota{SubjectID: subjectID, Quota: quota}
			haveOne = true
		}

		billable, err := Billable(bytes, Factors{
			Node: nodeCoef, Service: serviceCoef,
			Subject: subjectCoef, Reseller: resellerCoef,
		})
		if err != nil {
			// A subject whose bill cannot be computed must not be silently
			// treated as under quota. Freezing on a guess is wrong and so is
			// letting them run unmetered, so the sweep fails and says why.
			return nil, fmt.Errorf("subject %d: %w", subjectID, err)
		}
		current.Billable += billable
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate billable usage: %w", err)
	}
	flush()

	return out, nil
}

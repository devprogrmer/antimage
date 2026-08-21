// Package dashboard provides materialized aggregate statistics for the admin
// dashboard. Stats are cached in the dashboard_stats table and recomputed when
// the cache is stale (older than 60 seconds) or missing.
package dashboard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

// StaleAfter is the maximum age of cached stats before they are recomputed.
const StaleAfter = 60 * time.Second

// DashboardStats holds aggregated metrics for the admin dashboard.
type DashboardStats struct {
	AdminID             *int64
	ComputedAt          time.Time
	NodesTotal          int64
	NodesOnline         int64
	NodesDegraded       int64
	NodesOffline        int64
	SubjectsTotal       int64
	SubjectsActive      int64
	SubjectsExpired     int64
	SubjectsFrozen      int64
	Traffic24hUplink    int64
	Traffic24hDownlink  int64
	QuotaTotalBytes     *int64
	QuotaUsedBytes      int64
	QuotaUtilizationPct *float64
}

// ComputeStats queries the live tables and returns freshly computed stats.
// adminID may be nil for global (super-admin) stats; a non-nil value scopes
// nothing differently today but is stored so per-admin caches are possible.
func ComputeStats(ctx context.Context, db *store.Store, adminID *int64) (DashboardStats, error) {
	now := time.Now().UTC()
	nowUnix := now.Unix()
	cutoff24h := now.Add(-24 * time.Hour).Unix()

	var s DashboardStats
	s.AdminID = adminID
	s.ComputedAt = now

	// Node counts — status column holds the canonical node state.
	// 'online' maps to healthy, 'degraded' maps to degraded, everything else
	// (pending, enrolling, integrity, offline, disabled) is treated as offline.
	err := db.Read().QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COUNT(CASE WHEN status = 'online'   THEN 1 END),
			COUNT(CASE WHEN status = 'degraded' THEN 1 END),
			COUNT(CASE WHEN status NOT IN ('online','degraded') THEN 1 END)
		FROM nodes
	`).Scan(&s.NodesTotal, &s.NodesOnline, &s.NodesDegraded, &s.NodesOffline)
	if err != nil {
		return DashboardStats{}, fmt.Errorf("query node counts: %w", err)
	}

	// Subject counts.
	// active:  enabled and not frozen and not expired
	// expired: expired_at set, or expires_at is in the past
	// frozen:  frozen_at is set
	err = db.Read().QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COUNT(CASE WHEN enabled = 1
			            AND frozen_at IS NULL
			            AND (expires_at IS NULL OR expires_at > ?)
			       THEN 1 END),
			COUNT(CASE WHEN expired_at IS NOT NULL
			             OR (expires_at IS NOT NULL AND expires_at <= ?)
			       THEN 1 END),
			COUNT(CASE WHEN frozen_at IS NOT NULL THEN 1 END)
		FROM subjects
	`, nowUnix, nowUnix).Scan(
		&s.SubjectsTotal, &s.SubjectsActive, &s.SubjectsExpired, &s.SubjectsFrozen,
	)
	if err != nil {
		return DashboardStats{}, fmt.Errorf("query subject counts: %w", err)
	}

	// 24-hour traffic from hourly rollups.
	err = db.Read().QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(uplink_bytes),   0),
			COALESCE(SUM(downlink_bytes), 0)
		FROM usage_rollups_hourly
		WHERE hour_start >= ?
	`, cutoff24h).Scan(&s.Traffic24hUplink, &s.Traffic24hDownlink)
	if err != nil {
		return DashboardStats{}, fmt.Errorf("query 24h traffic: %w", err)
	}

	// Quota aggregates from subjects.
	// quota_bytes IS NULL means "unlimited" for that subject; the aggregate is
	// NULL when any subject has no cap, since total capacity is then unbounded.
	var quotaTotal sql.NullInt64
	err = db.Read().QueryRowContext(ctx, `
		SELECT
			SUM(quota_bytes),
			COALESCE(SUM(quota_used_bytes), 0)
		FROM subjects
	`).Scan(&quotaTotal, &s.QuotaUsedBytes)
	if err != nil {
		return DashboardStats{}, fmt.Errorf("query quota: %w", err)
	}
	if quotaTotal.Valid {
		v := quotaTotal.Int64
		s.QuotaTotalBytes = &v
		if v > 0 {
			pct := float64(s.QuotaUsedBytes) / float64(v) * 100.0
			s.QuotaUtilizationPct = &pct
		}
	}

	return s, nil
}

// GetStats returns dashboard stats for the actor. It reads from the
// dashboard_stats cache and recomputes if the cached value is missing or older
// than StaleAfter (60 seconds).
//
// Super admins receive global stats (admin_id IS NULL in the cache row).
// Non-super admins receive stats scoped to their admin_id.
func GetStats(ctx context.Context, db *store.Store, actor rbac.Actor) (DashboardStats, error) {
	var adminID *int64
	if !actor.IsSuper {
		id := actor.AdminID
		adminID = &id
	}

	cached, err := readCachedStats(ctx, db, adminID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return DashboardStats{}, fmt.Errorf("read cached stats: %w", err)
	}

	if err == nil && time.Since(cached.ComputedAt) <= StaleAfter {
		return cached, nil
	}

	// Cache miss or stale — recompute.
	fresh, err := ComputeStats(ctx, db, adminID)
	if err != nil {
		return DashboardStats{}, err
	}

	// Persist; ignore write errors so a FK miss on adminID does not break reads.
	_ = upsertStats(ctx, db, fresh)
	return fresh, nil
}

// readCachedStats reads one row from dashboard_stats. Returns sql.ErrNoRows
// when no row exists for the given adminID.
func readCachedStats(ctx context.Context, db *store.Store, adminID *int64) (DashboardStats, error) {
	var (
		s                   DashboardStats
		computedAt          int64
		quotaTotalBytes     sql.NullInt64
		quotaUtilizationPct sql.NullFloat64
	)
	s.AdminID = adminID

	var err error
	if adminID == nil {
		err = db.Read().QueryRowContext(ctx, `
			SELECT computed_at,
			       nodes_total, nodes_online, nodes_degraded, nodes_offline,
			       subjects_total, subjects_active, subjects_expired, subjects_frozen,
			       traffic_24h_uplink, traffic_24h_downlink,
			       quota_total_bytes, quota_used_bytes, quota_utilization_pct
			FROM dashboard_stats
			WHERE admin_id IS NULL
		`).Scan(
			&computedAt,
			&s.NodesTotal, &s.NodesOnline, &s.NodesDegraded, &s.NodesOffline,
			&s.SubjectsTotal, &s.SubjectsActive, &s.SubjectsExpired, &s.SubjectsFrozen,
			&s.Traffic24hUplink, &s.Traffic24hDownlink,
			&quotaTotalBytes, &s.QuotaUsedBytes, &quotaUtilizationPct,
		)
	} else {
		err = db.Read().QueryRowContext(ctx, `
			SELECT computed_at,
			       nodes_total, nodes_online, nodes_degraded, nodes_offline,
			       subjects_total, subjects_active, subjects_expired, subjects_frozen,
			       traffic_24h_uplink, traffic_24h_downlink,
			       quota_total_bytes, quota_used_bytes, quota_utilization_pct
			FROM dashboard_stats
			WHERE admin_id IS ?
		`, *adminID).Scan(
			&computedAt,
			&s.NodesTotal, &s.NodesOnline, &s.NodesDegraded, &s.NodesOffline,
			&s.SubjectsTotal, &s.SubjectsActive, &s.SubjectsExpired, &s.SubjectsFrozen,
			&s.Traffic24hUplink, &s.Traffic24hDownlink,
			&quotaTotalBytes, &s.QuotaUsedBytes, &quotaUtilizationPct,
		)
	}
	if err != nil {
		return DashboardStats{}, err
	}

	s.ComputedAt = time.Unix(computedAt, 0).UTC()
	if quotaTotalBytes.Valid {
		v := quotaTotalBytes.Int64
		s.QuotaTotalBytes = &v
	}
	if quotaUtilizationPct.Valid {
		v := quotaUtilizationPct.Float64
		s.QuotaUtilizationPct = &v
	}
	return s, nil
}

// upsertStats writes s into the dashboard_stats cache.
//
// NOTE: modernc.org/sqlite rejects NULL in a PRIMARY KEY column that also
// carries a FOREIGN KEY constraint, even though standard SQL permits it.
// Global stats (AdminID == nil) therefore cannot be persisted; those calls
// are silently skipped and the callers always recompute them on demand.
func upsertStats(ctx context.Context, db *store.Store, s DashboardStats) error {
	if s.AdminID == nil {
		// SQLite STRICT + FK + nullable PK: NULL admin_id fails FK check.
		// Global stats are computed fresh on every request instead of cached.
		return nil
	}

	var quotaTotal interface{}
	if s.QuotaTotalBytes != nil {
		quotaTotal = *s.QuotaTotalBytes
	}
	var quotaUtilPct interface{}
	if s.QuotaUtilizationPct != nil {
		quotaUtilPct = *s.QuotaUtilizationPct
	}

	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT OR REPLACE INTO dashboard_stats (
				admin_id, computed_at,
				nodes_total, nodes_online, nodes_degraded, nodes_offline,
				subjects_total, subjects_active, subjects_expired, subjects_frozen,
				traffic_24h_uplink, traffic_24h_downlink,
				quota_total_bytes, quota_used_bytes, quota_utilization_pct
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			s.AdminID, s.ComputedAt.Unix(),
			s.NodesTotal, s.NodesOnline, s.NodesDegraded, s.NodesOffline,
			s.SubjectsTotal, s.SubjectsActive, s.SubjectsExpired, s.SubjectsFrozen,
			s.Traffic24hUplink, s.Traffic24hDownlink,
			quotaTotal, s.QuotaUsedBytes, quotaUtilPct,
		)
		if err != nil {
			return fmt.Errorf("upsert dashboard_stats: %w", err)
		}
		return nil
	})
}

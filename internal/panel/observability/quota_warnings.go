package observability

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

// enforceQuotaWarnings creates alerts when subjects approach their quota limits.
// Runs periodically to warn at 80% and 90% thresholds.
func (s *Sweeper) enforceQuotaWarnings(ctx context.Context) error {
	// Query subjects at 80% quota threshold
	query80 := `
		SELECT id, name, quota_bytes, quota_used_bytes
		FROM subjects
		WHERE enabled = 1
		  AND frozen_at IS NULL
		  AND quota_bytes > 0
		  AND quota_used_bytes >= (quota_bytes * 0.8)
		  AND quota_used_bytes < quota_bytes
		  AND id NOT IN (
		    SELECT target_id FROM alerts
		    WHERE target_type = 'subject'
		      AND alert_type = 'quota_warning_80'
		      AND resolved_at IS NULL
		  )
	`

	rows80, err := s.store.Read().QueryContext(ctx, query80)
	if err != nil {
		return fmt.Errorf("query 80%% quota warnings: %w", err)
	}
	defer rows80.Close()

	var warned80 int
	for rows80.Next() {
		var id int64
		var name string
		var quota, used int64
		if err := rows80.Scan(&id, &name, &quota, &used); err != nil {
			slog.ErrorContext(ctx, "scan quota warning subject", "error", err)
			continue
		}

		pct := float64(used) / float64(quota) * 100

		err := s.store.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO alerts (alert_type, severity, target_type, target_id, message, metadata, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)
			`, "quota_warning_80", "warning", "subject", id,
				fmt.Sprintf("Subject %s has used %.1f%% of quota", name, pct),
				fmt.Sprintf(`{"quota_bytes":%d,"used_bytes":%d,"percent":%.1f}`, quota, used, pct),
				time.Now().Unix())
			return err
		})

		if err != nil {
			slog.ErrorContext(ctx, "create 80% quota alert", "subject_id", id, "error", err)
			continue
		}

		warned80++
		slog.InfoContext(ctx, "quota warning created",
			"subject_id", id,
			"subject_name", name,
			"threshold", "80%",
			"used", used,
			"quota", quota,
			"percent", fmt.Sprintf("%.1f%%", pct))
	}

	// Query subjects at 90% quota threshold
	query90 := `
		SELECT id, name, quota_bytes, quota_used_bytes
		FROM subjects
		WHERE enabled = 1
		  AND frozen_at IS NULL
		  AND quota_bytes > 0
		  AND quota_used_bytes >= (quota_bytes * 0.9)
		  AND quota_used_bytes < quota_bytes
		  AND id NOT IN (
		    SELECT target_id FROM alerts
		    WHERE target_type = 'subject'
		      AND alert_type = 'quota_warning_90'
		      AND resolved_at IS NULL
		  )
	`

	rows90, err := s.store.Read().QueryContext(ctx, query90)
	if err != nil {
		return fmt.Errorf("query 90%% quota warnings: %w", err)
	}
	defer rows90.Close()

	var warned90 int
	for rows90.Next() {
		var id int64
		var name string
		var quota, used int64
		if err := rows90.Scan(&id, &name, &quota, &used); err != nil {
			slog.ErrorContext(ctx, "scan quota warning subject", "error", err)
			continue
		}

		pct := float64(used) / float64(quota) * 100

		err := s.store.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO alerts (alert_type, severity, target_type, target_id, message, metadata, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)
			`, "quota_warning_90", "critical", "subject", id,
				fmt.Sprintf("Subject %s has used %.1f%% of quota", name, pct),
				fmt.Sprintf(`{"quota_bytes":%d,"used_bytes":%d,"percent":%.1f}`, quota, used, pct),
				time.Now().Unix())
			return err
		})

		if err != nil {
			slog.ErrorContext(ctx, "create 90% quota alert", "subject_id", id, "error", err)
			continue
		}

		warned90++
		slog.InfoContext(ctx, "quota warning created",
			"subject_id", id,
			"subject_name", name,
			"threshold", "90%",
			"used", used,
			"quota", quota,
			"percent", fmt.Sprintf("%.1f%%", pct))
	}

	if warned80 > 0 || warned90 > 0 {
		slog.InfoContext(ctx, "quota warnings enforced",
			"warned_80pct", warned80,
			"warned_90pct", warned90)
	}

	return nil
}

package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// TestSP7Schema verifies the SP7 observability tables and triggers.
func TestSP7Schema(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	t.Run("alerts table exists with constraints", func(t *testing.T) {
		// Insert valid alert
		err := s.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO alerts (alert_type, severity, target_type, target_id, state, dedup_key, first_seen_at, last_seen_at, threshold_value, current_value)
				VALUES ('cert_expiry', 'warning', 'node', 1, 'active', 'cert_expiry:node:1:warning', ?, ?, '30 days', '25 days')`,
				time.Now().Unix(), time.Now().Unix())
			return err
		})
		if err != nil {
			t.Fatalf("insert alert: %v", err)
		}

		// Verify alert exists
		var count int
		err = s.Read().QueryRow(`SELECT COUNT(*) FROM alerts WHERE alert_type = 'cert_expiry'`).Scan(&count)
		if err != nil {
			t.Fatalf("query alert: %v", err)
		}
		if count != 1 {
			t.Errorf("expected 1 alert, got %d", count)
		}
	})

	t.Run("alerts dedup_key is unique", func(t *testing.T) {
		err := s.Write(ctx, func(tx *sql.Tx) error {
			// Try to insert duplicate dedup_key
			_, err := tx.Exec(`
				INSERT INTO alerts (alert_type, severity, target_type, target_id, state, dedup_key, first_seen_at, last_seen_at)
				VALUES ('cert_expiry', 'warning', 'node', 1, 'active', 'cert_expiry:node:1:warning', ?, ?)`,
				time.Now().Unix(), time.Now().Unix())
			return err
		})
		if err == nil {
			t.Error("expected UNIQUE constraint violation, got nil")
		}
	})

	t.Run("alerts resolved_at constraint", func(t *testing.T) {
		// Active alert with resolved_at should fail
		err := s.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO alerts (alert_type, severity, target_type, target_id, state, dedup_key, first_seen_at, last_seen_at, resolved_at)
				VALUES ('quota_warning', 'critical', 'subject', 1, 'active', 'quota:subject:1:critical', ?, ?, ?)`,
				time.Now().Unix(), time.Now().Unix(), time.Now().Unix())
			return err
		})
		if err == nil {
			t.Error("expected CHECK constraint violation (active with resolved_at), got nil")
		}
	})

	t.Run("node_health_rollups_hourly table exists", func(t *testing.T) {
		// Create test node first
		var nodeID int64
		err := s.Write(ctx, func(tx *sql.Tx) error {
			res, err := tx.Exec(`INSERT INTO nodes (name, address, created_at) VALUES ('test-node-sp7', '10.0.0.1', ?)`, time.Now().Unix())
			if err != nil {
				return err
			}
			nodeID, err = res.LastInsertId()
			return err
		})
		if err != nil {
			t.Fatalf("create test node: %v", err)
		}

		// Insert hourly rollup
		hourStart := time.Now().Truncate(time.Hour).Unix()
		err = s.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO node_health_rollups_hourly (node_id, hour_start, samples, avg_load1, avg_mem_used, min_rtt_ms, avg_rtt_ms, max_rtt_ms, uptime_seconds)
				VALUES (?, ?, 120, 1.5, 1073741824, 30, 45, 60, 86400)`,
				nodeID, hourStart)
			return err
		})
		if err != nil {
			t.Fatalf("insert hourly rollup: %v", err)
		}

		// Verify rollup exists
		var samples int
		err = s.Read().QueryRow(`SELECT samples FROM node_health_rollups_hourly WHERE node_id = ?`, nodeID).Scan(&samples)
		if err != nil {
			t.Fatalf("query hourly rollup: %v", err)
		}
		if samples != 120 {
			t.Errorf("expected 120 samples, got %d", samples)
		}
	})

	t.Run("node_health_rollups_daily table exists", func(t *testing.T) {
		// Create test node
		var nodeID int64
		err := s.Write(ctx, func(tx *sql.Tx) error {
			res, err := tx.Exec(`INSERT INTO nodes (name, address, created_at) VALUES ('test-node-sp7-daily', '10.0.0.2', ?)`, time.Now().Unix())
			if err != nil {
				return err
			}
			nodeID, err = res.LastInsertId()
			return err
		})
		if err != nil {
			t.Fatalf("create test node: %v", err)
		}

		// Insert daily rollup
		dayStart := time.Now().Truncate(24 * time.Hour).Unix()
		err = s.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO node_health_rollups_daily (node_id, day_start, samples, avg_load1, avg_mem_used, min_rtt_ms, avg_rtt_ms, max_rtt_ms, uptime_seconds)
				VALUES (?, ?, 2880, 1.2, 1073741824, 25, 40, 65, 86400)`,
				nodeID, dayStart)
			return err
		})
		if err != nil {
			t.Fatalf("insert daily rollup: %v", err)
		}

		// Verify rollup exists
		var samples int
		err = s.Read().QueryRow(`SELECT samples FROM node_health_rollups_daily WHERE node_id = ?`, nodeID).Scan(&samples)
		if err != nil {
			t.Fatalf("query daily rollup: %v", err)
		}
		if samples != 2880 {
			t.Errorf("expected 2880 samples, got %d", samples)
		}
	})
}

// TestSP7RetentionTriggers verifies cleanup triggers fire correctly.
func TestSP7RetentionTriggers(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	t.Run("node_health 7-day retention", func(t *testing.T) {
		// Create test node
		var nodeID int64
		err := s.Write(ctx, func(tx *sql.Tx) error {
			res, err := tx.Exec(`INSERT INTO nodes (name, address, created_at) VALUES ('test-node-health-retention', '10.0.0.3', ?)`, time.Now().Unix())
			if err != nil {
				return err
			}
			nodeID, err = res.LastInsertId()
			return err
		})
		if err != nil {
			t.Fatalf("create test node: %v", err)
		}

		// Insert old node_health sample (8 days ago)
		oldTime := time.Now().Add(-8 * 24 * time.Hour).Unix()
		err = s.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO node_health (node_id, at, load1, mem_used, uptime_s, rtt_ms, adapter_status)
				VALUES (?, ?, 1.0, 1073741824, 86400, 50, '[]')`,
				nodeID, oldTime)
			return err
		})
		if err != nil {
			t.Fatalf("insert old node_health: %v", err)
		}

		// Insert new node_health sample (triggers cleanup)
		newTime := time.Now().Unix()
		err = s.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO node_health (node_id, at, load1, mem_used, uptime_s, rtt_ms, adapter_status)
				VALUES (?, ?, 1.2, 1073741824, 86400, 45, '[]')`,
				nodeID, newTime)
			return err
		})
		if err != nil {
			t.Fatalf("insert new node_health: %v", err)
		}

		// Verify only new sample exists (old one cleaned up)
		var count int
		err = s.Read().QueryRow(`SELECT COUNT(*) FROM node_health WHERE node_id = ?`, nodeID).Scan(&count)
		if err != nil {
			t.Fatalf("query node_health count: %v", err)
		}
		if count != 1 {
			t.Errorf("expected 1 node_health sample after cleanup, got %d", count)
		}
	})

	t.Run("hourly rollups 90-day retention", func(t *testing.T) {
		// Create test node
		var nodeID int64
		err := s.Write(ctx, func(tx *sql.Tx) error {
			res, err := tx.Exec(`INSERT INTO nodes (name, address, created_at) VALUES ('test-node-hourly-retention', '10.0.0.4', ?)`, time.Now().Unix())
			if err != nil {
				return err
			}
			nodeID, err = res.LastInsertId()
			return err
		})
		if err != nil {
			t.Fatalf("create test node: %v", err)
		}

		// Insert old hourly rollup (91 days ago)
		oldHour := time.Now().Add(-91 * 24 * time.Hour).Truncate(time.Hour).Unix()
		err = s.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO node_health_rollups_hourly (node_id, hour_start, samples, avg_load1, avg_mem_used, uptime_seconds)
				VALUES (?, ?, 120, 1.0, 1073741824, 86400)`,
				nodeID, oldHour)
			return err
		})
		if err != nil {
			t.Fatalf("insert old hourly rollup: %v", err)
		}

		// Insert new hourly rollup (triggers cleanup)
		newHour := time.Now().Truncate(time.Hour).Unix()
		err = s.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO node_health_rollups_hourly (node_id, hour_start, samples, avg_load1, avg_mem_used, uptime_seconds)
				VALUES (?, ?, 120, 1.5, 1073741824, 86400)`,
				nodeID, newHour)
			return err
		})
		if err != nil {
			t.Fatalf("insert new hourly rollup: %v", err)
		}

		// Verify only new rollup exists
		var count int
		err = s.Read().QueryRow(`SELECT COUNT(*) FROM node_health_rollups_hourly WHERE node_id = ?`, nodeID).Scan(&count)
		if err != nil {
			t.Fatalf("query hourly rollup count: %v", err)
		}
		if count != 1 {
			t.Errorf("expected 1 hourly rollup after cleanup, got %d", count)
		}
	})

	t.Run("alerts resolved retention 90 days", func(t *testing.T) {
		// Insert old resolved alert (91 days ago)
		oldTime := time.Now().Add(-91 * 24 * time.Hour).Unix()
		err := s.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO alerts (alert_type, severity, target_type, target_id, state, dedup_key, first_seen_at, last_seen_at, resolved_at)
				VALUES ('cert_expiry', 'warning', 'node', 999, 'resolved', 'cert_expiry:node:999:warning:old', ?, ?, ?)`,
				oldTime, oldTime, oldTime)
			return err
		})
		if err != nil {
			t.Fatalf("insert old resolved alert: %v", err)
		}

		// Insert new alert (triggers cleanup)
		newTime := time.Now().Unix()
		err = s.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO alerts (alert_type, severity, target_type, target_id, state, dedup_key, first_seen_at, last_seen_at)
				VALUES ('cert_expiry', 'critical', 'node', 1000, 'active', 'cert_expiry:node:1000:critical:new', ?, ?)`,
				newTime, newTime)
			return err
		})
		if err != nil {
			t.Fatalf("insert new alert: %v", err)
		}

		// Verify old resolved alert was cleaned up
		var count int
		err = s.Read().QueryRow(`SELECT COUNT(*) FROM alerts WHERE dedup_key = 'cert_expiry:node:999:warning:old'`).Scan(&count)
		if err != nil {
			t.Fatalf("query old alert: %v", err)
		}
		if count != 0 {
			t.Errorf("expected old resolved alert to be cleaned up, still found %d", count)
		}

		// Verify new alert still exists
		err = s.Read().QueryRow(`SELECT COUNT(*) FROM alerts WHERE dedup_key = 'cert_expiry:node:1000:critical:new'`).Scan(&count)
		if err != nil {
			t.Fatalf("query new alert: %v", err)
		}
		if count != 1 {
			t.Errorf("expected new alert to exist, got %d", count)
		}
	})

	t.Run("active alerts never expire", func(t *testing.T) {
		// Insert old active alert (91 days ago, but still active)
		oldTime := time.Now().Add(-91 * 24 * time.Hour).Unix()
		err := s.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO alerts (alert_type, severity, target_type, target_id, state, dedup_key, first_seen_at, last_seen_at)
				VALUES ('quota_warning', 'critical', 'subject', 1, 'active', 'quota:subject:1:critical:persist', ?, ?)`,
				oldTime, oldTime)
			return err
		})
		if err != nil {
			t.Fatalf("insert old active alert: %v", err)
		}

		// Insert another alert to trigger cleanup
		newTime := time.Now().Unix()
		err = s.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO alerts (alert_type, severity, target_type, target_id, state, dedup_key, first_seen_at, last_seen_at)
				VALUES ('cert_expiry', 'warning', 'node', 2000, 'active', 'cert_expiry:node:2000:trigger', ?, ?)`,
				newTime, newTime)
			return err
		})
		if err != nil {
			t.Fatalf("insert trigger alert: %v", err)
		}

		// Verify old active alert still exists (not cleaned up)
		var count int
		err = s.Read().QueryRow(`SELECT COUNT(*) FROM alerts WHERE dedup_key = 'quota:subject:1:critical:persist'`).Scan(&count)
		if err != nil {
			t.Fatalf("query old active alert: %v", err)
		}
		if count != 1 {
			t.Errorf("expected old active alert to persist, got %d", count)
		}
	})
}

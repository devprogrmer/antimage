package observability

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestGenerateHourlyRollup(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	rg := NewRollupGenerator(s)

	// Create test node
	var nodeID int64
	now := time.Now().UTC()
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO nodes (name, address, created_at) VALUES (?, ?, ?)`,
			"test-node-rollup", "10.0.0.1", now.Unix())
		if err != nil {
			return err
		}
		nodeID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}

	// Insert 120 heartbeat samples over 1 hour (every 30 seconds)
	hourStart := now.Truncate(time.Hour)
	err = s.Write(ctx, func(tx *sql.Tx) error {
		for i := 0; i < 120; i++ {
			sampleTime := hourStart.Add(time.Duration(i) * 30 * time.Second)
			load := 1.0 + float64(i)*0.01
			memUsed := 1073741824 + int64(i)*1000000
			rttMs := 40 + i%20

			_, err := tx.Exec(`
				INSERT INTO node_health (node_id, at, load1, mem_used, uptime_s, rtt_ms, adapter_status)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				nodeID, sampleTime.Unix(), load, memUsed, 86400, rttMs, "[]")
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("insert node_health samples: %v", err)
	}

	// Generate hourly rollup
	if err := rg.GenerateHourlyRollup(ctx, hourStart); err != nil {
		t.Fatalf("GenerateHourlyRollup: %v", err)
	}

	// Verify rollup exists
	var samples int
	var avgLoad1 float64
	var avgMemUsed int64
	var minRTT, avgRTT, maxRTT sql.NullInt64
	var uptimeSeconds int64

	err = s.Read().QueryRow(`
		SELECT samples, avg_load1, avg_mem_used, min_rtt_ms, avg_rtt_ms, max_rtt_ms, uptime_seconds
		FROM node_health_rollups_hourly
		WHERE node_id = ? AND hour_start = ?`,
		nodeID, hourStart.Unix()).Scan(&samples, &avgLoad1, &avgMemUsed, &minRTT, &avgRTT, &maxRTT, &uptimeSeconds)
	if err != nil {
		t.Fatalf("query hourly rollup: %v", err)
	}

	if samples != 120 {
		t.Errorf("samples = %d, want 120", samples)
	}
	if avgLoad1 < 1.0 || avgLoad1 > 2.5 {
		t.Errorf("avg_load1 = %.2f, expected in range [1.0, 2.5]", avgLoad1)
	}
	if !minRTT.Valid || minRTT.Int64 != 40 {
		t.Errorf("min_rtt_ms = %v, want 40", minRTT)
	}
	if !maxRTT.Valid || maxRTT.Int64 != 59 {
		t.Errorf("max_rtt_ms = %v, want 59", maxRTT)
	}
	if uptimeSeconds != 86400 {
		t.Errorf("uptime_seconds = %d, want 86400", uptimeSeconds)
	}
}

func TestGenerateHourlyRollup_MultipleNodes(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	rg := NewRollupGenerator(s)

	now := time.Now().UTC()
	hourStart := now.Truncate(time.Hour)

	// Create 2 nodes
	var node1ID, node2ID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res1, err := tx.Exec(`INSERT INTO nodes (name, address, created_at) VALUES (?, ?, ?)`,
			"node-1", "10.0.0.1", now.Unix())
		if err != nil {
			return err
		}
		node1ID, _ = res1.LastInsertId()

		res2, err := tx.Exec(`INSERT INTO nodes (name, address, created_at) VALUES (?, ?, ?)`,
			"node-2", "10.0.0.2", now.Unix())
		if err != nil {
			return err
		}
		node2ID, _ = res2.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("create nodes: %v", err)
	}

	// Insert samples for both nodes
	err = s.Write(ctx, func(tx *sql.Tx) error {
		for i := 0; i < 60; i++ {
			sampleTime := hourStart.Add(time.Duration(i) * time.Minute)

			// Node 1
			_, err := tx.Exec(`
				INSERT INTO node_health (node_id, at, load1, mem_used, uptime_s, rtt_ms, adapter_status)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				node1ID, sampleTime.Unix(), 1.5, 1073741824, 86400, 45, "[]")
			if err != nil {
				return err
			}

			// Node 2
			_, err = tx.Exec(`
				INSERT INTO node_health (node_id, at, load1, mem_used, uptime_s, rtt_ms, adapter_status)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				node2ID, sampleTime.Unix(), 2.0, 2147483648, 172800, 30, "[]")
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("insert samples: %v", err)
	}

	// Generate rollup
	if err := rg.GenerateHourlyRollup(ctx, hourStart); err != nil {
		t.Fatalf("GenerateHourlyRollup: %v", err)
	}

	// Verify both nodes have rollups
	var count int
	err = s.Read().QueryRow(`SELECT COUNT(*) FROM node_health_rollups_hourly WHERE hour_start = ?`,
		hourStart.Unix()).Scan(&count)
	if err != nil {
		t.Fatalf("count rollups: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 rollups (one per node), got %d", count)
	}

	// Verify node1 rollup
	var node1Samples int
	var node1AvgLoad float64
	err = s.Read().QueryRow(`SELECT samples, avg_load1 FROM node_health_rollups_hourly WHERE node_id = ?`,
		node1ID).Scan(&node1Samples, &node1AvgLoad)
	if err != nil {
		t.Fatalf("query node1 rollup: %v", err)
	}
	if node1Samples != 60 {
		t.Errorf("node1 samples = %d, want 60", node1Samples)
	}
	if node1AvgLoad != 1.5 {
		t.Errorf("node1 avg_load1 = %.2f, want 1.50", node1AvgLoad)
	}

	// Verify node2 rollup
	var node2Samples int
	var node2AvgLoad float64
	err = s.Read().QueryRow(`SELECT samples, avg_load1 FROM node_health_rollups_hourly WHERE node_id = ?`,
		node2ID).Scan(&node2Samples, &node2AvgLoad)
	if err != nil {
		t.Fatalf("query node2 rollup: %v", err)
	}
	if node2Samples != 60 {
		t.Errorf("node2 samples = %d, want 60", node2Samples)
	}
	if node2AvgLoad != 2.0 {
		t.Errorf("node2 avg_load1 = %.2f, want 2.00", node2AvgLoad)
	}
}

func TestGenerateHourlyRollup_NoData(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	rg := NewRollupGenerator(s)

	now := time.Now().UTC()
	hourStart := now.Truncate(time.Hour)

	// Generate rollup for hour with no data - should not error
	if err := rg.GenerateHourlyRollup(ctx, hourStart); err != nil {
		t.Errorf("GenerateHourlyRollup with no data: unexpected error: %v", err)
	}

	// Verify no rollup created
	var count int
	err := s.Read().QueryRow(`SELECT COUNT(*) FROM node_health_rollups_hourly WHERE hour_start = ?`,
		hourStart.Unix()).Scan(&count)
	if err != nil {
		t.Fatalf("count rollups: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rollups for empty hour, got %d", count)
	}
}

func TestGenerateHourlyRollup_Idempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	rg := NewRollupGenerator(s)

	now := time.Now().UTC()
	hourStart := now.Truncate(time.Hour)

	// Create node and samples
	var nodeID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO nodes (name, address, created_at) VALUES (?, ?, ?)`,
			"node-idempotent", "10.0.0.1", now.Unix())
		if err != nil {
			return err
		}
		nodeID, _ = res.LastInsertId()

		for i := 0; i < 10; i++ {
			sampleTime := hourStart.Add(time.Duration(i) * time.Minute)
			_, err := tx.Exec(`
				INSERT INTO node_health (node_id, at, load1, mem_used, uptime_s, rtt_ms, adapter_status)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				nodeID, sampleTime.Unix(), 1.0, 1073741824, 86400, 50, "[]")
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Generate rollup first time
	if err := rg.GenerateHourlyRollup(ctx, hourStart); err != nil {
		t.Fatalf("GenerateHourlyRollup (first): %v", err)
	}

	// Get first rollup data
	var firstSamples int
	var firstAvgLoad float64
	err = s.Read().QueryRow(`SELECT samples, avg_load1 FROM node_health_rollups_hourly WHERE node_id = ? AND hour_start = ?`,
		nodeID, hourStart.Unix()).Scan(&firstSamples, &firstAvgLoad)
	if err != nil {
		t.Fatalf("query first rollup: %v", err)
	}

	// Generate rollup second time (idempotent)
	if err := rg.GenerateHourlyRollup(ctx, hourStart); err != nil {
		t.Fatalf("GenerateHourlyRollup (second): %v", err)
	}

	// Verify same data (INSERT OR REPLACE behavior)
	var secondSamples int
	var secondAvgLoad float64
	err = s.Read().QueryRow(`SELECT samples, avg_load1 FROM node_health_rollups_hourly WHERE node_id = ? AND hour_start = ?`,
		nodeID, hourStart.Unix()).Scan(&secondSamples, &secondAvgLoad)
	if err != nil {
		t.Fatalf("query second rollup: %v", err)
	}

	if firstSamples != secondSamples {
		t.Errorf("samples changed: first=%d, second=%d", firstSamples, secondSamples)
	}
	if firstAvgLoad != secondAvgLoad {
		t.Errorf("avg_load1 changed: first=%.2f, second=%.2f", firstAvgLoad, secondAvgLoad)
	}

	// Verify only one rollup exists (not duplicated)
	var count int
	err = s.Read().QueryRow(`SELECT COUNT(*) FROM node_health_rollups_hourly WHERE node_id = ? AND hour_start = ?`,
		nodeID, hourStart.Unix()).Scan(&count)
	if err != nil {
		t.Fatalf("count rollups: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 rollup after repeated execution, got %d", count)
	}
}

func TestGenerateDailyRollup(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	rg := NewRollupGenerator(s)

	now := time.Now().UTC()
	dayStart := now.Truncate(24 * time.Hour)

	// Create node
	var nodeID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO nodes (name, address, created_at) VALUES (?, ?, ?)`,
			"node-daily", "10.0.0.1", now.Unix())
		if err != nil {
			return err
		}
		nodeID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}

	// Insert 24 hourly rollups for one day
	err = s.Write(ctx, func(tx *sql.Tx) error {
		for hour := 0; hour < 24; hour++ {
			hourStart := dayStart.Add(time.Duration(hour) * time.Hour)
			_, err := tx.Exec(`
				INSERT INTO node_health_rollups_hourly (
					node_id, hour_start, samples, avg_load1, avg_mem_used,
					min_rtt_ms, avg_rtt_ms, max_rtt_ms, uptime_seconds
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				nodeID, hourStart.Unix(), 120, 1.5+float64(hour)*0.05, 1073741824,
				30, 45, 60, 86400)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("insert hourly rollups: %v", err)
	}

	// Generate daily rollup
	if err := rg.GenerateDailyRollup(ctx, dayStart); err != nil {
		t.Fatalf("GenerateDailyRollup: %v", err)
	}

	// Verify daily rollup
	var samples int
	var avgLoad1 float64
	var minRTT, avgRTT, maxRTT sql.NullInt64

	err = s.Read().QueryRow(`
		SELECT samples, avg_load1, min_rtt_ms, avg_rtt_ms, max_rtt_ms
		FROM node_health_rollups_daily
		WHERE node_id = ? AND day_start = ?`,
		nodeID, dayStart.Unix()).Scan(&samples, &avgLoad1, &minRTT, &avgRTT, &maxRTT)
	if err != nil {
		t.Fatalf("query daily rollup: %v", err)
	}

	if samples != 2880 {
		t.Errorf("samples = %d, want 2880 (24 hours * 120 samples)", samples)
	}
	if avgLoad1 < 1.5 || avgLoad1 > 2.5 {
		t.Errorf("avg_load1 = %.2f, expected in range [1.5, 2.5]", avgLoad1)
	}
	if !minRTT.Valid || minRTT.Int64 != 30 {
		t.Errorf("min_rtt_ms = %v, want 30", minRTT)
	}
	if !avgRTT.Valid || avgRTT.Int64 != 45 {
		t.Errorf("avg_rtt_ms = %v, want 45", avgRTT)
	}
	if !maxRTT.Valid || maxRTT.Int64 != 60 {
		t.Errorf("max_rtt_ms = %v, want 60", maxRTT)
	}
}

func TestGenerateDailyRollup_SparseHours(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	rg := NewRollupGenerator(s)

	now := time.Now().UTC()
	dayStart := now.Truncate(24 * time.Hour)

	// Create node
	var nodeID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO nodes (name, address, created_at) VALUES (?, ?, ?)`,
			"node-sparse", "10.0.0.1", now.Unix())
		if err != nil {
			return err
		}
		nodeID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}

	// Insert only 6 hourly rollups (sparse data - node offline most of day)
	err = s.Write(ctx, func(tx *sql.Tx) error {
		for _, hour := range []int{0, 3, 6, 12, 18, 23} {
			hourStart := dayStart.Add(time.Duration(hour) * time.Hour)
			_, err := tx.Exec(`
				INSERT INTO node_health_rollups_hourly (
					node_id, hour_start, samples, avg_load1, avg_mem_used,
					min_rtt_ms, avg_rtt_ms, max_rtt_ms, uptime_seconds
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				nodeID, hourStart.Unix(), 50, 1.2, 1073741824, 40, 50, 65, 86400)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("insert sparse hourly rollups: %v", err)
	}

	// Generate daily rollup
	if err := rg.GenerateDailyRollup(ctx, dayStart); err != nil {
		t.Fatalf("GenerateDailyRollup: %v", err)
	}

	// Verify daily rollup aggregates only available hours
	var samples int
	err = s.Read().QueryRow(`SELECT samples FROM node_health_rollups_daily WHERE node_id = ?`,
		nodeID).Scan(&samples)
	if err != nil {
		t.Fatalf("query daily rollup: %v", err)
	}

	if samples != 300 {
		t.Errorf("samples = %d, want 300 (6 hours * 50 samples)", samples)
	}
}

func TestRollupRetentionTriggers(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()

	// Create node
	var nodeID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO nodes (name, address, created_at) VALUES (?, ?, ?)`,
			"node-retention", "10.0.0.1", now.Unix())
		if err != nil {
			return err
		}
		nodeID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}

	// Insert old hourly rollup (91 days ago)
	oldHourStart := now.Add(-91 * 24 * time.Hour).Truncate(time.Hour)
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO node_health_rollups_hourly (
				node_id, hour_start, samples, avg_load1, avg_mem_used, uptime_seconds
			) VALUES (?, ?, ?, ?, ?, ?)`,
			nodeID, oldHourStart.Unix(), 120, 1.0, 1073741824, 86400)
		return err
	})
	if err != nil {
		t.Fatalf("insert old hourly rollup: %v", err)
	}

	// Insert new hourly rollup (triggers cleanup)
	newHourStart := now.Truncate(time.Hour)
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO node_health_rollups_hourly (
				node_id, hour_start, samples, avg_load1, avg_mem_used, uptime_seconds
			) VALUES (?, ?, ?, ?, ?, ?)`,
			nodeID, newHourStart.Unix(), 120, 1.5, 1073741824, 86400)
		return err
	})
	if err != nil {
		t.Fatalf("insert new hourly rollup: %v", err)
	}

	// Verify only new rollup exists (old one cleaned up by trigger)
	var count int
	err = s.Read().QueryRow(`SELECT COUNT(*) FROM node_health_rollups_hourly WHERE node_id = ?`,
		nodeID).Scan(&count)
	if err != nil {
		t.Fatalf("count rollups: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 rollup after cleanup, got %d", count)
	}

	// Verify it's the new one
	var hourStart int64
	err = s.Read().QueryRow(`SELECT hour_start FROM node_health_rollups_hourly WHERE node_id = ?`,
		nodeID).Scan(&hourStart)
	if err != nil {
		t.Fatalf("query rollup: %v", err)
	}
	if hourStart != newHourStart.Unix() {
		t.Errorf("rollup hour_start = %d, want %d (new rollup)", hourStart, newHourStart.Unix())
	}
}

package observability

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

// RollupGenerator aggregates detailed node_health data into hourly and daily rollups.
type RollupGenerator struct {
	store *store.Store
}

// NewRollupGenerator creates a new rollup generator instance.
func NewRollupGenerator(s *store.Store) *RollupGenerator {
	return &RollupGenerator{store: s}
}

// RunHourly starts hourly rollup generation. Runs at minute 5 of each hour until context is cancelled.
func (rg *RollupGenerator) RunHourly(ctx context.Context) {
	// Calculate next hour + 5 minutes
	now := time.Now().UTC()
	nextRun := now.Truncate(time.Hour).Add(time.Hour).Add(5 * time.Minute)
	if now.After(nextRun.Add(-time.Hour)) {
		// If we're already past this hour's :05, schedule for next hour
		nextRun = nextRun.Add(time.Hour)
	}

	// Wait until next run time
	timer := time.NewTimer(time.Until(nextRun))
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		// Generate rollup for previous hour
		hourStart := now.Truncate(time.Hour)
		if err := rg.GenerateHourlyRollup(ctx, hourStart); err != nil {
			log.Printf("[observability] hourly rollup failed: %v", err)
		}
	}

	// Continue with regular ticker
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hourStart := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
			if err := rg.GenerateHourlyRollup(ctx, hourStart); err != nil {
				log.Printf("[observability] hourly rollup failed: %v", err)
			}
		}
	}
}

// RunDaily starts daily rollup generation. Runs at 00:15 each day until context is cancelled.
func (rg *RollupGenerator) RunDaily(ctx context.Context) {
	// Calculate next day at 00:15
	now := time.Now().UTC()
	nextRun := now.Truncate(24 * time.Hour).Add(24 * time.Hour).Add(15 * time.Minute)
	if now.Hour() == 0 && now.Minute() < 15 {
		// If it's 00:00-00:14, run today at 00:15
		nextRun = now.Truncate(24 * time.Hour).Add(15 * time.Minute)
	}

	// Wait until next run time
	timer := time.NewTimer(time.Until(nextRun))
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		// Generate rollup for previous day
		dayStart := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
		if err := rg.GenerateDailyRollup(ctx, dayStart); err != nil {
			log.Printf("[observability] daily rollup failed: %v", err)
		}
	}

	// Continue with daily ticker
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			dayStart := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
			if err := rg.GenerateDailyRollup(ctx, dayStart); err != nil {
				log.Printf("[observability] daily rollup failed: %v", err)
			}
		}
	}
}

// GenerateHourlyRollup aggregates node_health samples for the given hour.
// Idempotent: uses INSERT OR REPLACE to allow re-execution.
func (rg *RollupGenerator) GenerateHourlyRollup(ctx context.Context, hourStart time.Time) error {
	hourStartUnix := hourStart.Unix()
	hourEndUnix := hourStart.Add(time.Hour).Unix()

	return rg.store.Write(ctx, func(tx *sql.Tx) error {
		// Aggregate node_health samples for this hour
		_, err := tx.ExecContext(ctx, `
			INSERT OR REPLACE INTO node_health_rollups_hourly (
				node_id, hour_start, samples, avg_load1, avg_mem_used,
				min_rtt_ms, avg_rtt_ms, max_rtt_ms, uptime_seconds
			)
			SELECT
				node_id,
				? AS hour_start,
				COUNT(*) AS samples,
				AVG(load1) AS avg_load1,
				CAST(AVG(mem_used) AS INTEGER) AS avg_mem_used,
				MIN(CASE WHEN rtt_ms > 0 THEN rtt_ms END) AS min_rtt_ms,
				CAST(AVG(CASE WHEN rtt_ms > 0 THEN rtt_ms END) AS INTEGER) AS avg_rtt_ms,
				MAX(rtt_ms) AS max_rtt_ms,
				MAX(uptime_s) AS uptime_seconds
			FROM node_health
			WHERE at >= ? AND at < ?
			GROUP BY node_id
			HAVING COUNT(*) > 0`,
			hourStartUnix, hourStartUnix, hourEndUnix)

		if err != nil {
			return fmt.Errorf("insert hourly rollup: %w", err)
		}

		return nil
	})
}

// GenerateDailyRollup aggregates hourly rollups for the given day.
// Idempotent: uses INSERT OR REPLACE to allow re-execution.
func (rg *RollupGenerator) GenerateDailyRollup(ctx context.Context, dayStart time.Time) error {
	dayStartUnix := dayStart.Unix()
	dayEndUnix := dayStart.Add(24 * time.Hour).Unix()

	return rg.store.Write(ctx, func(tx *sql.Tx) error {
		// Aggregate hourly rollups for this day
		_, err := tx.ExecContext(ctx, `
			INSERT OR REPLACE INTO node_health_rollups_daily (
				node_id, day_start, samples, avg_load1, avg_mem_used,
				min_rtt_ms, avg_rtt_ms, max_rtt_ms, uptime_seconds
			)
			SELECT
				node_id,
				? AS day_start,
				SUM(samples) AS samples,
				AVG(avg_load1) AS avg_load1,
				CAST(AVG(avg_mem_used) AS INTEGER) AS avg_mem_used,
				MIN(min_rtt_ms) AS min_rtt_ms,
				CAST(AVG(avg_rtt_ms) AS INTEGER) AS avg_rtt_ms,
				MAX(max_rtt_ms) AS max_rtt_ms,
				MAX(uptime_seconds) AS uptime_seconds
			FROM node_health_rollups_hourly
			WHERE hour_start >= ? AND hour_start < ?
			GROUP BY node_id
			HAVING SUM(samples) > 0`,
			dayStartUnix, dayStartUnix, dayEndUnix)

		if err != nil {
			return fmt.Errorf("insert daily rollup: %w", err)
		}

		return nil
	})
}

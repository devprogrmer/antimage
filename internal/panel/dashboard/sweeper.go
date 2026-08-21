// Package dashboard provides materialized aggregate statistics for the admin
// dashboard. Stats are cached in the dashboard_stats table and recomputed when
// the cache is stale (older than 60 seconds) or missing.
package dashboard

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

// RefreshInterval is how often the background sweeper refreshes global stats.
const RefreshInterval = 60 * time.Second

// StartSweeper launches a background goroutine that refreshes the global
// (admin_id IS NULL) dashboard stats every RefreshInterval until ctx is done.
// It returns nil immediately after launching the goroutine; the goroutine runs
// until ctx is cancelled.
func StartSweeper(ctx context.Context, db *store.Store) error {
	go func() {
		ticker := time.NewTicker(RefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := RefreshStats(ctx, db); err != nil {
					log.Printf("dashboard sweeper: refresh failed: %v", err)
				}
			}
		}
	}()
	return nil
}

// RefreshStats computes fresh global stats and persists them to the cache.
func RefreshStats(ctx context.Context, db *store.Store) error {
	s, err := ComputeStats(ctx, db, nil)
	if err != nil {
		return fmt.Errorf("compute stats: %w", err)
	}
	if err := upsertStats(ctx, db, s); err != nil {
		return fmt.Errorf("upsert stats: %w", err)
	}
	return nil
}

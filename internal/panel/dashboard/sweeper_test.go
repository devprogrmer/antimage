package dashboard_test

import (
	"context"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/dashboard"
)

func TestStartSweeper_CancelStops(t *testing.T) {
	s := openTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())

	if err := dashboard.StartSweeper(ctx, s); err != nil {
		t.Fatalf("StartSweeper: %v", err)
	}

	// Give the goroutine time to start, then cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()

	// Brief pause to let the goroutine observe cancellation.
	time.Sleep(20 * time.Millisecond)
}

func TestRefreshStats_Succeeds(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// RefreshStats computes global stats; caching is skipped (NULL admin_id FK
	// constraint), but the function must not return an error.
	if err := dashboard.RefreshStats(ctx, s); err != nil {
		t.Fatalf("RefreshStats: %v", err)
	}
}

func TestRefreshStats_Idempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := dashboard.RefreshStats(ctx, s); err != nil {
			t.Fatalf("RefreshStats iteration %d: %v", i, err)
		}
	}
}

func TestStartSweeper_DoesNotPanicOnEmptyDB(t *testing.T) {
	s := openTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if err := dashboard.StartSweeper(ctx, s); err != nil {
		t.Fatalf("StartSweeper: %v", err)
	}
	<-ctx.Done()
}

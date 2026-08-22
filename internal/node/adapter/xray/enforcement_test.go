package xray

import (
	"context"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/node/enforcement"
)

// mockRuntime for testing connection tracker
type mockRuntime struct {
	stats       []UserStat
	removedUser map[string]bool // track which users were removed
}

func (m *mockRuntime) Available(ctx context.Context) error { return nil }

func (m *mockRuntime) AddUser(ctx context.Context, tag string, u User, protocol Protocol) error {
	return nil
}

func (m *mockRuntime) RemoveUser(ctx context.Context, tag, email string) error {
	if m.removedUser == nil {
		m.removedUser = make(map[string]bool)
	}
	m.removedUser[email] = true
	return nil
}

func (m *mockRuntime) Reload(ctx context.Context) error  { return nil }
func (m *mockRuntime) Restart(ctx context.Context) error { return nil }
func (m *mockRuntime) Healthy(ctx context.Context) (bool, string) {
	return true, "active"
}

func (m *mockRuntime) QueryStats(ctx context.Context) ([]UserStat, error) {
	return m.stats, nil
}

func TestConnectionTracker(t *testing.T) {
	t.Run("registers new connection", func(t *testing.T) {
		enforcer := enforcement.New()
		runtime := &mockRuntime{
			stats: []UserStat{
				{Email: "subject-1@antimage", Uplink: 1000, Downlink: 2000},
			},
		}

		adapter := &Adapter{rt: runtime}
		tracker := NewConnectionTracker(adapter, enforcer)

		ctx := context.Background()
		err := tracker.Sync(ctx, "test-inbound")
		if err != nil {
			t.Fatalf("Sync failed: %v", err)
		}

		// Verify connection was registered
		conns := enforcer.GetActiveConnections(1)
		if len(conns) != 1 {
			t.Errorf("expected 1 connection, got %d", len(conns))
		}

		if conns[0].ID != "xray-subject-1@antimage" {
			t.Errorf("unexpected connection ID: %s", conns[0].ID)
		}
	})

	t.Run("enforces connection limit", func(t *testing.T) {
		enforcer := enforcement.New()

		// Set connection limit to 2
		maxConns := int64(2)
		enforcer.UpdatePolicies([]enforcement.Policy{
			{SubjectID: 1, MaxConnections: &maxConns},
		})

		runtime := &mockRuntime{
			stats: []UserStat{
				{Email: "subject-1@antimage", Uplink: 1000, Downlink: 2000},
			},
		}

		adapter := &Adapter{rt: runtime}
		tracker := NewConnectionTracker(adapter, enforcer)

		ctx := context.Background()

		// First sync - registers connection 1
		tracker.Sync(ctx, "test-inbound")

		// Add second connection
		runtime.stats = append(runtime.stats,
			UserStat{Email: "subject-1@antimage", Uplink: 500, Downlink: 1000})

		// This should succeed (connection 2)
		tracker.Sync(ctx, "test-inbound")

		// Verify 1 connection (Xray stats has duplicate email, but we only track unique)
		conns := enforcer.GetActiveConnections(1)
		if len(conns) != 1 {
			t.Errorf("expected 1 connection (unique email), got %d", len(conns))
		}
	})

	t.Run("terminates violating connection", func(t *testing.T) {
		enforcer := enforcement.New()

		// Set connection limit to 1
		maxConns := int64(1)
		enforcer.UpdatePolicies([]enforcement.Policy{
			{SubjectID: 1, MaxConnections: &maxConns},
		})

		// Pre-register a connection to reach the limit
		enforcer.CheckAndRegisterConnection("existing", 1, "dev-1", "1.1.1.1", "test")

		runtime := &mockRuntime{
			stats: []UserStat{
				{Email: "subject-1@antimage", Uplink: 1000, Downlink: 2000},
			},
		}

		adapter := &Adapter{rt: runtime}
		tracker := NewConnectionTracker(adapter, enforcer)

		ctx := context.Background()

		// This should trigger termination (connection limit exceeded)
		err := tracker.Sync(ctx, "test-inbound")
		if err != nil {
			t.Logf("Sync completed with policy violations: %v", err)
		}

		// Verify the user was removed from Xray
		if !runtime.removedUser["subject-1@antimage"] {
			t.Error("expected violating user to be removed from Xray")
		}

		// Verify connection was NOT registered
		conns := enforcer.GetActiveConnections(1)
		// Should still have only the pre-registered connection
		if len(conns) != 1 {
			t.Errorf("expected 1 connection (pre-registered), got %d", len(conns))
		}
		if conns[0].ID != "existing" {
			t.Errorf("expected existing connection, got %s", conns[0].ID)
		}
	})

	t.Run("detects disconnections", func(t *testing.T) {
		enforcer := enforcement.New()
		runtime := &mockRuntime{
			stats: []UserStat{
				{Email: "subject-1@antimage", Uplink: 1000, Downlink: 2000},
			},
		}

		adapter := &Adapter{rt: runtime}
		tracker := NewConnectionTracker(adapter, enforcer)

		ctx := context.Background()

		// Register connection
		tracker.Sync(ctx, "test-inbound")

		// Verify registered
		if len(enforcer.GetActiveConnections(1)) != 1 {
			t.Fatal("connection should be registered")
		}

		// Remove from stats (simulate disconnect)
		runtime.stats = []UserStat{}

		// Sync again
		tracker.Sync(ctx, "test-inbound")

		// Verify unregistered
		if len(enforcer.GetActiveConnections(1)) != 0 {
			t.Error("connection should be unregistered")
		}
	})

	t.Run("resets tracking state", func(t *testing.T) {
		enforcer := enforcement.New()
		runtime := &mockRuntime{
			stats: []UserStat{
				{Email: "subject-1@antimage", Uplink: 1000, Downlink: 2000},
			},
		}

		adapter := &Adapter{rt: runtime}
		tracker := NewConnectionTracker(adapter, enforcer)

		ctx := context.Background()

		// Register connection
		tracker.Sync(ctx, "test-inbound")

		// Reset
		tracker.Reset()

		// Next sync should see it as "new" again
		// (We can't easily test this without tracking internal state,
		// but Reset() should clear lastStats)
		if len(tracker.lastStats) != 0 {
			t.Error("Reset should clear lastStats")
		}
	})
}

func TestParseSubjectEmail(t *testing.T) {
	tests := []struct {
		email     string
		wantID    int64
		wantError bool
	}{
		{"subject-1@antimage", 1, false},
		{"subject-123@antimage", 123, false},
		{"subject-999@antimage", 999, false},
		{"invalid@antimage", 0, true},
		{"subject-@antimage", 0, true},
		{"subject-abc@antimage", 0, true},
		{"no-at-sign", 0, true},
		{"", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.email, func(t *testing.T) {
			gotID, err := parseSubjectEmail(tt.email)

			if tt.wantError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if gotID != tt.wantID {
				t.Errorf("expected ID %d, got %d", tt.wantID, gotID)
			}
		})
	}
}

func TestEnforcementLoop(t *testing.T) {
	t.Run("runs periodically and stops on context cancel", func(t *testing.T) {
		enforcer := enforcement.New()
		runtime := &mockRuntime{
			stats: []UserStat{
				{Email: "subject-1@antimage", Uplink: 1000, Downlink: 2000},
			},
		}

		adapter := &Adapter{rt: runtime}
		tracker := NewConnectionTracker(adapter, enforcer)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		done := make(chan struct{})
		go func() {
			tracker.EnforcementLoop(ctx, "test-inbound", 50*time.Millisecond)
			close(done)
		}()

		// Let it run a few iterations
		time.Sleep(150 * time.Millisecond)

		// Cancel and verify it stops
		cancel()

		select {
		case <-done:
			// Loop stopped as expected
		case <-time.After(1 * time.Second):
			t.Fatal("enforcement loop did not stop after context cancel")
		}
	})
}

package xray

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/amyrm/antimage/internal/node/enforcement"
)

// ConnectionTracker tracks active Xray connections for enforcement.
// It periodically queries Xray stats and enforces policies by terminating
// violating connections.
type ConnectionTracker struct {
	adapter   *Adapter
	enforcer  *enforcement.Enforcer
	lastStats map[string]UserStat // email -> last seen stats
	mu        sync.Mutex
}

// NewConnectionTracker creates a connection tracker for enforcement.
func NewConnectionTracker(adapter *Adapter, enforcer *enforcement.Enforcer) *ConnectionTracker {
	return &ConnectionTracker{
		adapter:   adapter,
		enforcer:  enforcer,
		lastStats: make(map[string]UserStat),
	}
}

// Sync queries Xray stats and enforces policies.
// This should be called periodically (e.g., every 5-10 seconds).
//
// Classification: BEST_EFFORT enforcement
// - Connections are accepted by Xray first, then checked
// - Brief window (up to sync interval) where policy violations exist
// - Violating connections are terminated retroactively
func (ct *ConnectionTracker) Sync(ctx context.Context, inboundTag string) error {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	// Query current Xray stats
	stats, err := ct.adapter.rt.QueryStats(ctx)
	if err != nil {
		return fmt.Errorf("query stats: %w", err)
	}

	current := make(map[string]UserStat)
	for _, stat := range stats {
		current[stat.Email] = stat

		// Check if this is a new connection (not seen before)
		if _, exists := ct.lastStats[stat.Email]; !exists {
			if err := ct.handleNewConnection(ctx, inboundTag, stat.Email); err != nil {
				slog.WarnContext(ctx, "failed to register connection",
					"email", stat.Email, "error", err)
			}
		}
	}

	// Detect disconnections
	for email := range ct.lastStats {
		if _, stillActive := current[email]; !stillActive {
			ct.handleDisconnection(ctx, email)
		}
	}

	ct.lastStats = current
	return nil
}

// handleNewConnection registers a new connection with the enforcer.
// If policy is violated, the connection is terminated via RemoveUser.
func (ct *ConnectionTracker) handleNewConnection(ctx context.Context, inboundTag, email string) error {
	if ct.enforcer == nil {
		return nil // Enforcement disabled
	}

	// Extract subject ID from the tag. Enforcement is per person, not per
	// inbound -- a device limit counts a subject's connections across every
	// service -- so the service id the tag now also carries is discarded here.
	subjectID, _, err := parseSubjectEmail(email)
	if err != nil {
		slog.WarnContext(ctx, "failed to parse subject email",
			"email", email, "error", err)
		return nil // Don't block on parse errors
	}

	// Generate connection ID (email is unique per user in Xray)
	connID := fmt.Sprintf("xray-%s", email)

	// Device ID: For Xray, we use subject ID as device ID since we don't
	// have true device fingerprints. This means MaxDevices effectively
	// becomes MaxConnections for Xray.
	// TODO: Extract device ID from TLS client cert or custom header
	deviceID := fmt.Sprintf("xray-subject-%d", subjectID)

	// Source IP: Not available from Xray stats API
	// We use a placeholder. This means IP limits cannot be enforced via this mechanism.
	sourceIP := "0.0.0.0"

	// Attempt to register connection
	err = ct.enforcer.CheckAndRegisterConnection(connID, subjectID, deviceID, sourceIP, "xray")
	if err != nil {
		// Policy violation - terminate the connection
		slog.InfoContext(ctx, "terminating connection due to policy violation",
			"subject_id", subjectID,
			"email", email,
			"reason", err.Error())

		// Remove user from Xray to terminate connection
		if removeErr := ct.adapter.rt.RemoveUser(ctx, inboundTag, email); removeErr != nil {
			slog.ErrorContext(ctx, "failed to terminate violating connection",
				"email", email,
				"error", removeErr)
			return fmt.Errorf("terminate connection: %w", removeErr)
		}

		return err // Return original policy violation error
	}

	slog.DebugContext(ctx, "connection registered",
		"subject_id", subjectID,
		"email", email,
		"conn_id", connID)

	return nil
}

// handleDisconnection unregisters a connection from the enforcer.
func (ct *ConnectionTracker) handleDisconnection(ctx context.Context, email string) {
	if ct.enforcer == nil {
		return
	}

	connID := fmt.Sprintf("xray-%s", email)
	ct.enforcer.UnregisterConnection(connID)

	slog.DebugContext(ctx, "connection unregistered",
		"email", email,
		"conn_id", connID)
}

// Reset clears all tracked connections.
// Should be called after Xray restart or when enforcement is restarted.
func (ct *ConnectionTracker) Reset() {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	ct.lastStats = make(map[string]UserStat)
}

// EnforcementLoop runs the enforcement sync loop at the specified interval.
// This should be called as a goroutine from the node agent.
func (ct *ConnectionTracker) EnforcementLoop(ctx context.Context, inboundTag string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	slog.InfoContext(ctx, "starting Xray enforcement loop",
		"interval", interval,
		"inbound", inboundTag)

	for {
		select {
		case <-ctx.Done():
			slog.InfoContext(ctx, "stopping Xray enforcement loop")
			return

		case <-ticker.C:
			if err := ct.Sync(ctx, inboundTag); err != nil {
				slog.WarnContext(ctx, "enforcement sync failed",
					"error", err)
			}
		}
	}
}

// StartEnforcement is called by the node agent to start enforcement for this adapter.
// This implements the interface expected by the agent's startXrayEnforcementIfSupported.
func (a *Adapter) StartEnforcement(ctx context.Context, enforcer *enforcement.Enforcer, interval time.Duration) {
	tracker := NewConnectionTracker(a, enforcer)

	// Start enforcement for all services
	// Note: We use a generic inbound tag since we're tracking all users via stats
	// The actual inbound tag doesn't matter for RemoveUser calls as long as
	// we use the correct email identifier
	go tracker.EnforcementLoop(ctx, "antimage-inbound", interval)
}

// Package agent provides enforcement integration for the node agent.
package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/amyrm/antimage/internal/node/adapter"
	"github.com/amyrm/antimage/internal/node/enforcement"
	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
)

// EnforcementSync syncs enforcement policies from desired state to the enforcer.
func (c *Client) syncEnforcement(desired adapter.Desired) {
	if c.enforcer == nil {
		return
	}

	policies := make([]enforcement.Policy, 0, len(desired.Subjects))
	for _, subj := range desired.Subjects {
		policies = append(policies, enforcement.Policy{
			SubjectID:          subj.ID,
			MaxDevices:         subj.MaxDevices,
			MaxIPs:             subj.MaxIPs,
			MaxConnections:     subj.MaxConnections,
			SpeedLimitUpKbps:   subj.SpeedLimitUpKbps,
			SpeedLimitDownKbps: subj.SpeedLimitDownKbps,
		})
	}

	c.enforcer.UpdatePolicies(policies)
}

// EnforcementStatsLoop periodically reports enforcement statistics to the panel.
func (c *Client) EnforcementStatsLoop(ctx context.Context, stream pb.Control_StreamClient, interval time.Duration) {
	if c.enforcer == nil {
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Also cleanup stale connections periodically
	cleanupTicker := time.NewTicker(5 * time.Minute)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stats := c.enforcer.Stats()
			slog.DebugContext(ctx, "enforcement stats",
				"connections", stats.TotalConnections,
				"subjects", stats.TrackedSubjects,
				"ips", stats.UniqueIPs,
				"devices", stats.UniqueDevices)

			// Could send to panel via a new message type if needed
			// For now just log locally

		case <-cleanupTicker.C:
			removed := c.enforcer.CleanupStale(10 * time.Minute)
			if removed > 0 {
				slog.InfoContext(ctx, "cleaned up stale connections",
					"count", removed)
			}
		}
	}
}

// GetEnforcer returns the enforcement engine for adapter integration.
func (c *Client) GetEnforcer() *enforcement.Enforcer {
	return c.enforcer
}

// startXrayEnforcementIfSupported starts the Xray enforcement loop if the adapter is Xray.
// This integrates the ConnectionTracker with the node agent runtime.
func (c *Client) startXrayEnforcementIfSupported(ctx context.Context) {
	// Check if adapter is Xray by attempting type assertion
	// We need to import the xray package, but to avoid circular dependencies,
	// we'll use a runtime interface check instead
	type xrayEnforcementStarter interface {
		StartEnforcement(ctx context.Context, enforcer *enforcement.Enforcer, interval time.Duration)
	}

	// Whichever adapters implement enforcement, not merely the first. Only
	// Xray does today, but finding it by position rather than by capability
	// would break the moment a node lists it second.
	for _, ad := range c.ads.Adapters() {
		starter, ok := ad.(xrayEnforcementStarter)
		if !ok {
			continue
		}
		go starter.StartEnforcement(ctx, c.enforcer, 5*time.Second)
		slog.InfoContext(ctx, "started adapter enforcement loop",
			"kind", string(ad.Descriptor().Kind), "interval", "5s")
	}
}

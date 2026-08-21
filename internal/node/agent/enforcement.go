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

package nodes

import (
	"fmt"
)

// Status represents the current state of a node in the system.
type Status string

const (
	// StatusPending: Node created but not yet enrolled
	StatusPending Status = "pending"

	// StatusEnrolling: Node is in the enrollment process
	StatusEnrolling Status = "enrolling"

	// StatusOnline: Node is healthy and responsive
	StatusOnline Status = "online"

	// StatusDegraded: Node is responding but with issues (high resource usage, sync errors, slow heartbeat)
	StatusDegraded Status = "degraded"

	// StatusOffline: Node has not sent heartbeat within timeout period
	StatusOffline Status = "offline"

	// StatusMaintenance: Node is in maintenance mode (admin action)
	StatusMaintenance Status = "maintenance"

	// StatusError: Node has encountered a critical error
	StatusError Status = "error"

	// StatusDisabled: Node has been disabled by admin
	StatusDisabled Status = "disabled"

	// StatusIntegrity: Node has configuration integrity fault
	StatusIntegrity Status = "integrity"
)

// AllStatuses returns all valid node statuses.
func AllStatuses() []Status {
	return []Status{
		StatusPending,
		StatusEnrolling,
		StatusOnline,
		StatusDegraded,
		StatusOffline,
		StatusMaintenance,
		StatusError,
		StatusDisabled,
		StatusIntegrity,
	}
}

// IsValid checks if the status is a valid node status.
func (s Status) IsValid() bool {
	for _, valid := range AllStatuses() {
		if s == valid {
			return true
		}
	}
	return false
}

// String returns the string representation of the status.
func (s Status) String() string {
	return string(s)
}

// StateTransition represents a status transition with rules.
type StateTransition struct {
	From   Status
	To     Status
	Reason string // Why this transition occurred
}

// ValidateTransition checks if a status transition is valid.
// Returns nil if valid, error with explanation if invalid.
func ValidateTransition(from, to Status) error {
	// Validate both statuses
	if !from.IsValid() {
		return fmt.Errorf("invalid source status: %s", from)
	}
	if !to.IsValid() {
		return fmt.Errorf("invalid target status: %s", to)
	}

	// Same state is always valid (idempotent)
	if from == to {
		return nil
	}

	// Define valid transitions
	validTransitions := map[Status][]Status{
		StatusPending: {
			StatusEnrolling,  // Start enrollment
			StatusDisabled,   // Admin disables before enrollment
		},
		StatusEnrolling: {
			StatusOnline,     // Enrollment successful
			StatusError,      // Enrollment failed
			StatusDisabled,   // Admin disables during enrollment
		},
		StatusOnline: {
			StatusDegraded,   // Health degradation detected
			StatusOffline,    // Lost connectivity
			StatusMaintenance, // Admin puts in maintenance
			StatusError,      // Critical error occurred
			StatusDisabled,   // Admin disables node
			StatusIntegrity,  // Configuration drift detected
		},
		StatusDegraded: {
			StatusOnline,     // Health recovered
			StatusOffline,    // Connectivity lost completely
			StatusMaintenance, // Admin puts in maintenance
			StatusError,      // Degradation worsened to error
			StatusDisabled,   // Admin disables node
		},
		StatusOffline: {
			StatusOnline,     // Reconnected and healthy
			StatusDegraded,   // Reconnected but degraded
			StatusMaintenance, // Admin puts in maintenance while offline
			StatusDisabled,   // Admin disables offline node
		},
		StatusMaintenance: {
			StatusOnline,     // Exited maintenance, healthy
			StatusDegraded,   // Exited maintenance, degraded
			StatusOffline,    // Node went offline during maintenance
			StatusDisabled,   // Admin disables node
		},
		StatusError: {
			StatusOnline,     // Error resolved
			StatusDegraded,   // Partially recovered
			StatusOffline,    // Lost connectivity
			StatusMaintenance, // Admin puts in maintenance to fix
			StatusDisabled,   // Admin disables broken node
		},
		StatusDisabled: {
			StatusOnline,     // Admin re-enables, node healthy
			StatusDegraded,   // Admin re-enables, node degraded
			StatusOffline,    // Admin re-enables, node offline
			StatusMaintenance, // Admin re-enables into maintenance
		},
		StatusIntegrity: {
			StatusOnline,     // Configuration synchronized
			StatusDegraded,   // Synchronized but degraded
			StatusOffline,    // Lost connectivity during sync
			StatusMaintenance, // Admin puts in maintenance to fix
			StatusDisabled,   // Admin disables node
		},
	}

	allowedTargets, exists := validTransitions[from]
	if !exists {
		return fmt.Errorf("no transitions defined from status: %s", from)
	}

	for _, allowed := range allowedTargets {
		if to == allowed {
			return nil
		}
	}

	return fmt.Errorf("invalid transition: %s -> %s (not allowed)", from, to)
}

// CanTransitionTo checks if a transition is valid without returning an error.
func (s Status) CanTransitionTo(target Status) bool {
	return ValidateTransition(s, target) == nil
}

// TransitionReason returns a human-readable reason for a state transition.
func TransitionReason(from, to Status) string {
	if from == to {
		return "state unchanged"
	}

	reasons := map[string]string{
		"pending->enrolling":       "enrollment process started",
		"pending->disabled":        "node disabled before enrollment",
		"enrolling->online":        "enrollment successful, node online",
		"enrolling->error":         "enrollment failed",
		"enrolling->disabled":      "node disabled during enrollment",
		"online->degraded":         "health degradation detected",
		"online->offline":          "lost connectivity",
		"online->maintenance":      "entered maintenance mode",
		"online->error":            "critical error occurred",
		"online->disabled":         "node disabled by admin",
		"online->integrity":        "configuration drift detected",
		"degraded->online":         "health recovered",
		"degraded->offline":        "connectivity lost",
		"degraded->maintenance":    "entered maintenance mode",
		"degraded->error":          "condition worsened to error",
		"degraded->disabled":       "node disabled by admin",
		"offline->online":          "reconnected, fully operational",
		"offline->degraded":        "reconnected with issues",
		"offline->maintenance":     "entered maintenance mode while offline",
		"offline->disabled":        "node disabled while offline",
		"maintenance->online":      "exited maintenance, fully operational",
		"maintenance->degraded":    "exited maintenance with issues",
		"maintenance->offline":     "went offline during maintenance",
		"maintenance->disabled":    "node disabled from maintenance",
		"error->online":            "error resolved, fully operational",
		"error->degraded":          "partially recovered from error",
		"error->offline":           "lost connectivity after error",
		"error->maintenance":       "entered maintenance to resolve error",
		"error->disabled":          "node disabled due to error",
		"disabled->online":         "re-enabled, fully operational",
		"disabled->degraded":       "re-enabled with issues",
		"disabled->offline":        "re-enabled but offline",
		"disabled->maintenance":    "re-enabled into maintenance mode",
		"integrity->online":        "configuration synchronized",
		"integrity->degraded":      "synchronized with issues",
		"integrity->offline":       "lost connectivity during sync",
		"integrity->maintenance":   "entered maintenance for manual sync",
		"integrity->disabled":      "node disabled due to drift",
	}

	key := fmt.Sprintf("%s->%s", from, to)
	if reason, ok := reasons[key]; ok {
		return reason
	}

	return fmt.Sprintf("transition from %s to %s", from, to)
}

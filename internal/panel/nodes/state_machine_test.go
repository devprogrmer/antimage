package nodes

import (
	"testing"
)

func TestStatusIsValid(t *testing.T) {
	tests := []struct {
		status Status
		valid  bool
	}{
		{StatusPending, true},
		{StatusEnrolling, true},
		{StatusOnline, true},
		{StatusDegraded, true},
		{StatusOffline, true},
		{StatusMaintenance, true},
		{StatusError, true},
		{StatusDisabled, true},
		{StatusIntegrity, true},
		{Status("invalid"), false},
		{Status(""), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := tt.status.IsValid(); got != tt.valid {
				t.Errorf("Status(%q).IsValid() = %v, want %v", tt.status, got, tt.valid)
			}
		})
	}
}

func TestValidateTransition_ValidTransitions(t *testing.T) {
	validTransitions := []struct {
		from Status
		to   Status
		desc string
	}{
		// From pending
		{StatusPending, StatusEnrolling, "start enrollment"},
		{StatusPending, StatusDisabled, "disable before enrollment"},

		// From enrolling
		{StatusEnrolling, StatusOnline, "successful enrollment"},
		{StatusEnrolling, StatusError, "enrollment failed"},
		{StatusEnrolling, StatusDisabled, "disable during enrollment"},

		// From online
		{StatusOnline, StatusDegraded, "health degraded"},
		{StatusOnline, StatusOffline, "lost connectivity"},
		{StatusOnline, StatusMaintenance, "enter maintenance"},
		{StatusOnline, StatusError, "critical error"},
		{StatusOnline, StatusDisabled, "admin disable"},
		{StatusOnline, StatusIntegrity, "config drift"},

		// From degraded
		{StatusDegraded, StatusOnline, "health recovered"},
		{StatusDegraded, StatusOffline, "connectivity lost"},
		{StatusDegraded, StatusMaintenance, "enter maintenance"},
		{StatusDegraded, StatusError, "worsened to error"},
		{StatusDegraded, StatusDisabled, "admin disable"},

		// From offline
		{StatusOffline, StatusOnline, "reconnected healthy"},
		{StatusOffline, StatusDegraded, "reconnected degraded"},
		{StatusOffline, StatusMaintenance, "enter maintenance offline"},
		{StatusOffline, StatusDisabled, "disable offline node"},

		// From maintenance
		{StatusMaintenance, StatusOnline, "exit maintenance healthy"},
		{StatusMaintenance, StatusDegraded, "exit maintenance degraded"},
		{StatusMaintenance, StatusOffline, "went offline during maintenance"},
		{StatusMaintenance, StatusDisabled, "disable from maintenance"},

		// From error
		{StatusError, StatusOnline, "error resolved"},
		{StatusError, StatusDegraded, "partially recovered"},
		{StatusError, StatusOffline, "lost connectivity"},
		{StatusError, StatusMaintenance, "enter maintenance to fix"},
		{StatusError, StatusDisabled, "disable broken node"},

		// From disabled
		{StatusDisabled, StatusOnline, "re-enable healthy"},
		{StatusDisabled, StatusDegraded, "re-enable degraded"},
		{StatusDisabled, StatusOffline, "re-enable offline"},
		{StatusDisabled, StatusMaintenance, "re-enable into maintenance"},

		// From integrity
		{StatusIntegrity, StatusOnline, "config synchronized"},
		{StatusIntegrity, StatusDegraded, "synchronized degraded"},
		{StatusIntegrity, StatusOffline, "lost connectivity during sync"},
		{StatusIntegrity, StatusMaintenance, "enter maintenance for sync"},
		{StatusIntegrity, StatusDisabled, "disable due to drift"},

		// Idempotent transitions (same state)
		{StatusOnline, StatusOnline, "idempotent online"},
		{StatusOffline, StatusOffline, "idempotent offline"},
	}

	for _, tt := range validTransitions {
		t.Run(tt.desc, func(t *testing.T) {
			err := ValidateTransition(tt.from, tt.to)
			if err != nil {
				t.Errorf("ValidateTransition(%s, %s) returned error: %v (expected valid)", tt.from, tt.to, err)
			}

			// Also test CanTransitionTo
			if !tt.from.CanTransitionTo(tt.to) {
				t.Errorf("%s.CanTransitionTo(%s) = false, want true", tt.from, tt.to)
			}
		})
	}
}

func TestValidateTransition_InvalidTransitions(t *testing.T) {
	invalidTransitions := []struct {
		from Status
		to   Status
		desc string
	}{
		// Cannot go directly from pending to most states
		{StatusPending, StatusOnline, "pending cannot skip enrollment"},
		{StatusPending, StatusDegraded, "pending cannot become degraded"},
		{StatusPending, StatusOffline, "pending cannot become offline"},
		{StatusPending, StatusMaintenance, "pending cannot enter maintenance"},
		{StatusPending, StatusError, "pending cannot error"},
		{StatusPending, StatusIntegrity, "pending cannot have integrity fault"},

		// Cannot go from enrolling to certain states
		{StatusEnrolling, StatusDegraded, "enrolling cannot become degraded directly"},
		{StatusEnrolling, StatusOffline, "enrolling cannot become offline"},
		{StatusEnrolling, StatusMaintenance, "enrolling cannot enter maintenance"},
		{StatusEnrolling, StatusIntegrity, "enrolling cannot have integrity"},

		// Cannot go from disabled to error
		{StatusDisabled, StatusError, "disabled cannot become error"},

		// Invalid status tests
		{Status("invalid"), StatusOnline, "invalid source status"},
		{StatusOnline, Status("invalid"), "invalid target status"},
	}

	for _, tt := range invalidTransitions {
		t.Run(tt.desc, func(t *testing.T) {
			err := ValidateTransition(tt.from, tt.to)
			if err == nil {
				t.Errorf("ValidateTransition(%s, %s) returned nil, expected error", tt.from, tt.to)
			}

			// Only test CanTransitionTo if both statuses are valid
			if tt.from.IsValid() && tt.to.IsValid() {
				if tt.from.CanTransitionTo(tt.to) {
					t.Errorf("%s.CanTransitionTo(%s) = true, want false", tt.from, tt.to)
				}
			}
		})
	}
}

func TestTransitionReason(t *testing.T) {
	tests := []struct {
		from Status
		to   Status
		want string
	}{
		{StatusOnline, StatusOnline, "state unchanged"},
		{StatusPending, StatusEnrolling, "enrollment process started"},
		{StatusEnrolling, StatusOnline, "enrollment successful, node online"},
		{StatusOnline, StatusDegraded, "health degradation detected"},
		{StatusDegraded, StatusOnline, "health recovered"},
		{StatusOnline, StatusMaintenance, "entered maintenance mode"},
		{StatusMaintenance, StatusOnline, "exited maintenance, fully operational"},
		{StatusOnline, StatusIntegrity, "configuration drift detected"},
		{StatusIntegrity, StatusOnline, "configuration synchronized"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := TransitionReason(tt.from, tt.to)
			if got != tt.want {
				t.Errorf("TransitionReason(%s, %s) = %q, want %q", tt.from, tt.to, got, tt.want)
			}
		})
	}
}

// Test failure scenarios
func TestStateTransitions_FailureScenarios(t *testing.T) {
	t.Run("missed_heartbeat_degraded", func(t *testing.T) {
		// Online → Degraded (missed 1-2 heartbeats, within grace period)
		err := ValidateTransition(StatusOnline, StatusDegraded)
		if err != nil {
			t.Errorf("online->degraded should be valid for missed heartbeat scenario: %v", err)
		}
	})

	t.Run("missed_heartbeat_offline", func(t *testing.T) {
		// Degraded → Offline (missed heartbeats beyond grace)
		err := ValidateTransition(StatusDegraded, StatusOffline)
		if err != nil {
			t.Errorf("degraded->offline should be valid for complete timeout: %v", err)
		}
	})

	t.Run("reconnect_healthy", func(t *testing.T) {
		// Offline → Online (node reconnects and is healthy)
		err := ValidateTransition(StatusOffline, StatusOnline)
		if err != nil {
			t.Errorf("offline->online should be valid for reconnect: %v", err)
		}
	})

	t.Run("reconnect_degraded", func(t *testing.T) {
		// Offline → Degraded (node reconnects with issues)
		err := ValidateTransition(StatusOffline, StatusDegraded)
		if err != nil {
			t.Errorf("offline->degraded should be valid for degraded reconnect: %v", err)
		}
	})

	t.Run("stale_node_offline", func(t *testing.T) {
		// Online → Offline (stale node, no recent heartbeat)
		err := ValidateTransition(StatusOnline, StatusOffline)
		if err != nil {
			t.Errorf("online->offline should be valid for stale node: %v", err)
		}
	})

	t.Run("invalid_heartbeat_error", func(t *testing.T) {
		// Online → Error (invalid heartbeat data)
		err := ValidateTransition(StatusOnline, StatusError)
		if err != nil {
			t.Errorf("online->error should be valid for invalid heartbeat: %v", err)
		}
	})

	t.Run("node_restart_reconnect", func(t *testing.T) {
		// Offline → Online (node agent restart and reconnect)
		err := ValidateTransition(StatusOffline, StatusOnline)
		if err != nil {
			t.Errorf("offline->online should be valid for node restart: %v", err)
		}
	})

	t.Run("agent_version_mismatch", func(t *testing.T) {
		// Online → Error (incompatible agent version)
		err := ValidateTransition(StatusOnline, StatusError)
		if err != nil {
			t.Errorf("online->error should be valid for version mismatch: %v", err)
		}
	})

	t.Run("synchronization_failure", func(t *testing.T) {
		// Online → Integrity (failed to apply desired state)
		err := ValidateTransition(StatusOnline, StatusIntegrity)
		if err != nil {
			t.Errorf("online->integrity should be valid for sync failure: %v", err)
		}
	})

	t.Run("synchronization_recovered", func(t *testing.T) {
		// Integrity → Online (successfully synchronized)
		err := ValidateTransition(StatusIntegrity, StatusOnline)
		if err != nil {
			t.Errorf("integrity->online should be valid for sync recovery: %v", err)
		}
	})
}

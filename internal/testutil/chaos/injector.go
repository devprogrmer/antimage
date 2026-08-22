// Package chaos provides fault injection primitives for reliability testing.
//
// This package enables controlled failure injection to verify system resilience:
// - Network failures (timeouts, partitions, packet loss)
// - Database failures (lock timeouts, connection loss)
// - Timing issues (clock skew, delays)
// - gRPC failures (connection drops, timeouts)
//
// Usage:
//   injector := chaos.NewInjector()
//   defer injector.Cleanup()
//
//   // Inject network timeout
//   fault := injector.InjectNetworkTimeout(5 * time.Second)
//   defer injector.RemoveFault(fault.ID)
//
//   // Test system behavior under failure
//   err := systemUnderTest.Connect()
//   assert.Error(t, err) // Should handle timeout gracefully
package chaos

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// FaultType categorizes fault injection strategies
type FaultType string

const (
	FaultTypeNetwork  FaultType = "network"
	FaultTypeDatabase FaultType = "database"
	FaultTypeTiming   FaultType = "timing"
	FaultTypeGRPC     FaultType = "grpc"
)

// Fault represents an active fault injection
type Fault struct {
	ID          string
	Type        FaultType
	Description string
	InjectedAt  time.Time
	RemoveFunc  func() error
}

// Injector manages fault injection
type Injector struct {
	mu     sync.Mutex
	faults map[string]*Fault
}

// NewInjector creates a fault injector
func NewInjector() *Injector {
	return &Injector{
		faults: make(map[string]*Fault),
	}
}

// InjectFault injects a fault into the system
func (i *Injector) InjectFault(ctx context.Context, fault Fault) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if _, exists := i.faults[fault.ID]; exists {
		return fmt.Errorf("fault %s already active", fault.ID)
	}

	i.faults[fault.ID] = &fault
	return nil
}

// RemoveFault removes an active fault
func (i *Injector) RemoveFault(ctx context.Context, faultID string) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	fault, exists := i.faults[faultID]
	if !exists {
		return nil // Already removed
	}

	if fault.RemoveFunc != nil {
		if err := fault.RemoveFunc(); err != nil {
			return fmt.Errorf("remove fault %s: %w", faultID, err)
		}
	}

	delete(i.faults, faultID)
	return nil
}

// ListActiveFaults returns all currently active faults
func (i *Injector) ListActiveFaults() []Fault {
	i.mu.Lock()
	defer i.mu.Unlock()

	faults := make([]Fault, 0, len(i.faults))
	for _, f := range i.faults {
		faults = append(faults, *f)
	}
	return faults
}

// Cleanup removes all active faults
func (i *Injector) Cleanup() error {
	i.mu.Lock()
	defer i.mu.Unlock()

	var errs []error
	for id, fault := range i.faults {
		if fault.RemoveFunc != nil {
			if err := fault.RemoveFunc(); err != nil {
				errs = append(errs, fmt.Errorf("cleanup fault %s: %w", id, err))
			}
		}
		delete(i.faults, id)
	}

	if len(errs) > 0 {
		return fmt.Errorf("cleanup errors: %v", errs)
	}
	return nil
}

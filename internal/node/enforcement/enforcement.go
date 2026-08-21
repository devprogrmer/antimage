// Package enforcement implements runtime policy enforcement for user management.
//
// This is the node-side enforcement layer that works across all adapters.
// It tracks active connections, enforces limits, and applies speed restrictions
// where technically supported by the underlying protocol.
package enforcement

import (
	"fmt"
	"sync"
	"time"
)

// Policy represents enforcement policies for a single subject.
type Policy struct {
	SubjectID          int64
	MaxDevices         *int64
	MaxIPs             *int64
	MaxConnections     *int64
	SpeedLimitUpKbps   *int64
	SpeedLimitDownKbps *int64
}

// Connection represents an active connection being tracked.
type Connection struct {
	ID         string
	SubjectID  int64
	DeviceID   string // hardware ID or device fingerprint
	SourceIP   string
	Protocol   string
	ConnectedAt time.Time
	LastSeenAt  time.Time
}

// Enforcer tracks active connections and enforces policies at runtime.
type Enforcer struct {
	mu          sync.RWMutex
	policies    map[int64]Policy      // subjectID -> policy
	connections map[string]Connection // connectionID -> connection

	// Index structures for fast lookups
	subjectConns map[int64][]string           // subjectID -> []connectionID
	subjectIPs   map[int64]map[string]struct{} // subjectID -> set of IPs
	subjectDevs  map[int64]map[string]struct{} // subjectID -> set of deviceIDs

	now func() time.Time
}

// New creates a new Enforcer.
func New() *Enforcer {
	return &Enforcer{
		policies:     make(map[int64]Policy),
		connections:  make(map[string]Connection),
		subjectConns: make(map[int64][]string),
		subjectIPs:   make(map[int64]map[string]struct{}),
		subjectDevs:  make(map[int64]map[string]struct{}),
		now:          func() time.Time { return time.Now().UTC() },
	}
}

// ErrPolicyViolation indicates a connection was rejected due to policy enforcement.
type ErrPolicyViolation struct {
	Reason string
}

func (e *ErrPolicyViolation) Error() string {
	return fmt.Sprintf("policy violation: %s", e.Reason)
}

// UpdatePolicies replaces all policies with the provided set.
// Removed policies are deleted, existing policies are updated.
func (e *Enforcer) UpdatePolicies(policies []Policy) {
	e.mu.Lock()
	defer e.mu.Unlock()

	newPolicies := make(map[int64]Policy, len(policies))
	for _, p := range policies {
		newPolicies[p.SubjectID] = p
	}

	// Find removed policies
	for subjectID := range e.policies {
		if _, exists := newPolicies[subjectID]; !exists {
			// Policy removed - terminate all connections for this subject
			e.terminateSubjectLocked(subjectID)
		}
	}

	e.policies = newPolicies
}

// CheckConnection validates if a new connection is allowed under current policies.
// Returns nil if allowed, or ErrPolicyViolation if rejected.
func (e *Enforcer) CheckConnection(subjectID int64, deviceID, sourceIP string) error {
	e.mu.RLock()
	defer e.mu.RUnlock()

	policy, exists := e.policies[subjectID]
	if !exists {
		// No policy means no restrictions
		return nil
	}

	// Check device limit
	if policy.MaxDevices != nil {
		devices := e.subjectDevs[subjectID]
		if len(devices) >= int(*policy.MaxDevices) {
			// Check if this device is already registered
			if _, known := devices[deviceID]; !known {
				return &ErrPolicyViolation{
					Reason: fmt.Sprintf("device limit reached (%d/%d)", len(devices), *policy.MaxDevices),
				}
			}
		}
	}

	// Check IP limit
	if policy.MaxIPs != nil {
		ips := e.subjectIPs[subjectID]
		if len(ips) >= int(*policy.MaxIPs) {
			// Check if this IP is already connected
			if _, known := ips[sourceIP]; !known {
				return &ErrPolicyViolation{
					Reason: fmt.Sprintf("IP limit reached (%d/%d)", len(ips), *policy.MaxIPs),
				}
			}
		}
	}

	// Check connection limit
	if policy.MaxConnections != nil {
		conns := e.subjectConns[subjectID]
		if len(conns) >= int(*policy.MaxConnections) {
			return &ErrPolicyViolation{
				Reason: fmt.Sprintf("connection limit reached (%d/%d)", len(conns), *policy.MaxConnections),
			}
		}
	}

	return nil
}

// RegisterConnection records a new active connection.
// Must be called after CheckConnection succeeds.
func (e *Enforcer) RegisterConnection(connID string, subjectID int64, deviceID, sourceIP, protocol string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := e.now()
	conn := Connection{
		ID:          connID,
		SubjectID:   subjectID,
		DeviceID:    deviceID,
		SourceIP:    sourceIP,
		Protocol:    protocol,
		ConnectedAt: now,
		LastSeenAt:  now,
	}

	e.connections[connID] = conn

	// Update indexes
	e.subjectConns[subjectID] = append(e.subjectConns[subjectID], connID)

	if e.subjectIPs[subjectID] == nil {
		e.subjectIPs[subjectID] = make(map[string]struct{})
	}
	e.subjectIPs[subjectID][sourceIP] = struct{}{}

	if e.subjectDevs[subjectID] == nil {
		e.subjectDevs[subjectID] = make(map[string]struct{})
	}
	e.subjectDevs[subjectID][deviceID] = struct{}{}
}

// UnregisterConnection removes a connection from tracking.
func (e *Enforcer) UnregisterConnection(connID string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	conn, exists := e.connections[connID]
	if !exists {
		return
	}

	delete(e.connections, connID)

	// Update indexes
	e.removeSubjectConnLocked(conn.SubjectID, connID)
	e.rebuildIPIndexLocked(conn.SubjectID)
	e.rebuildDeviceIndexLocked(conn.SubjectID)
}

// GetSpeedLimits returns speed limits for a subject (in kbps).
// Returns nil, nil if no limits are set.
func (e *Enforcer) GetSpeedLimits(subjectID int64) (upKbps, downKbps *int64) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	policy, exists := e.policies[subjectID]
	if !exists {
		return nil, nil
	}

	return policy.SpeedLimitUpKbps, policy.SpeedLimitDownKbps
}

// GetActiveConnections returns all active connections for a subject.
func (e *Enforcer) GetActiveConnections(subjectID int64) []Connection {
	e.mu.RLock()
	defer e.mu.RUnlock()

	connIDs := e.subjectConns[subjectID]
	result := make([]Connection, 0, len(connIDs))

	for _, connID := range connIDs {
		if conn, exists := e.connections[connID]; exists {
			result = append(result, conn)
		}
	}

	return result
}

// CleanupStale removes connections not seen within the threshold.
func (e *Enforcer) CleanupStale(threshold time.Duration) int {
	e.mu.Lock()
	defer e.mu.Unlock()

	cutoff := e.now().Add(-threshold)
	var removed []string

	for connID, conn := range e.connections {
		if conn.LastSeenAt.Before(cutoff) {
			removed = append(removed, connID)
		}
	}

	for _, connID := range removed {
		conn := e.connections[connID]
		delete(e.connections, connID)
		e.removeSubjectConnLocked(conn.SubjectID, connID)
	}

	// Rebuild indexes for affected subjects
	affected := make(map[int64]struct{})
	for _, connID := range removed {
		if conn, exists := e.connections[connID]; exists {
			affected[conn.SubjectID] = struct{}{}
		}
	}

	for subjectID := range affected {
		e.rebuildIPIndexLocked(subjectID)
		e.rebuildDeviceIndexLocked(subjectID)
	}

	return len(removed)
}

// Stats returns current enforcement statistics.
func (e *Enforcer) Stats() EnforcementStats {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return EnforcementStats{
		TotalConnections: len(e.connections),
		TrackedSubjects:  len(e.policies),
		UniqueIPs:        e.countUniqueIPs(),
		UniqueDevices:    e.countUniqueDevices(),
	}
}

// EnforcementStats provides visibility into enforcement state.
type EnforcementStats struct {
	TotalConnections int
	TrackedSubjects  int
	UniqueIPs        int
	UniqueDevices    int
}

// terminateSubjectLocked removes all connections for a subject.
// Must be called with e.mu locked.
func (e *Enforcer) terminateSubjectLocked(subjectID int64) {
	connIDs := e.subjectConns[subjectID]
	for _, connID := range connIDs {
		delete(e.connections, connID)
	}

	delete(e.subjectConns, subjectID)
	delete(e.subjectIPs, subjectID)
	delete(e.subjectDevs, subjectID)
}

// removeSubjectConnLocked removes a connection from the subject index.
// Must be called with e.mu locked.
func (e *Enforcer) removeSubjectConnLocked(subjectID int64, connID string) {
	conns := e.subjectConns[subjectID]
	for i, id := range conns {
		if id == connID {
			e.subjectConns[subjectID] = append(conns[:i], conns[i+1:]...)
			if len(e.subjectConns[subjectID]) == 0 {
				delete(e.subjectConns, subjectID)
			}
			return
		}
	}
}

// rebuildIPIndexLocked rebuilds the IP index for a subject.
// Must be called with e.mu locked.
func (e *Enforcer) rebuildIPIndexLocked(subjectID int64) {
	ips := make(map[string]struct{})
	for _, connID := range e.subjectConns[subjectID] {
		if conn, exists := e.connections[connID]; exists {
			ips[conn.SourceIP] = struct{}{}
		}
	}

	if len(ips) == 0 {
		delete(e.subjectIPs, subjectID)
	} else {
		e.subjectIPs[subjectID] = ips
	}
}

// rebuildDeviceIndexLocked rebuilds the device index for a subject.
// Must be called with e.mu locked.
func (e *Enforcer) rebuildDeviceIndexLocked(subjectID int64) {
	devices := make(map[string]struct{})
	for _, connID := range e.subjectConns[subjectID] {
		if conn, exists := e.connections[connID]; exists {
			devices[conn.DeviceID] = struct{}{}
		}
	}

	if len(devices) == 0 {
		delete(e.subjectDevs, subjectID)
	} else {
		e.subjectDevs[subjectID] = devices
	}
}

func (e *Enforcer) countUniqueIPs() int {
	seen := make(map[string]struct{})
	for _, ips := range e.subjectIPs {
		for ip := range ips {
			seen[ip] = struct{}{}
		}
	}
	return len(seen)
}

func (e *Enforcer) countUniqueDevices() int {
	seen := make(map[string]struct{})
	for _, devices := range e.subjectDevs {
		for dev := range devices {
			seen[dev] = struct{}{}
		}
	}
	return len(seen)
}

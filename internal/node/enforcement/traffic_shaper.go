package enforcement

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// TrafficShaper manages external bandwidth enforcement via Linux tc (traffic control).
// This is required because protocol adapters (Xray, etc.) do not provide native
// per-user bandwidth limiting.
type TrafficShaper struct {
	mu sync.Mutex

	// Interface to shape traffic on (e.g., "eth0", "wg0")
	iface string

	// Track active shapes: subjectID -> classID
	shapes map[int64]int

	// Next available class ID
	nextClassID int
}

// NewTrafficShaper creates a traffic shaper for the given network interface.
// Requires Linux with tc (iproute2) installed and CAP_NET_ADMIN capability.
func NewTrafficShaper(iface string) (*TrafficShaper, error) {
	ts := &TrafficShaper{
		iface:       iface,
		shapes:      make(map[int64]int),
		nextClassID: 10, // Start at 10 to avoid conflicts
	}

	// Verify tc is available
	if err := ts.checkTC(); err != nil {
		return nil, fmt.Errorf("tc not available: %w", err)
	}

	// Initialize root qdisc
	if err := ts.initializeQdisc(); err != nil {
		return nil, fmt.Errorf("failed to initialize qdisc: %w", err)
	}

	return ts, nil
}

// checkTC verifies that tc command is available
func (ts *TrafficShaper) checkTC() error {
	cmd := exec.Command("tc", "-Version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tc command not found or not executable: %w", err)
	}
	return nil
}

// initializeQdisc sets up the root HTB qdisc on the interface
func (ts *TrafficShaper) initializeQdisc() error {
	// Delete existing qdisc (ignore errors)
	exec.Command("tc", "qdisc", "del", "dev", ts.iface, "root").Run()

	// Create HTB root qdisc
	// handle 1: means root qdisc with handle 1
	// default 999 means unclassified traffic goes to class 1:999
	cmd := exec.Command("tc", "qdisc", "add", "dev", ts.iface, "root",
		"handle", "1:", "htb", "default", "999")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to add HTB qdisc: %w", err)
	}

	// Create default class for unclassified traffic (no limit)
	cmd = exec.Command("tc", "class", "add", "dev", ts.iface,
		"parent", "1:", "classid", "1:999", "htb", "rate", "1gbit")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to add default class: %w", err)
	}

	return nil
}

// ApplyLimit applies bandwidth limits for a subject based on their source IP.
// uploadKbps and downloadKbps are in kilobits per second.
func (ts *TrafficShaper) ApplyLimit(ctx context.Context, subjectID int64, sourceIP string, uploadKbps, downloadKbps int64) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	// Allocate class ID
	classID := ts.nextClassID
	ts.nextClassID++

	// Create upload limit class (egress)
	if uploadKbps > 0 {
		if err := ts.createClass(classID, uploadKbps); err != nil {
			return fmt.Errorf("failed to create upload class: %w", err)
		}

		// Add filter to match source IP for upload (egress direction)
		if err := ts.addFilter(classID, sourceIP, "src"); err != nil {
			return fmt.Errorf("failed to add upload filter: %w", err)
		}
	}

	// Note: Download limiting requires ingress qdisc or IFB device
	// For simplicity, we only implement egress (upload) here
	// Full implementation would use IFB (Intermediate Functional Block) device

	ts.shapes[subjectID] = classID
	return nil
}

// createClass creates an HTB class with the specified rate limit
func (ts *TrafficShaper) createClass(classID int, rateKbps int64) error {
	classIDStr := fmt.Sprintf("1:%d", classID)
	rateStr := fmt.Sprintf("%dkbit", rateKbps)

	cmd := exec.Command("tc", "class", "add", "dev", ts.iface,
		"parent", "1:", "classid", classIDStr, "htb",
		"rate", rateStr, "ceil", rateStr)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tc class add failed: %w: %s", err, output)
	}

	return nil
}

// addFilter adds a u32 filter to match traffic by IP address
func (ts *TrafficShaper) addFilter(classID int, ip, direction string) error {
	classIDStr := fmt.Sprintf("1:%d", classID)

	// u32 filter matches IP addresses
	// direction: "src" for source IP (upload), "dst" for destination IP (download)
	matchStr := fmt.Sprintf("match ip %s %s", direction, ip)

	cmd := exec.Command("tc", "filter", "add", "dev", ts.iface,
		"protocol", "ip", "parent", "1:0", "prio", "1", "u32",
		matchStr, "flowid", classIDStr)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tc filter add failed: %w: %s", err, output)
	}

	return nil
}

// RemoveLimit removes bandwidth limits for a subject
func (ts *TrafficShaper) RemoveLimit(ctx context.Context, subjectID int64) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	classID, exists := ts.shapes[subjectID]
	if !exists {
		return nil // Already removed
	}

	// Delete the class (this also removes associated filters)
	classIDStr := fmt.Sprintf("1:%d", classID)
	cmd := exec.Command("tc", "class", "del", "dev", ts.iface,
		"classid", classIDStr)

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Log but don't fail - class may already be gone
		_ = fmt.Errorf("tc class del warning: %w: %s", err, output)
	}

	delete(ts.shapes, subjectID)
	return nil
}

// GetStats retrieves traffic statistics for a subject
func (ts *TrafficShaper) GetStats(ctx context.Context, subjectID int64) (sent, received uint64, err error) {
	ts.mu.Lock()
	classID, exists := ts.shapes[subjectID]
	ts.mu.Unlock()

	if !exists {
		return 0, 0, fmt.Errorf("no shape found for subject %d", subjectID)
	}

	classIDStr := fmt.Sprintf("1:%d", classID)
	cmd := exec.Command("tc", "-s", "class", "show", "dev", ts.iface, "classid", classIDStr)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, 0, fmt.Errorf("tc class show failed: %w", err)
	}

	// Parse output to extract bytes sent
	// Output format:
	// class htb 1:10 parent 1: leaf 10: prio 0 rate 5Mbit ceil 5Mbit burst 1600b cburst 1600b
	//  Sent 12345 bytes 100 pkt (dropped 0, overlimits 5 requeues 0)
	//  backlog 0b 0p requeues 0
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "Sent") {
			fields := strings.Fields(line)
			for i, field := range fields {
				if field == "Sent" && i+1 < len(fields) {
					bytes, _ := strconv.ParseUint(fields[i+1], 10, 64)
					return bytes, 0, nil // Only egress stats available
				}
			}
		}
	}

	return 0, 0, nil
}

// Cleanup removes all traffic shaping rules
func (ts *TrafficShaper) Cleanup() error {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	// Delete root qdisc (removes all classes and filters)
	cmd := exec.Command("tc", "qdisc", "del", "dev", ts.iface, "root")
	if err := cmd.Run(); err != nil {
		// Ignore errors - qdisc may not exist
		_ = err
	}

	ts.shapes = make(map[int64]int)
	ts.nextClassID = 10

	return nil
}

// IsSupported checks if the current platform supports traffic shaping
func IsSupported() bool {
	// Check if tc command exists
	cmd := exec.Command("tc", "-Version")
	return cmd.Run() == nil
}

package l2tp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// accountingCursor persists the last poll time and counter values.
// This enables delta computation and survives agent restarts.
type accountingCursor struct {
	LastPoll int64                     `json:"last_poll"`
	Counters map[string]trafficCounter `json:"counters"`
}

type trafficCounter struct {
	RxBytes uint64 `json:"rx"`
	TxBytes uint64 `json:"tx"`
}

// Usage implements the UsageReporter interface (SP3 integration).
// It reads nftables counters, computes deltas, and maps IPs to subject IDs.
func (a *Adapter) Usage(ctx context.Context) ([]adapter.UsageSample, error) {
	// 1. Load previous cursor.
	prev, err := a.loadCursor()
	if err != nil {
		// First run or corrupted cursor → start fresh.
		prev = accountingCursor{
			LastPoll: time.Now().Unix(),
			Counters: make(map[string]trafficCounter),
		}
	}

	// 2. Read current nftables counters.
	current, err := a.readNftablesCounters()
	if err != nil {
		return nil, fmt.Errorf("read nftables: %w", err)
	}

	// 3. Compute deltas.
	var samples []adapter.UsageSample
	for ip, curCount := range current {
		prevCount := prev.Counters[ip]

		// Detect counter resets (service restart).
		if curCount.RxBytes < prevCount.RxBytes || curCount.TxBytes < prevCount.TxBytes {
			// Reset detected, start from current.
			prevCount = trafficCounter{}
		}

		deltaRx := curCount.RxBytes - prevCount.RxBytes
		deltaTx := curCount.TxBytes - prevCount.TxBytes

		if deltaRx > 0 || deltaTx > 0 {
			// Map IP to subject ID.
			subjectID, err := a.ipToSubjectID(ip)
			if err != nil {
				// IP not mapped (stale session), skip.
				continue
			}

			samples = append(samples, adapter.UsageSample{
				SubjectID:     subjectID,
				UplinkBytes:   deltaTx,
				DownlinkBytes: deltaRx,
			})
		}
	}

	// 4. Save new cursor.
	newCursor := accountingCursor{
		LastPoll: time.Now().Unix(),
		Counters: current,
	}
	if err := a.saveCursor(newCursor); err != nil {
		return nil, fmt.Errorf("save cursor: %w", err)
	}

	return samples, nil
}

func (a *Adapter) cursorPath() string {
	return filepath.Join(a.stateDir, "l2tp-accounting.json")
}

func (a *Adapter) loadCursor() (accountingCursor, error) {
	var cursor accountingCursor
	data, err := os.ReadFile(a.cursorPath())
	if err != nil {
		return cursor, err
	}
	if err := json.Unmarshal(data, &cursor); err != nil {
		return cursor, err
	}
	if cursor.Counters == nil {
		cursor.Counters = make(map[string]trafficCounter)
	}
	return cursor, nil
}

func (a *Adapter) saveCursor(cursor accountingCursor) error {
	data, err := json.Marshal(cursor)
	if err != nil {
		return err
	}

	dir := filepath.Dir(a.cursorPath())
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	tmp := a.cursorPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, a.cursorPath())
}

// readNftablesCounters queries nftables for L2TP traffic counters.
// Returns a map of client IP → traffic counters.
func (a *Adapter) readNftablesCounters() (map[string]trafficCounter, error) {
	// Check if nftables table exists.
	cmd := exec.Command("nft", "list", "table", "inet", "antimage_l2tp")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Table doesn't exist yet (services not started or accounting not set up).
		return make(map[string]trafficCounter), nil
	}

	// Parse nft output.
	// Format (simplified):
	//   table inet antimage_l2tp {
	//     chain input {
	//       type filter hook input priority 0; policy accept;
	//       ip saddr 10.8.0.2 counter packets 1234 bytes 1048576
	//       ip saddr 10.8.0.3 counter packets 5678 bytes 524288
	//     }
	//     chain output {
	//       type filter hook output priority 0; policy accept;
	//       ip daddr 10.8.0.2 counter packets 2345 bytes 2097152
	//       ip daddr 10.8.0.3 counter packets 6789 bytes 1048576
	//     }
	//   }

	counters := make(map[string]trafficCounter)
	lines := strings.Split(string(output), "\n")

	var currentChain string
	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Detect chain.
		if strings.HasPrefix(line, "chain input") {
			currentChain = "input"
			continue
		}
		if strings.HasPrefix(line, "chain output") {
			currentChain = "output"
			continue
		}

		// Parse counter lines.
		if strings.Contains(line, "counter") {
			ip, bytes := parseCounterLine(line)
			if ip == "" {
				continue
			}

			c := counters[ip]
			switch currentChain {
			case "input":
				c.RxBytes = bytes
			case "output":
				c.TxBytes = bytes
			}
			counters[ip] = c
		}
	}

	return counters, nil
}

// parseCounterLine extracts IP and byte count from a nftables counter line.
// Example: "ip saddr 10.8.0.2 counter packets 1234 bytes 1048576"
func parseCounterLine(line string) (ip string, bytes uint64) {
	fields := strings.Fields(line)
	for i := 0; i < len(fields)-1; i++ {
		if (fields[i] == "saddr" || fields[i] == "daddr") && i+1 < len(fields) {
			ip = fields[i+1]
		}
		if fields[i] == "bytes" && i+1 < len(fields) {
			parsed, _ := strconv.ParseUint(fields[i+1], 10, 64)
			bytes = parsed
		}
	}
	return
}

// ipToSubjectID maps a client IP address to a subject ID.
// This reads from the active sessions log maintained by PPP hooks.
func (a *Adapter) ipToSubjectID(ip string) (int64, error) {
	// Read session mapping file.
	// Format: username ip_address (one per line)
	// Example: user1 10.8.0.2
	sessionsPath := filepath.Join(a.stateDir, "l2tp-sessions.txt")
	data, err := os.ReadFile(sessionsPath)
	if err != nil {
		// No sessions file → no active sessions.
		return 0, fmt.Errorf("sessions file not found: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		username, sessionIP := fields[0], fields[1]
		if sessionIP == ip {
			// Extract subject ID from username (format: userN).
			var subjectID int64
			if _, err := fmt.Sscanf(username, "user%d", &subjectID); err == nil {
				return subjectID, nil
			}
		}
	}

	return 0, fmt.Errorf("no subject mapping for IP %s", ip)
}

// The following functions are reserved for future enhancements (automatic rule management).
// Currently, rules are set up manually via Apply steps.

// setupAccounting creates the nftables table and chains for L2TP accounting.
// Reserved for future use: automatic accounting setup during service installation.
func (a *Adapter) setupAccounting() error { //nolint:unused
	// Create nftables table and chains.
	script := `
table inet antimage_l2tp {
	chain input {
		type filter hook input priority 0; policy accept;
	}
	chain output {
		type filter hook output priority 0; policy accept;
	}
}
`
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("setup nftables: %w", err)
	}

	return nil
}

// addAccountingRule adds a counter rule for a specific client IP.
// Reserved for future use: dynamic rule addition on PPP session start.
func (a *Adapter) addAccountingRule(ip string) error { //nolint:unused
	// Add input rule (RX).
	rxCmd := exec.Command("nft", "add", "rule", "inet", "antimage_l2tp", "input",
		"ip", "saddr", ip, "counter")
	if err := rxCmd.Run(); err != nil {
		return fmt.Errorf("add rx rule for %s: %w", ip, err)
	}

	// Add output rule (TX).
	txCmd := exec.Command("nft", "add", "rule", "inet", "antimage_l2tp", "output",
		"ip", "daddr", ip, "counter")
	if err := txCmd.Run(); err != nil {
		return fmt.Errorf("add tx rule for %s: %w", ip, err)
	}

	return nil
}

// removeAccountingRule removes counter rules for a specific IP.
// Reserved for future use: dynamic rule cleanup on PPP session end.
func (a *Adapter) removeAccountingRule(ip string) error { //nolint:unused
	// Note: Removing specific rules requires finding their handle.
	// For simplicity, we flush and recreate all rules periodically.
	// A production implementation would track rule handles.
	return nil
}

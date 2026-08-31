package wireguard

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// accountingCursor persists the last poll time and per-peer counters.
// This enables delta computation across agent restarts.
type accountingCursor struct {
	LastPoll int64                   `json:"last_poll"`
	Peers    map[string]peerCounters `json:"peers"` // publicKey -> counters
}

type peerCounters struct {
	RxBytes uint64 `json:"rx"`
	TxBytes uint64 `json:"tx"`
}

// Usage implements the UsageReporter interface for WireGuard.
//
// WireGuard provides per-peer traffic stats via `wg show {interface} transfer`:
//
//	publicKey1  rxBytes  txBytes
//	publicKey2  rxBytes  txBytes
//
// The adapter:
// 1. Reads current counters from all managed interfaces
// 2. Computes deltas since last poll (accounting cursor)
// 3. Maps public keys to subject IDs
// 4. Reports samples to Enforcer for quota tracking
//
// Note: WireGuard counters are cumulative and reset on interface restart.
func (a *Adapter) Usage(ctx context.Context) ([]adapter.UsageSample, error) {
	// 1. Load previous cursor
	prev, err := a.loadCursor()
	if err != nil {
		// First run or corrupted cursor → start fresh
		prev = accountingCursor{
			LastPoll: time.Now().Unix(),
			Peers:    make(map[string]peerCounters),
		}
	}

	// 2. Read current counters from all managed interfaces
	current, err := a.readAllTransfers(ctx)
	if err != nil {
		return nil, fmt.Errorf("read wg transfers: %w", err)
	}

	// 3. Compute deltas and build samples
	now := time.Now().Unix()
	var samples []adapter.UsageSample

	for publicKey, curCount := range current {
		prevCount := prev.Peers[publicKey]

		// Handle counter reset (interface restarted)
		var deltaRx, deltaTx uint64
		if curCount.RxBytes >= prevCount.RxBytes {
			deltaRx = curCount.RxBytes - prevCount.RxBytes
		} else {
			// Counter reset, use current value as delta
			deltaRx = curCount.RxBytes
		}
		if curCount.TxBytes >= prevCount.TxBytes {
			deltaTx = curCount.TxBytes - prevCount.TxBytes
		} else {
			deltaTx = curCount.TxBytes
		}

		// Skip if no traffic since last poll
		if deltaRx == 0 && deltaTx == 0 {
			continue
		}

		// Map public key to subject ID
		subjectID, ok := a.publicKeyToSubject(publicKey)
		if !ok {
			// Peer not in current desired state (revoked or unknown)
			continue
		}

		samples = append(samples, adapter.UsageSample{
			SubjectID:     subjectID,
			UplinkBytes:   deltaTx,
			DownlinkBytes: deltaRx,
		})
	}

	// 4. Save cursor for next poll
	newCursor := accountingCursor{
		LastPoll: now,
		Peers:    current,
	}
	if err := a.saveCursor(newCursor); err != nil {
		// Non-fatal: next poll will compute larger deltas
		return samples, fmt.Errorf("save cursor: %w (samples still valid)", err)
	}

	return samples, nil
}

// readAllTransfers reads per-peer traffic from all managed WireGuard interfaces.
func (a *Adapter) readAllTransfers(ctx context.Context) (map[string]peerCounters, error) {
	// Find all managed interfaces (antimage-*.conf files)
	pattern := filepath.Join(a.dir, filePrefix+"*.conf")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("glob %s: %w", pattern, err)
	}

	counters := make(map[string]peerCounters)

	for _, path := range matches {
		// Extract interface name from filename
		// /etc/wireguard/antimage-51820.conf → antimage-51820
		base := filepath.Base(path)
		iface := base[:len(base)-len(fileSuffix)]

		// Check if interface is actually up
		_, up, err := a.rt.InterfaceStatus(ctx, iface)
		if err != nil || !up {
			// Interface down or error, skip
			continue
		}

		// Read transfer stats
		transfers, err := a.rt.ShowTransfer(ctx, iface)
		if err != nil {
			// Interface might have gone down between check and read, skip
			continue
		}

		// Merge into global counters map
		for publicKey, transfer := range transfers {
			counters[publicKey] = peerCounters{
				RxBytes: transfer.RxBytes,
				TxBytes: transfer.TxBytes,
			}
		}
	}

	return counters, nil
}

// publicKeyToSubject maps WireGuard public key to subject ID.
func (a *Adapter) publicKeyToSubject(publicKey string) (int64, bool) {
	if a.registry == nil {
		return 0, false
	}
	return a.registry.lookup(publicKey)
}

// loadCursor reads the last accounting cursor from disk.
func (a *Adapter) loadCursor() (accountingCursor, error) {
	path := filepath.Join(a.stateDir, "wireguard-accounting.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return accountingCursor{}, err
	}

	var cursor accountingCursor
	if err := json.Unmarshal(data, &cursor); err != nil {
		return accountingCursor{}, fmt.Errorf("unmarshal cursor: %w", err)
	}

	return cursor, nil
}

// saveCursor writes the current accounting cursor to disk.
func (a *Adapter) saveCursor(cursor accountingCursor) error {
	path := filepath.Join(a.stateDir, "wireguard-accounting.json")

	data, err := json.MarshalIndent(cursor, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cursor: %w", err)
	}

	// Atomic write: tmp file + rename
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath) // Best effort cleanup
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}

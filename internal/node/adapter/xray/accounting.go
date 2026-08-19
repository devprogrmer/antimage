package xray

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Usage implements adapter.UsageReporter. It queries Xray stats, computes deltas
// since the last call, detects restarts, and persists cursors.
//
// SP3 design decision 1: the node computes deltas; the panel never sees raw
// cumulative counters. A counter moving backwards means the process restarted,
// and the delta is then the new absolute value rather than a negative number.
func (a *Adapter) Usage(ctx context.Context) ([]adapter.UsageSample, error) {
	if !a.hotAdd {
		// No accounting capability.
		return nil, nil
	}

	rt, ok := a.rt.(*ExecRuntime)
	if !ok || rt.APIAddress == "" {
		return nil, nil
	}

	// Query current cumulative stats from Xray.
	stats, err := rt.QueryStats(ctx)
	if err != nil {
		return nil, fmt.Errorf("query xray stats: %w", err)
	}

	// Load persisted cursors (last-seen cumulative values per user).
	cursors, err := a.loadCursors()
	if err != nil {
		return nil, fmt.Errorf("load cursors: %w", err)
	}

	var samples []adapter.UsageSample
	newCursors := make(map[string]userCursor, len(stats))

	for _, stat := range stats {
		// Extract subject ID from email (format: subject-<id>@antimage).
		subjectID, err := parseSubjectEmail(stat.Email)
		if err != nil {
			// Skip entries that don't match our format (shouldn't happen).
			continue
		}

		prev := cursors[stat.Email]
		var uplink, downlink uint64

		// Detect restart: counter moved backwards.
		if stat.Uplink < prev.Uplink || stat.Downlink < prev.Downlink {
			// Restart detected. Delta is the new absolute value.
			uplink = stat.Uplink
			downlink = stat.Downlink
		} else {
			// Normal case: delta since last poll.
			uplink = stat.Uplink - prev.Uplink
			downlink = stat.Downlink - prev.Downlink
		}

		// Only report non-zero deltas.
		if uplink > 0 || downlink > 0 {
			samples = append(samples, adapter.UsageSample{
				SubjectID:     subjectID,
				UplinkBytes:   uplink,
				DownlinkBytes: downlink,
			})
		}

		// Update cursor with current cumulative value.
		newCursors[stat.Email] = userCursor{
			Uplink:   stat.Uplink,
			Downlink: stat.Downlink,
		}
	}

	// Persist new cursors for next poll.
	if err := a.saveCursors(newCursors); err != nil {
		return nil, fmt.Errorf("save cursors: %w", err)
	}

	return samples, nil
}

// UserStat is one user's cumulative traffic from Xray stats API.
type UserStat struct {
	Email    string
	Uplink   uint64
	Downlink uint64
}

// userCursor tracks the last-seen cumulative value for restart detection.
type userCursor struct {
	Uplink   uint64 `json:"uplink"`
	Downlink uint64 `json:"downlink"`
}

func (a *Adapter) cursorsPath() string {
	return filepath.Join(a.dir, ".xray_cursors.json")
}

func (a *Adapter) loadCursors() (map[string]userCursor, error) {
	path := a.cursorsPath()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// First run: no cursors yet.
		return make(map[string]userCursor), nil
	}
	if err != nil {
		return nil, err
	}
	var cursors map[string]userCursor
	if err := json.Unmarshal(raw, &cursors); err != nil {
		// Corrupted cursors: start fresh rather than fail.
		return make(map[string]userCursor), nil
	}
	return cursors, nil
}

func (a *Adapter) saveCursors(cursors map[string]userCursor) error {
	raw, err := json.Marshal(cursors)
	if err != nil {
		return err
	}
	return os.WriteFile(a.cursorsPath(), raw, 0o600)
}

// parseSubjectEmail extracts the subject ID from "subject-<id>@antimage".
func parseSubjectEmail(email string) (int64, error) {
	const prefix = "subject-"
	const suffix = "@antimage"
	if !strings.HasPrefix(email, prefix) || !strings.HasSuffix(email, suffix) {
		return 0, fmt.Errorf("invalid subject email format: %q", email)
	}
	idStr := strings.TrimSuffix(strings.TrimPrefix(email, prefix), suffix)
	var id int64
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		return 0, fmt.Errorf("parse subject id from %q: %w", email, err)
	}
	return id, nil
}

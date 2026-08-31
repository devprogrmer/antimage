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
		// No accounting capability. hotAdd is set from HotAddSupported(), which
		// is exactly "the management API address is configured" -- and that
		// address is what QueryStats reads through, so this one check answers
		// both questions.
		return nil, nil
	}

	// Through the Runtime interface rather than a *ExecRuntime assertion.
	// QueryStats is on the interface, so the concrete type was never needed,
	// and requiring it made the accounting path -- where C2's attribution is
	// recovered -- impossible to exercise without launching a real Xray.
	stats, err := a.rt.QueryStats(ctx)
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
		// Extract subject and service from the tag. Xray keys one counter per
		// email and the email carries both ids, so this is where C2's
		// attribution is recovered.
		subjectID, serviceID, err := parseSubjectEmail(stat.Email)
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
				ServiceID:     serviceID,
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

// parseSubjectEmail extracts the subject and service ids from an Xray user tag.
//
// Two shapes are accepted:
//
//	subject-<sid>.svc-<svcid>@antimage   the C2 form, attributed
//	subject-<sid>@antimage               the legacy form, service id 0
//
// The legacy form is still parsed on purpose. Emails only change when the
// inbound is rewritten, so between an agent upgrade and the next convergence
// Xray is still counting against the old tags. Rejecting them there would throw
// away real traffic for the sake of a format, and unattributed traffic is worth
// far more than none.
//
// A trailing suffix on the domain ("@antimage-2") is tolerated as before.
func parseSubjectEmail(email string) (subjectID, serviceID int64, err error) {
	const prefix = "subject-"
	const domain = "@antimage"

	if !strings.HasPrefix(email, prefix) {
		return 0, 0, fmt.Errorf("invalid subject email format: %q", email)
	}
	atIndex := strings.Index(email, domain)
	if atIndex == -1 {
		return 0, 0, fmt.Errorf("invalid subject email format: %q", email)
	}

	// Everything between "subject-" and "@antimage": either "<sid>" or
	// "<sid>.svc-<svcid>".
	body := email[len(prefix):atIndex]
	idStr, svcStr, hasService := strings.Cut(body, ".svc-")

	if _, err := fmt.Sscanf(idStr, "%d", &subjectID); err != nil {
		return 0, 0, fmt.Errorf("parse subject id from %q: %w", email, err)
	}
	if hasService {
		if _, err := fmt.Sscanf(svcStr, "%d", &serviceID); err != nil {
			// The subject id parsed, so the traffic is still attributable to a
			// person. Losing the service is a smaller loss than losing the
			// sample, so report it unattributed rather than failing.
			return subjectID, 0, nil
		}
	}
	return subjectID, serviceID, nil
}

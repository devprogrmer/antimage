package ocserv

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// cursorName is the sidecar holding the last poll's counters.
const cursorName = "ocserv-accounting.json"

// cursor is what Usage remembers between polls.
//
// Keyed by SESSION id, not by username. occtl's byte counters belong to one
// connection: a client that reconnects gets a new session whose counters start
// at zero, and a cursor keyed by username would read that as a counter going
// backwards on every reconnect. Keyed by session, a reconnect is simply a key
// that was not there before, and its counters are the delta.
type cursor struct {
	Sessions map[string]sessionCounters `json:"sessions"`
	// ServiceID is recorded by Apply, because Usage is not given the desired
	// document and C2 wants traffic attributed to the service that carried it.
	// Zero means unattributed, which the panel already handles.
	ServiceID int64 `json:"service_id"`
}

type sessionCounters struct {
	Username string `json:"username"`
	RX       int64  `json:"rx"`
	TX       int64  `json:"tx"`
}

// Usage reports traffic deltas since the last successful call.
//
// KNOWN LIMIT, stated rather than hidden: a session that starts and ends
// entirely between two polls is never seen, and the bytes a session moves
// between the last poll and its disconnect are lost. occtl only reports LIVE
// sessions, so there is nothing left to read once one is gone. The loss is
// bounded by one poll interval per disconnecting session. Closing it properly
// needs ocserv's connect/disconnect scripts, which is a host-integration
// change rather than an adapter one.
func (a *Adapter) Usage(ctx context.Context) ([]adapter.UsageSample, error) {
	prev := a.loadCursor()

	live, err := a.rt.ShowUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("read ocserv sessions: %w", err)
	}

	next := cursor{
		Sessions:  make(map[string]sessionCounters, len(live)),
		ServiceID: prev.ServiceID,
	}
	// Deltas accumulate per subject: one customer may hold several concurrent
	// sessions, and reporting them separately would make the panel's
	// per-subject arithmetic depend on how many devices they happened to have
	// connected at poll time.
	bySubject := make(map[int64]*adapter.UsageSample)

	for _, u := range live {
		key := u.Session
		if key == "" {
			// A build of occtl that does not report a session id. Falling back
			// to the username keeps accounting working; it just cannot tell a
			// reconnect from continued use, which the reset guard below
			// absorbs.
			key = u.Username
		}
		cur := sessionCounters{Username: u.Username, RX: int64(u.RX), TX: int64(u.TX)}
		next.Sessions[key] = cur

		subjectID, ours := subjectIDFromUsername(u.Username)
		if !ours {
			// An account somebody else created on this ocserv. Not ours to
			// bill, and there is no subject to bill it to.
			continue
		}

		before := prev.Sessions[key]
		// A counter that went backwards means the session was replaced under
		// the same key. Crediting the current value rather than a negative
		// delta is the same reading wireguard takes of an interface restart.
		rx, tx := cur.RX-before.RX, cur.TX-before.TX
		if rx < 0 {
			rx = cur.RX
		}
		if tx < 0 {
			tx = cur.TX
		}
		if rx == 0 && tx == 0 {
			continue
		}

		s := bySubject[subjectID]
		if s == nil {
			s = &adapter.UsageSample{SubjectID: subjectID, ServiceID: prev.ServiceID}
			bySubject[subjectID] = s
		}
		// occtl reports from the SERVER's point of view: RX is what the server
		// received from the client, which is the client's uplink.
		s.UplinkBytes += uint64(rx)
		s.DownlinkBytes += uint64(tx)
	}

	samples := make([]adapter.UsageSample, 0, len(bySubject))
	for _, s := range bySubject {
		samples = append(samples, *s)
	}

	if err := a.saveCursor(next); err != nil {
		// The samples are still correct; only the next delta will be wrong.
		// Returning them with the error lets the caller record the traffic
		// rather than discarding a poll's worth of billing over a disk hiccup.
		return samples, fmt.Errorf("save accounting cursor: %w (samples still valid)", err)
	}
	return samples, nil
}

// rememberServiceID records which service this node's ocserv belongs to, so
// Usage can attribute traffic without being handed the desired document.
func (a *Adapter) rememberServiceID(id int64) {
	c := a.loadCursor()
	if c.ServiceID == id {
		return
	}
	c.ServiceID = id
	// Best effort: failing an apply because an accounting sidecar could not be
	// written would take a working service down over a bookkeeping detail.
	// The cost of losing it is traffic attributed to service 0, which the
	// panel already handles.
	_ = a.saveCursor(c)
}

func (a *Adapter) cursorPath() string { return filepath.Join(a.stateDir, cursorName) }

// loadCursor returns the stored cursor, or an empty one.
//
// A missing or unreadable cursor is an empty one rather than an error: the
// first poll after an agent upgrade legitimately has none, and the effect is
// that the first deltas are the sessions' current totals. Over-reporting once
// is better than refusing to account at all.
func (a *Adapter) loadCursor() cursor {
	empty := cursor{Sessions: map[string]sessionCounters{}}
	body, err := os.ReadFile(a.cursorPath())
	if err != nil {
		return empty
	}
	var c cursor
	if err := json.Unmarshal(body, &c); err != nil {
		return empty
	}
	if c.Sessions == nil {
		c.Sessions = map[string]sessionCounters{}
	}
	return c
}

func (a *Adapter) saveCursor(c cursor) error {
	if err := os.MkdirAll(a.stateDir, 0o700); err != nil {
		return err
	}
	body, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tmp := a.cursorPath() + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.cursorPath())
}

package openvpn

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/amyrm/antimage/internal/node/adapter"
)

const cursorName = "openvpn-accounting.json"

// cursor is what Usage remembers between polls.
//
// Keyed by the client id OpenVPN assigns, not by common name. A client that
// reconnects gets a new id and its counters start again at zero; keyed by name
// that would read as a counter running backwards on every reconnect.
type cursor struct {
	Clients   map[string]clientCounters `json:"clients"`
	ServiceID int64                     `json:"service_id"`
}

type clientCounters struct {
	CommonName string `json:"common_name"`
	Received   int64  `json:"received"`
	Sent       int64  `json:"sent"`
}

// statusClient is one CLIENT_LIST row.
type statusClient struct {
	CommonName string
	Received   int64
	Sent       int64
	ClientID   string
}

// parseStatus reads OpenVPN's status-version 2 output.
//
// The format is comma-separated with a leading record type. The columns that
// matter:
//
//	CLIENT_LIST,<common name>,<real addr>,<virtual addr>,<virtual ipv6>,
//	<bytes received>,<bytes sent>,<connected since>,<connected since unix>,
//	<username>,<client id>,<peer id>,<cipher>
//
// Fields are located by INDEX because that is what the format guarantees, but
// every row is length-checked first: OpenVPN has added columns across versions
// and a short row from an older build must be skipped, not read as if the
// columns it does have were the ones we wanted.
func parseStatus(body string) []statusClient {
	var out []statusClient
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "CLIENT_LIST,") {
			continue
		}
		f := strings.Split(line, ",")
		// Through the byte counters at 5 and 6 is the minimum this can use.
		if len(f) < 7 {
			continue
		}
		received, err1 := strconv.ParseInt(strings.TrimSpace(f[5]), 10, 64)
		sent, err2 := strconv.ParseInt(strings.TrimSpace(f[6]), 10, 64)
		if err1 != nil || err2 != nil {
			// A row whose counters do not parse is skipped rather than counted
			// as zero: zero is a real value meaning "no traffic", and treating
			// a parse failure as one would quietly under-bill.
			continue
		}
		c := statusClient{CommonName: f[1], Received: received, Sent: sent}
		// Client id is at 10 when the build reports it. Older builds do not,
		// and fall back to the common name -- which loses reconnect detection
		// but keeps accounting working.
		if len(f) > 10 && strings.TrimSpace(f[10]) != "" {
			c.ClientID = strings.TrimSpace(f[10])
		} else {
			c.ClientID = c.CommonName
		}
		out = append(out, c)
	}
	return out
}

// Usage reports traffic deltas since the last successful call.
//
// KNOWN LIMIT, stated rather than hidden: the status file lists only CONNECTED
// clients. Traffic a client moves between the last poll and its disconnect is
// never seen, because the row is gone by the next poll. The loss is bounded by
// one poll interval per disconnecting client. Closing it needs OpenVPN's
// client-disconnect script, which is host integration rather than adapter work
// -- the same gap the ocserv adapter has, for the same reason.
func (a *Adapter) Usage(ctx context.Context) ([]adapter.UsageSample, error) {
	prev := a.loadCursor()

	body, err := a.rt.ReadStatus(ctx, a.statusPath())
	if err != nil {
		return nil, fmt.Errorf("read openvpn status: %w", err)
	}

	live := parseStatus(body)
	next := cursor{
		Clients:   make(map[string]clientCounters, len(live)),
		ServiceID: prev.ServiceID,
	}
	// Accumulated per subject: one customer may hold several concurrent
	// connections under duplicate-cn, and reporting them separately would make
	// the panel's arithmetic depend on how many were up at poll time.
	bySubject := make(map[int64]*adapter.UsageSample)

	for _, c := range live {
		next.Clients[c.ClientID] = clientCounters{
			CommonName: c.CommonName, Received: c.Received, Sent: c.Sent,
		}

		subjectID, ours := subjectIDFromUsername(c.CommonName)
		if !ours {
			// A common name this adapter never issued. Not ours to bill, and
			// there is no subject to bill it to.
			continue
		}

		before := prev.Clients[c.ClientID]
		rx, tx := c.Received-before.Received, c.Sent-before.Sent
		// Backwards means the id was reused by a new connection. Crediting the
		// current value is the same reading the other adapters take.
		if rx < 0 {
			rx = c.Received
		}
		if tx < 0 {
			tx = c.Sent
		}
		if rx == 0 && tx == 0 {
			continue
		}

		s := bySubject[subjectID]
		if s == nil {
			s = &adapter.UsageSample{SubjectID: subjectID, ServiceID: prev.ServiceID}
			bySubject[subjectID] = s
		}
		// The status file is written from the SERVER's point of view: bytes
		// received are what the client uploaded.
		s.UplinkBytes += uint64(rx)
		s.DownlinkBytes += uint64(tx)
	}

	samples := make([]adapter.UsageSample, 0, len(bySubject))
	for _, s := range bySubject {
		samples = append(samples, *s)
	}

	if err := a.saveCursor(next); err != nil {
		// The samples are correct; only the next delta will be wrong. Returning
		// them alongside the error records the traffic rather than discarding a
		// poll's worth of billing over a disk hiccup.
		return samples, fmt.Errorf("save accounting cursor: %w (samples still valid)", err)
	}
	return samples, nil
}

// rememberServiceID records which service this node's OpenVPN belongs to, so
// Usage can attribute traffic without being handed the desired document.
func (a *Adapter) rememberServiceID(id int64) {
	c := a.loadCursor()
	if c.ServiceID == id {
		return
	}
	c.ServiceID = id
	// Best effort: failing an apply because an accounting sidecar could not be
	// written would take a working VPN down over bookkeeping. The cost is
	// traffic attributed to service 0, which the panel already handles.
	_ = a.saveCursor(c)
}

func (a *Adapter) cursorPath() string { return filepath.Join(a.stateDir, cursorName) }

func (a *Adapter) loadCursor() cursor {
	empty := cursor{Clients: map[string]clientCounters{}}
	body, err := os.ReadFile(a.cursorPath())
	if err != nil {
		return empty
	}
	var c cursor
	if err := json.Unmarshal(body, &c); err != nil {
		return empty
	}
	if c.Clients == nil {
		c.Clients = map[string]clientCounters{}
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

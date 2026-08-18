package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/amyrm/antimage/internal/panel/auth"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

// defaultSSEInterval is how often the panel pushes a node-status snapshot.
// Status is low-cardinality and changes slowly, so a small periodic snapshot
// is simpler and more robust than a per-event fan-out, and it self-heals
// after a dropped connection.
const defaultSSEInterval = 3 * time.Second

func (d Deps) sseInterval() time.Duration {
	if d.SSEInterval > 0 {
		return d.SSEInterval
	}
	return defaultSSEInterval
}

// nodeStatus is the per-node wire shape of a snapshot. It carries the name
// because the client has to label a row, and drift precomputed because
// "applied is behind desired" is the one thing the operator is watching for.
type nodeStatus struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	Online          bool   `json:"online"`
	DesiredRevision int64  `json:"desired_revision"`
	AppliedRevision int64  `json:"applied_revision"`
	Drift           bool   `json:"drift"`
}

// handleEvents streams node status as server-sent events.
//
// Unlike every other handler here, this one runs for hours, which changes two
// things. First, authorization is checked once up front as usual, but the
// session is re-validated on every tick: authMiddleware calls Sessions.Lookup
// exactly when the request arrives, so without a re-check an admin who logged
// out — or whose session an operator revoked, or which simply expired — would
// keep receiving live status until they chose to disconnect. This project uses
// opaque server-side sessions rather than JWTs precisely so revocation takes
// effect immediately, and the endpoint that stays open longest is the one that
// would otherwise defeat that. The re-check costs one indexed lookup per tick,
// against the node query already happening on the same tick.
//
// It uses Sessions.Validate, not Sessions.Lookup, because Lookup refreshes
// last_used_at and the idle window is measured from it. Re-checking with
// Lookup every few seconds would mean an unattended browser tab holding this
// stream open never reaches IdleTimeout: the connection alone would count as
// activity. Validate runs the identical checks and writes nothing, so the
// stream can enforce revocation without extending the idle window.
//
// Second, nothing may be held across a tick: each snapshot runs one scoped
// query through Store.ListNodes, which closes its rows and reports rows.Err
// before returning, so the read connection is not pinned between sends. No
// goroutine is started, so a client that disappears leaks nothing — the
// request context is the only thing keeping the loop alive.
func (d Deps) handleEvents(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermNodeRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	// authMiddleware would have rejected a request without this cookie, so
	// the failure below is unreachable in practice; it is here because the
	// re-validation loop needs the token and must not invent one.
	cookie, err := r.Cookie(auth.CookieName)
	if err != nil {
		WriteError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteError(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // defeat proxy buffering
	w.WriteHeader(http.StatusOK)

	ctx := r.Context()
	scope := rbac.ScopeOf(actor)

	// send reports whether the stream should continue. Once the 200 is on
	// the wire there is no status left to change, so a failure here can only
	// end the stream; the client reconnects and gets a fresh snapshot.
	send := func() bool {
		rows, err := d.Store.ListNodes(ctx, scope)
		if err != nil {
			return false
		}
		payload := make([]nodeStatus, 0, len(rows))
		for _, n := range rows {
			payload = append(payload, nodeStatus{
				ID:              n.ID,
				Name:            n.Name,
				Status:          n.Status,
				Online:          d.Hub.Online(n.ID),
				DesiredRevision: n.DesiredRevision,
				AppliedRevision: n.AppliedRevision,
				Drift:           n.AppliedRevision != n.DesiredRevision,
			})
		}
		body, err := json.Marshal(map[string]any{"nodes": payload})
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: nodes\ndata: %s\n\n", body); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// Send immediately so the UI paints without waiting a full interval. The
	// session was validated microseconds ago by the middleware, so this first
	// snapshot needs no re-check.
	if !send() {
		return
	}

	ticker := time.NewTicker(d.sseInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Every rejection reason collapses into ErrSessionInvalid, and
			// all of them mean the same thing here: stop streaming. A
			// storage failure stops it too — continuing to serve a session
			// that could not be verified is the failure mode this guards.
			if _, err := d.Sessions.Validate(ctx, cookie.Value); err != nil {
				return
			}
			if !send() {
				return
			}
		}
	}
}

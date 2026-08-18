package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/auth"
)

// streamRecorder is an httptest.ResponseRecorder that can be observed while
// the handler is still running.
//
// httptest.ResponseRecorder cannot: reading its Body from the test goroutine
// while the streaming handler writes to it is a data race, so a test using it
// can only sleep and hope. This one guards the buffer and signals every Flush,
// which lets a test block until a snapshot has actually gone out instead of
// timing one.
type streamRecorder struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	status  int
	wrote   bool
	hdr     http.Header
	flushed chan struct{}
}

func newStreamRecorder() *streamRecorder {
	return &streamRecorder{
		status:  http.StatusOK,
		hdr:     http.Header{},
		flushed: make(chan struct{}, 128),
	}
}

func (s *streamRecorder) Header() http.Header { return s.hdr }

func (s *streamRecorder) WriteHeader(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.wrote {
		s.status = status
		s.wrote = true
	}
}

func (s *streamRecorder) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wrote = true
	return s.buf.Write(b)
}

// Flush records that a snapshot reached the wire. The send is non-blocking so
// a handler that outruns the test is never wedged by it.
func (s *streamRecorder) Flush() {
	select {
	case s.flushed <- struct{}{}:
	default:
	}
}

func (s *streamRecorder) body() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *streamRecorder) code() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// awaitEvent blocks until the handler flushes, so tests synchronise on the
// stream rather than on the clock.
func (s *streamRecorder) awaitEvent(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case <-s.flushed:
	case <-time.After(within):
		t.Fatalf("no SSE event within %s; body so far:\n%s", within, s.body())
	}
}

// openStream runs GET /api/v1/events on its own goroutine and returns the
// recorder plus a channel closed when the handler returns.
func (e *testEnv) openStream(t *testing.T, ctx context.Context, token string) (*streamRecorder, <-chan struct{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil).WithContext(ctx)
	if token != "" {
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	}
	rec := newStreamRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.handler.ServeHTTP(rec, req)
	}()
	return rec, done
}

func awaitReturn(t *testing.T, done <-chan struct{}, within time.Duration, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(within):
		t.Fatalf("SSE handler did not return %s", what)
	}
}

func TestEventsStreamsSnapshotsAndClosesOnCancel(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	env.seedNode(t, "de-1")
	token := env.login(t, "root", "pw")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec, done := env.openStream(t, ctx, token)

	// The first snapshot is sent immediately, so the UI paints without
	// waiting a whole interval. Waiting for it also proves the stream is
	// live before the disconnect below means anything.
	rec.awaitEvent(t, 3*time.Second)
	cancel()
	awaitReturn(t, done, 3*time.Second, "after the client disconnected")

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if got := rec.code(); got != http.StatusOK {
		t.Errorf("status = %d, want 200", got)
	}
	body := rec.body()
	if !strings.Contains(body, "event: nodes") {
		t.Errorf("no nodes event in stream:\n%s", body)
	}
	if !strings.Contains(body, "de-1") {
		t.Errorf("node payload missing from stream:\n%s", body)
	}
}

func TestEventsRequiresAuthentication(t *testing.T) {
	env := newTestEnv(t)
	res := env.get(t, "/api/v1/events", "")
	if res.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", res.Code)
	}
}

// TestEventsRequiresNodeRead pins the permission layer: the stream is a node
// read like any other, so a role without node:read is refused before a single
// byte of event-stream is written.
//
// It goes through openStream rather than env.get because a handler that
// skipped the check would stream forever into a synchronous recorder and hang
// the test instead of failing it. The context deadline bounds that: a refused
// request returns at once, a wrongly-admitted one returns at the deadline with
// a 200 and a stream body, which is the assertion failure this test wants.
func TestEventsRequiresNodeRead(t *testing.T) {
	env := newTestEnv(t)
	env.seedNode(t, "edge-a")
	env.seedAdmin(t, "nobody", "pw", "no_permissions")
	token := env.login(t, "nobody", "pw")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rec, done := env.openStream(t, ctx, token)
	awaitReturn(t, done, 5*time.Second, "for a request it had no permission to serve")

	if got := rec.code(); got != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", got, rec.body())
	}
	if body := rec.body(); !strings.Contains(body, `"code":"forbidden"`) {
		t.Errorf("error code missing from denial: %s", body)
	}
	if ct := rec.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("denied request still opened a stream: Content-Type = %q", ct)
	}
}

// TestEventsScopesNodesToTheCaller pins the second enforcement layer inside
// the stream. A long-lived connection that ignored scope would leak another
// reseller's node continuously rather than once.
func TestEventsScopesNodesToTheCaller(t *testing.T) {
	env := newTestEnv(t)
	mine := env.seedNode(t, "edge-mine")
	env.seedNode(t, "edge-theirs")
	adminID := env.seedAdmin(t, "rachel", "pw", "reseller")
	env.grantNodeScope(t, adminID, mine)
	token := env.login(t, "rachel", "pw")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec, done := env.openStream(t, ctx, token)
	rec.awaitEvent(t, 3*time.Second)
	cancel()
	awaitReturn(t, done, 3*time.Second, "after the client disconnected")

	body := rec.body()
	if !strings.Contains(body, "edge-mine") {
		t.Errorf("caller's own node missing from stream:\n%s", body)
	}
	if strings.Contains(body, "edge-theirs") {
		t.Errorf("stream leaked another reseller's node:\n%s", body)
	}
}

// TestEventsStopsWhenTheSessionIsRevoked is the reason this handler re-checks
// the session on every tick.
//
// authMiddleware looks a session up once, when the request arrives. That is
// enough for every other endpoint, which lives for milliseconds, but an SSE
// stream stays open for hours: without the re-check, logging out, having a
// session revoked, or letting it expire would leave live node status flowing
// to a connection that is no longer authenticated — which is exactly the
// property opaque server-side sessions were chosen over JWTs to get.
//
// The revocation goes through DELETE /api/v1/sessions/{id}, so this is the
// real operator path, not a store poke. Timing is not asserted: the test
// blocks on the handler returning, and fails by timing out if it never does.
func TestEventsStopsWhenTheSessionIsRevoked(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) { d.SSEInterval = 20 * time.Millisecond })
	env.seedAdmin(t, "root", "pw", "super_admin")
	env.seedNode(t, "de-1")
	token := env.login(t, "root", "pw")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec, done := env.openStream(t, ctx, token)
	rec.awaitEvent(t, 3*time.Second)

	sessionID := env.currentSessionID(t, token)
	res := env.do(t, http.MethodDelete, "/api/v1/sessions/"+itoa64(sessionID), "", token)
	if res.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d, body = %s", res.Code, res.Body)
	}

	awaitReturn(t, done, 5*time.Second, "after its session was revoked")

	// The stream must not keep emitting after it returns, either.
	before := rec.body()
	time.Sleep(100 * time.Millisecond)
	if after := rec.body(); after != before {
		t.Errorf("stream wrote %d more bytes after returning", len(after)-len(before))
	}
}

// grantNodeScope puts one node into an admin's allow-list.
func (e *testEnv) grantNodeScope(t *testing.T, adminID, nodeID int64) {
	t.Helper()
	err := e.store.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO admin_scopes (admin_id, scope_type, scope_id) VALUES (?, 'node', ?)`,
			adminID, nodeID)
		return err
	})
	if err != nil {
		t.Fatalf("grant scope: %v", err)
	}
}

// currentSessionID returns the id of the session behind the given token.
func (e *testEnv) currentSessionID(t *testing.T, token string) int64 {
	t.Helper()
	res := e.get(t, "/api/v1/sessions", token)
	if res.Code != http.StatusOK {
		t.Fatalf("list sessions: %d %s", res.Code, res.Body)
	}
	var body struct {
		Sessions []struct {
			ID      int64 `json:"id"`
			Current bool  `json:"current"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode sessions: %v", err)
	}
	for _, s := range body.Sessions {
		if s.Current {
			return s.ID
		}
	}
	t.Fatal("no current session in list")
	return 0
}

// An open stream must not keep its own session alive. The handler re-checks
// its session every tick; doing that with Sessions.Lookup instead of
// Sessions.Validate would refresh last_used_at each time, and IdleTimeout is
// measured from that column, so an unattended browser tab would hold a
// logged-in session open forever.
//
// The clock must advance in steps SMALLER than IdleTimeout with a tick
// between them. A single jump past the timeout does not discriminate: both
// Lookup and Validate reject, because either way the gap since the previous
// refresh already exceeds the window. Only repeated small advances separate
// them — Lookup keeps resetting the window and the stream never dies, while
// Validate lets the window run out.
func TestEventsStreamDoesNotKeepItsOwnSessionAlive(t *testing.T) {
	cur := time.Unix(1_700_000_000, 0).UTC()
	var mu sync.Mutex
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return cur
	}
	advance := func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		cur = cur.Add(d)
	}

	env := newTestEnv(t, func(d *Deps) {
		d.Sessions = auth.NewSessions(d.Store, clock)
		d.Limiter = auth.NewLimiter(d.Store, clock)
		d.Now = clock
		d.SSEInterval = 10 * time.Millisecond
	})
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")
	env.post(t, "/api/v1/nodes", `{"name":"de-1","address":"1.2.3.4"}`, token)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec, done := env.openStream(t, ctx, token)
	rec.awaitEvent(t, 5*time.Second)

	// Step the clock in quarter-window increments, letting the stream tick
	// between each. No other request touches this session, so the stream is
	// the only thing that could be refreshing it. After four steps the window
	// has elapsed and the session must be gone.
	step := auth.IdleTimeout / 4
	deadline := time.Now().Add(5 * time.Second)
	for i := 0; i < 8; i++ {
		select {
		case <-done:
			return // died as required
		default:
		}
		advance(step)
		if time.Now().After(deadline) {
			break
		}
		// Give the handler a tick to observe the new time.
		select {
		case <-rec.flushed:
		case <-time.After(500 * time.Millisecond):
		}
	}

	awaitReturn(t, done, 5*time.Second,
		"after its session passed the idle timeout — the stream kept refreshing its own session")
}

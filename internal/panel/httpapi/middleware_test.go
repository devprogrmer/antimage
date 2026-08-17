package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// raw drives the router with full control over Host and Origin, which the
// shared helpers deliberately fix.
func (e *testEnv) raw(method, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
	req.Host = "panel.local"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// silenceLogs keeps the recovery middleware's stack traces out of test output
// while leaving the default logger intact for every other test.
func silenceLogs(t *testing.T) {
	t.Helper()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
}

func errorCode(t *testing.T, res *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return ""
	}
	return body.Error.Code
}

// TestOriginPolicy pins originMiddleware's current contract, including the
// deliberate skip when the header is absent.
//
// The skip is safe only because of properties asserted elsewhere: the session
// cookie is SameSite=Strict and Secure (TestLoginSucceedsAndSetsHardenedCookie),
// so a browser making a cross-site state change sends no cookie and is
// rejected as unauthenticated, and a same-site-but-cross-origin caller — a
// sibling subdomain, which SameSite treats as same-site — does send an Origin
// and is rejected here. Non-browsers such as antimage-ctl send no Origin and
// have no ambient authority to abuse. Changing any of that must break this
// test first.
func TestOriginPolicy(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		path     string
		headers  map[string]string
		wantCode int
		wantErr  string
	}{
		{
			name:     "absent origin on a state change is allowed through",
			method:   http.MethodPost,
			path:     "/api/v1/auth/logout",
			headers:  nil,
			wantCode: http.StatusUnauthorized, // reached authMiddleware, not blocked here
			wantErr:  "unauthenticated",
		},
		{
			name:     "matching origin is allowed through",
			method:   http.MethodPost,
			path:     "/api/v1/auth/logout",
			headers:  map[string]string{"Origin": "https://panel.local"},
			wantCode: http.StatusUnauthorized,
			wantErr:  "unauthenticated",
		},
		{
			name:     "cross-origin state change is rejected",
			method:   http.MethodPost,
			path:     "/api/v1/auth/logout",
			headers:  map[string]string{"Origin": "https://evil.example"},
			wantCode: http.StatusForbidden,
			wantErr:  "bad_origin",
		},
		{
			name:     "sibling subdomain is rejected even though it is same-site",
			method:   http.MethodPost,
			path:     "/api/v1/auth/logout",
			headers:  map[string]string{"Origin": "https://evil.panel.local"},
			wantCode: http.StatusForbidden,
			wantErr:  "bad_origin",
		},
		{
			name:     "opaque origin is rejected",
			method:   http.MethodPost,
			path:     "/api/v1/auth/logout",
			headers:  map[string]string{"Origin": "null"},
			wantCode: http.StatusForbidden,
			wantErr:  "bad_origin",
		},
		{
			name:     "safe methods skip the check entirely",
			method:   http.MethodGet,
			path:     "/api/v1/nodes",
			headers:  map[string]string{"Origin": "https://evil.example"},
			wantCode: http.StatusUnauthorized,
			wantErr:  "unauthenticated",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			res := env.raw(tc.method, tc.path, tc.headers)
			if res.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body = %s)", res.Code, tc.wantCode, res.Body)
			}
			if got := errorCode(t, res); got != tc.wantErr {
				t.Errorf("error code = %q, want %q", got, tc.wantErr)
			}
		})
	}
}

// TestPanicIsRecoveredIntoTheErrorEnvelope registers a panicking route on the
// real router, so it exercises the middleware stack NewRouter actually
// installs rather than a chain reassembled by the test.
func TestPanicIsRecoveredIntoTheErrorEnvelope(t *testing.T) {
	silenceLogs(t)

	env := newTestEnv(t)
	mux, ok := env.handler.(*chi.Mux)
	if !ok {
		t.Fatalf("router is %T, want *chi.Mux; cannot register the panic route", env.handler)
	}
	mux.Get("/test-only/panic", func(http.ResponseWriter, *http.Request) {
		panic("deliberate test panic")
	})

	res := env.get(t, "/test-only/panic", "")

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", res.Code)
	}
	if res.Header().Get("X-Request-ID") == "" {
		t.Error("X-Request-ID missing; a recovered panic must stay correlatable")
	}
	if got := errorCode(t, res); got != "internal" {
		t.Errorf("error code = %q, want %q", got, "internal")
	}
	if body := res.Body.String(); strings.Contains(body, "deliberate test panic") {
		t.Errorf("panic value leaked to the client: %s", body)
	}
}

// TestPanicAfterPartialWriteDoesNotAppendAnEnvelope guards the other half of
// recovery: once a status line is out, a second WriteHeader would corrupt the
// response rather than repair it.
func TestPanicAfterPartialWriteDoesNotAppendAnEnvelope(t *testing.T) {
	silenceLogs(t)

	env := newTestEnv(t)
	mux, ok := env.handler.(*chi.Mux)
	if !ok {
		t.Fatalf("router is %T, want *chi.Mux", env.handler)
	}
	mux.Get("/test-only/partial-panic", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"partial":true`))
		panic("after the header")
	})

	res := env.get(t, "/test-only/partial-panic", "")
	if res.Code != http.StatusOK {
		t.Errorf("status = %d, want the already-committed 200", res.Code)
	}
	if strings.Contains(res.Body.String(), `"error"`) {
		t.Errorf("an error envelope was appended to a committed response: %s", res.Body)
	}
}

// TestNotImplementedStubsAreReachableAndAuthenticated pins that a route whose
// handler has not landed yet is still routed and still behind the auth
// middleware — a stub must answer 501, never 404, and never before a session
// is checked.
//
// It points at whichever route is still a stub. That was /api/v1/audit until
// Task 25 implemented it; /api/v1/events is the remaining one, owned by Task
// 26. Retarget it again rather than deleting it when that lands.
func TestNotImplementedStubsAreReachableAndAuthenticated(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "alice", "pw", "super_admin")
	token := env.login(t, "alice", "pw")

	if res := env.get(t, "/api/v1/events", token); res.Code != http.StatusNotImplemented {
		t.Errorf("authenticated stub status = %d, want 501", res.Code)
	}
	if res := env.get(t, "/api/v1/events", ""); res.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated stub status = %d, want 401", res.Code)
	}
}

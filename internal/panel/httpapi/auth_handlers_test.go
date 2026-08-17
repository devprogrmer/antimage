package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/amyrm/antimage/internal/panel/auth"
)

// TestPrivateHandlersFailClosedWithoutAnActor pins the nil guard. Every
// handler behind authMiddleware can assume an actor, but a future routing
// mistake that drops the middleware must turn into a 401, not a nil-pointer
// panic that reveals the bug only in production.
func TestPrivateHandlersFailClosedWithoutAnActor(t *testing.T) {
	env := newTestEnv(t)
	d := Deps{Store: env.store}

	handlers := map[string]http.HandlerFunc{
		"me":         d.handleMe,
		"list_nodes": d.handleListNodes,
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if got := errorCode(t, rec); got != "unauthenticated" {
				t.Errorf("error code = %q, want %q", got, "unauthenticated")
			}
		})
	}
}

func TestLoginSucceedsAndSetsHardenedCookie(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "alice", "correct horse battery staple", "super_admin")

	res := env.post(t, "/api/v1/auth/login",
		`{"username":"alice","password":"correct horse battery staple"}`, "")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body)
	}

	cookies := res.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, want 1", len(cookies))
	}
	c := cookies[0]
	if c.Name != auth.CookieName {
		t.Errorf("cookie name = %q, want %q", c.Name, auth.CookieName)
	}
	if !c.HttpOnly {
		t.Error("cookie is not HttpOnly")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Error("cookie is not SameSite=Strict")
	}
	// Secure is what keeps the token off plaintext hops, and it is also half
	// of what makes the absent-Origin skip in originMiddleware defensible.
	if !c.Secure {
		t.Error("cookie is not Secure")
	}
}

func TestLoginFailureIsGenericAndAudited(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "alice", "right", "super_admin")

	unknown := env.post(t, "/api/v1/auth/login", `{"username":"nobody","password":"x"}`, "")
	wrong := env.post(t, "/api/v1/auth/login", `{"username":"alice","password":"wrong"}`, "")

	if unknown.Code != http.StatusUnauthorized || wrong.Code != http.StatusUnauthorized {
		t.Fatalf("status codes = %d/%d, want 401/401", unknown.Code, wrong.Code)
	}
	if unknown.Body.String() != wrong.Body.String() {
		t.Errorf("responses differ, so username existence leaks:\n%s\n%s",
			unknown.Body.String(), wrong.Body.String())
	}

	var denied int
	if err := env.store.Read().QueryRow(
		`SELECT count(*) FROM audit_log WHERE action = 'auth.login' AND result = 'denied'`,
	).Scan(&denied); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if denied != 2 {
		t.Errorf("denied login audit rows = %d, want 2", denied)
	}
}

func TestLoginLocksOutAfterRepeatedFailures(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "alice", "right", "super_admin")

	for i := 0; i < auth.AccountFailureLimit; i++ {
		env.post(t, "/api/v1/auth/login", `{"username":"alice","password":"wrong"}`, "")
	}
	res := env.post(t, "/api/v1/auth/login", `{"username":"alice","password":"right"}`, "")
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 even with the correct password", res.Code)
	}
	if res.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header missing")
	}
}

func TestUnauthenticatedRequestIsRejected(t *testing.T) {
	env := newTestEnv(t)
	res := env.get(t, "/api/v1/nodes", "")
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.Code)
	}
}

func TestLogoutRevokesTheSessionImmediately(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "alice", "pw", "super_admin")
	token := env.login(t, "alice", "pw")

	if res := env.get(t, "/api/v1/nodes", token); res.Code != http.StatusOK {
		t.Fatalf("pre-logout status = %d, want 200", res.Code)
	}
	if res := env.post(t, "/api/v1/auth/logout", "", token); res.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, want 204", res.Code)
	}
	if res := env.get(t, "/api/v1/nodes", token); res.Code != http.StatusUnauthorized {
		t.Fatalf("post-logout status = %d, want 401", res.Code)
	}
}

// TestLogoutRevokesServerSideNotJustTheCookie pins the half of logout that the
// cookie cannot prove. A handler that only expires the cookie still passes a
// test driven through a browser-like client, because the client stops sending
// the token. Here the token is replayed explicitly, so only a server-side
// revocation makes the second request fail.
func TestLogoutRevokesServerSideNotJustTheCookie(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "alice", "pw", "super_admin")
	token := env.login(t, "alice", "pw")

	if res := env.post(t, "/api/v1/auth/logout", "", token); res.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, want 204", res.Code)
	}

	var revoked int
	if err := env.store.Read().QueryRow(
		`SELECT count(*) FROM sessions WHERE revoked_at IS NOT NULL`).Scan(&revoked); err != nil {
		t.Fatalf("count revoked sessions: %v", err)
	}
	if revoked != 1 {
		t.Errorf("revoked sessions = %d, want 1; the row is still usable", revoked)
	}
	if res := env.get(t, "/api/v1/auth/me", token); res.Code != http.StatusUnauthorized {
		t.Errorf("replayed token status = %d, want 401", res.Code)
	}
}

func TestEveryResponseCarriesARequestID(t *testing.T) {
	env := newTestEnv(t)
	res := env.get(t, "/api/v1/nodes", "")
	if res.Header().Get("X-Request-ID") == "" {
		t.Error("X-Request-ID header missing; audit correlation depends on it")
	}
}

func TestMeReturnsTheResolvedActor(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "alice", "pw", "super_admin")
	token := env.login(t, "alice", "pw")

	res := env.get(t, "/api/v1/auth/me", token)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body)
	}
	var body struct {
		Role        string   `json:"role"`
		IsSuper     bool     `json:"is_super"`
		Permissions []string `json:"permissions"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Role != "super_admin" || !body.IsSuper {
		t.Errorf("role = %q, is_super = %v, want super_admin/true", body.Role, body.IsSuper)
	}
	if len(body.Permissions) == 0 {
		t.Error("permissions empty")
	}
}

func TestErrorBodyShape(t *testing.T) {
	env := newTestEnv(t)
	res := env.get(t, "/api/v1/nodes", "")
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.Error.Code == "" || body.Error.Message == "" {
		t.Errorf("error body missing code or message: %+v", body)
	}
	if strings.Contains(body.Error.Message, "sql") {
		t.Error("error message leaks internals")
	}
}

// TestErrorBodyHidesGenuineInternalFailures is the version of the check that
// can actually fail. Asserting the absence of "sql" in a 401 proves nothing:
// that response never touched an internal error. Here a real storage failure
// is forced, the underlying Go error is captured, and the response is required
// to share no meaningful token with it.
func TestErrorBodyHidesGenuineInternalFailures(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "alice", "pw", "super_admin")
	token := env.login(t, "alice", "pw")

	// Close the store out from under the handler. Every query now fails with a
	// driver error, which is exactly the class of error that must not reach a
	// reseller.
	if err := env.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	var scratch int
	underlying := env.store.Read().QueryRow(`SELECT 1`).Scan(&scratch)
	if underlying == nil {
		t.Fatal("store still answers queries after Close; the failure was not forced")
	}

	res := env.get(t, "/api/v1/nodes", token)
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body = %s)", res.Code, res.Body)
	}

	raw := res.Body.String()
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(strings.NewReader(raw)).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v (raw = %q)", err, raw)
	}
	if body.Error.Code == "" || body.Error.Message == "" {
		t.Errorf("error body missing code or message: %+v", body)
	}

	lowerRaw := strings.ToLower(raw)
	msg := underlying.Error()
	if strings.Contains(lowerRaw, strings.ToLower(msg)) {
		t.Errorf("response repeats the underlying error verbatim:\nunderlying = %q\nbody = %s", msg, raw)
	}
	// Token-level check, so a paraphrase of the driver error is caught too.
	for _, tok := range strings.FieldsFunc(msg, func(r rune) bool {
		return r == ' ' || r == ':' || r == ',' || r == '"' || r == '(' || r == ')'
	}) {
		if len(tok) < 3 {
			continue // "is", "a": too common to be a leak signal
		}
		if strings.Contains(lowerRaw, strings.ToLower(tok)) {
			t.Errorf("response leaks %q from the underlying error %q:\n%s", tok, msg, raw)
		}
	}
}

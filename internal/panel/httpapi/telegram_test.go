package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/notify/telegram"
)

func issueCodeVia(t *testing.T, env *testEnv, token string) string {
	t.Helper()
	res := env.post(t, "/api/v1/me/telegram/link", "{}", token)
	if res.Code != http.StatusCreated {
		t.Fatalf("issue code: %d %s", res.Code, res.Body)
	}
	var out struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Code == "" {
		t.Fatal("no code returned")
	}
	return out.Code
}

// The end-to-end path an operator actually walks: issue a code in the panel,
// send it to the bot, then see the binding reflected back in the panel.
func TestTelegramLinkRoundTrip(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")

	if res := env.get(t, "/api/v1/me/telegram", token); res.Code != http.StatusOK {
		t.Fatalf("status before linking: %d", res.Code)
	} else if !strings.Contains(res.Body.String(), `"linked":false`) {
		t.Errorf("expected linked:false, got %s", res.Body)
	}

	code := issueCodeVia(t, env, token)

	// Redeem exactly as the bot does.
	links := telegram.NewStore(env.store, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() })
	var adminID int64
	if err := env.store.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		adminID, err = links.Redeem(context.Background(), tx, 4242, "operator", code)
		return err
	}); err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if adminID == 0 {
		t.Fatal("redeem bound nothing")
	}

	res := env.get(t, "/api/v1/me/telegram", token)
	if !strings.Contains(res.Body.String(), `"linked":true`) {
		t.Fatalf("panel does not show the link: %s", res.Body)
	}
	if !strings.Contains(res.Body.String(), "4242") {
		t.Errorf("panel does not show the telegram id: %s", res.Body)
	}

	// And revoking from the panel cuts the bot off.
	if res := env.do(t, http.MethodDelete, "/api/v1/me/telegram", "", token); res.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", res.Code, res.Body)
	}
	if _, err := links.AdminFor(context.Background(), 4242); err == nil {
		t.Error("SECURITY: the account still resolves after a panel-side revoke")
	}
}

// SECURITY: the code must bind the CALLER, never an admin named in the request.
//
// Accepting an admin_id would let any authenticated user mint a code that binds
// their own Telegram account to somebody else's panel user -- a complete
// account takeover through a single field.
func TestLinkCodeAlwaysBindsTheCallerNotARequestedAdmin(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	env.seedAdmin(t, "victim", "pw", "super_admin")
	attackerToken := env.login(t, "root", "pw")

	var victimID int64
	if err := env.store.Read().QueryRow(
		`SELECT id FROM admins WHERE username = 'victim'`).Scan(&victimID); err != nil {
		t.Fatalf("read victim: %v", err)
	}

	// Every shape an attacker might try to smuggle a target admin through.
	for _, body := range []string{
		`{"admin_id":` + itoa64(victimID) + `}`,
		`{"adminId":` + itoa64(victimID) + `}`,
		`{"admin":"victim"}`,
		`{"username":"victim"}`,
	} {
		res := env.post(t, "/api/v1/me/telegram/link", body, attackerToken)
		if res.Code != http.StatusCreated {
			continue // rejected outright is also fine
		}
		var out struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(res.Body).Decode(&out)

		links := telegram.NewStore(env.store, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() })
		var bound int64
		_ = env.store.Write(context.Background(), func(tx *sql.Tx) error {
			var err error
			bound, err = links.Redeem(context.Background(), tx, 5555, "attacker", out.Code)
			return err
		})
		if bound == victimID {
			t.Fatalf("SECURITY: body %s bound the attacker's telegram account "+
				"to the victim's panel user", body)
		}
		// Clean up so the next iteration is not blocked by ErrAlreadyLinked.
		_ = env.store.Write(context.Background(), func(tx *sql.Tx) error {
			return links.Revoke(context.Background(), tx, 5555)
		})
	}
}

// Every route must require authentication: an unauthenticated caller must not
// be able to mint a link code at all.
func TestTelegramRoutesRequireAuthentication(t *testing.T) {
	env := newTestEnv(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/me/telegram"},
		{http.MethodPost, "/api/v1/me/telegram/link"},
		{http.MethodDelete, "/api/v1/me/telegram"},
	} {
		if res := env.do(t, tc.method, tc.path, "{}", ""); res.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", tc.method, tc.path, res.Code)
		}
	}
}

// A link code is a credential: it must not be cached by a browser or proxy,
// for the same reason a revealed subject credential must not.
func TestLinkCodeResponseIsNotCacheable(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")

	res := env.post(t, "/api/v1/me/telegram/link", "{}", token)
	if got := res.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// The audit trail must record that a code was issued, and must never record
// the code itself.
func TestIssuingACodeIsAuditedWithoutTheCode(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")

	code := issueCodeVia(t, env, token)

	var after string
	if err := env.store.Read().QueryRow(
		`SELECT coalesce(after_json,'') FROM audit_log
		  WHERE action = 'telegram.code_issued' ORDER BY id DESC LIMIT 1`).Scan(&after); err != nil {
		t.Fatalf("no audit record: %v", err)
	}
	if strings.Contains(after, code) {
		t.Error("SECURITY: the link code is in the audit record")
	}
}

// Revoking with no binding must not report success, or a UI would show
// "unlinked" for an account that was never linked.
func TestRevokingWithoutALinkIsNotFound(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")

	if res := env.do(t, http.MethodDelete, "/api/v1/me/telegram", "", token); res.Code != http.StatusNotFound {
		t.Errorf("revoke without a link = %d, want 404", res.Code)
	}
}

// One admin's binding must be invisible to another admin's /me.
func TestMeTelegramIsScopedToTheCaller(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "alice", "pw", "super_admin")
	env.seedAdmin(t, "bob", "pw", "super_admin")
	aliceToken := env.login(t, "alice", "pw")
	bobToken := env.login(t, "bob", "pw")

	code := issueCodeVia(t, env, aliceToken)
	links := telegram.NewStore(env.store, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() })
	_ = env.store.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := links.Redeem(context.Background(), tx, 7777, "alice-tg", code)
		return err
	})

	if res := env.get(t, "/api/v1/me/telegram", bobToken); strings.Contains(res.Body.String(), "7777") {
		t.Errorf("bob can see alice's telegram binding: %s", res.Body)
	}
	if res := env.get(t, "/api/v1/me/telegram", aliceToken); !strings.Contains(res.Body.String(), "7777") {
		t.Errorf("alice cannot see her own binding: %s", res.Body)
	}
}

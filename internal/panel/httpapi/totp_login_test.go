package httpapi

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/amyrm/antimage/internal/panel/auth"
	"github.com/amyrm/antimage/internal/panel/control"
	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/shared/secrets"
)

// enrolTOTP seals a fresh TOTP secret into the admin row, the way a future
// enrolment endpoint will, and returns the plaintext secret.
func enrolTOTP(t *testing.T, s *store.Store, box *secrets.Box, adminID int64) string {
	t.Helper()
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	sealed, err := box.Seal([]byte(secret))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE admins SET totp_secret_enc = ? WHERE id = ?`, sealed, adminID)
		return err
	}); err != nil {
		t.Fatalf("store secret: %v", err)
	}
	return secret
}

func newTOTPEnv(t *testing.T, withBox bool) (*testEnv, *secrets.Box, func() time.Time) {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}
	now := func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }

	s, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	deps := Deps{
		Store: s, Sessions: auth.NewSessions(s, now),
		Limiter: auth.NewLimiter(s, now), Hub: control.NewHub(), Now: now,
	}
	if withBox {
		deps.Box = box
	}
	return &testEnv{store: s, handler: NewRouter(deps)}, box, now
}

// An admin who enrolled TOTP must not be admissible on a password alone.
// Before this was enforced, handleLogin parsed loginRequest.TOTP and threw it
// away: the Task 30 UI collects a code and the server ignored whatever was
// typed, so the account was single-factor while presenting as two-factor.
func TestLoginRequiresTOTPOnceEnrolled(t *testing.T) {
	env, box, _ := newTOTPEnv(t, true)
	adminID := env.seedAdmin(t, "alice", "pw", "super_admin")
	enrolTOTP(t, env.store, box, adminID)

	res := env.post(t, "/api/v1/auth/login", `{"username":"alice","password":"pw"}`, "")
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("password-only login = %d, want 401: the second factor was ignored", res.Code)
	}
	for _, c := range res.Result().Cookies() {
		if c.Name == auth.CookieName && c.Value != "" {
			t.Fatal("a session cookie was issued without the second factor")
		}
	}
}

func TestLoginAcceptsAValidTOTPCode(t *testing.T) {
	env, box, now := newTOTPEnv(t, true)
	adminID := env.seedAdmin(t, "alice", "pw", "super_admin")
	secret := enrolTOTP(t, env.store, box, adminID)

	code, err := totp.GenerateCode(secret, now())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	res := env.post(t, "/api/v1/auth/login",
		`{"username":"alice","password":"pw","totp":"`+code+`"}`, "")
	if res.Code != http.StatusOK {
		t.Fatalf("valid code login = %d, want 200; body=%s", res.Code, res.Body)
	}
}

func TestLoginRejectsAWrongTOTPCode(t *testing.T) {
	env, box, _ := newTOTPEnv(t, true)
	adminID := env.seedAdmin(t, "alice", "pw", "super_admin")
	enrolTOTP(t, env.store, box, adminID)

	res := env.post(t, "/api/v1/auth/login",
		`{"username":"alice","password":"pw","totp":"000000"}`, "")
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("wrong code login = %d, want 401", res.Code)
	}
}

// A panel started without the master key cannot verify the factor. It must
// deny rather than decide the factor passed — otherwise a misconfiguration
// silently downgrades every TOTP account to single-factor.
func TestLoginFailsClosedWhenTheSecretBoxIsAbsent(t *testing.T) {
	env, box, now := newTOTPEnv(t, false) // Deps.Box left nil
	adminID := env.seedAdmin(t, "alice", "pw", "super_admin")
	secret := enrolTOTP(t, env.store, box, adminID)

	code, err := totp.GenerateCode(secret, now())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	res := env.post(t, "/api/v1/auth/login",
		`{"username":"alice","password":"pw","totp":"`+code+`"}`, "")
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("login without a secret box = %d, want 401 (fail closed)", res.Code)
	}
}

// Admins who never enrolled must be unaffected: TOTP is optional per admin.
func TestLoginUnaffectedForAdminsWithoutTOTP(t *testing.T) {
	env, _, _ := newTOTPEnv(t, true)
	env.seedAdmin(t, "bob", "pw", "super_admin")

	res := env.post(t, "/api/v1/auth/login", `{"username":"bob","password":"pw"}`, "")
	if res.Code != http.StatusOK {
		t.Fatalf("login = %d, want 200; body=%s", res.Code, res.Body)
	}
}

// A failed second factor must consume rate-limiter budget, or the 6-digit
// code space is brute-forceable at full speed once the password is known.
func TestFailedTOTPCountsAgainstTheRateLimiter(t *testing.T) {
	env, box, _ := newTOTPEnv(t, true)
	adminID := env.seedAdmin(t, "alice", "pw", "super_admin")
	enrolTOTP(t, env.store, box, adminID)

	for i := 0; i < auth.AccountFailureLimit; i++ {
		env.post(t, "/api/v1/auth/login",
			`{"username":"alice","password":"pw","totp":"000000"}`, "")
	}
	res := env.post(t, "/api/v1/auth/login",
		`{"username":"alice","password":"pw","totp":"000000"}`, "")
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429: TOTP guesses are not rate limited", res.Code)
	}
}

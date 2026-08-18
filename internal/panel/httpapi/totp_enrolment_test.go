package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

// enrolmentEnv seeds one admin with no TOTP and logs them in, which is the
// state every enrolment flow starts from.
func enrolmentEnv(t *testing.T) (env *testEnv, now func() time.Time, adminID int64, token string) {
	t.Helper()
	env, _, now = newTOTPEnv(t, true)
	adminID = env.seedAdmin(t, "alice", "pw", "super_admin")
	token = env.login(t, "alice", "pw")
	return env, now, adminID, token
}

func decodeBody(t *testing.T, res *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(res.Body.Bytes(), v); err != nil {
		t.Fatalf("decode body %q: %v", res.Body.String(), err)
	}
}

type enrolResponse struct {
	Secret          string `json:"secret"`
	ProvisioningURI string `json:"provisioning_uri"`
}

func (e *testEnv) enrol(t *testing.T, token string) enrolResponse {
	t.Helper()
	res := e.post(t, "/api/v1/auth/totp/enrol", `{}`, token)
	if res.Code != http.StatusOK {
		t.Fatalf("enrol = %d, want 200; body=%s", res.Code, res.Body)
	}
	var body enrolResponse
	decodeBody(t, res, &body)
	if body.Secret == "" {
		t.Fatal("enrol returned an empty secret")
	}
	return body
}

func codeFor(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	code, err := totp.GenerateCode(secret, at)
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	return code
}

// confirmEnrolment runs enrol + confirm and returns the secret and the
// plaintext recovery codes the server handed back exactly once.
func confirmEnrolment(t *testing.T, env *testEnv, now func() time.Time, token string) (string, []string) {
	t.Helper()
	body := env.enrol(t, token)
	res := env.post(t, "/api/v1/auth/totp/confirm",
		`{"totp":"`+codeFor(t, body.Secret, now())+`"}`, token)
	if res.Code != http.StatusOK {
		t.Fatalf("confirm = %d, want 200; body=%s", res.Code, res.Body)
	}
	var out struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	decodeBody(t, res, &out)
	if len(out.RecoveryCodes) != recoveryCodeCount {
		t.Fatalf("got %d recovery codes, want %d", len(out.RecoveryCodes), recoveryCodeCount)
	}
	return body.Secret, out.RecoveryCodes
}

// totpColumns reads the caller's stored enrolment state directly, so a test
// can tell "the endpoint returned an error" apart from "the endpoint returned
// an error but wrote the secret anyway".
func totpColumns(t *testing.T, env *testEnv, adminID int64) (secret, pending []byte) {
	t.Helper()
	err := env.store.Read().QueryRow(
		`SELECT totp_secret_enc, totp_pending_enc FROM admins WHERE id = ?`,
		adminID).Scan(&secret, &pending)
	if err != nil {
		t.Fatalf("read totp columns: %v", err)
	}
	return secret, pending
}

func unconsumedRecoveryCodes(t *testing.T, env *testEnv, adminID int64) int {
	t.Helper()
	var n int
	if err := env.store.Read().QueryRow(
		`SELECT count(*) FROM admin_recovery_codes WHERE admin_id = ? AND consumed_at IS NULL`,
		adminID).Scan(&n); err != nil {
		t.Fatalf("count recovery codes: %v", err)
	}
	return n
}

// The whole point of the task: before it, enforcement existed and enrolment
// did not, so no admin could turn TOTP on through the API at all. Enrol,
// confirm with a real code, and the account must be two-factor afterwards.
func TestTOTPEnrolConfirmThenLoginRequiresTOTP(t *testing.T) {
	env, now, adminID, token := enrolmentEnv(t)

	body := env.enrol(t, token)
	if !strings.Contains(body.ProvisioningURI, "otpauth://totp/") ||
		!strings.Contains(body.ProvisioningURI, "issuer=antimage") {
		t.Fatalf("provisioning_uri = %q, want an antimage otpauth URI", body.ProvisioningURI)
	}

	// Enrolling alone must not enable anything: an unproven secret is pending.
	if secret, pending := totpColumns(t, env, adminID); len(secret) != 0 || len(pending) == 0 {
		t.Fatalf("after enrol: secret set=%v pending set=%v, want pending only",
			len(secret) != 0, len(pending) != 0)
	}
	res := env.post(t, "/api/v1/auth/login", `{"username":"alice","password":"pw"}`, "")
	if res.Code != http.StatusOK {
		t.Fatalf("password-only login after enrol = %d, want 200: a pending secret must not enforce", res.Code)
	}

	confirmRes := env.post(t, "/api/v1/auth/totp/confirm",
		`{"totp":"`+codeFor(t, body.Secret, now())+`"}`, token)
	if confirmRes.Code != http.StatusOK {
		t.Fatalf("confirm = %d, want 200; body=%s", confirmRes.Code, confirmRes.Body)
	}
	if secret, pending := totpColumns(t, env, adminID); len(secret) == 0 || len(pending) != 0 {
		t.Fatalf("after confirm: secret set=%v pending set=%v, want secret only",
			len(secret) != 0, len(pending) != 0)
	}

	res = env.post(t, "/api/v1/auth/login", `{"username":"alice","password":"pw"}`, "")
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("password-only login after confirm = %d, want 401: confirm did not enable the factor", res.Code)
	}
	res = env.post(t, "/api/v1/auth/login",
		`{"username":"alice","password":"pw","totp":"`+codeFor(t, body.Secret, now())+`"}`, "")
	if res.Code != http.StatusOK {
		t.Fatalf("login with the enrolled code = %d, want 200; body=%s", res.Code, res.Body)
	}
}

// A rejected confirmation must leave the account exactly as it was. Writing
// the secret first and validating afterwards would enable a factor whose code
// the admin has just demonstrated they cannot produce.
func TestTOTPConfirmRejectsAWrongCodeAndDoesNotEnable(t *testing.T) {
	env, _, adminID, token := enrolmentEnv(t)
	env.enrol(t, token)

	res := env.post(t, "/api/v1/auth/totp/confirm", `{"totp":"000000"}`, token)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("confirm with a wrong code = %d, want 401; body=%s", res.Code, res.Body)
	}
	secret, pending := totpColumns(t, env, adminID)
	if len(secret) != 0 {
		t.Fatal("a rejected confirmation set totp_secret_enc")
	}
	if len(pending) == 0 {
		t.Fatal("a rejected confirmation cleared the pending secret")
	}
	if n := unconsumedRecoveryCodes(t, env, adminID); n != 0 {
		t.Fatalf("a rejected confirmation minted %d recovery codes", n)
	}
	// The account is still single-factor, which is only correct because the
	// factor was never enabled.
	if res := env.post(t, "/api/v1/auth/login",
		`{"username":"alice","password":"pw"}`, ""); res.Code != http.StatusOK {
		t.Fatalf("login = %d, want 200: a rejected confirm changed the login requirement", res.Code)
	}
}

func TestTOTPConfirmWithoutAPendingEnrolmentIs400(t *testing.T) {
	env, _, _, token := enrolmentEnv(t)
	res := env.post(t, "/api/v1/auth/totp/confirm", `{"totp":"000000"}`, token)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("confirm with nothing pending = %d, want 400; body=%s", res.Code, res.Body)
	}
}

// Re-enrolling over a live secret would let an unlocked browser swap the
// second factor out without ever proving it held the old one.
func TestTOTPEnrolRefusesWhenAlreadyEnabled(t *testing.T) {
	env, now, adminID, token := enrolmentEnv(t)
	secret, _ := confirmEnrolment(t, env, now, token)

	res := env.post(t, "/api/v1/auth/totp/enrol", `{}`, token)
	if res.Code != http.StatusConflict {
		t.Fatalf("enrol while enabled = %d, want 409; body=%s", res.Code, res.Body)
	}
	if _, pending := totpColumns(t, env, adminID); len(pending) != 0 {
		t.Fatal("a refused enrolment still parked a pending secret")
	}
	// The original secret must still be the one that works.
	if res := env.post(t, "/api/v1/auth/login",
		`{"username":"alice","password":"pw","totp":"`+codeFor(t, secret, now())+`"}`,
		""); res.Code != http.StatusOK {
		t.Fatalf("login with the original secret = %d, want 200: the refused enrolment moved the factor", res.Code)
	}
}

// Recovery codes are single use. A code that still works after being spent is
// a permanent password-equivalent sitting in whatever the admin printed it on.
func TestRecoveryCodeLogsInOnceAndIsRefusedTheSecondTime(t *testing.T) {
	env, now, adminID, token := enrolmentEnv(t)
	_, codes := confirmEnrolment(t, env, now, token)

	res := env.post(t, "/api/v1/auth/login",
		`{"username":"alice","password":"pw","totp":"`+codes[0]+`"}`, "")
	if res.Code != http.StatusOK {
		t.Fatalf("login with a recovery code = %d, want 200; body=%s", res.Code, res.Body)
	}
	if n := unconsumedRecoveryCodes(t, env, adminID); n != recoveryCodeCount-1 {
		t.Fatalf("%d codes remain unconsumed, want %d: the code was not spent",
			n, recoveryCodeCount-1)
	}

	res = env.post(t, "/api/v1/auth/login",
		`{"username":"alice","password":"pw","totp":"`+codes[0]+`"}`, "")
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("reusing a spent recovery code = %d, want 401", res.Code)
	}
	// A different, unspent code must still work, so the test above is proving
	// single use rather than that recovery codes broke entirely.
	res = env.post(t, "/api/v1/auth/login",
		`{"username":"alice","password":"pw","totp":"`+codes[1]+`"}`, "")
	if res.Code != http.StatusOK {
		t.Fatalf("login with a second recovery code = %d, want 200; body=%s", res.Code, res.Body)
	}

	// The operator has to be able to see the set being burned through.
	var remaining sql.NullString
	if err := env.store.Read().QueryRow(
		`SELECT group_concat(after_json, ' ') FROM audit_log WHERE action = 'auth.recovery_used'`,
	).Scan(&remaining); err != nil {
		t.Fatalf("read recovery audit rows: %v", err)
	}
	if !strings.Contains(remaining.String, `"remaining":9`) ||
		!strings.Contains(remaining.String, `"remaining":8`) {
		t.Fatalf("auth.recovery_used after_json = %q, want the remaining count on each use",
			remaining.String)
	}
}

// A session is what an unlocked browser already has. If a session alone could
// strip the second factor, the factor would protect only the first login.
func TestTOTPDisableRefusesWithoutAValidCodeAndSucceedsWithOne(t *testing.T) {
	env, now, adminID, token := enrolmentEnv(t)
	secret, _ := confirmEnrolment(t, env, now, token)

	res := env.post(t, "/api/v1/auth/totp/disable", `{"totp":"000000"}`, token)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("disable with a wrong code = %d, want 401; body=%s", res.Code, res.Body)
	}
	if s, _ := totpColumns(t, env, adminID); len(s) == 0 {
		t.Fatal("a refused disable cleared totp_secret_enc anyway")
	}
	if res := env.post(t, "/api/v1/auth/login",
		`{"username":"alice","password":"pw"}`, ""); res.Code != http.StatusUnauthorized {
		t.Fatalf("password-only login = %d, want 401: a refused disable dropped the factor", res.Code)
	}

	res = env.post(t, "/api/v1/auth/totp/disable",
		`{"totp":"`+codeFor(t, secret, now())+`"}`, token)
	if res.Code != http.StatusNoContent {
		t.Fatalf("disable with a valid code = %d, want 204; body=%s", res.Code, res.Body)
	}
	if s, p := totpColumns(t, env, adminID); len(s) != 0 || len(p) != 0 {
		t.Fatalf("after disable: secret set=%v pending set=%v, want both cleared",
			len(s) != 0, len(p) != 0)
	}
	if n := unconsumedRecoveryCodes(t, env, adminID); n != 0 {
		t.Fatalf("%d recovery codes survived the disable", n)
	}
	if res := env.post(t, "/api/v1/auth/login",
		`{"username":"alice","password":"pw"}`, ""); res.Code != http.StatusOK {
		t.Fatalf("password-only login after disable = %d, want 200; body=%s", res.Code, res.Body)
	}
}

// An admin who has lost their authenticator still has to be able to turn the
// factor off, or the recovery codes only defer the lockout by one login.
func TestTOTPDisableAcceptsARecoveryCode(t *testing.T) {
	env, now, adminID, token := enrolmentEnv(t)
	_, codes := confirmEnrolment(t, env, now, token)

	res := env.post(t, "/api/v1/auth/totp/disable", `{"totp":"`+codes[3]+`"}`, token)
	if res.Code != http.StatusNoContent {
		t.Fatalf("disable with a recovery code = %d, want 204; body=%s", res.Code, res.Body)
	}
	if s, _ := totpColumns(t, env, adminID); len(s) != 0 {
		t.Fatal("disable via recovery code left totp_secret_enc set")
	}
}

func TestTOTPEndpointsRequireASession(t *testing.T) {
	env, _, _ := newTOTPEnv(t, true)
	env.seedAdmin(t, "alice", "pw", "super_admin")

	for _, path := range []string{"enrol", "confirm", "disable"} {
		res := env.post(t, "/api/v1/auth/totp/"+path, `{"totp":"000000"}`, "")
		if res.Code != http.StatusUnauthorized {
			t.Errorf("POST /auth/totp/%s without a session = %d, want 401", path, res.Code)
		}
	}
}

// auditText concatenates every text-bearing column of every audit row. The
// numeric columns cannot hold a secret, so this is the whole surface an
// audit:read holder can see.
func auditText(t *testing.T, env *testEnv) string {
	t.Helper()
	var all sql.NullString
	err := env.store.Read().QueryRow(`
		SELECT group_concat(
			action || char(10) || target_type || char(10) || actor_label || char(10) ||
			actor_ip || char(10) || request_id || char(10) || result || char(10) ||
			coalesce(before_json,'') || char(10) || coalesce(after_json,''), char(10))
		FROM audit_log`).Scan(&all)
	if err != nil {
		t.Fatalf("read audit_log: %v", err)
	}
	return all.String
}

// The audit log is readable by every holder of audit:read. A TOTP secret, the
// provisioning URI that embeds it, or a recovery code landing there would
// hand the second factor to everyone who can read the log.
func TestTOTPAuditRecordsCarryNoSecrets(t *testing.T) {
	env, now, _, token := enrolmentEnv(t)

	body := env.enrol(t, token)
	// A rejected confirmation, so the denial record is covered too.
	env.post(t, "/api/v1/auth/totp/confirm", `{"totp":"000000"}`, token)

	res := env.post(t, "/api/v1/auth/totp/confirm",
		`{"totp":"`+codeFor(t, body.Secret, now())+`"}`, token)
	if res.Code != http.StatusOK {
		t.Fatalf("confirm = %d, want 200; body=%s", res.Code, res.Body)
	}
	var out struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	decodeBody(t, res, &out)

	// Spend one, so auth.recovery_used is in the log as well.
	if res := env.post(t, "/api/v1/auth/login",
		`{"username":"alice","password":"pw","totp":"`+out.RecoveryCodes[0]+`"}`,
		""); res.Code != http.StatusOK {
		t.Fatalf("recovery login = %d, want 200; body=%s", res.Code, res.Body)
	}
	if res := env.post(t, "/api/v1/auth/totp/disable",
		`{"totp":"`+codeFor(t, body.Secret, now())+`"}`, token); res.Code != http.StatusNoContent {
		t.Fatalf("disable = %d, want 204; body=%s", res.Code, res.Body)
	}

	log := auditText(t, env)
	// Guard against a vacuous pass: if nothing was audited, nothing leaking
	// proves nothing.
	for _, want := range []string{"totp.enrol", "totp.confirm", "totp.disable", "auth.recovery_used"} {
		if !strings.Contains(log, want) {
			t.Fatalf("audit_log has no %q record; the leak assertions below would be vacuous", want)
		}
	}
	if strings.Contains(log, body.Secret) {
		t.Errorf("the TOTP secret appears verbatim in audit_log")
	}
	if strings.Contains(log, body.ProvisioningURI) || strings.Contains(log, "otpauth://") {
		t.Errorf("the provisioning URI appears in audit_log")
	}
	for i, code := range out.RecoveryCodes {
		if strings.Contains(log, code) {
			t.Errorf("recovery code %d appears verbatim in audit_log", i)
		}
	}
}

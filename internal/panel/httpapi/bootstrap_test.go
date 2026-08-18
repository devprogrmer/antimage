package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// The enrollment token must never reach the audit log. The bootstrap command
// carries --token <token> and shell output echoes it readily; the audit log
// is readable by every audit:read holder, a wider audience than the admin who
// supplied the credentials.
func TestRedactTokenRemovesEveryOccurrence(t *testing.T) {
	const tok = "s3cret-enroll-token"
	out := "running: curl ... --token " + tok + "\nretrying with --token " + tok + "\n"
	got := redactToken(out, tok)

	if strings.Contains(got, tok) {
		t.Fatalf("token survived redaction: %q", got)
	}
	if n := strings.Count(got, "<redacted>"); n != 2 {
		t.Errorf("replaced %d occurrences, want 2: %q", n, got)
	}
	// An empty token must not turn into a wildcard that redacts everything.
	if redactToken("untouched output", "") != "untouched output" {
		t.Error("an empty token altered the output")
	}
}

func TestTruncateForAuditCapsOutput(t *testing.T) {
	small := strings.Repeat("x", 100)
	if truncateForAudit(small) != small {
		t.Error("short output was altered")
	}

	huge := strings.Repeat("y", maxAuditedOutput*3)
	got := truncateForAudit(huge)
	if len(got) > maxAuditedOutput+len("\n[truncated]") {
		t.Fatalf("truncated length %d exceeds the cap %d", len(got), maxAuditedOutput)
	}
	if !strings.HasSuffix(got, "[truncated]") {
		t.Error("truncation is not marked, so a reader cannot tell output was cut")
	}
}

// Phase two needs the CA to pin a fingerprint into the node's config. Deps.CA
// is nil until the panel entrypoint builds one, and dereferencing it would
// panic inside a handler holding SSH private keys.
func TestBootstrapWithoutACAReturns503(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")
	env.post(t, "/api/v1/nodes", `{"name":"de-1","address":"1.2.3.4"}`, token)

	res := env.post(t, "/api/v1/nodes/1/bootstrap-ssh",
		`{"host":"1.2.3.4","user":"root","private_key_pem":"x","host_key_fingerprint":"SHA256:abc"}`,
		token)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", res.Code, res.Body)
	}
	if strings.Contains(res.Body.String(), "panic") {
		t.Error("the nil CA panicked instead of failing cleanly")
	}
}

// The route must sit behind authentication and the node:enroll permission.
func TestBootstrapRequiresAuthAndEnrollPermission(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	rootToken := env.login(t, "root", "pw")
	env.post(t, "/api/v1/nodes", `{"name":"de-1","address":"1.2.3.4"}`, rootToken)

	if res := env.post(t, "/api/v1/nodes/1/bootstrap-ssh", `{}`, ""); res.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated status = %d, want 401", res.Code)
	}

	env.seedAdmin(t, "ro", "pw", "readonly")
	roToken := env.login(t, "ro", "pw")
	res := env.post(t, "/api/v1/nodes/1/bootstrap-ssh", `{}`, roToken)
	if res.Code != http.StatusForbidden {
		t.Errorf("readonly status = %d, want 403", res.Code)
	}
}

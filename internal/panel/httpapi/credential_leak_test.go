package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// auditText concatenates every free-text column of every audit row, which is
// where a credential would end up if a handler ever passed one into a Record.
func allAuditText(t *testing.T, env *testEnv) string {
	t.Helper()
	rows, err := env.store.Read().Query(
		`SELECT action, target_type, actor_label, coalesce(before_json,''),
		        coalesce(after_json,''), result
		   FROM audit_log ORDER BY id`)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var b strings.Builder
	var n int
	for rows.Next() {
		var action, target, actor, before, after, result string
		if err := rows.Scan(&action, &target, &actor, &before, &after, &result); err != nil {
			t.Fatalf("scan audit: %v", err)
		}
		b.WriteString(strings.Join([]string{action, target, actor, before, after, result}, " "))
		b.WriteString("\n")
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("audit rows: %v", err)
	}
	if n == 0 {
		t.Fatal("no audit rows at all; this test would pass vacuously")
	}
	t.Logf("scanned %d audit row(s)", n)
	return b.String()
}

// SECURITY: a credential must not escape through any channel other than the
// reveal endpoint itself.
//
// The existing tests cover list and get responses, and that reveal audits by
// kind rather than by value. They do not cover the other three places a secret
// classically escapes: the application log, the audit trail of every OTHER
// operation in the lifecycle, and error messages. A credential in any of those
// outlives the database it was sealed in -- logs get shipped, audit trails get
// exported, errors get pasted into tickets.
func TestCredentialsNeverLeakIntoLogsAuditsOrErrors(t *testing.T) {
	// Capture everything the panel logs for the duration of the lifecycle.
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	env, token, svcID := newSubjectEnv(t)
	const seeded = "11111111-2222-3333-4444-555555555555"

	// --- Full lifecycle, so every audited action runs. ---
	id := createSubjectVia(t, env, token,
		`{"name":"alice","service_ids":[`+itoa64(svcID)+`],`+
			`"credentials":{"uuid":"`+seeded+`"}}`)

	revealed := env.get(t, "/api/v1/subjects/"+itoa64(id)+"/credentials/uuid", token)
	if revealed.Code != http.StatusOK {
		t.Fatalf("reveal: %d %s", revealed.Code, revealed.Body)
	}
	if !strings.Contains(revealed.Body.String(), seeded) {
		t.Fatal("precondition: reveal did not return the seeded credential")
	}

	if res := env.post(t, "/api/v1/subjects/"+itoa64(id)+"/credentials/uuid/rotate",
		"{}", token); res.Code != http.StatusOK && res.Code != http.StatusNoContent {
		t.Fatalf("rotate: %d %s", res.Code, res.Body)
	}
	// Whatever rotation produced is also a secret; capture it to scan for.
	after := env.get(t, "/api/v1/subjects/"+itoa64(id)+"/credentials/uuid", token)
	if after.Code != http.StatusOK {
		t.Fatalf("reveal after rotate: %d %s", after.Code, after.Body)
	}
	rotated := strings.TrimSpace(after.Body.String())
	if strings.Contains(rotated, seeded) {
		t.Fatal("rotation did not change the credential")
	}

	if res := env.do(t, http.MethodPut, "/api/v1/subjects/"+itoa64(id),
		`{"enabled":false}`, token); res.Code != http.StatusNoContent {
		t.Fatalf("update: %d %s", res.Code, res.Body)
	}

	// --- Error paths, which is where secrets get spilled into messages. ---
	var errorBodies strings.Builder
	errorBodies.WriteString(env.get(t, "/api/v1/subjects/999999/credentials/uuid", token).Body.String())
	errorBodies.WriteString(env.get(t, "/api/v1/subjects/"+itoa64(id)+"/credentials/bogus", token).Body.String())
	errorBodies.WriteString(env.post(t, "/api/v1/subjects",
		`{"name":"dup","credentials":{"uuid":"`+seeded+`"},"service_ids":[999999]}`, token).Body.String())
	errorBodies.WriteString(env.do(t, http.MethodDelete, "/api/v1/subjects/999999", "", token).Body.String())

	if res := env.do(t, http.MethodDelete, "/api/v1/subjects/"+itoa64(id), "", token); res.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", res.Code, res.Body)
	}

	// --- Now scan every channel. ---
	// Extract the raw credential values, since the reveal body wraps them.
	secrets := []string{seeded}
	if v := extractUUID(rotated); v != "" {
		secrets = append(secrets, v)
	} else {
		t.Fatalf("could not extract the rotated credential from %q", rotated)
	}
	t.Logf("scanning for %d credential value(s)", len(secrets))

	channels := map[string]string{
		"application log": logs.String(),
		"audit trail":     allAuditText(t, env),
		"error responses": errorBodies.String(),
	}
	for name, content := range channels {
		if content == "" && name == "application log" {
			// Not a failure on its own, but say so rather than passing silently.
			t.Logf("note: the application log was empty for this lifecycle")
		}
		for _, secret := range secrets {
			if strings.Contains(content, secret) {
				t.Errorf("SECURITY: the %s contains a credential (%s...)", name, secret[:8])
			}
		}
	}

	// The sealed bytes must not have leaked either.
	var leftover int
	_ = env.store.Read().QueryRow(
		`SELECT count(*) FROM subject_credentials WHERE subject_id = ?`, id).Scan(&leftover)
	if leftover != 0 {
		t.Errorf("%d credential row(s) survived the delete", leftover)
	}
}

// extractUUID pulls the first UUID-shaped token out of a JSON body.
func extractUUID(body string) string {
	for _, field := range strings.FieldsFunc(body, func(r rune) bool {
		return r == '"' || r == '{' || r == '}' || r == ':' || r == ',' || r == ' ' || r == '\n'
	}) {
		if len(field) == 36 && strings.Count(field, "-") == 4 {
			return field
		}
	}
	return ""
}

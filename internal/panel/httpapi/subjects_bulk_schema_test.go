package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Gap 4 from docs/TENANT-ISOLATION.md: the bulk, export and import handlers
// referenced columns the subjects table does not have -- `disabled`, `frozen`,
// `node_id` and `updated_at` -- so every one of them failed at SQL. They
// returned HTTP 200 while doing nothing, because the per-item error accounting
// swallowed the SQL error into the response body.
//
// That is why these tests assert on the DATABASE and on the "failed" counter,
// never on the status code alone. A test that only checked for 200 passed
// against the broken version.

// execSQL puts a subject into a state the API cannot reach directly, so a
// mutation has something to change and the assertion cannot pass by accident
// on a freshly created row.
func execSQL(t *testing.T, env *testEnv, query string, args ...any) error {
	t.Helper()
	return env.store.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(query, args...)
		return err
	})
}

// subjectRow reads back the columns the bulk operations claim to write.
type subjectRow struct {
	enabled        bool
	expiresAt      *int64
	quotaBytes     *int64
	quotaUsedBytes int64
}

func readSubject(t *testing.T, env *testEnv, id int64) subjectRow {
	t.Helper()
	var out subjectRow
	err := env.store.Read().QueryRow(
		`SELECT enabled, expires_at, quota_bytes, quota_used_bytes
		   FROM subjects WHERE id = ?`, id,
	).Scan(&out.enabled, &out.expiresAt, &out.quotaBytes, &out.quotaUsedBytes)
	if err != nil {
		t.Fatalf("read subject %d: %v", id, err)
	}
	return out
}

// decodeBulk pulls the shared failure accounting out of a bulk response.
func decodeBulk(t *testing.T, body string) (failed int, errs []string) {
	t.Helper()
	var out struct {
		Failed int      `json:"failed"`
		Errors []string `json:"errors"`
	}
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&out); err != nil {
		t.Fatalf("decode bulk response %q: %v", body, err)
	}
	return out.Failed, out.Errors
}

func TestBulkOperationsReachTheDatabase(t *testing.T) {
	t.Run("set-quota writes quota_bytes", func(t *testing.T) {
		env, adminToken, svcID := newSubjectEnv(t)
		_, id := seedTenant(t, env, "alice", svcID, adminToken)

		res := env.post(t, "/api/v1/subjects/bulk/set-quota",
			`{"subject_ids":[`+itoa64(id)+`],"quota_bytes":4096}`, adminToken)
		failed, errs := decodeBulk(t, res.Body.String())
		if failed != 0 {
			t.Fatalf("set-quota failed %d: %v", failed, errs)
		}
		got := readSubject(t, env, id)
		if got.quotaBytes == nil || *got.quotaBytes != 4096 {
			t.Errorf("quota_bytes = %v, want 4096", got.quotaBytes)
		}
	})

	t.Run("set-quota zero clears the quota", func(t *testing.T) {
		env, adminToken, svcID := newSubjectEnv(t)
		_, id := seedTenant(t, env, "alice", svcID, adminToken)

		env.post(t, "/api/v1/subjects/bulk/set-quota",
			`{"subject_ids":[`+itoa64(id)+`],"quota_bytes":4096}`, adminToken)
		res := env.post(t, "/api/v1/subjects/bulk/set-quota",
			`{"subject_ids":[`+itoa64(id)+`],"quota_bytes":0}`, adminToken)
		if failed, errs := decodeBulk(t, res.Body.String()); failed != 0 {
			t.Fatalf("set-quota failed %d: %v", failed, errs)
		}
		if got := readSubject(t, env, id); got.quotaBytes != nil {
			t.Errorf("quota_bytes = %v, want NULL for unlimited", *got.quotaBytes)
		}
	})

	t.Run("reset-traffic zeroes quota_used_bytes", func(t *testing.T) {
		env, adminToken, svcID := newSubjectEnv(t)
		_, id := seedTenant(t, env, "alice", svcID, adminToken)

		if err := execSQL(t, env, `UPDATE subjects SET quota_used_bytes = 999 WHERE id = ?`, id); err != nil {
			t.Fatalf("seed usage: %v", err)
		}
		res := env.post(t, "/api/v1/subjects/bulk/reset-traffic",
			`{"subject_ids":[`+itoa64(id)+`]}`, adminToken)
		if failed, errs := decodeBulk(t, res.Body.String()); failed != 0 {
			t.Fatalf("reset-traffic failed %d: %v", failed, errs)
		}
		if got := readSubject(t, env, id); got.quotaUsedBytes != 0 {
			t.Errorf("quota_used_bytes = %d, want 0", got.quotaUsedBytes)
		}
	})

	t.Run("extend moves expires_at forward", func(t *testing.T) {
		env, adminToken, svcID := newSubjectEnv(t)
		_, id := seedTenant(t, env, "alice", svcID, adminToken)

		before := readSubject(t, env, id)
		res := env.post(t, "/api/v1/subjects/bulk/extend",
			`{"subject_ids":[`+itoa64(id)+`],"days":30}`, adminToken)
		if failed, errs := decodeBulk(t, res.Body.String()); failed != 0 {
			t.Fatalf("extend failed %d: %v", failed, errs)
		}
		after := readSubject(t, env, id)
		if after.expiresAt == nil {
			t.Fatal("expires_at still NULL after extend")
		}
		if before.expiresAt != nil && *after.expiresAt <= *before.expiresAt {
			t.Errorf("expires_at %d did not move past %d", *after.expiresAt, *before.expiresAt)
		}
	})

	t.Run("enable sets enabled", func(t *testing.T) {
		env, adminToken, svcID := newSubjectEnv(t)
		_, id := seedTenant(t, env, "alice", svcID, adminToken)

		if err := execSQL(t, env, `UPDATE subjects SET enabled = 0 WHERE id = ?`, id); err != nil {
			t.Fatalf("seed disabled: %v", err)
		}
		res := env.post(t, "/api/v1/subjects/bulk/enable",
			`{"subject_ids":[`+itoa64(id)+`]}`, adminToken)
		if failed, errs := decodeBulk(t, res.Body.String()); failed != 0 {
			t.Fatalf("enable failed %d: %v", failed, errs)
		}
		if got := readSubject(t, env, id); !got.enabled {
			t.Error("subject still disabled after bulk enable")
		}
	})

	t.Run("delete removes the row", func(t *testing.T) {
		env, adminToken, svcID := newSubjectEnv(t)
		_, id := seedTenant(t, env, "alice", svcID, adminToken)

		res := env.post(t, "/api/v1/subjects/bulk/delete",
			`{"subject_ids":[`+itoa64(id)+`]}`, adminToken)
		if failed, errs := decodeBulk(t, res.Body.String()); failed != 0 {
			t.Fatalf("delete failed %d: %v", failed, errs)
		}
		var n int
		if err := env.store.Read().QueryRow(
			`SELECT COUNT(*) FROM subjects WHERE id = ?`, id).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		if n != 0 {
			t.Errorf("subject %d still present after bulk delete", id)
		}
	})
}

// Deleting a subject removes it from every node's desired document, so the
// revision must move. The version this replaces collected the affected nodes
// into a map and discarded it, leaving nodes serving a deleted user until some
// unrelated change bumped them.
func TestBulkDeleteRepublishesAffectedNodes(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	_, id := seedTenant(t, env, "alice", svcID, adminToken)

	var before int64
	if err := env.store.Read().QueryRow(
		`SELECT desired_revision FROM nodes WHERE id = 1`).Scan(&before); err != nil {
		t.Fatalf("read revision: %v", err)
	}

	res := env.post(t, "/api/v1/subjects/bulk/delete",
		`{"subject_ids":[`+itoa64(id)+`]}`, adminToken)
	if failed, errs := decodeBulk(t, res.Body.String()); failed != 0 {
		t.Fatalf("delete failed %d: %v", failed, errs)
	}

	var after int64
	if err := env.store.Read().QueryRow(
		`SELECT desired_revision FROM nodes WHERE id = 1`).Scan(&after); err != nil {
		t.Fatalf("read revision: %v", err)
	}
	if after <= before {
		t.Errorf("desired_revision %d did not advance past %d: the node still serves a deleted subject",
			after, before)
	}
}

// Export selected `disabled`, `frozen` and `updated_at`, so it returned 500 on
// every call. Disabled and Frozen are derived from `enabled` and `frozen_at`.
func TestExportReturnsRealRows(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	_, id := seedTenant(t, env, "alice", svcID, adminToken)

	res := env.get(t, "/api/v1/subjects/export", adminToken)
	if res.Code != http.StatusOK {
		t.Fatalf("export = %d, want 200: %s", res.Code, res.Body)
	}
	body := res.Body.String()
	if !strings.Contains(body, "alice-customer") {
		t.Errorf("export omitted the seeded subject:\n%s", body)
	}
	if !strings.HasPrefix(body, "ID,Name,Note,Disabled,Frozen,") {
		t.Errorf("unexpected CSV header:\n%s", body)
	}
	if strings.Contains(body, "UpdatedAt") {
		t.Error("export still advertises UpdatedAt, a column that does not exist")
	}

	// Disabled is derived, so it must track `enabled` rather than being a
	// constant that happens to look right for a freshly created subject.
	if err := execSQL(t, env, `UPDATE subjects SET enabled = 0 WHERE id = ?`, id); err != nil {
		t.Fatalf("disable: %v", err)
	}
	res = env.get(t, "/api/v1/subjects/export", adminToken)
	line := subjectLine(t, res.Body.String(), "alice-customer")
	if !strings.Contains(line, ",true,") {
		t.Errorf("Disabled did not follow enabled=0: %q", line)
	}
}

func subjectLine(t *testing.T, csv, name string) string {
	t.Helper()
	for _, l := range strings.Split(csv, "\n") {
		if strings.Contains(l, name) {
			return l
		}
	}
	t.Fatalf("no row for %q in:\n%s", name, csv)
	return ""
}

// Import wrote `disabled`, `frozen` and `updated_at` and so created nothing. It
// also had no permission check; repairing the columns without one would have
// handed subject creation to any authenticated caller.
func TestImportCreatesSubjects(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	seedTenant(t, env, "alice", svcID, adminToken)

	csv := "Name,Note,Disabled,Frozen\nimported-one,from csv,false,false\nimported-two,,true,false\n"
	payload, err := json.Marshal(map[string]string{"csv": csv})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	res := env.post(t, "/api/v1/subjects/import", string(payload), adminToken)
	if res.Code != http.StatusOK {
		t.Fatalf("import = %d: %s", res.Code, res.Body)
	}
	var out struct {
		Imported int      `json:"imported"`
		Failed   int      `json:"failed"`
		Errors   []string `json:"errors"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Imported != 2 || out.Failed != 0 {
		t.Fatalf("imported=%d failed=%d errors=%v, want 2/0", out.Imported, out.Failed, out.Errors)
	}

	var enabled bool
	if err := env.store.Read().QueryRow(
		`SELECT enabled FROM subjects WHERE name = 'imported-two'`).Scan(&enabled); err != nil {
		t.Fatalf("read imported-two: %v", err)
	}
	if enabled {
		t.Error("Disabled=true in the CSV did not translate to enabled=0")
	}
}

func TestImportRequiresSubjectWrite(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	readerToken, _ := seedTenantWithRole(t, env, "auditor", "readonly", svcID, adminToken)

	payload := `{"csv":"Name\nsneaky\n"}`
	res := env.post(t, "/api/v1/subjects/import", payload, readerToken)
	if res.Code != http.StatusForbidden {
		t.Errorf("import as readonly = %d, want %d", res.Code, http.StatusForbidden)
	}
	var n int
	if err := env.store.Read().QueryRow(
		`SELECT COUNT(*) FROM subjects WHERE name = 'sneaky'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Error("a readonly actor created a subject through import")
	}
}

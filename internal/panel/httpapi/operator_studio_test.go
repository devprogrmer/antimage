package httpapi

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/shared/secrets"
)

func TestSettingsRoundTrip(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "password-password", "super_admin")
	token := env.login(t, "root", "password-password")

	res := env.put(t, "/api/v1/settings",
		`{"settings":{"public_url":"https://panel.example.com","remark_template":"{name} - {node}"}}`,
		token)
	if res.Code != http.StatusNoContent {
		t.Fatalf("put settings: %d %s", res.Code, res.Body.String())
	}
	got := env.get(t, "/api/v1/settings", token)
	if got.Code != http.StatusOK {
		t.Fatalf("get settings: %d %s", got.Code, got.Body.String())
	}
	var body struct {
		Settings map[string]string `json:"settings"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Settings["public_url"] != "https://panel.example.com" {
		t.Errorf("public_url = %q", body.Settings["public_url"])
	}
}

func TestSettingsRejectUnknownKey(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "password-password", "super_admin")
	token := env.login(t, "root", "password-password")
	res := env.put(t, "/api/v1/settings", `{"settings":{"master_key":"x"}}`, token)
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d %s", res.Code, res.Body.String())
	}
}

func TestSettingsWriteRequiresPermission(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "reader", "password-password", "readonly")
	token := env.login(t, "reader", "password-password")
	res := env.put(t, "/api/v1/settings",
		`{"settings":{"public_url":"https://x.example"}}`, token)
	if res.Code != http.StatusForbidden {
		t.Fatalf("readonly wrote settings: %d %s", res.Code, res.Body.String())
	}
}

func TestHostCRUD(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "password-password", "super_admin")
	token := env.login(t, "root", "password-password")
	ctx := context.Background()
	var serviceID int64
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO nodes (name, address, status, created_at) VALUES (?,?,?,?)`,
			"de-1", "203.0.113.10", "online", time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, _ := res.LastInsertId()
		res, err = tx.Exec(`INSERT INTO services (node_id, adapter_kind, params, enabled, created_at) VALUES (?,?,?,?,?)`,
			nodeID, "xray", `{"protocol":"vless","port":443,"security":"reality"}`, 1, time.Now().Unix())
		if err != nil {
			return err
		}
		serviceID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	create := env.post(t, "/api/v1/hosts", `{
		"service_id": `+itoa64(serviceID)+`,
		"remark": "DE CDN",
		"address": "cdn.example.com",
		"sni": "www.microsoft.com",
		"security": "reality",
		"public_key": "abc",
		"short_id": "1a2b"
	}`, token)
	if create.Code != http.StatusCreated {
		t.Fatalf("create host: %d %s", create.Code, create.Body.String())
	}
	list := env.get(t, "/api/v1/hosts", token)
	if list.Code != http.StatusOK {
		t.Fatalf("list hosts: %d %s", list.Code, list.Body.String())
	}
	if !strings.Contains(list.Body.String(), "cdn.example.com") {
		t.Errorf("host missing from list: %s", list.Body.String())
	}
}

func TestSubscribeUsesHostAndReality(t *testing.T) {
	key := make([]byte, 32)
	box, err := secrets.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	env := newTestEnv(t, func(d *Deps) { d.Box = box })
	ctx := context.Background()

	var subjectID, serviceID int64
	err = env.store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO subjects (name, enabled, subscription_token, created_at) VALUES (?,?,?,?)`,
			"alice", 1, "tok-reality", time.Now().Unix())
		if err != nil {
			return err
		}
		subjectID, _ = res.LastInsertId()
		res, err = tx.Exec(`INSERT INTO nodes (name, address, status, created_at) VALUES (?,?,?,?)`,
			"edge", "10.0.0.1", "online", time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, _ := res.LastInsertId()
		res, err = tx.Exec(`INSERT INTO services (node_id, adapter_kind, params, enabled, created_at) VALUES (?,?,?,?,?)`,
			nodeID, "xray", `{"protocol":"vless","port":443,"network":"tcp","security":"reality","dest":"www.microsoft.com:443","server_names":["www.microsoft.com"],"private_key":"priv","short_ids":["abcd"]}`, 1, time.Now().Unix())
		if err != nil {
			return err
		}
		serviceID, _ = res.LastInsertId()
		if _, err := tx.Exec(`INSERT INTO subject_services (subject_id, service_id) VALUES (?,?)`, subjectID, serviceID); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO subscription_hosts (service_id, remark, address, sni, security, public_key, short_id, enabled, priority, created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`, serviceID, "Alice DE", "cdn.example.com", "www.microsoft.com", "reality", "pbk123", "abcd", 1, 0, time.Now().Unix()); err != nil {
			return err
		}
		enc, _ := box.Seal([]byte("11111111-2222-3333-4444-555555555555"))
		_, err = tx.Exec(`INSERT INTO subject_credentials (subject_id, kind, value_enc, created_at) VALUES (?,?,?,?)`,
			subjectID, "uuid", enc, time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := env.get(t, "/api/v1/subscribe/tok-reality", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("subscribe: %d %s", rec.Code, rec.Body.String())
	}
	decoded, err := base64.StdEncoding.DecodeString(rec.Body.String())
	if err != nil {
		t.Fatal(err)
	}
	uri := string(decoded)
	for _, want := range []string{"vless://", "cdn.example.com", "security=reality", "pbk=pbk123", "Alice DE"} {
		if !strings.Contains(uri, want) {
			t.Errorf("missing %q in %s", want, uri)
		}
	}
	if strings.Contains(uri, "10.0.0.1") {
		t.Errorf("raw node address leaked despite host overlay: %s", uri)
	}
}

func TestCreateSubjectWithQuotaAndServices(t *testing.T) {
	key := make([]byte, 32)
	box, err := secrets.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	env := newTestEnv(t, func(d *Deps) { d.Box = box })
	env.seedAdmin(t, "root", "password-password", "super_admin")
	token := env.login(t, "root", "password-password")
	ctx := context.Background()
	var serviceID int64
	err = env.store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO nodes (name, address, status, created_at) VALUES (?,?,?,?)`,
			"n1", "1.1.1.1", "online", time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, _ := res.LastInsertId()
		res, err = tx.Exec(`INSERT INTO services (node_id, adapter_kind, params, enabled, created_at) VALUES (?,?,?,?,?)`,
			nodeID, "xray", `{"protocol":"vless","port":443}`, 1, time.Now().Unix())
		if err != nil {
			return err
		}
		serviceID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	res := env.post(t, "/api/v1/subjects", `{
		"name":"bob",
		"expire_days":30,
		"quota_bytes":1073741824,
		"service_ids":[`+itoa64(serviceID)+`]
	}`, token)
	if res.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", res.Code, res.Body.String())
	}
	var created struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(res.Body.Bytes(), &created)
	got := env.get(t, "/api/v1/subjects/"+itoa64(created.ID), token)
	if got.Code != http.StatusOK {
		t.Fatalf("get: %d %s", got.Code, got.Body.String())
	}
	if !strings.Contains(got.Body.String(), `"quota_bytes":1073741824`) {
		t.Errorf("quota missing: %s", got.Body.String())
	}
	sub := env.get(t, "/api/v1/subjects/"+itoa64(created.ID)+"/subscription", token)
	if sub.Code != http.StatusOK {
		t.Fatalf("subscription: %d %s", sub.Code, sub.Body.String())
	}
	if !strings.Contains(sub.Body.String(), "/api/v1/subscribe/") {
		t.Errorf("url missing: %s", sub.Body.String())
	}
}

func TestAdminsRequireManage(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "ops", "password-password", "admin")
	token := env.login(t, "ops", "password-password")
	res := env.get(t, "/api/v1/admins", token)
	if res.Code != http.StatusForbidden {
		t.Fatalf("admin listed admins: %d %s", res.Code, res.Body.String())
	}
}

func TestCreateAdminAsSuper(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "password-password", "super_admin")
	token := env.login(t, "root", "password-password")
	res := env.post(t, "/api/v1/admins",
		`{"username":"newops","password":"long-pass-word","role":"admin"}`, token)
	if res.Code != http.StatusCreated {
		t.Fatalf("create admin: %d %s", res.Code, res.Body.String())
	}
}

func TestSubjectSubscriptionIsTenantScoped(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	aliceToken, _ := seedTenant(t, env, "alice", svcID, adminToken)
	_, bobSubject := seedTenant(t, env, "bob", svcID, adminToken)
	res := env.get(t, "/api/v1/subjects/"+itoa64(bobSubject)+"/subscription", aliceToken)
	if res.Code != http.StatusNotFound {
		t.Errorf("foreign subscription = %d %s, want 404", res.Code, res.Body.String())
	}
}

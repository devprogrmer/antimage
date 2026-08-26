package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/amyrm/antimage/internal/panel/nodes"
)

// An outbound's params carry the credentials for an UPSTREAM provider: a
// WireGuard outbound has private_key, socks and http have password. Those are
// the panel operator's own secrets with a third party, not a subscriber's.
//
// They were returned verbatim by GET /nodes/{id}/outbounds, which is gated on
// outbound:read -- a permission the reseller role holds (rbac/perm.go). A
// tenant scoped to a node could therefore read the platform's upstream key by
// listing the outbounds they are allowed to see.
//
// The role comment reasoned about the write side ("Redirecting traffic stays a
// platform decision either way") and treated read as harmless visibility. It is
// not: the read returns the key.
//
// Note what does NOT reach it: a readonly account holds outbound:read but has
// no node scope, and TargetNode treats a non-super actor's node list as an
// exhaustive allow-list, so it is refused before the handler runs. The scope
// layer contains that case; it does not contain a tenant who legitimately has
// the node.

const wgOutboundKey = "aFakePrivateKeyForTestingOnly0000000000000o="

// seedWireGuardOutbound creates an outbound whose params contain a secret.
func seedWireGuardOutbound(t *testing.T, env *testEnv, token string) {
	t.Helper()
	egressNode(t, env, 1, `["xray"]`)
	body := `{"tag":"upstream","kind":"wireguard","params":{` +
		`"private_key":"` + wgOutboundKey + `",` +
		`"peer_public_key":"aFakePublicKeyForTestingOnly00000000000o=",` +
		`"endpoint":"198.51.100.1:51820"}}`
	createOutbound(t, env, token, body)
}

// THE disclosure: a tenant who legitimately holds the node reads the
// platform's upstream credential.
func TestOutboundSecretsAreNotReturnedToTenants(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	seedWireGuardOutbound(t, env, adminToken)

	tenantToken, _ := seedTenant(t, env, "alice", svcID, adminToken)
	grantNodeScope(t, env, "alice", 1)

	res := env.get(t, "/api/v1/nodes/1/outbounds", tenantToken)
	if res.Code != http.StatusOK {
		t.Fatalf("tenant listing outbounds = %d, want 200; this test asserts "+
			"what a PERMITTED reader sees, so a denial makes it vacuous", res.Code)
	}
	if strings.Contains(res.Body.String(), wgOutboundKey) {
		t.Errorf("a reseller read the platform's upstream private key; params "+
			"are returned verbatim and nothing redacts the credential "+
			"fields:\n%s", res.Body)
	}
}

// The scope layer, recorded so a later widening of the readonly role's scope
// does not silently reopen the same hole through a different door.
func TestReadonlyWithoutNodeScopeCannotListOutboundsAtAll(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	seedWireGuardOutbound(t, env, adminToken)

	env.seedAdmin(t, "readonly-bob", "pw", "readonly")
	readerToken := env.login(t, "readonly-bob", "pw")

	if res := env.get(t, "/api/v1/nodes/1/outbounds", readerToken); res.Code != http.StatusForbidden {
		t.Errorf("readonly listing a node it has no scope for = %d, want 403", res.Code)
	}
}

// Redaction must not become deletion. The operator still has to see that a
// credential IS set, and the non-secret fields have to survive so the UI can
// show what the outbound actually is.
func TestOutboundRedactionKeepsTheRestOfTheParams(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	seedWireGuardOutbound(t, env, adminToken)

	res := env.get(t, "/api/v1/nodes/1/outbounds", adminToken)
	if res.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", res.Code, res.Body)
	}
	var out struct {
		Outbounds []struct {
			Tag    string                 `json:"tag"`
			Params map[string]interface{} `json:"params"`
		} `json:"outbounds"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Outbounds) != 1 {
		t.Fatalf("listed %d outbounds, want 1", len(out.Outbounds))
	}
	p := out.Outbounds[0].Params
	if p["endpoint"] != "198.51.100.1:51820" {
		t.Errorf("endpoint = %v, want it preserved; redaction removed a "+
			"non-secret field the operator needs to see", p["endpoint"])
	}
	if p["peer_public_key"] == nil {
		t.Error("peer_public_key was removed; a PUBLIC key is not a secret and " +
			"the operator needs it to verify the peer")
	}
	if _, present := p["private_key"]; !present {
		t.Error("private_key is absent entirely, so the operator cannot tell a " +
			"configured credential from a missing one; it should be present " +
			"and redacted")
	}
}

// Redaction must not destroy what it protects.
//
// The editor reads an outbound, sees "__redacted__" in place of the key, and
// submits the whole document back when the operator changes something else.
// Taken literally that overwrites a working upstream credential with the
// sentinel: the outbound stops connecting and the real key is gone.
func TestEditingAnOutboundKeepsTheUnchangedSecret(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	seedWireGuardOutbound(t, env, adminToken)

	// What a client would send back after reading: the sentinel, plus a real
	// edit to another field.
	body := `{"tag":"upstream","kind":"wireguard","params":{` +
		`"private_key":"` + RedactedValue + `",` +
		`"peer_public_key":"aFakePublicKeyForTestingOnly00000000000o=",` +
		`"endpoint":"203.0.113.9:51820"}}`
	if res := env.put(t, "/api/v1/nodes/1/outbounds/1", body, adminToken); res.Code != http.StatusNoContent {
		t.Fatalf("update = %d: %s", res.Code, res.Body)
	}

	var stored string
	if err := env.store.Read().QueryRow(
		`SELECT params FROM outbounds WHERE id = 1`).Scan(&stored); err != nil {
		t.Fatalf("read stored params: %v", err)
	}
	if !strings.Contains(stored, wgOutboundKey) {
		t.Errorf("the stored private key was overwritten by the round trip; "+
			"stored params are now %s", stored)
	}
	if strings.Contains(stored, RedactedValue) {
		t.Errorf("the literal sentinel was written into storage: %s", stored)
	}
	// And the edit the operator actually made took effect.
	if !strings.Contains(stored, "203.0.113.9") {
		t.Errorf("the endpoint edit was lost: %s", stored)
	}
}

// A deliberate change of the credential must still go through, or the operator
// can never rotate an upstream key.
func TestASecretCanStillBeChanged(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	seedWireGuardOutbound(t, env, adminToken)

	const rotated = "aDifferentFakePrivateKeyForTests000000000o="
	body := `{"tag":"upstream","kind":"wireguard","params":{` +
		`"private_key":"` + rotated + `",` +
		`"peer_public_key":"aFakePublicKeyForTestingOnly00000000000o=",` +
		`"endpoint":"198.51.100.1:51820"}}`
	if res := env.put(t, "/api/v1/nodes/1/outbounds/1", body, adminToken); res.Code != http.StatusNoContent {
		t.Fatalf("update = %d: %s", res.Code, res.Body)
	}

	var stored string
	if err := env.store.Read().QueryRow(
		`SELECT params FROM outbounds WHERE id = 1`).Scan(&stored); err != nil {
		t.Fatalf("read stored params: %v", err)
	}
	if !strings.Contains(stored, rotated) {
		t.Errorf("the rotated key was not stored, so an operator cannot change "+
			"an upstream credential: %s", stored)
	}
	if strings.Contains(stored, wgOutboundKey) {
		t.Errorf("the old key survived a deliberate rotation: %s", stored)
	}
}

// Redaction is a presentation concern. The node still needs the real key, so
// the desired document must carry it unredacted. A redaction that reached the
// document would be worse than the disclosure: every outbound would silently
// stop connecting.
func TestTheRealSecretStillReachesTheDesiredDocument(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	seedWireGuardOutbound(t, env, adminToken)

	// Built through the real snapshot path, inside a transaction the way
	// CommitNodeChange does it, so this reads what the node would be sent.
	var snap *nodes.Snapshot
	if err := env.store.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		snap, err = nodes.BuildDesiredSnapshot(context.Background(), tx, 1, nodes.WithUnsealer(env.deps.Box))
		return err
	}); err != nil {
		t.Fatalf("build desired snapshot: %v", err)
	}
	if !strings.Contains(string(snap.Bytes), wgOutboundKey) {
		t.Errorf("the desired document does not carry the real private key, so "+
			"the node cannot bring the outbound up:\n%s", snap.Bytes)
	}
	if strings.Contains(string(snap.Bytes), RedactedValue) {
		t.Errorf("the redaction sentinel reached the node:\n%s", snap.Bytes)
	}
}

// Fail closed when the schema cannot be read.
//
// "We could not tell which fields were secret" is not a reason to assume none
// of them were. A node whose adapter kind does not resolve -- an agent the
// panel was not built with, a malformed report -- would otherwise disclose
// every credential it holds, which is a strictly worse version of the bug this
// file exists to fix.
func TestParamsAreWithheldWhenTheSchemaIsUnknown(t *testing.T) {
	// No schema at all: whatever the params say, nothing may come back.
	got := redactParams(nil, []byte(`{"private_key":"`+wgOutboundKey+`","port":443}`))
	if strings.Contains(string(got), wgOutboundKey) {
		t.Errorf("params were passed through for an unknown schema, disclosing "+
			"the credential: %s", got)
	}
	if string(got) != `{}` {
		t.Errorf("redactParams with no schema = %s, want {}; anything else is a "+
			"partial disclosure decided by a schema nobody could read", got)
	}

	// A schema that is not valid JSON is the same situation.
	got = redactParams([]byte(`{not json`), []byte(`{"password":"hunter2"}`))
	if strings.Contains(string(got), "hunter2") {
		t.Errorf("a malformed schema let the credential through: %s", got)
	}
}

// A schema that declares no secrets passes its params through untouched: the
// fail-closed rule is about not being ABLE to tell, not about being told there
// is nothing to hide.
func TestParamsPassThroughWhenTheSchemaDeclaresNoSecrets(t *testing.T) {
	schema := []byte(`{"type":"object","properties":{"port":{"type":"integer"}}}`)
	got := redactParams(schema, []byte(`{"port":443}`))
	if !strings.Contains(string(got), "443") {
		t.Errorf("redactParams = %s, want the port preserved", got)
	}
}

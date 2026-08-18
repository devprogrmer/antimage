package xray

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func tlsInbound() Inbound {
	return Inbound{
		Protocol: VLESS, Port: 443, Listen: "0.0.0.0",
		Network: TCP, Security: SecurityTLS,
		CertFile: "/etc/ssl/x.crt", KeyFile: "/etc/ssl/x.key", SNI: "example.com",
	}
}

func users(n int) []User {
	out := make([]User, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, User{
			SubjectID:  int64(i),
			Email:      "subject-" + string(rune('a'+i-1)),
			Credential: "11111111-2222-3333-4444-55555555555" + string(rune('0'+i)),
		})
	}
	return out
}

// Determinism is load-bearing: the adapter detects drift by comparing a
// checksum of generated config against what is on disk, so output that varied
// between runs would present as permanent, uncorrectable drift and the node
// would rewrite its config on every reconcile forever.
func TestGenerateIsDeterministic(t *testing.T) {
	in := tlsInbound()
	first, err := in.Generate(users(5))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for i := 0; i < 20; i++ {
		again, err := in.Generate(users(5))
		if err != nil {
			t.Fatalf("Generate %d: %v", i, err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("run %d differed:\n%s\n---\n%s", i, first, again)
		}
	}
}

// The same users supplied in a different order must produce identical bytes,
// because the panel's document ordering is by subject id but nothing forbids a
// caller from reordering.
func TestGenerateIsOrderIndependent(t *testing.T) {
	in := tlsInbound()
	ordered := users(4)
	shuffled := []User{ordered[2], ordered[0], ordered[3], ordered[1]}

	a, err := in.Generate(ordered)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	b, err := in.Generate(shuffled)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("output depends on input order:\n%s\n---\n%s", a, b)
	}
}

// Two subjects sharing a tag would make SP3's per-user accounting ambiguous,
// and Xray's behaviour on duplicates is undefined. Refuse to generate.
func TestGenerateRejectsDuplicateUserTags(t *testing.T) {
	in := tlsInbound()
	dup := []User{
		{SubjectID: 1, Email: "same", Credential: "11111111-2222-3333-4444-555555555551"},
		{SubjectID: 2, Email: "same", Credential: "11111111-2222-3333-4444-555555555552"},
	}
	_, err := in.Generate(dup)
	if err == nil {
		t.Fatal("generated a config with two users sharing a tag")
	}
	if !errors.Is(err, ErrInvalidInbound) {
		t.Errorf("err = %v, want ErrInvalidInbound", err)
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("err = %v, want it to name the duplicate", err)
	}
}

// A subject with no credential must not silently become a client entry with an
// empty id, which Xray would accept and which would authenticate nobody.
func TestGenerateRejectsAMissingCredential(t *testing.T) {
	in := tlsInbound()
	_, err := in.Generate([]User{{SubjectID: 9, Email: "ghost"}})
	if err == nil {
		t.Fatal("generated a client entry with no credential")
	}
	if !strings.Contains(err.Error(), "subject 9") {
		t.Errorf("err = %v, want it to name the subject", err)
	}
}

// The generated shape must actually be the shape Xray expects, checked
// structurally rather than by substring so a rename is caught.
func TestGenerateProducesTheExpectedXrayShape(t *testing.T) {
	in := tlsInbound()
	raw, err := in.Generate(users(2))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("generated config is not valid JSON: %v", err)
	}
	for _, key := range []string{"tag", "listen", "port", "protocol", "settings", "streamSettings"} {
		if _, ok := got[key]; !ok {
			t.Errorf("generated inbound is missing %q", key)
		}
	}
	if got["protocol"] != "vless" {
		t.Errorf("protocol = %v", got["protocol"])
	}
	if got["port"].(float64) != 443 {
		t.Errorf("port = %v", got["port"])
	}

	settings := got["settings"].(map[string]any)
	if settings["decryption"] != "none" {
		t.Errorf("vless requires decryption=none, got %v", settings["decryption"])
	}
	clients := settings["clients"].([]any)
	if len(clients) != 2 {
		t.Fatalf("clients = %d, want 2", len(clients))
	}
	first := clients[0].(map[string]any)
	if first["id"] == nil || first["email"] == nil {
		t.Errorf("client entry missing id or email: %v", first)
	}
	// VLESS over raw TCP with TLS gets XTLS vision; anything else must not.
	if first["flow"] != "xtls-rprx-vision" {
		t.Errorf("flow = %v, want xtls-rprx-vision for vless/tcp/tls", first["flow"])
	}

	stream := got["streamSettings"].(map[string]any)
	if stream["security"] != "tls" {
		t.Errorf("security = %v", stream["security"])
	}
	if _, ok := stream["tlsSettings"]; !ok {
		t.Error("tls inbound has no tlsSettings")
	}
}

// Trojan uses password, not id. Getting this wrong produces a config that
// starts cleanly and authenticates nobody.
func TestTrojanUsesPasswordNotID(t *testing.T) {
	in := tlsInbound()
	in.Protocol = Trojan
	raw, err := in.Generate([]User{{SubjectID: 1, Email: "a", Credential: "a-long-enough-password"}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	client := got["settings"].(map[string]any)["clients"].([]any)[0].(map[string]any)

	if client["password"] != "a-long-enough-password" {
		t.Errorf("trojan client has no password: %v", client)
	}
	if _, hasID := client["id"]; hasID {
		t.Errorf("trojan client carries an id field: %v", client)
	}
	if in.CredentialKind() != "password" {
		t.Errorf("CredentialKind = %q, want password", in.CredentialKind())
	}
}

func TestValidateRejectsUnusableInbounds(t *testing.T) {
	for name, in := range map[string]Inbound{
		"unknown protocol": {Protocol: "wireguard", Port: 443},
		"port zero":        {Protocol: VLESS, Port: 0},
		"port too high":    {Protocol: VLESS, Port: 70000},
		"listen not an ip": {Protocol: VLESS, Port: 443, Listen: "example.com"},
		"unknown network":  {Protocol: VLESS, Port: 443, Network: "quic"},
		"tls without cert": {Protocol: VLESS, Port: 443, Security: SecurityTLS},
		"trojan plaintext": {Protocol: Trojan, Port: 443, Security: SecurityNone},
		"ws without path":  {Protocol: VLESS, Port: 443, Network: WS},
	} {
		t.Run(name, func(t *testing.T) {
			if err := in.Validate(); err == nil {
				t.Fatalf("accepted an unusable inbound: %+v", in)
			} else if !errors.Is(err, ErrInvalidInbound) {
				t.Errorf("err = %v, want ErrInvalidInbound", err)
			}
		})
	}
}

// The node validates independently of the panel. A hand-edited database must
// not be able to restart Xray into a crash loop.
func TestParseInboundRejectsUnknownFields(t *testing.T) {
	_, err := ParseInbound(json.RawMessage(`{"protocol":"vless","port":443,"backdoor":true}`))
	if err == nil {
		t.Fatal("accepted params with an unknown field")
	}
	if !errors.Is(err, ErrInvalidInbound) {
		t.Errorf("err = %v, want ErrInvalidInbound", err)
	}
}

func TestParseInboundAppliesDefaults(t *testing.T) {
	in, err := ParseInbound(json.RawMessage(`{"protocol":"vless","port":8080}`))
	if err != nil {
		t.Fatalf("ParseInbound: %v", err)
	}
	if in.Listen != "0.0.0.0" || in.Network != TCP || in.Security != SecurityNone {
		t.Errorf("defaults not applied: %+v", in)
	}
}

// The tag identifies the inbound inside Xray and inside SP3's accounting. It
// must be stable for a given port and distinct across ports.
func TestTagIsStableAndPortDistinct(t *testing.T) {
	a := Inbound{Protocol: VLESS, Port: 443}
	b := Inbound{Protocol: VMess, Port: 443, Network: WS, Path: "/x"}
	c := Inbound{Protocol: VLESS, Port: 8443}

	if a.Tag() != b.Tag() {
		t.Errorf("tag depends on more than the port: %q vs %q", a.Tag(), b.Tag())
	}
	if a.Tag() == c.Tag() {
		t.Errorf("two ports share the tag %q", a.Tag())
	}
}

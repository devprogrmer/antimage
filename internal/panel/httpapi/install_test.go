package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/shared/secrets"
)

// sourceScript is the authoritative copy. The embedded one is a duplicate
// that only exists because go:embed cannot reach outside its package.
const sourceScript = "../../../scripts/install.sh"

func TestInstallScriptIsServedUnauthenticated(t *testing.T) {
	env := newTestEnv(t)
	res := env.get(t, "/install.sh", "")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	body := res.Body.String()
	if !strings.Contains(body, "set -euo pipefail") {
		t.Error("served script is not the hardened bootstrap script")
	}
	if !strings.Contains(body, "sha256sum -c") {
		t.Error("served script does not verify the binary checksum")
	}
}

func TestInstallScriptContainsNoSecrets(t *testing.T) {
	env := newTestEnv(t)
	body := env.get(t, "/install.sh", "").Body.String()
	for _, forbidden := range []string{"master.key", "password", "BEGIN EC PRIVATE KEY"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("install.sh leaks %q", forbidden)
		}
	}
}

// TestEmbeddedScriptMatchesSource is the drift guard. `make sync-install`
// copies scripts/install.sh over the embedded copy, but a Makefile rule only
// helps someone who builds through make; edit either file directly and the
// panel would serve a stale bootstrap script indefinitely. Byte equality is
// checked, not a substring, because any divergence is a divergence.
func TestEmbeddedScriptMatchesSource(t *testing.T) {
	want, err := os.ReadFile(sourceScript)
	if err != nil {
		t.Fatalf("read %s: %v", sourceScript, err)
	}
	if !bytes.Equal(want, installScript) {
		t.Errorf("internal/panel/httpapi/install.sh has drifted from %s "+
			"(%d bytes embedded, %d on disk); run `make sync-install`",
			sourceScript, len(installScript), len(want))
	}
}

// TestEmbeddedScriptHasNoCarriageReturns protects the node, not the panel.
// go:embed captures whatever bytes are on disk at build time, so a CRLF
// checkout would ship a script whose shebang line ends in \r and every node
// running it would fail with "bad interpreter: /bin/bash^M". The substring
// assertions above cannot see that; only a byte-level check can.
func TestEmbeddedScriptHasNoCarriageReturns(t *testing.T) {
	if i := bytes.IndexByte(installScript, '\r'); i >= 0 {
		t.Errorf("embedded install.sh contains a carriage return at byte %d; "+
			"it must be LF-only or nodes cannot execute it "+
			"(see .gitattributes)", i)
	}
	onDisk, err := os.ReadFile(sourceScript)
	if err != nil {
		t.Fatalf("read %s: %v", sourceScript, err)
	}
	if i := bytes.IndexByte(onDisk, '\r'); i >= 0 {
		t.Errorf("%s contains a carriage return at byte %d", sourceScript, i)
	}
}

// TestInstallScriptServedBytesMatchEmbedded pins the wire format: the handler
// must not transform the script on its way out.
func TestInstallScriptServedBytes(t *testing.T) {
	env := newTestEnv(t)
	res := env.get(t, "/install.sh", "")
	if got := res.Body.Bytes(); !bytes.Equal(got, installScript) {
		t.Errorf("served body differs from the embedded script (%d vs %d bytes)",
			len(got), len(installScript))
	}
	if got := res.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// TestCAFingerprintUnavailableWithoutCA pins the fail-closed branch. The
// default test env leaves Deps.CA nil, exactly as it is before main.go loads
// one, and the endpoint must say so rather than panic or serve an empty
// fingerprint that a node would then pin against.
func TestCAFingerprintUnavailableWithoutCA(t *testing.T) {
	env := newTestEnv(t)
	res := env.get(t, "/api/v1/ca-fingerprint", "")
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", res.Code, res.Body)
	}
	if !strings.Contains(res.Body.String(), "unavailable") {
		t.Errorf("body = %s, want the unavailable error code", res.Body)
	}
}

func TestCAFingerprintServesTheRealFingerprint(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}

	var ca *nodes.CA
	env := newTestEnv(t, func(d *Deps) {
		ca, err = nodes.LoadOrCreateCA(context.Background(), d.Store, box)
		if err != nil {
			t.Fatalf("LoadOrCreateCA: %v", err)
		}
		d.CA = ca
	})

	res := env.get(t, "/api/v1/ca-fingerprint", "")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body)
	}
	want := ca.FingerprintSHA256()
	if got := res.Body.String(); got != want {
		t.Errorf("fingerprint = %q, want %q", got, want)
	}
	// install.sh pipes this straight into node.yaml, so a trailing newline or
	// a JSON envelope would be pinned verbatim and never match.
	if strings.ContainsAny(res.Body.String(), "\r\n\"{ ") {
		t.Errorf("fingerprint body %q must be the bare hex digest", res.Body)
	}
}

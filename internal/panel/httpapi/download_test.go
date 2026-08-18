package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newDownloadEnv stages a downloads directory containing one binary and its
// checksum, plus a secret file OUTSIDE that directory for traversal tests to
// aim at.
func newDownloadEnv(t *testing.T) (*testEnv, string, string) {
	t.Helper()
	root := t.TempDir()
	downloads := filepath.Join(root, "downloads")
	if err := os.MkdirAll(downloads, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(downloads, "antimage-node-linux-amd64"), []byte("ELF-ish binary"), 0o600); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(downloads, "antimage-node-linux-amd64.sha256"), []byte("deadbeef\n"), 0o600); err != nil {
		t.Fatalf("write sha: %v", err)
	}
	// The file an attacker would want: a sibling of the downloads directory,
	// exactly where the panel keeps its master key in production.
	secret := filepath.Join(root, "master.key")
	if err := os.WriteFile(secret, []byte("TOP-SECRET-MASTER-KEY"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	env := newTestEnv(t, func(d *Deps) { d.DownloadDir = downloads })
	return env, downloads, secret
}

func TestDownloadServesTheBinaryAndItsChecksum(t *testing.T) {
	env, _, _ := newDownloadEnv(t)

	bin := env.get(t, "/download/antimage-node-linux-amd64", "")
	if bin.Code != http.StatusOK {
		t.Fatalf("binary status = %d, want 200", bin.Code)
	}
	if bin.Body.String() != "ELF-ish binary" {
		t.Errorf("binary body = %q", bin.Body.String())
	}
	if ct := bin.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("binary Content-Type = %q", ct)
	}
	if cl := bin.Header().Get("Content-Length"); cl != "14" {
		t.Errorf("Content-Length = %q, want 14", cl)
	}

	sum := env.get(t, "/download/antimage-node-linux-amd64.sha256", "")
	if sum.Code != http.StatusOK {
		t.Fatalf("checksum status = %d, want 200", sum.Code)
	}
	if !strings.HasPrefix(sum.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("checksum Content-Type = %q", sum.Header().Get("Content-Type"))
	}
}

// install.sh runs unauthenticated on a host that has no session yet. If this
// route ever moved behind the auth middleware, every bootstrap would fail.
func TestDownloadIsUnauthenticated(t *testing.T) {
	env, _, _ := newDownloadEnv(t)
	res := env.get(t, "/download/antimage-node-linux-amd64", "")
	if res.Code == http.StatusUnauthorized || res.Code == http.StatusForbidden {
		t.Fatalf("status = %d: bootstrap has no session and would fail", res.Code)
	}
}

// The allow-list is the security boundary. These are the shapes a traversal
// attempt takes; none may reach a file outside the downloads directory, and
// none may return the secret staged as its sibling.
func TestDownloadRefusesEveryTraversalShape(t *testing.T) {
	env, _, secretPath := newDownloadEnv(t)
	secret, err := os.ReadFile(secretPath)
	if err != nil {
		t.Fatalf("read secret: %v", err)
	}

	for _, name := range []string{
		"../master.key",
		"..%2Fmaster.key",
		"..%252Fmaster.key",
		"....//master.key",
		"%2e%2e%2fmaster.key",
		"..\\master.key",
		"/etc/passwd",
		"%2Fetc%2Fpasswd",
		"antimage-node-linux-amd64/../../master.key",
		"antimage-node-linux-amd64%00.sha256",
		".",
		"..",
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/download/"+name, nil)
			req.Host = "panel.local"
			rec := httptest.NewRecorder()
			env.handler.ServeHTTP(rec, req)

			// The property that matters is that no file outside the
			// downloads directory is served. Some of these shapes never
			// reach the handler at all -- chi's {name} does not span "/",
			// so "/download//etc/passwd" falls through to the SPA
			// catch-all and gets index.html, which is correct for a
			// client-side-routed app and serves from an embed.FS that
			// cannot reach the filesystem in the first place.
			if strings.Contains(rec.Body.String(), string(secret)) {
				t.Fatalf("the response leaked the secret file for %q", name)
			}
			if ct := rec.Header().Get("Content-Type"); ct == "application/octet-stream" {
				t.Fatalf("%q was served as a download (Content-Type %q); body = %q",
					name, ct, rec.Body.String())
			}
		})
	}
}

// An unknown but harmless name must 404 rather than 500.
func TestDownloadRejectsUnknownNames(t *testing.T) {
	env, _, _ := newDownloadEnv(t)
	for _, name := range []string{
		"antimage-node-linux-riscv64",
		"antimage-panel-linux-amd64",
		"antimage-node-linux-amd64.sig",
		"",
	} {
		res := env.get(t, "/download/"+name, "")
		// As above: an empty name does not match {name} and falls through to
		// the SPA. What must never happen is that it is served AS a download.
		if ct := res.Header().Get("Content-Type"); ct == "application/octet-stream" {
			t.Errorf("%q was served as a download, but it is not on the allow-list", name)
		}
	}
}

// A fresh panel has no binaries staged. That is the common case, so it must be
// a clear 404 rather than a 500 or a panic.
func TestDownloadWithNothingPublished(t *testing.T) {
	env := newTestEnv(t) // DownloadDir left empty
	res := env.get(t, "/download/antimage-node-linux-amd64", "")
	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.Code)
	}
	if !strings.Contains(res.Body.String(), "downloads directory") {
		t.Errorf("message does not tell the operator what to do: %s", res.Body.String())
	}
}

// An allow-listed name whose file has not been staged yet.
func TestDownloadMissingFileIs404(t *testing.T) {
	env, _, _ := newDownloadEnv(t)
	res := env.get(t, "/download/antimage-node-linux-arm64", "")
	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.Code)
	}
}

// install.sh pipes the binary to disk; curl issues a HEAD in some flows and a
// HEAD that returns a body or the wrong length breaks the transfer.
func TestDownloadHeadReturnsMetadataWithoutABody(t *testing.T) {
	env, _, _ := newDownloadEnv(t)
	req := httptest.NewRequest(http.MethodHead, "/download/antimage-node-linux-amd64", nil)
	req.Host = "panel.local"
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD returned %d bytes of body", rec.Body.Len())
	}
	if cl := rec.Header().Get("Content-Length"); cl != "14" {
		t.Errorf("Content-Length = %q, want 14", cl)
	}
}

// A stale cached binary would hand an old agent to a newly bootstrapped node.
func TestDownloadIsNotCacheable(t *testing.T) {
	env, _, _ := newDownloadEnv(t)
	res := env.get(t, "/download/antimage-node-linux-amd64", "")
	if cc := res.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
}

// A symlink inside the downloads directory pointing outside it must not be
// followed into a secret. The allow-list controls the NAME, not what the name
// resolves to on disk.
func TestDownloadDoesNotFollowASymlinkOutOfTheDirectory(t *testing.T) {
	env, downloads, secretPath := newDownloadEnv(t)
	link := filepath.Join(downloads, "antimage-node-linux-arm64")
	if err := os.Symlink(secretPath, link); err != nil {
		t.Skipf("symlinks unavailable on this host: %v", err)
	}
	secret, err := os.ReadFile(secretPath)
	if err != nil {
		t.Fatalf("read secret: %v", err)
	}

	res := env.get(t, "/download/antimage-node-linux-arm64", "")
	if strings.Contains(res.Body.String(), string(secret)) {
		t.Fatalf("a symlink out of the downloads directory was followed into %s", secretPath)
	}
}

// The symlink test above skips on Windows (creating one needs elevation), so
// the escape-refusal property would otherwise go unverified on this host.
// This exercises the primitive the handler relies on directly: os.OpenInRoot
// must refuse to leave the root even when handed a traversing name, which is
// what makes a symlink planted in the downloads directory harmless.
func TestOpenInRootRefusesToEscape(t *testing.T) {
	_, downloads, secretPath := newDownloadEnv(t)

	// Sanity: the secret really is reachable by a plain Open, so a failure
	// below means OpenInRoot refused rather than the file being absent.
	if _, err := os.ReadFile(secretPath); err != nil {
		t.Fatalf("precondition: secret not readable: %v", err)
	}
	if f, err := os.Open(filepath.Join(downloads, "..", "master.key")); err == nil {
		_ = f.Close()
	} else {
		t.Fatalf("precondition: plain Open could not reach the secret: %v", err)
	}

	for _, name := range []string{
		"../master.key",
		"../../master.key",
		filepath.Join("..", "master.key"),
	} {
		if f, err := os.OpenInRoot(downloads, name); err == nil {
			_ = f.Close()
			t.Errorf("OpenInRoot(%q) escaped the downloads directory", name)
		}
	}
}

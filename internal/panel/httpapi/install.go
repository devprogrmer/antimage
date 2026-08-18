package httpapi

import (
	_ "embed"
	"net/http"
)

// installScript is a byte-for-byte copy of scripts/install.sh. go:embed
// cannot reach outside the package directory, so the duplicate is unavoidable;
// TestEmbeddedScriptMatchesSource fails the build the moment the two diverge,
// and `make sync-install` refreshes the copy.
//
// The embed captures whatever bytes sit on disk at build time, so the file's
// line endings matter: a CRLF checkout would ship a shebang ending in \r and
// every node would die with "bad interpreter: /bin/bash^M". The repository
// .gitattributes pins *.sh to LF and TestEmbeddedScriptHasNoCarriageReturns
// enforces it.
//
//go:embed install.sh
var installScript []byte

// handleInstallScript serves the bootstrap script unauthenticated: it
// contains no secrets, and the enrollment token is supplied as an argument by
// whoever runs it.
func (d Deps) handleInstallScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(installScript)
}

// handleCAFingerprint lets install.sh pin the panel CA on first contact.
func (d Deps) handleCAFingerprint(w http.ResponseWriter, _ *http.Request) {
	if d.CA == nil {
		WriteError(w, http.StatusServiceUnavailable, "unavailable", "CA not initialised")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(d.CA.FingerprintSHA256()))
}

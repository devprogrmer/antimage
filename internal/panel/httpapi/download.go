package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// downloadableFiles is an allow-list, deliberately, rather than a sanitiser.
//
// The alternative — take the requested name, strip "..", and join it onto a
// root — is how directory-traversal bugs happen: every sanitiser has to
// anticipate every encoding an attacker might use, while an allow-list only
// has to recognise the handful of names that are actually valid. Nothing the
// client sends is ever joined into a path; the map value is.
//
// install.sh fetches the binary and then its .sha256, and verifies one against
// the other before installing, so both must be served.
var downloadableFiles = map[string]string{
	"antimage-node-linux-amd64":        "antimage-node-linux-amd64",
	"antimage-node-linux-amd64.sha256": "antimage-node-linux-amd64.sha256",
	"antimage-node-linux-arm64":        "antimage-node-linux-arm64",
	"antimage-node-linux-arm64.sha256": "antimage-node-linux-arm64.sha256",
}

// handleDownload serves agent binaries to nodes being bootstrapped.
//
// Unauthenticated, like GET /install.sh: a node running the curl one-liner has
// no session, and the binary is not a secret. The enrolment token is what
// authorises a node to join, and it is verified separately — publishing the
// agent binary grants nobody anything they could not get from the release page.
func (d Deps) handleDownload(w http.ResponseWriter, r *http.Request) {
	requested := chi.URLParam(r, "name")

	filename, ok := downloadableFiles[requested]
	if !ok {
		// Deliberately identical to the not-configured case below: an operator
		// needs to know it is missing, and probing for which names exist tells
		// an attacker nothing either way.
		WriteError(w, http.StatusNotFound, "not_found", "no such download")
		return
	}
	if d.DownloadDir == "" {
		slog.WarnContext(r.Context(),
			"download requested but no download directory is configured",
			"file", filename, "request_id", RequestID(r.Context()))
		WriteError(w, http.StatusNotFound, "not_found",
			"no agent binaries are published; place them in the panel's downloads directory")
		return
	}

	// OpenInRoot, not Open: the allow-list controls the NAME, but not what that
	// name resolves to on disk. The downloads directory is written by a release
	// process, and a symlink placed there pointing at ../master.key would
	// otherwise be served unauthenticated to anyone who asked. OpenInRoot
	// refuses to escape the root, symlinks included.
	file, err := os.OpenInRoot(d.DownloadDir, filename)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// A fresh install with nothing staged is the common case, so the
			// message says what to do rather than just refusing.
			WriteError(w, http.StatusNotFound, "not_found",
				"that agent binary has not been published to this panel yet")
			return
		}
		slog.ErrorContext(r.Context(), "open download", "file", filename, "error", err)
		WriteError(w, http.StatusInternalServerError, "internal", "could not read the download")
		return
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil || info.IsDir() {
		WriteError(w, http.StatusNotFound, "not_found", "no such download")
		return
	}

	if filepath.Ext(filename) == ".sha256" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	// The panel reissues binaries in place on upgrade, so a cached copy would
	// hand an old agent to a new node.
	w.Header().Set("Cache-Control", "no-store")

	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	if _, err := io.Copy(w, file); err != nil {
		// The client hung up mid-transfer; the status line is already sent.
		slog.WarnContext(r.Context(), "download interrupted", "file", filename, "error", err)
	}
}

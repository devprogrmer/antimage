// Package webui serves the compiled single-page application.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// The pattern resolves relative to this directory, so the Vite build writes
// into internal/panel/webui/dist (see web/vite.config.ts). The all: prefix is
// what keeps .gitkeep — and any dotfile Vite emits — inside the FS, so the
// package still compiles on a checkout where the UI has never been built.
//
//go:embed all:dist
var assets embed.FS

// Handler serves the embedded build. When devProxy is set, requests are
// forwarded to a running Vite server instead, so hot reload works without a
// separate router.
func Handler(devProxy string) http.Handler {
	if devProxy != "" {
		target, err := url.Parse(devProxy)
		if err == nil {
			return httputil.NewSingleHostReverseProxy(target)
		}
	}

	dist, err := fs.Sub(assets, "dist")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "ui not built", http.StatusInternalServerError)
		})
	}
	files := http.FileServer(http.FS(dist))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Client-side routing: unknown non-asset paths fall back to index.html.
		if !strings.Contains(r.URL.Path, ".") && r.URL.Path != "/" {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}

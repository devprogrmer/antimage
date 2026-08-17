package httpapi

import "net/http"

// uiHandler serves the embedded single-page app. Task 30 replaces this with
// the real embed.FS handler; until then it keeps the router complete.
func (d Deps) uiHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "ui not built", http.StatusNotFound)
	})
}

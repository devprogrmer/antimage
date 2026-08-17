package httpapi

import "net/http"

// The handlers here are placeholders so every route the router declares is
// reachable and authenticated before the task that owns it lands. Each one
// names the task that replaces it, so an unowned stub is visible on sight.
//
// They live in their own file because the alternative — parking them beside
// the first real handler that happens to exist — makes implementing that
// handler's task an invitation to delete a stub some other task still owns.

func notImplemented(w http.ResponseWriter, _ *http.Request) {
	WriteError(w, http.StatusNotImplemented, "not_implemented", "not implemented yet")
}

// Task 26 (SSE live status) replaces this one.
func (d Deps) handleEvents(w http.ResponseWriter, r *http.Request) { notImplemented(w, r) }

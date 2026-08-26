// Package httpapi serves the panel's JSON API and the embedded UI.
package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		// RequestID is the same id stamped on the audit row and the server
		// log for this request. Without it a failed action gives an operator
		// nothing to quote: they can describe what they clicked, and whoever
		// reads the logs has to guess which of the day's requests they mean.
		//
		// Omitted rather than sent empty when there is no id, which happens
		// only for a response written outside the middleware chain. An empty
		// string in the field would look like an id that failed to generate.
		RequestID string `json:"request_id,omitempty"`
	} `json:"error"`
}

// WriteError emits a uniform error envelope. Messages are written for
// operators, never copied from internal errors, so a SQL failure cannot leak
// schema details to a reseller.
//
// The request id is read back off the response header rather than taken as an
// argument. requestIDMiddleware has already stamped X-Request-ID on the way in,
// so the id is available here without threading a context through 301 call
// sites -- and a signature change on that scale is how a handful of them end up
// passing the wrong context and reporting somebody else's id.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	var body errorBody
	body.Error.Code = code
	body.Error.Message = message
	body.Error.RequestID = w.Header().Get("X-Request-ID")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("write error response", "error", err)
	}
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json response", "error", err)
	}
}

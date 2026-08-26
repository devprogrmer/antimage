package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every error response carries the id that identifies the request.
//
// Without it a failed action gives an operator nothing to quote: they can say
// what they clicked, and whoever reads the logs has to guess which of the day's
// requests they mean. The id is already on the audit row and in the server log;
// this is the third place it has to appear for those two to be reachable.

func decodeError(t *testing.T, body []byte) struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
} {
	t.Helper()
	var envelope struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("error body is not JSON: %v (%s)", err, body)
	}
	return envelope.Error
}

// Through the real router, so the id is the one the middleware actually
// stamped rather than one the test invented.
func TestErrorResponseCarriesTheRequestID(t *testing.T) {
	env, _, _ := newSubjectEnv(t)

	// Any request that fails will do; an unauthenticated one is the simplest.
	res := env.get(t, "/api/v1/nodes", "")
	if res.Code == http.StatusOK {
		t.Fatalf("expected a failure to inspect, got %d", res.Code)
	}

	got := decodeError(t, res.Body.Bytes())
	if got.RequestID == "" {
		t.Fatal("the error envelope carries no request_id, so an operator " +
			"reporting this failure has no id to quote")
	}
	// And it is the SAME id as the header, or the two identify different
	// things and correlating them is worse than having neither.
	if header := res.Header().Get("X-Request-ID"); got.RequestID != header {
		t.Errorf("request_id = %q but X-Request-ID = %q; they must be the same "+
			"request", got.RequestID, header)
	}
}

// Two requests must not report the same id, or it identifies nothing.
func TestEachRequestGetsItsOwnID(t *testing.T) {
	env, _, _ := newSubjectEnv(t)

	first := decodeError(t, env.get(t, "/api/v1/nodes", "").Body.Bytes())
	second := decodeError(t, env.get(t, "/api/v1/nodes", "").Body.Bytes())

	if first.RequestID == second.RequestID {
		t.Errorf("two requests both reported %q", first.RequestID)
	}
}

// The envelope keeps its existing shape. Anything already parsing code and
// message must not break on a field being added beside them.
func TestErrorEnvelopeStillCarriesCodeAndMessage(t *testing.T) {
	env, _, _ := newSubjectEnv(t)

	got := decodeError(t, env.get(t, "/api/v1/nodes", "").Body.Bytes())
	if got.Code == "" || got.Message == "" {
		t.Errorf("envelope = %+v, want code and message preserved", got)
	}
}

// A response written outside the middleware chain has no id. The field is
// omitted rather than sent empty: an empty string reads as an id that failed
// to generate, which is a different and more alarming thing than no id.
func TestErrorWithoutAMiddlewareIDOmitsTheField(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, http.StatusTeapot, "test", "no middleware ran")

	if strings.Contains(rec.Body.String(), "request_id") {
		t.Errorf("body = %s, want request_id absent entirely", rec.Body)
	}
}

// indistinguishableError reduces an error response to the parts that could
// carry information about what was asked for.
//
// The request id is deliberately excluded: it is random per request, so two
// responses to two requests always differ by it, and it says nothing about
// whether a username or a subject exists. Everything else must match exactly.
//
// Comparing decoded fields rather than raw bytes is also stricter than the
// string comparison this replaced, which would have been satisfied by two
// equally empty bodies.
func indistinguishableError(t *testing.T, body []byte) string {
	t.Helper()
	got := decodeError(t, body)
	if got.Code == "" && got.Message == "" {
		t.Fatalf("error body carries neither code nor message: %s", body)
	}
	return got.Code + "|" + got.Message
}

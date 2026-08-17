package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"

	"github.com/amyrm/antimage/internal/panel/auth"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

type ctxKey int

const (
	ctxRequestID ctxKey = iota
	ctxActor
	ctxSessionID
)

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(ctxRequestID).(string)
	return id
}

func ActorFrom(ctx context.Context) *rbac.Actor {
	a, _ := ctx.Value(ctxActor).(*rbac.Actor)
	return a
}

func sessionIDFrom(ctx context.Context) int64 {
	id, _ := ctx.Value(ctxSessionID).(int64)
	return id
}

// requestIDMiddleware stamps every request so audit rows, logs, and the
// client's error report can be correlated.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, 12)
		_, _ = rand.Read(raw)
		id := base64.RawURLEncoding.EncodeToString(raw)

		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, id)))
	})
}

// statusRecorder remembers whether the wrapped handler already committed a
// status line. recoverMiddleware needs that: once the header is out, the only
// honest thing a recovering middleware can do is close the connection, not
// append a second, contradictory body.
type statusRecorder struct {
	http.ResponseWriter
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(status int) {
	s.wroteHeader = true
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wroteHeader = true
	return s.ResponseWriter.Write(b)
}

// Flush keeps streaming endpoints (the SSE event feed) working through the
// wrapper. Unwrap lets http.ResponseController reach anything else.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// recoverMiddleware turns a handler panic into a logged, request-ID-correlated
// 500 in the standard error envelope. Without it a panic unwinds through
// net/http, which drops the connection with nothing tying the failure to the
// request ID the operator was given.
//
// It runs inside requestIDMiddleware so the log line and the response header
// carry the same identifier.
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w}
		defer func() {
			p := recover()
			if p == nil {
				return
			}
			// net/http uses this panic value to abort a handler deliberately;
			// swallowing it would suppress that contract.
			if err, ok := p.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(p)
			}

			slog.ErrorContext(r.Context(), "panic in http handler",
				"request_id", RequestID(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"panic", p,
				"stack", string(debug.Stack()))

			if rec.wroteHeader {
				// A partial response is already on the wire. Anything further
				// would be appended to it, so let the connection break.
				return
			}
			WriteError(rec, http.StatusInternalServerError, "internal", "internal server error")
		}()
		next.ServeHTTP(rec, r)
	})
}

// originMiddleware rejects cross-site state changes. SameSite=Strict already
// covers browsers; this covers everything else.
//
// The absent-Origin skip is deliberate and load-bearing for antimage-ctl,
// which is not a browser and sends no Origin. It is safe only because the
// session cookie is SameSite=Strict and Secure: a cross-site request from a
// browser carries no session cookie at all and dies at authMiddleware, while
// a same-site-but-cross-origin request (a sibling subdomain, which SameSite
// does not separate) does carry an Origin and is caught here. See
// TestOriginPolicy for the pinned behaviour.
func originMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !strings.EqualFold(u.Host, r.Host) {
				WriteError(w, http.StatusForbidden, "bad_origin", "cross-origin request rejected")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// authMiddleware resolves the session into an rbac.Actor with permissions and
// scope allow-lists already loaded, so handlers never query them ad hoc.
func (d Deps) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(auth.CookieName)
		if err != nil {
			WriteError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
			return
		}
		session, err := d.Sessions.Lookup(r.Context(), cookie.Value)
		if err != nil {
			if !errors.Is(err, auth.ErrSessionInvalid) {
				WriteError(w, http.StatusInternalServerError, "internal", "session lookup failed")
				return
			}
			WriteError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
			return
		}
		actor, err := d.loadActor(r.Context(), session.AdminID)
		if err != nil {
			WriteError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
			return
		}

		ctx := context.WithValue(r.Context(), ctxActor, actor)
		ctx = context.WithValue(ctx, ctxSessionID, session.ID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// readOnlyMiddleware is defence in depth: the readonly role already lacks
// write permissions, but a blanket rejection means a future handler that
// forgets its Check still cannot mutate.
func readOnlyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor := ActorFrom(r.Context())
		if actor != nil && actor.RoleName == "readonly" {
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
				WriteError(w, http.StatusForbidden, "forbidden", "this account is read-only")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

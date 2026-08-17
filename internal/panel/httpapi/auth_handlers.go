package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/auth"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// requireActor returns the request's actor, or writes a 401 and reports false.
//
// authMiddleware always populates it, so nil here means a routing mistake put
// a private handler on a public path. Failing closed with the same
// unauthenticated envelope keeps that mistake a 401 rather than a panic.
func requireActor(w http.ResponseWriter, r *http.Request) (*rbac.Actor, bool) {
	actor := ActorFrom(r.Context())
	if actor == nil {
		WriteError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return nil, false
	}
	return actor, true
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	TOTP     string `json:"totp"`
}

func (d Deps) handleLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ip := clientIP(r)

	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}

	wait, err := d.Limiter.Check(ctx, req.Username, ip)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "login unavailable")
		return
	}
	if wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())))
		audit.BestEffort(ctx, d.Store, RequestID(ctx),
			audit.Actor{Type: audit.ActorSystem, Label: "login", IP: ip},
			audit.Record{Action: "auth.lockout", TargetType: "admin", Result: "denied"})
		WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many attempts; try again later")
		return
	}

	deny := func() {
		_ = d.Limiter.RecordFailure(ctx, req.Username, ip)
		audit.BestEffort(ctx, d.Store, RequestID(ctx),
			audit.Actor{Type: audit.ActorSystem, Label: "login", IP: ip},
			audit.Record{Action: "auth.login", TargetType: "admin", Result: "denied"})
		// One message for every failure mode: unknown user, wrong password,
		// suspended account, bad TOTP.
		WriteError(w, http.StatusUnauthorized, "invalid_credentials", "invalid credentials")
	}

	var (
		adminID int64
		hash    string
		status  string
	)
	var totpSecretEnc []byte
	err = d.Store.Read().QueryRowContext(ctx,
		`SELECT id, password_hash, status, totp_secret_enc
		   FROM admins WHERE username = ? COLLATE NOCASE`,
		req.Username).Scan(&adminID, &hash, &status, &totpSecretEnc)
	if errors.Is(err, sql.ErrNoRows) {
		// Hash anyway, so response timing does not reveal whether the user exists.
		_, _ = auth.VerifyPassword(
			"$argon2id$v=19$m=65536,t=3,p=4$YWFhYWFhYWFhYWFhYWFhYQ$"+
				"YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE", req.Password)
		deny()
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "login unavailable")
		return
	}

	ok, err := auth.VerifyPassword(hash, req.Password)
	if err != nil || !ok || status != "active" {
		deny()
		return
	}

	// Second factor. An admin who enrolled TOTP has been told they are
	// protected by it, so a password alone must never be enough. Every branch
	// below denies rather than proceeding: a panel that cannot check the
	// factor must not decide the factor passed.
	if len(totpSecretEnc) > 0 {
		if d.Box == nil {
			// The master key is absent while an encrypted secret exists. This
			// is the same fail-closed posture the store takes at startup;
			// letting the login through would silently downgrade the account
			// to single-factor exactly when the panel is misconfigured.
			slog.ErrorContext(ctx, "admin has TOTP enrolled but no secret box is configured; denying login",
				"admin_id", adminID, "request_id", RequestID(ctx))
			deny()
			return
		}
		secret, err := d.Box.Open(totpSecretEnc)
		if err != nil {
			slog.ErrorContext(ctx, "could not open TOTP secret; denying login",
				"admin_id", adminID, "request_id", RequestID(ctx), "error", err)
			deny()
			return
		}
		if !auth.VerifyTOTP(string(secret), req.TOTP, d.now()) {
			// deny() also records a rate-limiter failure, so the code space
			// cannot be brute-forced any faster than the password can.
			deny()
			return
		}
	}

	if err := d.Limiter.Reset(ctx, req.Username, ip); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "login unavailable")
		return
	}

	token, err := d.Sessions.Create(ctx, adminID, ip, r.UserAgent())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not start session")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		Expires:  d.now().Add(auth.AbsoluteLifetime),
	})
	audit.BestEffort(ctx, d.Store, RequestID(ctx),
		audit.AdminActor(adminID, ip),
		audit.Record{Action: "auth.login", TargetType: "admin",
			TargetID: sql.NullInt64{Int64: adminID, Valid: true}, Result: "ok"})

	WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (d Deps) handleLogout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if id := sessionIDFrom(ctx); id != 0 {
		if err := d.Sessions.Revoke(ctx, id); err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "logout failed")
			return
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: auth.CookieName, Value: "", Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
		Expires: time.Unix(0, 0), MaxAge: -1,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (d Deps) handleMe(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	perms := make([]string, 0, len(actor.Perms))
	for p := range actor.Perms {
		perms = append(perms, string(p))
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"admin_id":    actor.AdminID,
		"role":        actor.RoleName,
		"is_super":    actor.IsSuper,
		"permissions": perms,
	})
}

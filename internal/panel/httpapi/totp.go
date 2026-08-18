package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/auth"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

// recoveryCodeCount is how many single-use codes a confirmed enrolment mints.
const recoveryCodeCount = 10

// The three enrolment endpoints act on the caller's own account and nothing
// else. There is no admin id in any path and no rbac.Check against another
// target, so a compromised session can only ever manipulate its own second
// factor — never strip someone else's.
//
// The secret never round-trips through the client. POST /enrol generates it,
// seals it into admins.totp_pending_enc, and returns it once so an
// authenticator app can be provisioned; POST /confirm reads it back out of
// that column. Accepting a secret on the confirm request instead would let
// anything holding a session pin a secret it already knows, which is a
// silent, complete bypass of the factor it appears to be enabling.

type totpCodeRequest struct {
	TOTP string `json:"totp"`
}

// totpState is the caller's own enrolment state, read once per request.
type totpState struct {
	username   string
	secretEnc  []byte
	pendingEnc []byte
}

func (d Deps) loadTOTPState(ctx context.Context, adminID int64) (totpState, error) {
	var s totpState
	err := d.Store.Read().QueryRowContext(ctx,
		`SELECT username, totp_secret_enc, totp_pending_enc FROM admins WHERE id = ?`,
		adminID).Scan(&s.username, &s.secretEnc, &s.pendingEnc)
	return s, err
}

// totpUnavailable reports whether the panel cannot handle TOTP secrets at
// all. Without the master key nothing can be sealed or opened, and inventing
// a fallback would mean storing a secret the box cannot later read — an
// enrolment that locks the admin out permanently.
func (d Deps) totpUnavailable(w http.ResponseWriter) bool {
	if d.Box != nil {
		return false
	}
	WriteError(w, http.StatusServiceUnavailable, "unavailable",
		"the panel master key is not loaded; TOTP cannot be changed")
	return true
}

// totpThrottle applies the login rate limiter to a code-checking endpoint.
// /confirm and /disable both compare a 6-digit code, so without this the code
// space is walkable at full speed by anything holding a session. It reports
// false once it has written the 429.
func (d Deps) totpThrottle(w http.ResponseWriter, r *http.Request, username string) bool {
	ctx := r.Context()
	wait, err := d.Limiter.Check(ctx, username, clientIP(r))
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not check rate limit")
		return false
	}
	if wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())))
		WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many attempts; try again later")
		return false
	}
	return true
}

// handleTOTPEnrol generates a secret for the caller and parks it as pending.
//
// The audit record deliberately carries neither the secret nor the
// provisioning URI (which embeds the secret): audit_log is readable by every
// holder of audit:read, and a second factor recorded there is not a second
// factor.
func (d Deps) handleTOTPEnrol(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if d.totpUnavailable(w) {
		return
	}
	ctx := r.Context()

	state, err := d.loadTOTPState(ctx, actor.AdminID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read enrolment state")
		return
	}
	if len(state.secretEnc) > 0 {
		// Re-enrolling over a live secret would let an unlocked browser swap
		// the factor out without ever proving it held the old one. Disabling
		// first is what forces that proof.
		audit.BestEffort(ctx, d.Store, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action: "totp.enrol", TargetType: "admin",
			TargetID: sql.NullInt64{Int64: actor.AdminID, Valid: true},
			Result:   "denied",
			After:    map[string]any{"reason": "TOTP is already enabled"},
		})
		WriteError(w, http.StatusConflict, "conflict",
			"TOTP is already enabled; disable it before enrolling again")
		return
	}

	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not generate a secret")
		return
	}
	sealed, err := d.Box.Seal([]byte(secret))
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not store the secret")
		return
	}

	// Re-enrolling while a pending secret exists simply replaces it: the
	// admin has proven nothing yet, so there is nothing to protect.
	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE admins SET totp_pending_enc = ? WHERE id = ?`, sealed, actor.AdminID); err != nil {
			return err
		}
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action: "totp.enrol", TargetType: "admin",
			TargetID: sql.NullInt64{Int64: actor.AdminID, Valid: true}, Result: "ok",
		})
	})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not store the secret")
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"secret":           secret,
		"provisioning_uri": auth.TOTPProvisioningURI(secret, state.username, "antimage"),
	})
}

// handleTOTPConfirm promotes the pending secret once the caller proves an
// authenticator app holds it, and mints the recovery codes.
func (d Deps) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if d.totpUnavailable(w) {
		return
	}
	var req totpCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	ctx := r.Context()

	state, err := d.loadTOTPState(ctx, actor.AdminID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read enrolment state")
		return
	}
	if len(state.pendingEnc) == 0 {
		WriteError(w, http.StatusBadRequest, "no_pending_enrolment",
			"there is no pending TOTP enrolment to confirm")
		return
	}
	if !d.totpThrottle(w, r, state.username) {
		return
	}

	secret, err := d.Box.Open(state.pendingEnc)
	if err != nil {
		slog.ErrorContext(ctx, "could not open pending TOTP secret",
			"admin_id", actor.AdminID, "request_id", RequestID(ctx), "error", err)
		WriteError(w, http.StatusInternalServerError, "internal", "could not read the pending secret")
		return
	}
	if !auth.VerifyTOTP(string(secret), req.TOTP, d.now()) {
		d.denyTOTPCode(w, r, actor, state.username, "totp.confirm")
		return
	}

	// Generate and hash before opening the transaction. Ten argon2id hashes
	// at password cost is most of a second, and the store has exactly one
	// write connection — holding it for that long would stall every other
	// writer in the panel.
	codes, err := auth.GenerateRecoveryCodes(recoveryCodeCount)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not generate recovery codes")
		return
	}
	hashes := make([]string, 0, len(codes))
	for _, c := range codes {
		h, err := auth.HashRecoveryCode(c)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not generate recovery codes")
			return
		}
		hashes = append(hashes, h)
	}

	now := d.now().Unix()
	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		// One transaction: the factor is never live without its recovery
		// codes, and the codes never outlive the enrolment that minted them.
		if _, err := tx.ExecContext(ctx,
			`UPDATE admins SET totp_secret_enc = totp_pending_enc, totp_pending_enc = NULL
			  WHERE id = ?`, actor.AdminID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM admin_recovery_codes WHERE admin_id = ?`, actor.AdminID); err != nil {
			return err
		}
		for _, h := range hashes {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO admin_recovery_codes (admin_id, code_hash, created_at) VALUES (?,?,?)`,
				actor.AdminID, h, now); err != nil {
				return err
			}
		}
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action: "totp.confirm", TargetType: "admin",
			TargetID: sql.NullInt64{Int64: actor.AdminID, Valid: true}, Result: "ok",
			After: map[string]any{"recovery_codes_issued": len(hashes)},
		})
	})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not enable TOTP")
		return
	}
	// The caller has just proven the factor, so the failures they accumulated
	// getting here should not follow them to the login form.
	if err := d.Limiter.Reset(ctx, state.username, clientIP(r)); err != nil {
		slog.ErrorContext(ctx, "could not reset the rate limiter after TOTP confirm",
			"admin_id", actor.AdminID, "request_id", RequestID(ctx), "error", err)
	}

	// The only time the plaintext codes exist outside this response.
	WriteJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

// handleTOTPDisable turns the factor off, but only for a caller who can still
// produce a code or an unspent recovery code.
//
// Requiring that proof is the whole point: a session is something an unlocked
// browser already has, and if a session alone could strip the second factor
// then the second factor protects nothing past the first login.
func (d Deps) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	var req totpCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	ctx := r.Context()

	state, err := d.loadTOTPState(ctx, actor.AdminID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read enrolment state")
		return
	}
	if len(state.secretEnc) == 0 {
		WriteError(w, http.StatusBadRequest, "not_enrolled", "TOTP is not enabled on this account")
		return
	}
	if !d.totpThrottle(w, r, state.username) {
		return
	}

	// A recovery code still works here even with no master key loaded: its
	// hash lives in the database, not in the box. That is deliberate — an
	// operator who has lost the key must still be able to clear a secret
	// nothing can decrypt any more.
	verified := false
	if d.Box != nil {
		secret, err := d.Box.Open(state.secretEnc)
		if err != nil {
			slog.ErrorContext(ctx, "could not open TOTP secret while disabling",
				"admin_id", actor.AdminID, "request_id", RequestID(ctx), "error", err)
		} else {
			verified = auth.VerifyTOTP(string(secret), req.TOTP, d.now())
		}
	}
	if !verified {
		used, _, err := d.consumeRecoveryCode(ctx, actor.AdminID, req.TOTP)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not verify the code")
			return
		}
		verified = used
	}
	if !verified {
		d.denyTOTPCode(w, r, actor, state.username, "totp.disable")
		return
	}

	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE admins SET totp_secret_enc = NULL, totp_pending_enc = NULL WHERE id = ?`,
			actor.AdminID); err != nil {
			return err
		}
		// Including any code just spent above: the codes exist to recover
		// this enrolment, and the enrolment is gone.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM admin_recovery_codes WHERE admin_id = ?`, actor.AdminID); err != nil {
			return err
		}
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action: "totp.disable", TargetType: "admin",
			TargetID: sql.NullInt64{Int64: actor.AdminID, Valid: true}, Result: "ok",
		})
	})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not disable TOTP")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// denyTOTPCode is the single rejection path for a wrong code on /confirm and
// /disable. It charges the rate limiter exactly as a failed login does, so
// the 6-digit space costs the same to walk from behind a session as it does
// from the login form, and records the denial.
func (d Deps) denyTOTPCode(w http.ResponseWriter, r *http.Request, actor *rbac.Actor, username, action string) {
	ctx := r.Context()
	_ = d.Limiter.RecordFailure(ctx, username, clientIP(r))
	audit.BestEffort(ctx, d.Store, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
		Action: action, TargetType: "admin",
		TargetID: sql.NullInt64{Int64: actor.AdminID, Valid: true}, Result: "denied",
	})
	WriteError(w, http.StatusUnauthorized, "invalid_code", "invalid code")
}

// consumeRecoveryCode spends one of adminID's unconsumed recovery codes if
// code matches it, and reports how many remain.
//
// The codes are argon2id hashed, so there is no way to look one up by value:
// every unspent code has to be compared. That is bounded by
// recoveryCodeCount, and the callers are all behind the rate limiter.
//
// The UPDATE is guarded on consumed_at IS NULL and its row count is checked,
// so two requests racing on the same code cannot both be told they won it.
func (d Deps) consumeRecoveryCode(ctx context.Context, adminID int64, code string) (bool, int, error) {
	if strings.TrimSpace(code) == "" {
		return false, 0, nil
	}

	type candidate struct {
		id   int64
		hash string
	}
	rows, err := d.Store.Read().QueryContext(ctx,
		`SELECT id, code_hash FROM admin_recovery_codes
		  WHERE admin_id = ? AND consumed_at IS NULL ORDER BY id`, adminID)
	if err != nil {
		return false, 0, err
	}
	// The explicit Close below runs before the write and is the one that
	// matters; this defer is a safety net for the error return inside the loop.
	// sql.Rows.Close is idempotent, so closing twice is harmless.
	defer func() { _ = rows.Close() }()

	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.hash); err != nil {
			return false, 0, err
		}
		candidates = append(candidates, c)
	}
	// Drained and closed before any write: the store's write path needs a
	// connection this cursor would otherwise still be holding.
	if err := rows.Close(); err != nil {
		return false, 0, err
	}
	if err := rows.Err(); err != nil {
		return false, 0, err
	}

	var matchID int64
	for _, c := range candidates {
		ok, err := auth.VerifyPassword(c.hash, code)
		if err != nil {
			// A single malformed hash must not veto the rest of the set.
			slog.ErrorContext(ctx, "malformed recovery code hash",
				"admin_id", adminID, "code_id", c.id, "error", err)
			continue
		}
		if ok {
			matchID = c.id
			break
		}
	}
	if matchID == 0 {
		return false, 0, nil
	}

	var affected int64
	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE admin_recovery_codes SET consumed_at = ?
			  WHERE id = ? AND consumed_at IS NULL`, d.now().Unix(), matchID)
		if err != nil {
			return err
		}
		affected, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return false, 0, err
	}
	if affected == 0 {
		// Someone else spent it between the read and the update.
		return false, 0, nil
	}

	var remaining int
	if err := d.Store.Read().QueryRowContext(ctx,
		`SELECT count(*) FROM admin_recovery_codes WHERE admin_id = ? AND consumed_at IS NULL`,
		adminID).Scan(&remaining); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, 0, err
	}
	return true, remaining, nil
}

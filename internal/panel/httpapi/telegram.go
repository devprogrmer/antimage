package httpapi

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/notify/telegram"
)

// telegramLinks builds the link store lazily, matching subjectStore.
func (d Deps) telegramLinks() *telegram.Store {
	return telegram.NewStore(d.Store, d.now)
}

type telegramLinkDTO struct {
	TelegramID int64  `json:"telegram_id"`
	Username   string `json:"username"`
	LinkedAt   int64  `json:"linked_at"`
	LastSeenAt int64  `json:"last_seen_at"`
}

// handleGetMyTelegram reports the caller's own Telegram binding.
//
// Scoped to the caller by construction: it reads the link for the admin id in
// the session and takes no id from the request, so there is no parameter to
// tamper with and no permission to hold. A reseller checking their own binding
// is not an exercise of any reseller:* permission.
func (d Deps) handleGetMyTelegram(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	link, err := d.telegramLinks().ForAdmin(r.Context(), actor.AdminID)
	if errors.Is(err, telegram.ErrNotLinked) {
		WriteJSON(w, http.StatusOK, map[string]any{"linked": false})
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read link")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"linked": true,
		"link": telegramLinkDTO{
			TelegramID: link.TelegramID, Username: link.Username,
			LinkedAt: link.LinkedAt, LastSeenAt: link.LastSeenAt,
		},
	})
}

// handleCreateTelegramLinkCode issues a one-time code for the CALLER.
//
// The admin id comes from the authenticated session, never from the request
// body. Accepting an admin_id parameter would let anyone who can call this
// endpoint mint a code that binds THEIR Telegram account to somebody else's
// panel user -- a complete account takeover through a single field.
//
// The plaintext is returned exactly once and is never logged or audited by
// value; the audit record notes that a code was issued, not what it was.
func (d Deps) handleCreateTelegramLinkCode(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}

	ctx := r.Context()
	var code string
	err := d.Store.Write(ctx, func(tx *sql.Tx) error {
		var err error
		code, err = d.telegramLinks().IssueCode(ctx, tx, actor.AdminID)
		if err != nil {
			return err
		}
		return audit.InTx(ctx, tx, RequestID(ctx),
			d.actorAudit(actor, r), audit.Record{
				Action: "telegram.code_issued", TargetType: "admin",
				TargetID: sql.NullInt64{Int64: actor.AdminID, Valid: true},
				// That a code was issued, never the code itself.
				After:  map[string]any{"ttl_minutes": 10},
				Result: "ok",
			})
	})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not issue a link code")
		return
	}

	// no-store: a link code is a credential and must not sit in a browser or
	// proxy cache, for the same reason a revealed subject credential must not.
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, http.StatusCreated, map[string]any{
		"code":        code,
		"expires_in":  600,
		"instruction": "Send this to the bot: /link " + code,
	})
}

// handleDeleteMyTelegram revokes the caller's own binding.
//
// Also scoped to the session. Revocation has to be reachable from the panel
// as well as from the chat, because the reason to revoke is usually that the
// Telegram account itself is no longer under the operator's control.
func (d Deps) handleDeleteMyTelegram(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	err := d.Store.Write(ctx, func(tx *sql.Tx) error {
		if err := d.telegramLinks().RevokeByAdmin(ctx, tx, actor.AdminID); err != nil {
			return err
		}
		return audit.InTx(ctx, tx, RequestID(ctx),
			d.actorAudit(actor, r), audit.Record{
				Action: "telegram.unlink", TargetType: "admin",
				TargetID: sql.NullInt64{Int64: actor.AdminID, Valid: true},
				Result:   "ok",
			})
	})
	if errors.Is(err, telegram.ErrNotLinked) {
		WriteError(w, http.StatusNotFound, "not_found", "no linked telegram account")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not revoke the link")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

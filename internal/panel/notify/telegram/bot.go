package telegram

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/service"
	"github.com/amyrm/antimage/internal/panel/store"
)

// pollTimeout is the long-poll window. Telegram holds the request open until
// an update arrives or this elapses, so a longer value means fewer requests
// and lower latency, not more load.
const pollTimeout = 50 * time.Second

// maxLinkAttempts bounds /link tries per Telegram account per window.
//
// A link code is short enough to retype, which makes it short enough to guess
// at volume. Rate limiting is what turns "short enough to type" into "not
// worth attacking".
const (
	maxLinkAttempts = 5
	linkWindow      = 15 * time.Minute
)

// Bot dispatches Telegram commands against the shared service layer.
//
// It holds no query of its own. Every command resolves the chat to an admin,
// loads that admin's real rbac.Actor, and calls service methods -- so tenant
// isolation, permissions, transactions and audit are inherited rather than
// reimplemented. A command that needs data the service does not expose is a
// signal to extend the service, never to reach past it into the database.
type Bot struct {
	api   API
	db    *store.Store
	links *Store
	now   func() time.Time

	// attempts tracks failed /link tries per telegram id. In memory on
	// purpose: it protects against online guessing, and a restart clearing it
	// is acceptable when codes also expire in ten minutes.
	attempts map[int64][]time.Time
}

func NewBot(
	api API, db *store.Store, links *Store, now func() time.Time,
) *Bot {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Bot{
		api: api, db: db, links: links, now: now,
		attempts: map[int64][]time.Time{},
	}
}

// Run polls until the context is cancelled.
//
// Long polling rather than a webhook. A webhook needs a public HTTPS endpoint
// with a valid certificate, which would cost the single-binary, any-VPS
// install that is this project's distribution advantage. Polling is one
// outbound connection and works behind NAT.
func (b *Bot) Run(ctx context.Context) {
	var offset int64
	slog.InfoContext(ctx, "telegram bot started")

	for {
		if ctx.Err() != nil {
			slog.InfoContext(ctx, "telegram bot stopped")
			return
		}

		updates, err := b.api.GetUpdates(ctx, offset, pollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Telegram being unreachable is expected and transient. Back off
			// rather than spinning, and never surface the token in the log.
			slog.WarnContext(ctx, "telegram poll failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}

		for _, u := range updates {
			// Advance past this update BEFORE handling it. A command that
			// panics or errors must not be redelivered forever, blocking every
			// message behind it.
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			b.handle(ctx, u)
		}
	}
}

// handle processes one update. Errors are reported to the user and logged, but
// never propagated: one bad command must not stop the loop.
func (b *Bot) handle(ctx context.Context, u Update) {
	defer func() {
		if r := recover(); r != nil {
			slog.ErrorContext(ctx, "telegram handler panicked", "panic", r)
		}
	}()

	msg := u.Message
	if msg == nil || msg.From == nil || msg.Chat == nil {
		return
	}
	if msg.From.IsBot {
		return // bots do not hold panel identities
	}

	// PRIVATE CHATS ONLY.
	//
	// In a group, every member can issue commands that resolve to whichever
	// linked admin sent them -- handing that admin's entire tenant scope to
	// the whole room. This is the single most common way a management bot
	// leaks in production.
	if msg.Chat.Type != "private" {
		b.reply(ctx, msg.Chat.ID, "I only work in private chats. Message me directly.")
		return
	}

	cmd, arg := splitCommand(msg.Text)
	if cmd == "" {
		return
	}

	switch cmd {
	case "/start", "/help":
		b.cmdHelp(ctx, msg)
	case "/link":
		b.cmdLink(ctx, msg, arg)
	case "/unlink":
		b.cmdUnlink(ctx, msg)
	case "/whoami":
		b.cmdWhoami(ctx, msg)
	default:
		b.reply(ctx, msg.Chat.ID, "Unknown command. Send /help for the list.")
	}
}

// splitCommand extracts the command and its argument.
//
// Telegram appends @botname to commands sent in groups and sometimes in
// private chats after tapping a suggestion, so "/link@mybot ABC" and
// "/link ABC" must behave identically.
func splitCommand(text string) (cmd, arg string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", ""
	}
	head, rest, _ := strings.Cut(text, " ")
	if at := strings.IndexByte(head, '@'); at >= 0 {
		head = head[:at]
	}
	return strings.ToLower(head), strings.TrimSpace(rest)
}

// actorFor is the ONLY path from a chat to a panel identity.
//
// Called on every command rather than cached: a cached identity is a
// credential that outlives its own revocation, and revoking a hijacked
// Telegram account has to take effect immediately.
func (b *Bot) actorFor(ctx context.Context, telegramID int64) (*rbac.Actor, error) {
	adminID, err := b.links.AdminFor(ctx, telegramID)
	if err != nil {
		return nil, err
	}
	actor, err := service.LoadActor(ctx, b.db, adminID)
	if err != nil {
		// The link resolves but the admin does not load -- suspended or
		// deleted. Report it as not linked, so a suspended operator learns
		// nothing about why.
		return nil, ErrNotLinked
	}
	return actor, nil
}

func (b *Bot) reply(ctx context.Context, chatID int64, text string) {
	if err := b.api.SendMessage(ctx, chatID, text); err != nil {
		slog.WarnContext(ctx, "telegram reply failed", "chat_id", chatID, "error", err)
	}
}

// rateLimited reports whether this account has exhausted its /link attempts.
func (b *Bot) rateLimited(telegramID int64) bool {
	cutoff := b.now().Add(-linkWindow)
	kept := b.attempts[telegramID][:0]
	for _, t := range b.attempts[telegramID] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	b.attempts[telegramID] = kept
	return len(kept) >= maxLinkAttempts
}

func (b *Bot) recordFailure(telegramID int64) {
	b.attempts[telegramID] = append(b.attempts[telegramID], b.now())
}

// clearFailures drops the record after a success, so one mistyped code does
// not count against a legitimate operator for the rest of the window.
func (b *Bot) clearFailures(telegramID int64) {
	delete(b.attempts, telegramID)
}

var errNotLinkedReply = "This Telegram account is not linked to a panel user.\n\n" +
	"Open the panel, go to your profile, choose Link Telegram, and send me:\n" +
	"/link YOUR-CODE"

func (b *Bot) cmdHelp(ctx context.Context, msg *Message) {
	// Help is deliberately identical whether or not the account is linked. A
	// different reply for linked accounts would let a stranger probe which
	// Telegram accounts belong to panel operators.
	b.reply(ctx, msg.Chat.ID, strings.Join([]string{
		"Antimage panel bot.",
		"",
		"/link <code>  bind this Telegram account to your panel user",
		"/unlink       remove the binding",
		"/whoami       show which panel user this account is bound to",
		"/help         this message",
		"",
		"Get a link code from the panel: profile, then Link Telegram.",
	}, "\n"))
}

func (b *Bot) cmdLink(ctx context.Context, msg *Message, code string) {
	if code == "" {
		b.reply(ctx, msg.Chat.ID, "Usage: /link YOUR-CODE")
		return
	}
	if b.rateLimited(msg.From.ID) {
		b.reply(ctx, msg.Chat.ID,
			"Too many attempts. Wait a few minutes and try again.")
		return
	}

	var adminID int64
	err := b.db.Write(ctx, func(tx *sql.Tx) error {
		var err error
		adminID, err = b.links.Redeem(ctx, tx, msg.From.ID, msg.From.Username, code)
		return err
	})
	switch {
	case errors.Is(err, ErrBadCode):
		b.recordFailure(msg.From.ID)
		// One message for wrong, expired and already-used. Distinguishing them
		// tells an attacker whether a guess was structurally valid.
		b.reply(ctx, msg.Chat.ID, "That code is not valid. Codes expire after 10 minutes "+
			"and work once. Generate a fresh one in the panel.")
		return
	case errors.Is(err, ErrAlreadyLinked):
		b.reply(ctx, msg.Chat.ID,
			"That panel user already has a Telegram account linked. Unlink it first.")
		return
	case err != nil:
		slog.ErrorContext(ctx, "telegram link failed", "error", err)
		b.reply(ctx, msg.Chat.ID, "Something went wrong. Try again shortly.")
		return
	}

	b.clearFailures(msg.From.ID)
	// Audited: binding a chat account to an admin creates a standing
	// credential, and an incident review needs to know when it was created.
	audit.BestEffort(ctx, b.db, fmt.Sprintf("tg-%d", msg.MessageID), audit.AdminActor(adminID, ""), audit.Record{
		Action: "telegram.link", TargetType: "admin",
		TargetID: sql.NullInt64{Int64: adminID, Valid: true},
		// The telegram id is recorded; the code never is.
		After:  map[string]any{"telegram_id": msg.From.ID, "via": "telegram"},
		Result: "ok",
	})

	b.reply(ctx, msg.Chat.ID, "Linked. Send /whoami to confirm, or /help for commands.")
}

func (b *Bot) cmdUnlink(ctx context.Context, msg *Message) {
	err := b.db.Write(ctx, func(tx *sql.Tx) error {
		return b.links.Revoke(ctx, tx, msg.From.ID)
	})
	if errors.Is(err, ErrNotLinked) {
		b.reply(ctx, msg.Chat.ID, errNotLinkedReply)
		return
	}
	if err != nil {
		slog.ErrorContext(ctx, "telegram unlink failed", "error", err)
		b.reply(ctx, msg.Chat.ID, "Something went wrong. Try again shortly.")
		return
	}
	b.reply(ctx, msg.Chat.ID, "Unlinked. This account can no longer manage the panel.")
}

func (b *Bot) cmdWhoami(ctx context.Context, msg *Message) {
	actor, err := b.actorFor(ctx, msg.From.ID)
	if err != nil {
		b.reply(ctx, msg.Chat.ID, errNotLinkedReply)
		return
	}
	b.links.Touch(ctx, msg.From.ID)

	// Reports the role and the permission count, not the permission list: a
	// screenshot of this message should not be a map of what to attack.
	b.reply(ctx, msg.Chat.ID, fmt.Sprintf(
		"Linked to panel user #%d\nRole: %s\nPermissions: %d",
		actor.AdminID, actor.RoleName, len(actor.Perms)))
}

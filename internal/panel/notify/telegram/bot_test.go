package telegram

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// fakeAPI drives the bot without a network or a bot token.
type fakeAPI struct {
	sent []struct {
		chatID int64
		text   string
	}
}

func (f *fakeAPI) GetUpdates(context.Context, int64, time.Duration) ([]Update, error) {
	return nil, nil
}

func (f *fakeAPI) SendMessage(_ context.Context, chatID int64, text string) error {
	f.sent = append(f.sent, struct {
		chatID int64
		text   string
	}{chatID, text})
	return nil
}

func (f *fakeAPI) last() string {
	if len(f.sent) == 0 {
		return ""
	}
	return f.sent[len(f.sent)-1].text
}

// botFixture wires a real store and a real link store behind a fake API.
type botFixture struct {
	*fixture
	api *fakeAPI
	bot *Bot
}

func newBotFixture(t *testing.T) *botFixture {
	t.Helper()
	f := newFixture(t)
	api := &fakeAPI{}
	// subjects service is nil: these commands do not touch subjects, and
	// passing nil proves they do not reach for it. The read commands get a
	// real one in bot_reads_test.go.
	bot := NewBot(api, f.db, f.links, nil, "", func() time.Time { return f.now })
	return &botFixture{fixture: f, api: api, bot: bot}
}

// send delivers a private message from a Telegram account.
func (b *botFixture) send(from int64, text string) {
	b.bot.handle(context.Background(), Update{
		UpdateID: 1,
		Message: &Message{
			MessageID: 1,
			From:      &User{ID: from, Username: "u" + itoa(from)},
			Chat:      &Chat{ID: from, Type: "private"},
			Text:      text,
		},
	})
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

// SECURITY: a group chat must be refused outright.
//
// In a group, every member could issue commands that resolve to whichever
// linked admin sent them, handing that admin's entire tenant scope to the
// whole room. This is the most common way a management bot leaks.
func TestGroupChatsAreRefused(t *testing.T) {
	f := newBotFixture(t)
	code := f.issue(t, f.adminA)
	if _, err := f.redeem(555, code); err != nil {
		t.Fatalf("link: %v", err)
	}

	for _, chatType := range []string{"group", "supergroup", "channel"} {
		f.api.sent = nil
		f.bot.handle(context.Background(), Update{
			UpdateID: 2,
			Message: &Message{
				MessageID: 2,
				From:      &User{ID: 555},
				Chat:      &Chat{ID: -100, Type: chatType},
				Text:      "/whoami",
			},
		})
		if !strings.Contains(f.api.last(), "private") {
			t.Errorf("%s chat was not refused: %q", chatType, f.api.last())
		}
		// And it must not have leaked the identity.
		if strings.Contains(f.api.last(), "Linked to panel user") {
			t.Errorf("SECURITY: %s chat received identity details", chatType)
		}
	}
}

// An unlinked account gets no identity and no hint that one exists.
func TestWhoamiRefusesAnUnlinkedAccount(t *testing.T) {
	f := newBotFixture(t)
	f.send(999, "/whoami")

	if strings.Contains(f.api.last(), "Linked to panel user") {
		t.Errorf("SECURITY: an unlinked account got identity details: %q", f.api.last())
	}
	if !strings.Contains(f.api.last(), "not linked") {
		t.Errorf("unexpected reply: %q", f.api.last())
	}
}

// The full chain: link, then whoami reports the bound admin.
func TestWhoamiReportsTheLinkedAdmin(t *testing.T) {
	f := newBotFixture(t)
	code := f.issue(t, f.adminA)

	f.send(555, "/link "+code)
	if !strings.Contains(f.api.last(), "Linked") {
		t.Fatalf("link failed: %q", f.api.last())
	}

	f.send(555, "/whoami")
	if !strings.Contains(f.api.last(), "Linked to panel user") {
		t.Fatalf("whoami did not report the identity: %q", f.api.last())
	}
	if !strings.Contains(f.api.last(), "reseller") {
		t.Errorf("whoami did not report the role: %q", f.api.last())
	}
}

// whoami must not enumerate permissions. A screenshot of it should not be a
// map of what to attack.
func TestWhoamiDoesNotListPermissions(t *testing.T) {
	f := newBotFixture(t)
	f.send(555, "/link "+f.issue(t, f.adminA))
	f.send(555, "/whoami")

	for _, perm := range []string{"subject:read", "subject:write", "credential:reveal"} {
		if strings.Contains(f.api.last(), perm) {
			t.Errorf("whoami enumerated permission %q: %q", perm, f.api.last())
		}
	}
}

// SECURITY: revoking a link must cut off the next command immediately.
func TestRevocationCutsOffTheNextCommand(t *testing.T) {
	f := newBotFixture(t)
	f.send(555, "/link "+f.issue(t, f.adminA))
	f.send(555, "/whoami")
	if !strings.Contains(f.api.last(), "Linked to panel user") {
		t.Fatalf("precondition: not linked")
	}

	f.send(555, "/unlink")

	f.send(555, "/whoami")
	if strings.Contains(f.api.last(), "Linked to panel user") {
		t.Error("SECURITY: a revoked account still resolves to an admin")
	}
}

// SECURITY: suspending the admin must cut off the chat account, without
// telling it why.
func TestSuspendedAdminLosesBotAccess(t *testing.T) {
	f := newBotFixture(t)
	f.send(555, "/link "+f.issue(t, f.adminA))

	if err := f.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE admins SET status = 'suspended' WHERE id = ?`, f.adminA)
		return err
	}); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	f.send(555, "/whoami")
	if strings.Contains(f.api.last(), "Linked to panel user") {
		t.Error("SECURITY: a suspended admin still has bot access")
	}
	if strings.Contains(strings.ToLower(f.api.last()), "suspend") {
		t.Error("the reply reveals that the account is suspended")
	}
}

// Guessing link codes must be rate limited.
func TestLinkAttemptsAreRateLimited(t *testing.T) {
	f := newBotFixture(t)
	f.issue(t, f.adminA)

	for i := 0; i < maxLinkAttempts; i++ {
		f.send(555, "/link WRONGCODEWRONG")
	}
	if strings.Contains(f.api.last(), "Too many") {
		t.Fatalf("limited too early, after %d attempts", maxLinkAttempts)
	}

	f.send(555, "/link WRONGCODEWRONG")
	if !strings.Contains(f.api.last(), "Too many") {
		t.Errorf("attempt %d was not rate limited: %q", maxLinkAttempts+1, f.api.last())
	}
}

// A correct code must still work after a few typos, and must clear the count.
func TestASuccessfulLinkClearsTheFailureCount(t *testing.T) {
	f := newBotFixture(t)
	code := f.issue(t, f.adminA)

	f.send(555, "/link WRONG")
	f.send(555, "/link WRONG")
	f.send(555, "/link "+code)
	if !strings.Contains(f.api.last(), "Linked") {
		t.Fatalf("a correct code after typos was refused: %q", f.api.last())
	}
	if len(f.bot.attempts[555]) != 0 {
		t.Errorf("failure count survived a successful link: %d", len(f.bot.attempts[555]))
	}
}

// Wrong, expired and consumed codes must produce ONE message. A different
// reply per cause tells an attacker whether a guess was structurally valid.
func TestLinkFailuresAreIndistinguishable(t *testing.T) {
	f := newBotFixture(t)
	code := f.issue(t, f.adminA)
	if _, err := f.redeem(777, code); err != nil {
		t.Fatalf("consume: %v", err)
	}

	f.send(555, "/link NEVEREXISTEDXX")
	wrong := f.api.last()

	f.send(556, "/link "+code) // already consumed
	consumed := f.api.last()

	if wrong != consumed {
		t.Errorf("replies differ by cause:\n wrong:    %q\n consumed: %q", wrong, consumed)
	}
}

// Help must read the same whether or not the account is linked, so a stranger
// cannot probe which Telegram accounts belong to operators.
func TestHelpDoesNotRevealLinkStatus(t *testing.T) {
	f := newBotFixture(t)

	f.send(999, "/help")
	stranger := f.api.last()

	f.send(555, "/link "+f.issue(t, f.adminA))
	f.send(555, "/help")
	linked := f.api.last()

	if stranger != linked {
		t.Errorf("help differs by link status:\n stranger: %q\n linked:   %q", stranger, linked)
	}
}

// Telegram appends @botname to commands; both forms must behave identically.
func TestCommandsTolerateTheBotSuffix(t *testing.T) {
	cases := map[string][2]string{
		"/help":             {"/help", ""},
		"/help@antimagebot": {"/help", ""},
		"/link ABC":         {"/link", "ABC"},
		"/link@bot ABC":     {"/link", "ABC"},
		"/WhoAmI":           {"/whoami", ""},
		"  /help  ":         {"/help", ""},
		"not a command":     {"", ""},
	}
	for input, want := range cases {
		cmd, arg := splitCommand(input)
		if cmd != want[0] || arg != want[1] {
			t.Errorf("splitCommand(%q) = (%q,%q), want (%q,%q)",
				input, cmd, arg, want[0], want[1])
		}
	}
}

// Messages from other bots must be ignored: a bot has no panel identity.
func TestMessagesFromBotsAreIgnored(t *testing.T) {
	f := newBotFixture(t)
	f.bot.handle(context.Background(), Update{
		UpdateID: 3,
		Message: &Message{
			From: &User{ID: 555, IsBot: true},
			Chat: &Chat{ID: 555, Type: "private"},
			Text: "/whoami",
		},
	})
	if len(f.api.sent) != 0 {
		t.Errorf("replied to a bot: %q", f.api.last())
	}
}

// A malformed update must not panic the dispatch loop.
func TestMalformedUpdatesAreSurvivable(t *testing.T) {
	f := newBotFixture(t)
	for _, u := range []Update{
		{UpdateID: 1},
		{UpdateID: 2, Message: &Message{}},
		{UpdateID: 3, Message: &Message{From: &User{ID: 1}}},
		{UpdateID: 4, Message: &Message{Chat: &Chat{ID: 1, Type: "private"}}},
	} {
		f.bot.handle(context.Background(), u) // must not panic
	}
}

// Linking must be audited: it creates a standing credential.
func TestLinkingIsAudited(t *testing.T) {
	f := newBotFixture(t)
	code := f.issue(t, f.adminA)
	f.send(555, "/link "+code)

	var action, after string
	if err := f.db.Read().QueryRow(
		`SELECT action, coalesce(after_json,'') FROM audit_log
		  WHERE action = 'telegram.link' ORDER BY id DESC LIMIT 1`).
		Scan(&action, &after); err != nil {
		t.Fatalf("no audit record for a link: %v", err)
	}
	if !strings.Contains(after, "555") {
		t.Errorf("audit record does not name the telegram account: %s", after)
	}
	// The CODE must never be recorded.
	if strings.Contains(after, code) {
		t.Error("SECURITY: the link code is in the audit record")
	}
}

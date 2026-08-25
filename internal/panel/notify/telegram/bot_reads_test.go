package telegram

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/resellers"
	"github.com/amyrm/antimage/internal/panel/service"
	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/panel/subjects"
	"github.com/amyrm/antimage/internal/shared/secrets"
	"github.com/amyrm/antimage/internal/testutil/storetest"
)

// The read commands go through the service layer precisely so they inherit
// tenant scope. These tests exist to prove that inheritance actually happens:
// a bot that quietly queried the database itself would pass every link test
// and still hand one reseller another's customer list.

type readFixture struct {
	db     *store.Store
	links  *Store
	api    *fakeAPI
	bot    *Bot
	now    time.Time
	alice  int64 // admin id
	bob    int64 // admin id
	aliceR int64 // reseller id
	subA   int64 // alice's subject
	subB   int64 // bob's subject
}

const (
	aliceChat = 9001
	bobChat   = 9002
)

func newReadFixture(t *testing.T) *readFixture {
	t.Helper()
	db, err := storetest.OpenCopy(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	key := make([]byte, secrets.KeySize)
	for i := range key {
		key[i] = byte(i + 3)
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}

	now := time.Unix(1_700_000_000, 0).UTC()
	nowFn := func() time.Time { return now }

	f := &readFixture{db: db, now: now, api: &fakeAPI{}}
	f.links = NewStore(db, nowFn)

	subjStore := subjects.NewStore(db, box, nowFn)
	sellers := resellers.NewStore(db, subjStore, nowFn)
	// Notifier is nil: every command under test is a read, and a read that
	// republished would be a bug this would surface as a panic.
	svc := service.NewSubjects(db, subjStore, sellers, nil, nowFn)
	f.bot = NewBot(f.api, db, f.links, svc, "https://panel.example", nowFn)

	perms, err := json.Marshal([]rbac.Permission{
		rbac.PermSubjectRead, rbac.PermCredReveal,
	})
	if err != nil {
		t.Fatalf("marshal perms: %v", err)
	}

	err = db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`INSERT INTO roles (id, name, is_builtin, permissions) VALUES (1,'reseller',1,?)`,
			string(perms)); err != nil {
			return err
		}
		for _, who := range []string{"alice", "bob"} {
			res, err := tx.Exec(
				`INSERT INTO admins (username, password_hash, role_id, created_at)
				 VALUES (?, 'x', 1, ?)`, who, now.Unix())
			if err != nil {
				return err
			}
			id, _ := res.LastInsertId()
			if who == "alice" {
				f.alice = id
			} else {
				f.bob = id
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed admins: %v", err)
	}

	// One tenant each, with one customer each, so a scope failure shows up as
	// alice seeing "bob-customer".
	f.aliceR = f.seedTenant(t, subjStore, f.alice, "alice", &f.subA)
	f.seedTenant(t, subjStore, f.bob, "bob", &f.subB)

	f.link(t, aliceChat, f.alice)
	f.link(t, bobChat, f.bob)
	return f
}

func (f *readFixture) seedTenant(
	t *testing.T, subjStore *subjects.Store, adminID int64, who string, subjectID *int64,
) int64 {
	t.Helper()
	var resellerID int64
	err := f.db.Write(context.Background(), func(tx *sql.Tx) error {
		id, err := subjStore.Create(context.Background(), tx, subjects.CreateInput{
			Name: who + "-customer",
		})
		if err != nil {
			return err
		}
		*subjectID = id

		res, err := tx.Exec(
			`INSERT INTO resellers (admin_id, display_name, enabled, credit_floor,
			                        created_at, updated_at)
			 VALUES (?,?,1,0,?,?)`, adminID, who+"-vpn", f.now.Unix(), f.now.Unix())
		if err != nil {
			return err
		}
		resellerID, _ = res.LastInsertId()

		if _, err := tx.Exec(
			`INSERT INTO reseller_subjects (subject_id, reseller_id, cost, created_at)
			 VALUES (?,?,0,?)`, id, resellerID, f.now.Unix()); err != nil {
			return err
		}
		_, err = tx.Exec(
			`INSERT INTO reseller_credit_ledger (reseller_id, delta, reason, idempotency_key, at)
			 VALUES (?,?,?,?,?)`,
			resellerID, 750, "topup", "seed-"+who, f.now.Unix())
		return err
	})
	if err != nil {
		t.Fatalf("seed tenant %s: %v", who, err)
	}
	return resellerID
}

func (f *readFixture) link(t *testing.T, chatID, adminID int64) {
	t.Helper()
	err := f.db.Write(context.Background(), func(tx *sql.Tx) error {
		code, err := f.links.IssueCode(context.Background(), tx, adminID)
		if err != nil {
			return err
		}
		_, err = f.links.Redeem(context.Background(), tx, chatID, "u", code)
		return err
	})
	if err != nil {
		t.Fatalf("link chat %d: %v", chatID, err)
	}
}

func (f *readFixture) send(from int64, text string) string {
	f.bot.handle(context.Background(), Update{
		UpdateID: 1,
		Message: &Message{
			MessageID: 1,
			From:      &User{ID: from, Username: "u"},
			Chat:      &Chat{ID: from, Type: "private"},
			Text:      text,
		},
	})
	return f.api.last()
}

// The whole reason these commands go through the service layer.
func TestUsersCommandIsTenantScoped(t *testing.T) {
	f := newReadFixture(t)

	got := f.send(aliceChat, "/users")
	if !strings.Contains(got, "alice-customer") {
		t.Errorf("/users omitted alice's own customer:\n%s", got)
	}
	if strings.Contains(got, "bob-customer") {
		t.Errorf("LEAK: /users showed another tenant's customer:\n%s", got)
	}
}

// Naming another tenant's customer must be indistinguishable from naming one
// that does not exist, or the reply becomes an oracle for guessing names.
func TestUserCommandHidesOtherTenants(t *testing.T) {
	f := newReadFixture(t)

	mine := f.send(aliceChat, "/user alice-customer")
	if !strings.Contains(mine, "alice-customer") {
		t.Fatalf("/user did not return my own customer:\n%s", mine)
	}

	theirs := f.send(aliceChat, "/user bob-customer")
	missing := f.send(aliceChat, "/user no-such-name-at-all")
	if theirs != missing {
		t.Errorf("reply differs for another tenant's customer and a missing one:\n%q\nvs\n%q",
			theirs, missing)
	}
	if strings.Contains(theirs, "bob") {
		t.Errorf("LEAK: reply mentioned the other tenant's customer: %q", theirs)
	}
}

func TestBalanceCommandReportsOwnCredit(t *testing.T) {
	f := newReadFixture(t)

	got := f.send(aliceChat, "/balance")
	if !strings.Contains(got, "alice-vpn") {
		t.Errorf("/balance did not name the reseller:\n%s", got)
	}
	if !strings.Contains(got, "750") {
		t.Errorf("/balance did not report the seeded balance:\n%s", got)
	}
	if strings.Contains(got, "bob") {
		t.Errorf("LEAK: /balance mentioned another tenant:\n%s", got)
	}
}

func TestConfigCommandReturnsSubscriptionLink(t *testing.T) {
	f := newReadFixture(t)

	got := f.send(aliceChat, "/config alice-customer")
	if !strings.Contains(got, "https://panel.example/api/v1/subscribe/") {
		t.Errorf("/config did not return a usable link:\n%s", got)
	}
	// The disclosure must be labelled. A subscription URL is a bearer
	// credential and reads like a harmless link.
	if !strings.Contains(strings.ToLower(got), "password") {
		t.Errorf("/config did not warn that the link is a credential:\n%s", got)
	}

	// And it is still scoped.
	theirs := f.send(aliceChat, "/config bob-customer")
	if strings.Contains(theirs, "subscribe/") {
		t.Errorf("LEAK: /config issued a link for another tenant's customer:\n%s", theirs)
	}
}

// credential:reveal is what gates /config. A role that can read subjects but
// not reveal credentials must be able to run /users and /user, and be refused
// by /config.
func TestConfigRequiresCredentialReveal(t *testing.T) {
	f := newReadFixture(t)

	perms, err := json.Marshal([]rbac.Permission{rbac.PermSubjectRead})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := f.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE roles SET permissions = ? WHERE id = 1`, string(perms))
		return err
	}); err != nil {
		t.Fatalf("narrow role: %v", err)
	}

	if got := f.send(aliceChat, "/users"); !strings.Contains(got, "alice-customer") {
		t.Errorf("/users should still work with subject:read alone:\n%s", got)
	}
	got := f.send(aliceChat, "/config alice-customer")
	if strings.Contains(got, "subscribe/") {
		t.Errorf("/config issued a link without credential:reveal:\n%s", got)
	}
	if !strings.Contains(got, "role does not allow") {
		t.Errorf("/config gave an unhelpful refusal:\n%s", got)
	}
}

// An unlinked chat gets the same answer from every read command.
func TestReadCommandsRequireLinking(t *testing.T) {
	f := newReadFixture(t)
	const stranger = 9999

	for _, cmd := range []string{"/users", "/user alice-customer", "/balance", "/config alice-customer"} {
		got := f.send(stranger, cmd)
		if !strings.Contains(got, "not linked") {
			t.Errorf("%s from an unlinked chat replied %q", cmd, got)
		}
	}
}

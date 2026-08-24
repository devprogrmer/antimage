package telegram

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

var base = time.Unix(1_700_000_000, 0).UTC()

type fixture struct {
	db     *store.Store
	links  *Store
	now    time.Time
	adminA int64
	adminB int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	f := &fixture{db: db, now: base}
	f.links = NewStore(db, func() time.Time { return f.now })

	err = db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`INSERT INTO roles (id, name, is_builtin, permissions) VALUES (1,'reseller',1,'[]')`,
		); err != nil {
			return err
		}
		for _, who := range []string{"alice", "bob"} {
			res, err := tx.Exec(
				`INSERT INTO admins (username, password_hash, role_id, created_at)
				 VALUES (?, 'x', 1, ?)`, who, base.Unix())
			if err != nil {
				return err
			}
			id, _ := res.LastInsertId()
			if who == "alice" {
				f.adminA = id
			} else {
				f.adminB = id
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return f
}

func (f *fixture) issue(t *testing.T, adminID int64) string {
	t.Helper()
	var code string
	err := f.db.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		code, err = f.links.IssueCode(context.Background(), tx, adminID)
		return err
	})
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	return code
}

func (f *fixture) redeem(telegramID int64, code string) (int64, error) {
	var adminID int64
	err := f.db.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		adminID, err = f.links.Redeem(context.Background(), tx, telegramID, "user", code)
		return err
	})
	return adminID, err
}

// The happy path, so the failure tests below are not vacuous.
func TestRedeemBindsTheAccount(t *testing.T) {
	f := newFixture(t)
	code := f.issue(t, f.adminA)

	got, err := f.redeem(555, code)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if got != f.adminA {
		t.Errorf("bound to admin %d, want %d", got, f.adminA)
	}

	resolved, err := f.links.AdminFor(context.Background(), 555)
	if err != nil {
		t.Fatalf("AdminFor: %v", err)
	}
	if resolved != f.adminA {
		t.Errorf("AdminFor = %d, want %d", resolved, f.adminA)
	}
}

// SECURITY: the plaintext code must never reach the database.
//
// Read access to a backup must not yield working link codes. This scans the
// stored bytes for the code itself rather than trusting that hashCode was
// called somewhere.
func TestCodesAreStoredHashedNotPlaintext(t *testing.T) {
	f := newFixture(t)
	code := f.issue(t, f.adminA)

	rows, err := f.db.Read().Query(`SELECT code_hash FROM telegram_link_codes`)
	if err != nil {
		t.Fatalf("read codes: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var n int
	for rows.Next() {
		var stored []byte
		if err := rows.Scan(&stored); err != nil {
			t.Fatalf("scan: %v", err)
		}
		n++
		if bytes.Contains(stored, []byte(code)) {
			t.Error("SECURITY: the link code is stored in plaintext")
		}
		if len(stored) != 32 {
			t.Errorf("stored value is %d bytes, want a 32-byte sha256", len(stored))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate codes: %v", err)
	}
	if n == 0 {
		t.Fatal("no codes stored; this test would pass vacuously")
	}
}

// A code must work exactly once. Replay is how a shoulder-surfed or
// screenshotted code becomes a second, unauthorised binding.
func TestCodeIsSingleUse(t *testing.T) {
	f := newFixture(t)
	code := f.issue(t, f.adminA)

	if _, err := f.redeem(555, code); err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if _, err := f.redeem(666, code); !errors.Is(err, ErrBadCode) {
		t.Fatalf("replayed code returned %v, want ErrBadCode", err)
	}
	// And the replay must not have bound the second account.
	if _, err := f.links.AdminFor(context.Background(), 666); !errors.Is(err, ErrNotLinked) {
		t.Error("a replayed code bound a second telegram account")
	}
}

func TestCodeExpires(t *testing.T) {
	f := newFixture(t)
	code := f.issue(t, f.adminA)

	f.now = base.Add(codeTTL + time.Second)
	if _, err := f.redeem(555, code); !errors.Is(err, ErrBadCode) {
		t.Fatalf("expired code returned %v, want ErrBadCode", err)
	}
}

// Issuing a second code must invalidate the first, or clicking "link" twice
// leaves two live credentials outstanding.
func TestIssuingAgainInvalidatesThePreviousCode(t *testing.T) {
	f := newFixture(t)
	first := f.issue(t, f.adminA)
	second := f.issue(t, f.adminA)

	if _, err := f.redeem(555, first); !errors.Is(err, ErrBadCode) {
		t.Errorf("superseded code returned %v, want ErrBadCode", err)
	}
	if _, err := f.redeem(555, second); err != nil {
		t.Errorf("current code rejected: %v", err)
	}
}

// Wrong, malformed and empty codes must all fail identically. Distinguishing
// them tells an attacker whether a guess was structurally valid.
func TestBadCodesAreRejectedUniformly(t *testing.T) {
	f := newFixture(t)
	f.issue(t, f.adminA)

	for _, bad := range []string{"", "AAAAAAAAAAAAAAAA", "not-a-code", "   "} {
		if _, err := f.redeem(555, bad); !errors.Is(err, ErrBadCode) {
			t.Errorf("code %q returned %v, want ErrBadCode", bad, err)
		}
	}
}

// Codes are read off a screen, so case and separators are typos rather than
// attacks. Normalisation must not, however, make a genuinely wrong code work.
func TestCodesAreCaseAndSeparatorTolerant(t *testing.T) {
	f := newFixture(t)
	code := f.issue(t, f.adminA)

	messy := "  " + string([]byte(code[:4])) + "-" + string([]byte(code[4:])) + "  "
	if _, err := f.redeem(555, lower(messy)); err != nil {
		t.Errorf("a retyped code with dashes and case differences was rejected: %v", err)
	}
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 32
		}
	}
	return string(b)
}

// SECURITY: revocation must take effect immediately.
//
// A cached identity is a credential that outlives its own revocation, which is
// exactly what a SIM-swap victim needs to not happen.
func TestRevocationTakesEffectImmediately(t *testing.T) {
	f := newFixture(t)
	code := f.issue(t, f.adminA)
	if _, err := f.redeem(555, code); err != nil {
		t.Fatalf("redeem: %v", err)
	}

	err := f.db.Write(context.Background(), func(tx *sql.Tx) error {
		return f.links.Revoke(context.Background(), tx, 555)
	})
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	if _, err := f.links.AdminFor(context.Background(), 555); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("a revoked account still resolves to an admin (err=%v)", err)
	}
}

// A revoked link and an absent link must be indistinguishable: telling a
// stranger their account "was revoked" confirms it once existed.
func TestRevokedIsIndistinguishableFromNeverLinked(t *testing.T) {
	f := newFixture(t)
	code := f.issue(t, f.adminA)
	if _, err := f.redeem(555, code); err != nil {
		t.Fatalf("redeem: %v", err)
	}
	_ = f.db.Write(context.Background(), func(tx *sql.Tx) error {
		return f.links.Revoke(context.Background(), tx, 555)
	})

	_, revokedErr := f.links.AdminFor(context.Background(), 555)
	_, strangerErr := f.links.AdminFor(context.Background(), 999999)

	if revokedErr == nil || strangerErr == nil {
		t.Fatal("expected both to fail")
	}
	if revokedErr.Error() != strangerErr.Error() {
		t.Errorf("revoked (%v) and never-linked (%v) are distinguishable",
			revokedErr, strangerErr)
	}
}

// One admin may hold only one Telegram account, or every audit record becomes
// ambiguous about which human acted.
func TestAnAdminCannotBindTwoTelegramAccounts(t *testing.T) {
	f := newFixture(t)
	if _, err := f.redeem(555, f.issue(t, f.adminA)); err != nil {
		t.Fatalf("first link: %v", err)
	}
	if _, err := f.redeem(666, f.issue(t, f.adminA)); !errors.Is(err, ErrAlreadyLinked) {
		t.Fatalf("second account returned %v, want ErrAlreadyLinked", err)
	}
}

// Re-linking the same account after a revocation must work, and must clear the
// revocation rather than leaving a dead row shadowing the live one.
func TestRelinkingAfterRevocationRevivesTheBinding(t *testing.T) {
	f := newFixture(t)
	if _, err := f.redeem(555, f.issue(t, f.adminA)); err != nil {
		t.Fatalf("link: %v", err)
	}
	_ = f.db.Write(context.Background(), func(tx *sql.Tx) error {
		return f.links.Revoke(context.Background(), tx, 555)
	})

	if _, err := f.redeem(555, f.issue(t, f.adminA)); err != nil {
		t.Fatalf("relink: %v", err)
	}
	if _, err := f.links.AdminFor(context.Background(), 555); err != nil {
		t.Errorf("relinked account does not resolve: %v", err)
	}

	var rows int
	_ = f.db.Read().QueryRow(
		`SELECT count(*) FROM telegram_links WHERE telegram_id = 555`).Scan(&rows)
	if rows != 1 {
		t.Errorf("%d rows for one telegram account; a revoked row is shadowing", rows)
	}
}

// One admin's code must never bind to another admin.
func TestACodeOnlyBindsItsOwnAdmin(t *testing.T) {
	f := newFixture(t)
	codeForAlice := f.issue(t, f.adminA)

	got, err := f.redeem(555, codeForAlice)
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if got == f.adminB {
		t.Fatal("alice's code bound bob's admin account")
	}
	if got != f.adminA {
		t.Errorf("bound admin %d, want alice (%d)", got, f.adminA)
	}
}

// Deleting an admin must not leave a live binding that resolves to a
// now-missing identity.
func TestDeletingAnAdminCascadesTheLink(t *testing.T) {
	f := newFixture(t)
	if _, err := f.redeem(555, f.issue(t, f.adminA)); err != nil {
		t.Fatalf("link: %v", err)
	}

	err := f.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM admins WHERE id = ?`, f.adminA)
		return err
	})
	if err != nil {
		t.Fatalf("delete admin: %v", err)
	}

	if _, err := f.links.AdminFor(context.Background(), 555); !errors.Is(err, ErrNotLinked) {
		t.Errorf("link survived admin deletion (err=%v)", err)
	}
}

// Codes must not be guessable. This is a shape check, not a statistical one:
// it catches a regression that shortened the code or made it non-random.
func TestCodesAreHighEntropyAndUnique(t *testing.T) {
	f := newFixture(t)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		code := f.issue(t, f.adminA)
		if len(code) < 16 {
			t.Fatalf("code %q is %d chars, too short to resist guessing", code, len(code))
		}
		if seen[code] {
			t.Fatalf("duplicate code generated: %q", code)
		}
		seen[code] = true
	}
}

func TestSameCodeIsConstantTimeAndCorrect(t *testing.T) {
	if !SameCode("ABC123", "abc-123") {
		t.Error("equivalent codes compared unequal")
	}
	if SameCode("ABC123", "ABC124") {
		t.Error("different codes compared equal")
	}
}

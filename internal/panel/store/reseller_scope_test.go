package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/rbac"
)

// tenantFixture builds two competing resellers plus a platform-owned subject.
//
//	alice  -> reseller "alice-vpn"  owns subject alice-user
//	bob    -> reseller "bob-vpn"    owns subject bob-user
//	(none) -> platform-user, owned by nobody
//
// The platform-owned subject is the case most likely to be got wrong: it has
// no ownership row at all, so a predicate written as "owner is not somebody
// else" would leak it to everyone.
type tenantFixture struct {
	s   *Store
	ids map[string]int64
}

func seedTenantFixture(t *testing.T) *tenantFixture {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ids := map[string]int64{}
	now := time.Now().Unix()

	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`INSERT INTO roles (id, name, is_builtin, permissions) VALUES (1,'reseller',1,'[]')`,
		); err != nil {
			return err
		}
		for _, who := range []string{"alice", "bob", "super"} {
			res, err := tx.Exec(
				`INSERT INTO admins (username, password_hash, role_id, created_at)
				 VALUES (?, 'x', 1, ?)`, who, now)
			if err != nil {
				return err
			}
			id, _ := res.LastInsertId()
			ids["admin_"+who] = id
		}

		for _, who := range []string{"alice", "bob"} {
			res, err := tx.Exec(
				`INSERT INTO resellers (admin_id, display_name, enabled, credit_floor,
				                        created_at, updated_at)
				 VALUES (?, ?, 1, 0, ?, ?)`,
				ids["admin_"+who], who+"-vpn", now, now)
			if err != nil {
				return err
			}
			id, _ := res.LastInsertId()
			ids["reseller_"+who] = id
		}

		for _, name := range []string{"alice-user", "bob-user", "platform-user"} {
			res, err := tx.Exec(
				`INSERT INTO subjects (name, enabled, created_at) VALUES (?, 1, ?)`,
				name, now)
			if err != nil {
				return err
			}
			id, _ := res.LastInsertId()
			ids[name] = id
		}

		// Only the two reseller-owned subjects get ownership rows.
		for _, who := range []string{"alice", "bob"} {
			if _, err := tx.Exec(
				`INSERT INTO reseller_subjects (subject_id, reseller_id, cost, created_at)
				 VALUES (?, ?, 100, ?)`,
				ids[who+"-user"], ids["reseller_"+who], now); err != nil {
				return err
			}
		}

		// Give each reseller a distinguishable balance.
		if _, err := tx.Exec(
			`INSERT INTO reseller_credit_ledger
			   (reseller_id, delta, reason, idempotency_key, at)
			 VALUES (?, 5000, 'topup', 'a1', ?), (?, 9999, 'topup', 'b1', ?)`,
			ids["reseller_alice"], now, ids["reseller_bob"], now); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return &tenantFixture{s: s, ids: ids}
}

func (f *tenantFixture) scope(who string) rbac.Scope {
	if who == "super" {
		return rbac.Scope{AdminID: f.ids["admin_super"], IsSuper: true}
	}
	return rbac.Scope{AdminID: f.ids["admin_"+who]}
}

// A reseller listing subjects must see only their own.
func TestListSubjectsScopedNeverLeaksAcrossTenants(t *testing.T) {
	f := seedTenantFixture(t)

	for _, who := range []string{"alice", "bob"} {
		got, err := f.s.ListSubjectsScoped(context.Background(), f.scope(who))
		if err != nil {
			t.Fatalf("%s list: %v", who, err)
		}
		if len(got) != 1 {
			t.Fatalf("%s sees %d subjects, want exactly their own", who, len(got))
		}
		if got[0].Name != who+"-user" {
			t.Errorf("%s sees %q", who, got[0].Name)
		}
	}

	// A super admin sees all three, including the platform-owned one.
	got, err := f.s.ListSubjectsScoped(context.Background(), f.scope("super"))
	if err != nil {
		t.Fatalf("super list: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("super sees %d subjects, want 3", len(got))
	}
}

// The platform-owned subject belongs to nobody and must be invisible to every
// tenant. A predicate phrased as "not owned by someone else" would leak it.
func TestPlatformOwnedSubjectsAreInvisibleToResellers(t *testing.T) {
	f := seedTenantFixture(t)

	for _, who := range []string{"alice", "bob"} {
		_, err := f.s.GetSubjectScoped(context.Background(), f.scope(who),
			f.ids["platform-user"])
		if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("%s could read a platform-owned subject (err=%v); an unowned "+
				"subject must not default to being everybody's", who, err)
		}
	}
}

// THE leakage test: out-of-scope must be indistinguishable from missing.
//
// If a foreign subject returned a different error from a nonexistent one, a
// reseller could walk the id space and learn exactly how many customers their
// competitors have and which ids are real. Both must look identical.
func TestOutOfScopeIsIndistinguishableFromMissing(t *testing.T) {
	f := seedTenantFixture(t)
	alice := f.scope("alice")

	_, foreignErr := f.s.GetSubjectScoped(context.Background(), alice, f.ids["bob-user"])
	_, missingErr := f.s.GetSubjectScoped(context.Background(), alice, 9_999_999)

	if !errors.Is(foreignErr, sql.ErrNoRows) {
		t.Fatalf("reading another tenant's subject returned %v, want sql.ErrNoRows",
			foreignErr)
	}
	if !errors.Is(missingErr, sql.ErrNoRows) {
		t.Fatalf("reading a missing subject returned %v, want sql.ErrNoRows", missingErr)
	}
	if foreignErr.Error() != missingErr.Error() {
		t.Errorf("foreign (%v) and missing (%v) are distinguishable; a tenant can "+
			"probe for the existence of another tenant's customers",
			foreignErr, missingErr)
	}
}

// Mutations gate on SubjectInScope; it must refuse the same rows Get refuses.
func TestSubjectInScopeMatchesGet(t *testing.T) {
	f := seedTenantFixture(t)
	alice := f.scope("alice")

	cases := map[string]struct {
		id   int64
		want bool
	}{
		"own subject":      {f.ids["alice-user"], true},
		"foreign subject":  {f.ids["bob-user"], false},
		"platform subject": {f.ids["platform-user"], false},
		"missing subject":  {9_999_999, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ok, err := f.s.SubjectInScope(context.Background(), alice, tc.id)
			if err != nil {
				t.Fatalf("SubjectInScope: %v", err)
			}
			if ok != tc.want {
				t.Errorf("SubjectInScope = %v, want %v", ok, tc.want)
			}
			// And it must agree with the read path, or a mutation could touch
			// a row the caller cannot read.
			_, getErr := f.s.GetSubjectScoped(context.Background(), alice, tc.id)
			readable := getErr == nil
			if readable != ok {
				t.Errorf("SubjectInScope=%v but readable=%v; the write gate and the "+
					"read gate disagree", ok, readable)
			}
		})
	}
}

// A reseller must not be able to enumerate or read other tenants.
func TestResellerRecordsAreScoped(t *testing.T) {
	f := seedTenantFixture(t)

	list, err := f.s.ListResellersScoped(context.Background(), f.scope("alice"))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].DisplayName != "alice-vpn" {
		t.Fatalf("alice sees %d reseller(s): %+v", len(list), list)
	}

	if _, err := f.s.GetResellerScoped(context.Background(), f.scope("alice"),
		f.ids["reseller_bob"]); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("alice read bob's reseller record (err=%v)", err)
	}

	super, err := f.s.ListResellersScoped(context.Background(), f.scope("super"))
	if err != nil {
		t.Fatalf("super list: %v", err)
	}
	if len(super) != 2 {
		t.Errorf("super sees %d resellers, want 2", len(super))
	}
}

// A balance reveals how much business a competitor is doing.
func TestBalanceIsScoped(t *testing.T) {
	f := seedTenantFixture(t)

	own, err := f.s.BalanceScoped(context.Background(), f.scope("alice"),
		f.ids["reseller_alice"])
	if err != nil {
		t.Fatalf("own balance: %v", err)
	}
	if own != 5000 {
		t.Errorf("alice balance = %d, want 5000", own)
	}

	// Bob's balance must not be readable, and must not come back as a bare 0
	// that a caller could mistake for a real answer.
	got, err := f.s.BalanceScoped(context.Background(), f.scope("alice"),
		f.ids["reseller_bob"])
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("alice read bob's balance: %d (err=%v)", got, err)
	}
}

// Ledger scoping is applied to the reseller, not the movement rows.
func TestLedgerIsScopedByReseller(t *testing.T) {
	f := seedTenantFixture(t)

	own, err := f.s.ListLedgerScoped(context.Background(), f.scope("alice"),
		f.ids["reseller_alice"], 100)
	if err != nil {
		t.Fatalf("own ledger: %v", err)
	}
	if len(own) != 1 || own[0].Delta != 5000 {
		t.Fatalf("alice ledger = %+v", own)
	}

	foreign, err := f.s.ListLedgerScoped(context.Background(), f.scope("alice"),
		f.ids["reseller_bob"], 100)
	if err != nil {
		t.Fatalf("foreign ledger: %v", err)
	}
	if len(foreign) != 0 {
		t.Errorf("alice read %d of bob's credit movements: %+v", len(foreign), foreign)
	}
}

// A scope with a zero AdminID -- the value an unauthenticated or malformed
// actor produces -- must see nothing rather than everything.
//
// This is the fail-closed direction. rbac.ScopeOf returns a zero Scope for a
// nil actor, so a handler that forgot to authenticate hands us AdminID=0 and
// IsSuper=false. If that matched rows, a missing auth check would become a
// full data breach rather than an empty list.
func TestZeroScopeSeesNothing(t *testing.T) {
	f := seedTenantFixture(t)
	var zero rbac.Scope

	subjects, err := f.s.ListSubjectsScoped(context.Background(), zero)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(subjects) != 0 {
		t.Errorf("a zero scope saw %d subjects; an unauthenticated caller must "+
			"see nothing", len(subjects))
	}

	resellers, err := f.s.ListResellersScoped(context.Background(), zero)
	if err != nil {
		t.Fatalf("list resellers: %v", err)
	}
	if len(resellers) != 0 {
		t.Errorf("a zero scope saw %d resellers", len(resellers))
	}

	if _, err := f.s.GetSubjectScoped(context.Background(), zero,
		f.ids["alice-user"]); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("a zero scope read a subject (err=%v)", err)
	}
}

// An admin whose reseller row is deleted must lose access immediately, rather
// than retaining it through a stale ownership row.
func TestAccessFollowsTheResellerRecord(t *testing.T) {
	f := seedTenantFixture(t)
	alice := f.scope("alice")

	if _, err := f.s.GetSubjectScoped(context.Background(), alice,
		f.ids["alice-user"]); err != nil {
		t.Fatalf("precondition: alice cannot read her own subject: %v", err)
	}

	// Remove ownership, then the reseller. RESTRICT forbids the reverse order,
	// which is itself the guarantee tested elsewhere.
	err := f.s.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM reseller_subjects WHERE reseller_id = ?`,
			f.ids["reseller_alice"]); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM resellers WHERE id = ?`, f.ids["reseller_alice"])
		return err
	})
	if err != nil {
		t.Fatalf("remove reseller: %v", err)
	}

	if _, err := f.s.GetSubjectScoped(context.Background(), alice,
		f.ids["alice-user"]); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("a removed reseller still reads their former customer (err=%v)", err)
	}
}

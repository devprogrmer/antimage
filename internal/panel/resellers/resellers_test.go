package resellers

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/panel/subjects"
	"github.com/amyrm/antimage/internal/shared/secrets"
)

var testNow = time.Unix(1_700_000_000, 0).UTC()

type fixture struct {
	db       *store.Store
	rs       *Store
	subjects *subjects.Store
	adminID  int64
	svcID    int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	key := make([]byte, secrets.KeySize)
	for i := range key {
		key[i] = byte(i + 7)
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}
	subjStore := subjects.NewStore(db, box, func() time.Time { return testNow })

	f := &fixture{
		db:       db,
		subjects: subjStore,
		rs:       NewStore(db, subjStore, func() time.Time { return testNow }),
	}

	err = db.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`INSERT INTO roles (name, is_builtin, permissions) VALUES ('reseller', 1, '[]')`)
		if err != nil {
			return err
		}
		roleID, _ := res.LastInsertId()

		res, err = tx.Exec(
			`INSERT INTO admins (username, password_hash, role_id, created_at)
			 VALUES ('reseller-bob','x',?,?)`, roleID, testNow.Unix())
		if err != nil {
			return err
		}
		f.adminID, _ = res.LastInsertId()

		if _, err := tx.Exec(
			`INSERT INTO nodes (id, name, address, created_at) VALUES (1,'n1','1.1.1.1',?)`,
			testNow.Unix()); err != nil {
			return err
		}
		res, err = tx.Exec(
			`INSERT INTO services (node_id, adapter_kind, params, enabled, created_at)
			 VALUES (1,'xray','{"protocol":"vless","port":443}',1,?)`, testNow.Unix())
		if err != nil {
			return err
		}
		f.svcID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return f
}

// createReseller inserts a reseller and returns its id.
func (f *fixture) createReseller(t *testing.T, floor int64, maxSubjects, maxQuota *int64) int64 {
	t.Helper()
	var id int64
	err := f.db.Write(context.Background(), func(tx *sql.Tx) error {
		var ms, mq any
		if maxSubjects != nil {
			ms = *maxSubjects
		}
		if maxQuota != nil {
			mq = *maxQuota
		}
		res, err := tx.Exec(
			`INSERT INTO resellers
			   (admin_id, display_name, enabled, max_subjects, max_quota_bytes,
			    credit_floor, created_at, updated_at)
			 VALUES (?,?,1,?,?,?,?,?)`,
			f.adminID, "bob-vpn", ms, mq, floor, testNow.Unix(), testNow.Unix())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("create reseller: %v", err)
	}
	return id
}

func (f *fixture) topup(t *testing.T, resellerID, amount int64, key string) {
	t.Helper()
	err := f.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := f.rs.Credit(context.Background(), tx, CreditInput{
			ResellerID: resellerID, Delta: amount, Reason: ReasonTopup,
			IdempotencyKey: key,
		})
		return err
	})
	if err != nil {
		t.Fatalf("topup: %v", err)
	}
}

func (f *fixture) balance(t *testing.T, resellerID int64) int64 {
	t.Helper()
	b, err := f.rs.BalanceRead(context.Background(), resellerID)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	return b
}

func provisionInput(resellerID, svcID, cost int64, name, key string) ProvisionInput {
	return ProvisionInput{
		ResellerID: resellerID,
		Cost:       cost,
		Subject: subjects.CreateInput{
			Name:       name,
			ServiceIDs: []int64{svcID},
		},
		IdempotencyKey: key,
		Actor:          audit.SystemActor("test"),
	}
}

// INVARIANT 13: balance is always SUM(delta). Nothing caches it.
//
// A stored balance that disagrees with its ledger is unresolvable -- you
// cannot tell which is wrong. This asserts the schema offers no place to
// store one, so the invariant holds by construction rather than by discipline.
func TestInvariant13_NoCachedBalanceColumnExists(t *testing.T) {
	f := newFixture(t)

	// Inspect real columns, not the DDL text: the DDL carries comments, and a
	// comment mentioning "balance" is not a cached balance.
	rows, err := f.db.Read().Query(`SELECT name FROM pragma_table_info('resellers')`)
	if err != nil {
		t.Fatalf("read columns: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var columns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		columns = append(columns, strings.ToLower(name))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("columns: %v", err)
	}
	if len(columns) == 0 {
		t.Fatal("resellers table has no columns; this test would pass vacuously")
	}

	// credit_floor is a POLICY limit, not a running total, so it is allowed.
	for _, c := range columns {
		if c == "balance" || c == "credits" || c == "credit_remaining" ||
			c == "credit_balance" {
			t.Errorf("resellers has a %q column; a cached balance can disagree with "+
				"the ledger and there is no safe way to resolve which is right", c)
		}
	}
	t.Logf("columns: %v", columns)
}

func TestInvariant13_BalanceIsTheSumOfMovements(t *testing.T) {
	f := newFixture(t)
	id := f.createReseller(t, 0, nil, nil)

	if got := f.balance(t, id); got != 0 {
		t.Errorf("a new reseller has balance %d, want 0", got)
	}

	f.topup(t, id, 1000, "k1")
	f.topup(t, id, 500, "k2")
	if got := f.balance(t, id); got != 1500 {
		t.Errorf("balance = %d, want 1500", got)
	}

	// A negative movement reduces it.
	err := f.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := f.rs.Credit(context.Background(), tx, CreditInput{
			ResellerID: id, Delta: -200, Reason: ReasonAdjustment, IdempotencyKey: "k3",
		})
		return err
	})
	if err != nil {
		t.Fatalf("adjust: %v", err)
	}
	if got := f.balance(t, id); got != 1300 {
		t.Errorf("balance = %d, want 1300", got)
	}

	// And it equals a direct sum of the ledger, not a parallel counter.
	var direct sql.NullInt64
	_ = f.db.Read().QueryRow(
		`SELECT sum(delta) FROM reseller_credit_ledger WHERE reseller_id = ?`, id).Scan(&direct)
	if direct.Int64 != 1300 {
		t.Errorf("ledger sums to %d but Balance reported 1300", direct.Int64)
	}
}

// INVARIANT 14: the debit and the subject it pays for commit together.
func TestInvariant14_ProvisionDebitsAndCreatesAtomically(t *testing.T) {
	f := newFixture(t)
	id := f.createReseller(t, 0, nil, nil)
	f.topup(t, id, 1000, "k1")

	var out ProvisionResult
	err := f.db.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		out, err = f.rs.ProvisionSubject(context.Background(), tx,
			provisionInput(id, f.svcID, 300, "alice", "prov-1"))
		return err
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}

	if out.SubjectID == 0 {
		t.Fatal("no subject was created")
	}
	if out.Balance != 700 {
		t.Errorf("reported balance %d, want 700", out.Balance)
	}
	if got := f.balance(t, id); got != 700 {
		t.Errorf("persisted balance %d, want 700", got)
	}
	if len(out.NodeIDs) != 1 || out.NodeIDs[0] != 1 {
		t.Errorf("NodeIDs = %v, want [1]; the caller could not republish", out.NodeIDs)
	}

	// Ownership recorded.
	var owner int64
	if err := f.db.Read().QueryRow(
		`SELECT reseller_id FROM reseller_subjects WHERE subject_id = ?`,
		out.SubjectID).Scan(&owner); err != nil {
		t.Fatalf("ownership not recorded: %v", err)
	}
	if owner != id {
		t.Errorf("owner = %d, want %d", owner, id)
	}

	// The ledger movement points at the subject it paid for.
	var linked sql.NullInt64
	_ = f.db.Read().QueryRow(
		`SELECT subject_id FROM reseller_credit_ledger WHERE id = ?`, out.LedgerID).Scan(&linked)
	if !linked.Valid || linked.Int64 != out.SubjectID {
		t.Errorf("ledger movement is not linked to the subject it paid for")
	}
}

// The other half of atomicity: a failure after the debit must leave NOTHING.
//
// Simulated by failing the enclosing transaction after ProvisionSubject
// returns, which is exactly what a later error in the same handler would do.
func TestInvariant14_AFailedTransactionChargesNothing(t *testing.T) {
	f := newFixture(t)
	id := f.createReseller(t, 0, nil, nil)
	f.topup(t, id, 1000, "k1")

	sentinel := errors.New("something failed after provisioning")
	err := f.db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := f.rs.ProvisionSubject(context.Background(), tx,
			provisionInput(id, f.svcID, 300, "alice", "prov-1")); err != nil {
			return err
		}
		return sentinel // force rollback
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the sentinel", err)
	}

	if got := f.balance(t, id); got != 1000 {
		t.Errorf("balance = %d after a rolled-back provision, want 1000: "+
			"the reseller was charged for a customer that does not exist", got)
	}
	var subjectCount, ownerCount int
	_ = f.db.Read().QueryRow(`SELECT count(*) FROM subjects`).Scan(&subjectCount)
	_ = f.db.Read().QueryRow(`SELECT count(*) FROM reseller_subjects`).Scan(&ownerCount)
	if subjectCount != 0 || ownerCount != 0 {
		t.Errorf("rollback left %d subject(s) and %d ownership row(s)",
			subjectCount, ownerCount)
	}
}

// A retry after an ambiguous failure must not double-charge or duplicate.
func TestProvisionIsIdempotent(t *testing.T) {
	f := newFixture(t)
	id := f.createReseller(t, 0, nil, nil)
	f.topup(t, id, 1000, "k1")

	run := func() ProvisionResult {
		var out ProvisionResult
		err := f.db.Write(context.Background(), func(tx *sql.Tx) error {
			var err error
			out, err = f.rs.ProvisionSubject(context.Background(), tx,
				provisionInput(id, f.svcID, 300, "alice", "same-key"))
			return err
		})
		if err != nil {
			t.Fatalf("provision: %v", err)
		}
		return out
	}

	first := run()
	for i := 0; i < 3; i++ {
		again := run()
		if again.SubjectID != first.SubjectID {
			t.Fatalf("retry %d created a different subject: %d vs %d",
				i, again.SubjectID, first.SubjectID)
		}
	}

	if got := f.balance(t, id); got != 700 {
		t.Errorf("balance = %d after 4 identical calls, want 700", got)
	}
	var movements int
	_ = f.db.Read().QueryRow(
		`SELECT count(*) FROM reseller_credit_ledger WHERE reason = 'provision'`).Scan(&movements)
	if movements != 1 {
		t.Errorf("%d provision movements, want exactly 1", movements)
	}
}

// A top-up retried after a network failure must not credit twice.
func TestTopupIsIdempotent(t *testing.T) {
	f := newFixture(t)
	id := f.createReseller(t, 0, nil, nil)

	for i := 0; i < 5; i++ {
		f.topup(t, id, 1000, "same-topup")
	}
	if got := f.balance(t, id); got != 1000 {
		t.Errorf("balance = %d after 5 retries of one top-up, want 1000", got)
	}
}

func TestCreditRequiresAnIdempotencyKey(t *testing.T) {
	f := newFixture(t)
	id := f.createReseller(t, 0, nil, nil)

	err := f.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := f.rs.Credit(context.Background(), tx, CreditInput{
			ResellerID: id, Delta: 100, Reason: ReasonTopup,
		})
		return err
	})
	if err == nil {
		t.Fatal("accepted a credit movement with no idempotency key; a retry would double-credit")
	}
}

// Selling beyond the credit floor must be refused, and must charge nothing.
func TestInsufficientCreditRefusesAndChargesNothing(t *testing.T) {
	f := newFixture(t)
	id := f.createReseller(t, 0, nil, nil)
	f.topup(t, id, 100, "k1")

	err := f.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := f.rs.ProvisionSubject(context.Background(), tx,
			provisionInput(id, f.svcID, 300, "alice", "prov-1"))
		return err
	})
	if !errors.Is(err, ErrInsufficientCredit) {
		t.Fatalf("err = %v, want ErrInsufficientCredit", err)
	}
	if got := f.balance(t, id); got != 100 {
		t.Errorf("balance = %d, want 100 untouched", got)
	}
	var n int
	_ = f.db.Read().QueryRow(`SELECT count(*) FROM subjects`).Scan(&n)
	if n != 0 {
		t.Errorf("%d subject(s) created despite the refusal", n)
	}
}

// A post-paid reseller may go negative, down to their floor and no further.
func TestCreditFloorAllowsPostPaidResellers(t *testing.T) {
	f := newFixture(t)
	id := f.createReseller(t, -500, nil, nil)

	err := f.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := f.rs.ProvisionSubject(context.Background(), tx,
			provisionInput(id, f.svcID, 400, "alice", "p1"))
		return err
	})
	if err != nil {
		t.Fatalf("a post-paid reseller was refused within their floor: %v", err)
	}
	if got := f.balance(t, id); got != -400 {
		t.Errorf("balance = %d, want -400", got)
	}

	// One step beyond the floor is refused.
	err = f.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := f.rs.ProvisionSubject(context.Background(), tx,
			provisionInput(id, f.svcID, 200, "carol", "p2"))
		return err
	})
	if !errors.Is(err, ErrInsufficientCredit) {
		t.Fatalf("err = %v, want the floor to be enforced", err)
	}
}

func TestSubjectCeilingIsEnforced(t *testing.T) {
	f := newFixture(t)
	max := int64(2)
	id := f.createReseller(t, 0, &max, nil)
	f.topup(t, id, 100000, "k1")

	for i, name := range []string{"a", "b"} {
		err := f.db.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := f.rs.ProvisionSubject(context.Background(), tx,
				provisionInput(id, f.svcID, 1, name, "p"+name))
			return err
		})
		if err != nil {
			t.Fatalf("provision %d: %v", i, err)
		}
	}

	err := f.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := f.rs.ProvisionSubject(context.Background(), tx,
			provisionInput(id, f.svcID, 1, "c", "pc"))
		return err
	})
	if !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("err = %v, want ErrLimitExceeded; credit was ample", err)
	}
}

// A disabled reseller must not be able to transact.
func TestDisabledResellerCannotProvision(t *testing.T) {
	f := newFixture(t)
	id := f.createReseller(t, 0, nil, nil)
	f.topup(t, id, 1000, "k1")

	if _, err := f.db.Read().Exec(
		`UPDATE resellers SET enabled = 0 WHERE id = ?`, id); err != nil {
		t.Fatalf("disable: %v", err)
	}

	err := f.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := f.rs.ProvisionSubject(context.Background(), tx,
			provisionInput(id, f.svcID, 100, "alice", "p1"))
		return err
	})
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("err = %v, want ErrDisabled", err)
	}
}

// INVARIANT 15 (schema half): deleting a reseller with live customers must
// fail loudly rather than orphaning paying users.
func TestDeletingAResellerWithCustomersIsRefused(t *testing.T) {
	f := newFixture(t)
	id := f.createReseller(t, 0, nil, nil)
	f.topup(t, id, 1000, "k1")

	err := f.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := f.rs.ProvisionSubject(context.Background(), tx,
			provisionInput(id, f.svcID, 100, "alice", "p1"))
		return err
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}

	err = f.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM resellers WHERE id = ?`, id)
		return err
	})
	if err == nil {
		t.Fatal("deleted a reseller that still owns customers; those users would " +
			"keep being served with nobody accountable for them")
	}
}

// Ownership lookup is the read the scope predicate is built on.
func TestOwnerOfDistinguishesPlatformOwnedSubjects(t *testing.T) {
	f := newFixture(t)
	id := f.createReseller(t, 0, nil, nil)
	f.topup(t, id, 1000, "k1")

	var resold int64
	err := f.db.Write(context.Background(), func(tx *sql.Tx) error {
		out, err := f.rs.ProvisionSubject(context.Background(), tx,
			provisionInput(id, f.svcID, 100, "alice", "p1"))
		resold = out.SubjectID
		return err
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}

	// A platform-owned subject, created directly.
	var direct int64
	err = f.db.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		direct, err = f.subjects.Create(context.Background(), tx, subjects.CreateInput{
			Name: "platform-user", ServiceIDs: []int64{f.svcID},
		})
		return err
	})
	if err != nil {
		t.Fatalf("create direct subject: %v", err)
	}

	err = f.db.Write(context.Background(), func(tx *sql.Tx) error {
		owner, err := f.rs.OwnerOf(context.Background(), tx, resold)
		if err != nil {
			t.Errorf("resold subject has no owner: %v", err)
		} else if owner != id {
			t.Errorf("owner = %d, want %d", owner, id)
		}

		if _, err := f.rs.OwnerOf(context.Background(), tx, direct); !errors.Is(err, ErrNotFound) {
			t.Errorf("platform-owned subject reported owner err = %v, want ErrNotFound", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("owner checks: %v", err)
	}
}

// Provisioning must be audited, and the record must not carry credentials.
func TestProvisionIsAuditedWithoutCredentials(t *testing.T) {
	f := newFixture(t)
	id := f.createReseller(t, 0, nil, nil)
	f.topup(t, id, 1000, "k1")

	var subjectID int64
	err := f.db.Write(context.Background(), func(tx *sql.Tx) error {
		out, err := f.rs.ProvisionSubject(context.Background(), tx,
			provisionInput(id, f.svcID, 250, "alice", "p1"))
		subjectID = out.SubjectID
		return err
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}

	var action, after string
	var target sql.NullInt64
	if err := f.db.Read().QueryRow(
		`SELECT action, coalesce(after_json,''), target_id FROM audit_log
		  WHERE action = 'reseller.provision' ORDER BY id DESC LIMIT 1`).
		Scan(&action, &after, &target); err != nil {
		t.Fatalf("no audit record for a provision: %v", err)
	}
	if !target.Valid || target.Int64 != subjectID {
		t.Errorf("audit target = %v, want subject %d", target, subjectID)
	}
	if !strings.Contains(after, "250") {
		t.Errorf("audit record does not carry the cost: %s", after)
	}

	// The credential the subject was created with must not be in the record.
	cred, err := f.subjects.Credential(context.Background(), subjectID, "uuid")
	if err != nil {
		t.Fatalf("read credential: %v", err)
	}
	if cred != "" && strings.Contains(after, cred) {
		t.Error("SECURITY: the provision audit record contains the subject's credential")
	}
}

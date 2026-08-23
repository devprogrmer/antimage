package service

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/resellers"
	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/panel/subjects"
	"github.com/amyrm/antimage/internal/shared/secrets"
)

var testNow = time.Unix(1_700_000_000, 0).UTC()

// recorder stands in for control.Hub so a test can assert that republishing
// actually woke the agents it should have.
type recorder struct{ woken []int64 }

func (r *recorder) Notify(nodeID, _ int64) bool { r.woken = append(r.woken, nodeID); return true }

type fixture struct {
	db    *store.Store
	svc   *Subjects
	hub   *recorder
	svcID int64
	// alice and bob are competing resellers; each owns one customer.
	alice, bob       *rbac.Actor
	aliceSub, bobSub int64
	super            *rbac.Actor
	platformSub      int64
}

func perms(p ...rbac.Permission) map[rbac.Permission]struct{} {
	m := map[rbac.Permission]struct{}{}
	for _, x := range p {
		m[x] = struct{}{}
	}
	return m
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
		key[i] = byte(i + 5)
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}

	subjStore := subjects.NewStore(db, box, func() time.Time { return testNow })
	rsStore := resellers.NewStore(db, subjStore, func() time.Time { return testNow })
	hub := &recorder{}
	f := &fixture{db: db, hub: hub}
	f.svc = NewSubjects(db, subjStore, rsStore, hub,
		func() time.Time { return testNow }, nodes.WithUnsealer(box))

	resellerPerms := perms(rbac.PermSubjectRead, rbac.PermSubjectWrite, rbac.PermCredReveal)

	err = db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`INSERT INTO roles (id,name,is_builtin,permissions) VALUES (1,'reseller',1,'[]')`); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO nodes (id,name,address,created_at) VALUES (1,'n1','1.1.1.1',?)`,
			testNow.Unix()); err != nil {
			return err
		}
		res, err := tx.Exec(
			`INSERT INTO services (node_id,adapter_kind,params,enabled,created_at)
			 VALUES (1,'xray','{"protocol":"vless","port":443}',1,?)`, testNow.Unix())
		if err != nil {
			return err
		}
		f.svcID, _ = res.LastInsertId()

		for _, who := range []string{"alice", "bob"} {
			res, err := tx.Exec(
				`INSERT INTO admins (username,password_hash,role_id,created_at)
				 VALUES (?, 'x', 1, ?)`, who, testNow.Unix())
			if err != nil {
				return err
			}
			adminID, _ := res.LastInsertId()

			res, err = tx.Exec(
				`INSERT INTO resellers (admin_id,display_name,enabled,credit_floor,created_at,updated_at)
				 VALUES (?,?,1,0,?,?)`, adminID, who+"-vpn", testNow.Unix(), testNow.Unix())
			if err != nil {
				return err
			}
			resellerID, _ := res.LastInsertId()

			subjectID, err := subjStore.Create(context.Background(), tx, subjects.CreateInput{
				Name: who + "-customer", ServiceIDs: []int64{f.svcID},
			})
			if err != nil {
				return err
			}
			if _, err := tx.Exec(
				`INSERT INTO reseller_subjects (subject_id,reseller_id,cost,created_at)
				 VALUES (?,?,10,?)`, subjectID, resellerID, testNow.Unix()); err != nil {
				return err
			}
			if _, err := tx.Exec(
				`INSERT INTO reseller_credit_ledger (reseller_id,delta,reason,idempotency_key,at)
				 VALUES (?,100000,'topup',?,?)`, resellerID, who+"-topup", testNow.Unix()); err != nil {
				return err
			}

			actor := &rbac.Actor{AdminID: adminID, RoleName: "reseller", Perms: resellerPerms}
			if who == "alice" {
				f.alice, f.aliceSub = actor, subjectID
			} else {
				f.bob, f.bobSub = actor, subjectID
			}
		}

		// A platform-owned customer, belonging to no reseller.
		f.platformSub, err = subjStore.Create(context.Background(), tx, subjects.CreateInput{
			Name: "house-account", ServiceIDs: []int64{f.svcID},
		})
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	f.super = &rbac.Actor{AdminID: 999, RoleName: "super_admin", IsSuper: true,
		Perms: perms(rbac.PermSubjectRead, rbac.PermSubjectWrite, rbac.PermCredReveal)}
	return f
}

func actorFor(a *rbac.Actor, via string) Actor {
	return Actor{RBAC: a, Audit: audit.SystemActor("test"), RequestID: "req-1", Via: via}
}

// The service layer must enforce tenant isolation ON ITS OWN.
//
// This is the whole point of extracting it: the Telegram bot will call these
// methods with no HTTP middleware in front of them. If isolation lived only in
// the handlers, every bot command would be a bypass.
func TestServiceEnforcesIsolationWithoutHandlers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice := actorFor(f.alice, "telegram")

	list, err := f.svc.List(ctx, alice)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != f.aliceSub {
		t.Fatalf("alice sees %d subject(s), want only her own", len(list))
	}

	if _, err := f.svc.Get(ctx, alice, f.bobSub); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get on a foreign subject returned %v, want ErrNotFound", err)
	}
	if _, err := f.svc.Get(ctx, alice, f.platformSub); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get on a platform-owned subject returned %v, want ErrNotFound", err)
	}
	if _, err := f.svc.Get(ctx, alice, f.aliceSub); err != nil {
		t.Errorf("alice cannot read her own subject: %v", err)
	}
}

// The credential path is the highest-value read in the panel.
func TestServiceCredentialIsTenantScoped(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if v, err := f.svc.Credential(ctx, actorFor(f.alice, "telegram"), f.bobSub, "uuid"); err == nil {
		t.Fatalf("SECURITY: alice revealed bob's customer credential: %q", v)
	} else if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound (indistinguishable from missing)", err)
	}

	if _, err := f.svc.Credential(ctx, actorFor(f.alice, "telegram"), f.aliceSub, "uuid"); err != nil {
		t.Errorf("alice cannot reveal her own customer's credential: %v", err)
	}
}

// Every mutation must be scoped, not just the reads.
func TestServiceMutationsAreTenantScoped(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice := actorFor(f.alice, "telegram")

	cases := map[string]func() error{
		"disable": func() error { return f.svc.SetEnabled(ctx, alice, f.bobSub, false) },
		"freeze":  func() error { return f.svc.SetFrozen(ctx, alice, f.bobSub, true, "x") },
		"delete":  func() error { return f.svc.Delete(ctx, alice, f.bobSub) },
		"rotate": func() error {
			_, err := f.svc.RotateCredential(ctx, alice, f.bobSub, "uuid")
			return err
		},
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			if err := fn(); !errors.Is(err, ErrNotFound) {
				t.Errorf("%s on a foreign subject returned %v, want ErrNotFound", name, err)
			}
		})
	}

	// Bob's customer must be untouched and still enabled.
	got, err := f.svc.Get(ctx, actorFor(f.bob, "http"), f.bobSub)
	if err != nil {
		t.Fatalf("bob's subject is gone: %v", err)
	}
	if !got.Enabled {
		t.Error("alice disabled bob's customer through the service layer")
	}
}

// A caller with the scope but not the permission must still be refused.
// Both layers, not either.
func TestServiceRequiresThePermissionNotJustTheScope(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	readOnly := &rbac.Actor{
		AdminID: f.alice.AdminID, RoleName: "reseller",
		Perms: perms(rbac.PermSubjectRead), // no write, no reveal
	}
	a := actorFor(readOnly, "telegram")

	if err := f.svc.SetEnabled(ctx, a, f.aliceSub, false); err == nil {
		t.Error("a read-only actor disabled a subject it owns")
	} else if errors.Is(err, ErrNotFound) {
		t.Error("permission failure was reported as not-found; it should surface as denied")
	}
	if _, err := f.svc.Credential(ctx, a, f.aliceSub, "uuid"); err == nil {
		t.Error("a caller without credential:reveal revealed a credential")
	}
}

// A mutation must republish every node the subject reaches, or the panel and
// the node disagree about who is served.
func TestServiceRepublishesAffectedNodes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	var before int64
	_ = f.db.Read().QueryRow(`SELECT desired_revision FROM nodes WHERE id = 1`).Scan(&before)

	if err := f.svc.SetEnabled(ctx, actorFor(f.alice, "telegram"), f.aliceSub, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	var after int64
	_ = f.db.Read().QueryRow(`SELECT desired_revision FROM nodes WHERE id = 1`).Scan(&after)
	if after <= before {
		t.Errorf("revision did not move: %d -> %d; the node still serves them", before, after)
	}
	if len(f.hub.woken) == 0 {
		t.Error("no agent was woken, so the change waits for the next poll")
	}
}

// The channel a change arrived through must reach the audit trail. An incident
// review has to tell a browser action from one made through a chat account
// that may have been hijacked.
func TestServiceRecordsTheChannelInAudit(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if err := f.svc.SetEnabled(ctx, actorFor(f.alice, "telegram"), f.aliceSub, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	var after string
	if err := f.db.Read().QueryRow(
		`SELECT coalesce(after_json,'') FROM audit_log
		  WHERE action = 'subject.disable' ORDER BY id DESC LIMIT 1`).Scan(&after); err != nil {
		t.Fatalf("no audit record: %v", err)
	}
	if !strings.Contains(after, "telegram") {
		t.Errorf("audit record does not record the channel: %s", after)
	}
}

// A super admin still sees everything, so the filter is real rather than
// everything being hidden from everyone.
func TestSuperAdminSeesAllSubjects(t *testing.T) {
	f := newFixture(t)
	list, err := f.svc.List(context.Background(), actorFor(f.super, "http"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("super sees %d subjects, want 3 (alice, bob, platform)", len(list))
	}
}

// Provisioning through the service debits credit and creates the customer in
// one transaction, then republishes.
func TestServiceProvisionDebitsAndPublishes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	var resellerID int64
	_ = f.db.Read().QueryRow(
		`SELECT id FROM resellers WHERE admin_id = ?`, f.alice.AdminID).Scan(&resellerID)

	out, err := f.svc.Provision(ctx, actorFor(f.alice, "telegram"), resellers.ProvisionInput{
		ResellerID: resellerID,
		Cost:       250,
		Subject: subjects.CreateInput{
			Name: "new-customer", ServiceIDs: []int64{f.svcID},
		},
		IdempotencyKey: "prov-1",
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if out.SubjectID == 0 {
		t.Fatal("no subject created")
	}
	if out.Balance != 100000-250 {
		t.Errorf("balance = %d, want %d", out.Balance, 100000-250)
	}
	if len(f.hub.woken) == 0 {
		t.Error("provisioning did not wake the node")
	}

	// And the new customer is visible to alice, but not to bob.
	if _, err := f.svc.Get(ctx, actorFor(f.alice, "telegram"), out.SubjectID); err != nil {
		t.Errorf("alice cannot see the customer she just created: %v", err)
	}
	if _, err := f.svc.Get(ctx, actorFor(f.bob, "telegram"), out.SubjectID); !errors.Is(err, ErrNotFound) {
		t.Error("bob can see a customer alice just provisioned")
	}
}

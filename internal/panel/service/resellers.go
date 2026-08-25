package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/resellers"
	"github.com/amyrm/antimage/internal/panel/store"
)

// Resellers is the orchestration path for tenancy.
//
// Same shape as Subjects and for the same reason: HTTP, the Telegram bot and
// anything added later enter through here, so permission checks, scope, the
// transaction boundary and audit exist once rather than once per caller.
type Resellers struct {
	db      *store.Store
	sellers *resellers.Store
	now     func() time.Time
}

func NewResellers(db *store.Store, sellers *resellers.Store, now func() time.Time) *Resellers {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Resellers{db: db, sellers: sellers, now: now}
}

// ErrResellerNotFound covers both "no such reseller" and "not yours".
//
// One error for both on purpose: distinguishing them lets a caller walk the id
// space to count tenants, which is the same enumeration oracle the subject
// predicate is phrased to avoid.
var ErrResellerNotFound = errors.New("reseller not found")

// platformScope is the scope for the tenancy MANAGEMENT api.
//
// reseller:read and reseller:write are platform-wide by design: the reseller
// role is denied them precisely because, as docs/TENANT-ISOLATION.md puts it,
// granting reseller:read "would let one tenant enumerate the others". Holding
// one therefore already means platform staff.
//
// Applying the tenant predicate on top of that check made the permission
// unusable for the role that is meant to have it: an admin operates no tenant,
// so (is_super OR resellers.admin_id = ?) matched nothing and every management
// call 404ed. The predicate is phrased for tenant SELF-SERVICE, which is served
// separately through /me and stays scoped.
//
// Reached only after rbac.Check has passed, so this widens nothing on its own:
// an actor without the permission never gets here.
func platformScope() rbac.Scope { return rbac.Scope{IsSuper: true} }

// List returns the resellers this caller may see.
func (s *Resellers) List(ctx context.Context, a Actor) ([]store.ResellerRow, error) {
	if err := rbac.Check(a.RBAC, rbac.PermResellerRead, rbac.Target{Kind: rbac.TargetNone}); err != nil {
		return nil, err
	}
	return s.db.ListResellersScoped(ctx, platformScope())
}

// Get returns one reseller, or ErrResellerNotFound if it is missing or out of
// scope.
func (s *Resellers) Get(ctx context.Context, a Actor, id int64) (*store.ResellerRow, error) {
	if err := rbac.Check(a.RBAC, rbac.PermResellerRead, rbac.Target{Kind: rbac.TargetNone}); err != nil {
		return nil, err
	}
	row, err := s.db.GetResellerScoped(ctx, platformScope(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrResellerNotFound
	}
	return row, err
}

// Balance reports a reseller's credit position.
func (s *Resellers) Balance(ctx context.Context, a Actor, id int64) (int64, error) {
	if err := rbac.Check(a.RBAC, rbac.PermResellerRead, rbac.Target{Kind: rbac.TargetNone}); err != nil {
		return 0, err
	}
	bal, err := s.db.BalanceScoped(ctx, platformScope(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrResellerNotFound
	}
	return bal, err
}

// Ledger returns a reseller's credit movements, most recent first.
func (s *Resellers) Ledger(
	ctx context.Context, a Actor, id int64, limit int,
) ([]store.LedgerRow, error) {
	if err := rbac.Check(a.RBAC, rbac.PermResellerRead, rbac.Target{Kind: rbac.TargetNone}); err != nil {
		return nil, err
	}
	// Scope is applied to the RESELLER inside ListLedgerScoped, not to the
	// movements: a movement is visible exactly when its reseller is.
	rows, err := s.db.ListLedgerScoped(ctx, platformScope(), id, limit)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrResellerNotFound
	}
	return rows, err
}

// Create adds a tenant.
func (s *Resellers) Create(
	ctx context.Context, a Actor, in resellers.CreateInput,
) (int64, error) {
	if err := rbac.Check(a.RBAC, rbac.PermResellerWrite, rbac.Target{Kind: rbac.TargetNone}); err != nil {
		return 0, err
	}
	var id int64
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = s.sellers.Create(ctx, tx, in)
		if err != nil {
			return err
		}
		return audit.InTx(ctx, tx, a.RequestID, a.Audit, audit.Record{
			Action: "reseller.create", TargetType: "reseller",
			TargetID: sql.NullInt64{Int64: id, Valid: true},
			After: map[string]any{
				"display_name": in.DisplayName, "admin_id": in.AdminID,
				"credit_floor": in.CreditFloor, "via": a.Via,
			},
			Result: "ok",
		})
	})
	return id, err
}

// Update edits a tenant.
func (s *Resellers) Update(
	ctx context.Context, a Actor, id int64, in resellers.UpdateInput,
) error {
	if err := rbac.Check(a.RBAC, rbac.PermResellerWrite, rbac.Target{Kind: rbac.TargetNone}); err != nil {
		return err
	}
	// Scope before write. A non-super actor holding reseller:write must still
	// only reach tenants they own, and the read is what establishes that.
	if _, err := s.Get(ctx, a, id); err != nil {
		return err
	}

	return s.db.Write(ctx, func(tx *sql.Tx) error {
		if err := s.sellers.Update(ctx, tx, id, in); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrResellerNotFound
			}
			return err
		}
		return audit.InTx(ctx, tx, a.RequestID, a.Audit, audit.Record{
			Action: "reseller.update", TargetType: "reseller",
			TargetID: sql.NullInt64{Int64: id, Valid: true},
			After:    map[string]any{"via": a.Via},
			Result:   "ok",
		})
	})
}

// GrantCredit records a credit movement.
//
// Gated on credit:grant, which is deliberately NOT implied by reseller:write.
// Minting credit is the only operation in the panel that creates value from
// nothing; if one permission covered both, anyone who could rename a tenant
// could also pay themselves. Only super_admin holds it by default.
func (s *Resellers) GrantCredit(
	ctx context.Context, a Actor, in resellers.CreditInput,
) (ledgerID, balance int64, err error) {
	if err := rbac.Check(a.RBAC, rbac.PermCreditGrant, rbac.Target{Kind: rbac.TargetNone}); err != nil {
		return 0, 0, err
	}
	if a.RBAC != nil {
		actorID := a.RBAC.AdminID
		in.ActorAdminID = &actorID
	}

	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		var cerr error
		ledgerID, cerr = s.sellers.Credit(ctx, tx, in)
		if cerr != nil {
			return cerr
		}
		balance, cerr = s.sellers.Balance(ctx, tx, in.ResellerID)
		if cerr != nil {
			return cerr
		}
		return audit.InTx(ctx, tx, a.RequestID, a.Audit, audit.Record{
			Action: "reseller.credit", TargetType: "reseller",
			TargetID: sql.NullInt64{Int64: in.ResellerID, Valid: true},
			// The movement and the resulting balance, never the idempotency
			// key: it is a caller-supplied token and belongs in no record an
			// operator reads.
			After: map[string]any{
				"delta": in.Delta, "reason": in.Reason,
				"balance": balance, "via": a.Via,
			},
			Result: "ok",
		})
	})
	if err != nil {
		return 0, 0, fmt.Errorf("grant credit: %w", err)
	}
	return ledgerID, balance, nil
}

// Delete removes a tenant.
//
// Gated on reseller:write like the other mutations. The store refuses while the
// tenant still owns customers; deactivating through Update is the reversible
// option and is usually what an operator means.
func (s *Resellers) Delete(ctx context.Context, a Actor, id int64) error {
	if err := rbac.Check(a.RBAC, rbac.PermResellerWrite, rbac.Target{Kind: rbac.TargetNone}); err != nil {
		return err
	}
	if _, err := s.Get(ctx, a, id); err != nil {
		return err
	}

	return s.db.Write(ctx, func(tx *sql.Tx) error {
		if err := s.sellers.Delete(ctx, tx, id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrResellerNotFound
			}
			return err
		}
		// Audited inside the transaction. The ledger cascades away with the
		// tenant, so this record is the only surviving account of the deletion.
		return audit.InTx(ctx, tx, a.RequestID, a.Audit, audit.Record{
			Action: "reseller.delete", TargetType: "reseller",
			TargetID: sql.NullInt64{Int64: id, Valid: true},
			After:    map[string]any{"via": a.Via},
			Result:   "ok",
		})
	})
}

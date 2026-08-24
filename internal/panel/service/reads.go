package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/subjects"
)

// ErrNoReseller means the caller is not a tenant, so has no credit balance.
//
// A platform administrator is not a reseller: they mint credit rather than
// hold it. Returning zero for them would read as "no money left" instead of
// "this question does not apply".
var ErrNoReseller = errors.New("caller is not a reseller")

// Balance is a tenant's own credit position.
type Balance struct {
	ResellerID  int64
	DisplayName string
	Enabled     bool
	Balance     int64
	CreditFloor int64
}

// Balance reports the calling tenant's own credit.
//
// Served by SCOPE, not by permission, for the same reason /me is: the reseller
// role deliberately holds no reseller:read, because granting it would let one
// tenant enumerate the others. Reading your own record needs no permission
// because the predicate already guarantees it is yours.
//
// ListResellersScoped is the resolution step. For a tenant its predicate
// reduces to `resellers.admin_id = ?`, so it returns exactly their row and
// nothing else -- there is no separate "reseller for this admin" query to get
// wrong, and no chance of resolving somebody else's tenancy.
func (s *Subjects) Balance(ctx context.Context, a Actor) (Balance, error) {
	if a.RBAC == nil {
		return Balance{}, rbac.ErrForbidden
	}
	// A super admin's scope matches every reseller, so the same lookup would
	// return an arbitrary one. They have no balance of their own.
	if a.RBAC.IsSuper {
		return Balance{}, ErrNoReseller
	}

	rows, err := s.db.ListResellersScoped(ctx, a.scope())
	if err != nil {
		return Balance{}, fmt.Errorf("resolve reseller: %w", err)
	}
	if len(rows) != 1 {
		return Balance{}, ErrNoReseller
	}
	r := rows[0]

	bal, err := s.db.BalanceScoped(ctx, a.scope(), r.ID)
	if err != nil {
		return Balance{}, fmt.Errorf("read balance: %w", err)
	}
	return Balance{
		ResellerID:  r.ID,
		DisplayName: r.DisplayName,
		Enabled:     r.Enabled,
		Balance:     bal,
		CreditFloor: r.CreditFloor,
	}, nil
}

// FindByName resolves a subject by name within the caller's scope.
//
// Names carry a UNIQUE index with COLLATE NOCASE, so at most one can match.
// It filters the scoped list rather than issuing a new query: List already
// applies SubjectScopeSQL, and a second hand-written predicate is a second
// chance to get tenant isolation wrong. The cost is one scoped read per
// lookup, which is the right trade for a chat command.
func (s *Subjects) FindByName(ctx context.Context, a Actor, name string) (*subjects.Subject, error) {
	all, err := s.List(ctx, a)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if strings.EqualFold(all[i].Name, name) {
			return &all[i], nil
		}
	}
	// Same error as an out-of-scope id, so a tenant cannot probe another
	// tenant's customer names by comparing replies.
	return nil, ErrNotFound
}

// SubscriptionToken returns the subject's subscription token, minting one if
// it has none.
//
// Gated on credential:reveal and audited, because the token IS a credential:
// anyone holding it can fetch the subject's full configuration with no session
// at all. That it is a URL rather than a password does not make it less of a
// secret, and handing one to a chat client puts it on somebody else's servers.
func (s *Subjects) SubscriptionToken(ctx context.Context, a Actor, id int64) (string, error) {
	if err := s.authorize(ctx, a, rbac.PermCredReveal, id); err != nil {
		return "", err
	}
	token, err := subjects.EnsureToken(ctx, s.db, id)
	if err != nil {
		return "", fmt.Errorf("issue subscription token: %w", err)
	}
	audit.BestEffort(ctx, s.db, a.RequestID, a.Audit, audit.Record{
		Action: "subscription.reveal", TargetType: "subject",
		TargetID: sql.NullInt64{Int64: id, Valid: true},
		// The token is never recorded, only the fact of the disclosure.
		After:  map[string]any{"via": a.Via},
		Result: "ok",
	})
	return token, nil
}

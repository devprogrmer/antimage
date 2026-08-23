// Package service is the transaction boundary shared by every caller.
//
// It exists because orchestration used to live inside HTTP handlers: begin a
// transaction, call the store, commit, then republish each affected node. Any
// second caller -- the Telegram bot, the CLI, a future gRPC admin API -- would
// have had to reproduce that sequence, and a reproduced sequence is where
// guards get dropped. The cross-tenant credential leak in this codebase
// happened for exactly that reason: reveal was a second path that did not
// repeat the checks the first path made.
//
// Every method here does the same five things in the same order:
//
//  1. check the permission (layer one)
//  2. check the tenant scope (layer two)
//  3. mutate inside one transaction
//  4. republish every affected node through CommitNodeChange
//  5. audit
//
// A caller that skips this package skips all five.
package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/resellers"
	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/panel/subjects"
)

// ErrNotFound is returned for a subject that does not exist AND for one the
// caller may not see.
//
// One error for both, deliberately. Callers map it to 404. A distinct
// "forbidden" would confirm the id is real and let one tenant count another's
// customers by walking the id space.
var ErrNotFound = errors.New("subject not found")

// Notifier wakes an agent after its node's revision moves. The HTTP layer
// passes control.Hub; tests pass a recorder.
type Notifier interface {
	Notify(nodeID, revision int64)
}

// Subjects orchestrates subject lifecycle for every caller.
type Subjects struct {
	db       *store.Store
	subjects *subjects.Store
	sellers  *resellers.Store
	notify   Notifier
	// snapshotOpts carries the unsealer. Without it, building a document for a
	// node that HAS subjects fails rather than publishing one that omits them,
	// which would deprovision every user on that node.
	snapshotOpts []nodes.SnapshotOption
	now          func() time.Time
}

func NewSubjects(
	db *store.Store, subj *subjects.Store, sellers *resellers.Store,
	notify Notifier, now func() time.Time, opts ...nodes.SnapshotOption,
) *Subjects {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Subjects{
		db: db, subjects: subj, sellers: sellers, notify: notify,
		snapshotOpts: opts, now: now,
	}
}

// Actor bundles the identity and the request context an operation needs.
//
// RequestID is carried through to the audit record. The Telegram bot passes an
// update id, so a bot-initiated change is traceable to the message that caused
// it exactly as an HTTP change is traceable to its request.
type Actor struct {
	RBAC      *rbac.Actor
	Audit     audit.Actor
	RequestID string
	// Via records the channel: "http", "telegram", "ctl". An incident review
	// needs to distinguish a change made from a browser from one made through
	// a chat account that may have been hijacked.
	Via string
}

func (a Actor) scope() rbac.Scope { return rbac.ScopeOf(a.RBAC) }

// authorize runs both enforcement layers for a subject-scoped operation.
//
// Kept private and called by every method: a method that forgets it is a
// method that leaks, so there is no exported variant to reach for.
func (s *Subjects) authorize(
	ctx context.Context, a Actor, perm rbac.Permission, subjectID int64,
) error {
	if err := rbac.Check(a.RBAC, perm, rbac.Target{Kind: rbac.TargetNone}); err != nil {
		return err
	}
	if subjectID == 0 {
		return nil // creation has no existing subject to scope against
	}
	inScope, err := s.db.SubjectInScope(ctx, a.scope(), subjectID)
	if err != nil {
		return fmt.Errorf("check subject scope: %w", err)
	}
	if !inScope {
		return ErrNotFound
	}
	return nil
}

// List returns the subjects this caller may see.
func (s *Subjects) List(ctx context.Context, a Actor) ([]subjects.Subject, error) {
	if err := rbac.Check(a.RBAC, rbac.PermSubjectRead, rbac.Target{Kind: rbac.TargetNone}); err != nil {
		return nil, err
	}
	return s.subjects.List(ctx, a.scope())
}

// Get returns one subject, or ErrNotFound if it is missing or out of scope.
func (s *Subjects) Get(ctx context.Context, a Actor, id int64) (*subjects.Subject, error) {
	if err := rbac.Check(a.RBAC, rbac.PermSubjectRead, rbac.Target{Kind: rbac.TargetNone}); err != nil {
		return nil, err
	}
	out, err := s.subjects.Get(ctx, a.scope(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return out, err
}

// Credential reveals one credential.
//
// The highest-value read in the panel: it returns the secret a user connects
// with. Scoped like everything else, and audited by KIND, never by value.
func (s *Subjects) Credential(
	ctx context.Context, a Actor, id int64, kind subjects.CredentialKind,
) (string, error) {
	if err := s.authorize(ctx, a, rbac.PermCredReveal, id); err != nil {
		return "", err
	}
	value, err := s.subjects.Credential(ctx, id, kind)
	if err != nil {
		return "", ErrNotFound
	}
	audit.BestEffort(ctx, s.db, a.RequestID, a.Audit, audit.Record{
		Action: "credential.reveal", TargetType: "subject",
		TargetID: sql.NullInt64{Int64: id, Valid: true},
		After:    map[string]any{"kind": string(kind), "via": a.Via},
		Result:   "ok",
	})
	return value, nil
}

// SetEnabled enables or disables a subject and republishes its nodes.
func (s *Subjects) SetEnabled(ctx context.Context, a Actor, id int64, on bool) error {
	if err := s.authorize(ctx, a, rbac.PermSubjectWrite, id); err != nil {
		return err
	}
	action := "subject.disable"
	if on {
		action = "subject.enable"
	}
	return s.mutate(ctx, a, id, action, func(tx *sql.Tx) error {
		if on {
			return s.subjects.Enable(ctx, tx, id)
		}
		return s.subjects.Disable(ctx, tx, id)
	})
}

// SetFrozen freezes or unfreezes a subject.
//
// Freezing is a revocation, so on Xray it is restart-class: a frozen user who
// stays connected is not frozen.
func (s *Subjects) SetFrozen(ctx context.Context, a Actor, id int64, frozen bool, reason string) error {
	if err := s.authorize(ctx, a, rbac.PermSubjectWrite, id); err != nil {
		return err
	}
	action := "subject.unfreeze"
	if frozen {
		action = "subject.freeze"
	}
	return s.mutate(ctx, a, id, action, func(tx *sql.Tx) error {
		if frozen {
			return s.subjects.Freeze(ctx, tx, id, reason)
		}
		return s.subjects.Unfreeze(ctx, tx, id)
	})
}

// Delete removes a subject and deprovisions it everywhere.
func (s *Subjects) Delete(ctx context.Context, a Actor, id int64) error {
	if err := s.authorize(ctx, a, rbac.PermSubjectWrite, id); err != nil {
		return err
	}
	// Node ids are read BEFORE the delete: afterwards the grants are gone and
	// there is nothing left to tell us which nodes must be republished.
	nodeIDs, err := s.subjects.NodeIDsForRead(ctx, id)
	if err != nil {
		return fmt.Errorf("resolve affected nodes: %w", err)
	}
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		if err := s.subjects.Delete(ctx, tx, id); err != nil {
			return err
		}
		return audit.InTx(ctx, tx, a.RequestID, a.Audit, audit.Record{
			Action: "subject.delete", TargetType: "subject",
			TargetID: sql.NullInt64{Int64: id, Valid: true},
			After:    map[string]any{"via": a.Via},
			Result:   "ok",
		})
	}); err != nil {
		return err
	}
	return s.republish(ctx, a, nodeIDs, "subject deleted")
}

// RotateCredential replaces one credential and republishes.
func (s *Subjects) RotateCredential(
	ctx context.Context, a Actor, id int64, kind subjects.CredentialKind,
) (string, error) {
	if err := s.authorize(ctx, a, rbac.PermSubjectWrite, id); err != nil {
		return "", err
	}
	var value string
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		var err error
		value, err = s.subjects.Rotate(ctx, tx, id, kind)
		if err != nil {
			return err
		}
		return audit.InTx(ctx, tx, a.RequestID, a.Audit, audit.Record{
			Action: "credential.rotate", TargetType: "subject",
			TargetID: sql.NullInt64{Int64: id, Valid: true},
			// Kind and channel only. Never the new value.
			After:  map[string]any{"kind": string(kind), "via": a.Via},
			Result: "ok",
		})
	})
	if err != nil {
		return "", err
	}
	if err := s.republishFor(ctx, a, id, "credential rotated"); err != nil {
		return "", err
	}
	return value, nil
}

// Provision creates a customer on behalf of a reseller, debiting their credit.
//
// The debit and the creation share one transaction, so a customer nobody paid
// for and a charge for a customer who does not exist are both impossible.
func (s *Subjects) Provision(
	ctx context.Context, a Actor, in resellers.ProvisionInput,
) (resellers.ProvisionResult, error) {
	if err := rbac.Check(a.RBAC, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}); err != nil {
		return resellers.ProvisionResult{}, err
	}
	in.RequestID = a.RequestID
	in.Actor = a.Audit
	if a.RBAC != nil {
		id := a.RBAC.AdminID
		in.ActorAdminID = &id
	}

	var out resellers.ProvisionResult
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = s.sellers.ProvisionSubject(ctx, tx, in)
		return err
	}); err != nil {
		return resellers.ProvisionResult{}, err
	}
	if err := s.republish(ctx, a, out.NodeIDs, "subject provisioned"); err != nil {
		return out, err
	}
	return out, nil
}

// mutate is the shared shape: one transaction, one audit record, then
// republish every node the subject reaches.
func (s *Subjects) mutate(
	ctx context.Context, a Actor, id int64, action string, fn func(*sql.Tx) error,
) error {
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		return audit.InTx(ctx, tx, a.RequestID, a.Audit, audit.Record{
			Action: action, TargetType: "subject",
			TargetID: sql.NullInt64{Int64: id, Valid: true},
			After:    map[string]any{"via": a.Via},
			Result:   "ok",
		})
	}); err != nil {
		return err
	}
	return s.republishFor(ctx, a, id, action)
}

func (s *Subjects) republishFor(ctx context.Context, a Actor, id int64, reason string) error {
	ids, err := s.subjects.NodeIDsForRead(ctx, id)
	if err != nil {
		return fmt.Errorf("resolve affected nodes: %w", err)
	}
	return s.republish(ctx, a, ids, reason)
}

// republish bumps each affected node's revision through CommitNodeChange,
// which remains the ONLY path allowed to move desired_revision.
func (s *Subjects) republish(ctx context.Context, a Actor, nodeIDs []int64, reason string) error {
	for _, nodeID := range nodeIDs {
		result, err := nodes.CommitNodeChange(ctx, s.db, nodeID,
			a.Audit, a.RequestID, reason,
			func(*sql.Tx) error { return nil }, s.snapshotOpts...)
		if err != nil {
			return err
		}
		// Signal only when something moved, so a no-op edit does not wake
		// every agent in the fleet.
		if result.Changed && s.notify != nil {
			s.notify.Notify(nodeID, result.Revision)
		}
	}
	return nil
}

// Package audit writes antimage's append-only audit log.
//
// Two write paths exist, per spec invariant 9:
//
//   - InTx joins the caller's transaction, so a rolled-back mutation leaves
//     no audit row and a committed one can never be unaudited.
//   - BestEffort writes outside any transaction, for security-relevant
//     attempts that deliberately never commit: failed logins, authorization
//     denials, validation rejections, failed applies.
//
// The package intentionally exposes no update or delete path.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

type ActorType string

const (
	ActorAdmin  ActorType = "admin"
	ActorSystem ActorType = "system"
	ActorCtl    ActorType = "ctl"
)

type Actor struct {
	Type    ActorType
	AdminID sql.NullInt64
	Label   string
	IP      string
}

// SystemActor names a non-human actor: enrollment, reconciler, migration.
func SystemActor(label string) Actor {
	return Actor{Type: ActorSystem, Label: label}
}

// AdminActor names an authenticated admin acting from the given IP.
func AdminActor(adminID int64, ip string) Actor {
	return Actor{Type: ActorAdmin, AdminID: sql.NullInt64{Int64: adminID, Valid: true}, IP: ip}
}

type Record struct {
	Action     string
	TargetType string
	TargetID   sql.NullInt64
	// Before and After are marshaled to JSON verbatim. Callers must not put
	// credentials, tokens, session identifiers, or other secrets in either
	// field — build the snapshot from a copy with secret-bearing fields
	// stripped, rather than diffing the raw struct.
	Before any
	After  any
	Result string // "ok", "denied", or "failed"
}

func encode(v any) (sql.NullString, error) {
	if v == nil {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("encode audit payload: %w", err)
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}

const insertSQL = `
INSERT INTO audit_log
  (at, actor_type, actor_admin_id, actor_label, actor_ip, request_id,
   action, target_type, target_id, before_json, after_json, result)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`

func args(requestID string, a Actor, r Record) ([]any, error) {
	before, err := encode(r.Before)
	if err != nil {
		return nil, err
	}
	after, err := encode(r.After)
	if err != nil {
		return nil, err
	}
	result := r.Result
	if result == "" {
		result = "ok"
	}
	return []any{
		time.Now().UTC().Unix(), string(a.Type), a.AdminID, a.Label, a.IP,
		requestID, r.Action, r.TargetType, r.TargetID, before, after, result,
	}, nil
}

// InTx writes the record inside the caller's transaction. A rolled-back
// transaction takes the audit row with it; a committed one can never be
// unaudited.
func InTx(ctx context.Context, tx *sql.Tx, requestID string, a Actor, r Record) error {
	vals, err := args(requestID, a, r)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, insertSQL, vals...); err != nil {
		return fmt.Errorf("write audit record %q: %w", r.Action, err)
	}
	return nil
}

// bestEffortWriteTimeout bounds BestEffort's own store.Write call. It
// defends against a deadlock: BestEffort opens a transaction on the store's
// single write connection, so calling it while the caller already holds
// that connection (i.e. from inside a store.Write callback) would otherwise
// block forever with no deadline. Bounding it turns that mistake into a
// logged, recognizable timeout instead of a wedged panel.
const bestEffortWriteTimeout = 5 * time.Second

// BestEffort records an attempt that deliberately never commits: a failed
// login, an authorization denial, a validation rejection, a failed apply.
// It writes outside any caller transaction so those records survive
// rollback. It cannot fail the caller, so a storage error is logged rather
// than returned.
//
// Do not call BestEffort while already inside a store.Write callback on the
// same *store.Store: it needs the store's single write connection, which
// the outer transaction is holding. BestEffort bounds its own write with
// bestEffortWriteTimeout, so that mistake surfaces as a logged timeout
// rather than a hang — but the audit record is still lost either way.
func BestEffort(ctx context.Context, s *store.Store, requestID string, a Actor, r Record) {
	ctx, cancel := context.WithTimeout(ctx, bestEffortWriteTimeout)
	defer cancel()

	err := s.Write(ctx, func(tx *sql.Tx) error {
		return InTx(ctx, tx, requestID, a, r)
	})
	if err == nil {
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		slog.ErrorContext(ctx, "best-effort audit write timed out waiting for the store's write connection; "+
			"BestEffort was likely called from inside an in-progress store.Write on the same store",
			"action", r.Action, "target_type", r.TargetType, "result", r.Result,
			"request_id", requestID, "actor_type", a.Type, "timeout", bestEffortWriteTimeout, "error", err)
		return
	}
	slog.ErrorContext(ctx, "failed to write best-effort audit record",
		"action", r.Action, "target_type", r.TargetType, "result", r.Result,
		"request_id", requestID, "actor_type", a.Type, "error", err)
}

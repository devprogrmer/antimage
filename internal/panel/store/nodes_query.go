package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/amyrm/antimage/internal/panel/rbac"
)

type NodeRow struct {
	ID              int64
	Name            string
	Address         string
	Status          string
	DesiredRevision int64
	AppliedRevision int64
	LastSeenAt      sql.NullInt64
}

// scopePredicate is the second enforcement layer from spec section 6.3.
//
// It is a static SQL fragment rather than a string built at runtime: the
// caller supplies is_super and admin_id as bound parameters, so there is no
// path by which a caller can widen the filter.
const scopePredicate = `
  (? = 1 OR nodes.id IN (
      SELECT scope_id FROM admin_scopes
       WHERE admin_id = ? AND scope_type = 'node'))`

// NodeScopeSQL exposes the predicate to other packages that count or read
// nodes, so there is exactly ONE definition of "which nodes may this caller
// see" -- the same reasoning as SubjectScopeSQL. Callers bind ScopeArgs in the
// same order.
const NodeScopeSQL = scopePredicate

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) ListNodes(ctx context.Context, sc rbac.Scope) ([]NodeRow, error) {
	rows, err := s.Read().QueryContext(ctx,
		`SELECT id, name, address, status, desired_revision, applied_revision, last_seen_at
		   FROM nodes
		  WHERE `+scopePredicate+`
		  ORDER BY name`,
		boolToInt(sc.IsSuper), sc.AdminID)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []NodeRow
	for rows.Next() {
		var n NodeRow
		if err := rows.Scan(&n.ID, &n.Name, &n.Address, &n.Status,
			&n.DesiredRevision, &n.AppliedRevision, &n.LastSeenAt); err != nil {
			return nil, fmt.Errorf("scan node: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate nodes: %w", err)
	}
	return out, nil
}

// GetNode returns sql.ErrNoRows for both missing and out-of-scope nodes, so
// callers cannot use the error to probe for the existence of another
// reseller's node.
func (s *Store) GetNode(ctx context.Context, sc rbac.Scope, id int64) (*NodeRow, error) {
	var n NodeRow
	err := s.Read().QueryRowContext(ctx,
		`SELECT id, name, address, status, desired_revision, applied_revision, last_seen_at
		   FROM nodes
		  WHERE nodes.id = ? AND `+scopePredicate,
		id, boolToInt(sc.IsSuper), sc.AdminID,
	).Scan(&n.ID, &n.Name, &n.Address, &n.Status,
		&n.DesiredRevision, &n.AppliedRevision, &n.LastSeenAt)
	if err != nil {
		return nil, err // sql.ErrNoRows passes through unwrapped by design
	}
	return &n, nil
}

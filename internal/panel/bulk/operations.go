// Package bulk provides CRUD for bulk_operations and a sequential worker that
// processes queued jobs one at a time.
package bulk

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

// BulkStatus is the lifecycle state of a bulk operation.
type BulkStatus string

const (
	StatusQueued    BulkStatus = "queued"
	StatusRunning   BulkStatus = "running"
	StatusCompleted BulkStatus = "completed"
	StatusFailed    BulkStatus = "failed"
	StatusCancelled BulkStatus = "cancelled"
)

// ItemResult records the per-item outcome of a bulk operation.
type ItemResult struct {
	ItemID string `json:"item_id"`
	Status string `json:"status"` // "success" or "failed"
	Error  string `json:"error,omitempty"`
}

// BulkOperation mirrors a row in bulk_operations.
type BulkOperation struct {
	ID             int64
	OperationType  string
	ActorAdminID   *int64
	TotalItems     int
	CompletedItems int
	FailedItems    int
	Status         BulkStatus
	Results        []ItemResult // populated for completed/failed ops; nil for queued/running
	ResultsJSON    string       // raw column value (internal use)
	CreatedAt      int64
	StartedAt      *int64
	CompletedAt    *int64
}

// scanOp scans a single BulkOperation from a sql.Row.
func scanOp(row *sql.Row, op *BulkOperation) error {
	var status string
	err := row.Scan(
		&op.ID, &op.OperationType, &op.ActorAdminID,
		&op.TotalItems, &op.CompletedItems, &op.FailedItems,
		&status, &op.ResultsJSON, &op.CreatedAt, &op.StartedAt, &op.CompletedAt,
	)
	if err != nil {
		return err
	}
	op.Status = BulkStatus(status)
	return nil
}

const opColumns = `id, operation_type, actor_admin_id, total_items, completed_items,
	failed_items, status, results_json, created_at, started_at, completed_at`

// CreateBulkOperation inserts a new queued bulk operation. The caller-supplied
// items are serialised into results_json; the worker will decode them later.
func CreateBulkOperation(
	ctx context.Context,
	db *store.Store,
	actor rbac.Actor,
	kind string,
	items []interface{},
) (*BulkOperation, error) {
	if len(items) == 0 {
		return nil, errors.New("items must not be empty")
	}

	raw, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("marshal items: %w", err)
	}

	now := time.Now().Unix()
	var op BulkOperation

	err = db.Write(ctx, func(tx *sql.Tx) error {
		return scanOp(tx.QueryRowContext(ctx, `
			INSERT INTO bulk_operations
				(operation_type, actor_admin_id, total_items,
				 completed_items, failed_items, status, results_json, created_at)
			VALUES (?, ?, ?, 0, 0, 'queued', ?, ?)
			RETURNING `+opColumns,
			kind, actor.AdminID, len(items), string(raw), now,
		), &op)
	})
	if err != nil {
		return nil, fmt.Errorf("create bulk operation: %w", err)
	}

	return &op, nil
}

// GetBulkOperation fetches one bulk operation by ID. Super admins may fetch
// any; non-super admins may fetch only their own.
func GetBulkOperation(
	ctx context.Context,
	db *store.Store,
	actor rbac.Actor,
	id int64,
) (*BulkOperation, error) {
	query := `SELECT ` + opColumns + ` FROM bulk_operations WHERE id = ?`
	args := []interface{}{id}

	if !actor.IsSuper {
		query += " AND actor_admin_id = ?"
		args = append(args, actor.AdminID)
	}

	var op BulkOperation
	err := scanOp(db.Read().QueryRowContext(ctx, query, args...), &op)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("bulk operation not found or access denied")
	}
	if err != nil {
		return nil, fmt.Errorf("query bulk operation: %w", err)
	}

	if op.Status == StatusCompleted || op.Status == StatusFailed {
		if err := json.Unmarshal([]byte(op.ResultsJSON), &op.Results); err != nil {
			return nil, fmt.Errorf("unmarshal results: %w", err)
		}
	}

	return &op, nil
}

// ListBulkOperations returns bulk operations ordered newest-first. Super admins
// see all; non-super admins see only their own.
func ListBulkOperations(
	ctx context.Context,
	db *store.Store,
	actor rbac.Actor,
) ([]*BulkOperation, error) {
	query := `SELECT ` + opColumns + ` FROM bulk_operations`
	var args []interface{}

	if !actor.IsSuper {
		query += " WHERE actor_admin_id = ?"
		args = append(args, actor.AdminID)
	}

	query += " ORDER BY created_at DESC"

	rows, err := db.Read().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query bulk operations: %w", err)
	}
	defer rows.Close()

	var ops []*BulkOperation
	for rows.Next() {
		var op BulkOperation
		var status string
		if err := rows.Scan(
			&op.ID, &op.OperationType, &op.ActorAdminID,
			&op.TotalItems, &op.CompletedItems, &op.FailedItems,
			&status, &op.ResultsJSON, &op.CreatedAt, &op.StartedAt, &op.CompletedAt,
		); err != nil {
			return nil, fmt.Errorf("scan bulk operation: %w", err)
		}
		op.Status = BulkStatus(status)
		ops = append(ops, &op)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate bulk operations: %w", err)
	}

	return ops, nil
}

// CancelBulkOperation cancels a queued operation. Only queued operations may be
// cancelled. Super admins may cancel any; non-super admins may cancel only their
// own.
func CancelBulkOperation(
	ctx context.Context,
	db *store.Store,
	actor rbac.Actor,
	id int64,
) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		var status string
		var actorID sql.NullInt64

		err := tx.QueryRowContext(ctx,
			"SELECT status, actor_admin_id FROM bulk_operations WHERE id = ?", id).
			Scan(&status, &actorID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("bulk operation not found")
		}
		if err != nil {
			return fmt.Errorf("query bulk operation: %w", err)
		}

		if !actor.IsSuper {
			if !actorID.Valid || actorID.Int64 != actor.AdminID {
				return fmt.Errorf("access denied")
			}
		}

		if BulkStatus(status) != StatusQueued {
			return fmt.Errorf("only queued operations can be cancelled, current status: %s", status)
		}

		_, err = tx.ExecContext(ctx,
			"UPDATE bulk_operations SET status = 'cancelled' WHERE id = ?", id)
		return err
	})
}

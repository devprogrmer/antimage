package bulk

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

// ProcessFunc is invoked once per item in a bulk operation.
// It receives the operation type and a serialised item, returning either a
// result payload or an error.
type ProcessFunc func(ctx context.Context, kind string, item json.RawMessage) (json.RawMessage, error)

// Worker polls for queued bulk operations and processes them sequentially.
type Worker struct {
	db        *store.Store
	processFn ProcessFunc
}

// NewWorker creates a Worker that uses db for persistence and processFn to
// handle each item.
func NewWorker(db *store.Store, processFn ProcessFunc) *Worker {
	return &Worker{db: db, processFn: processFn}
}

// Run loops until ctx is cancelled, claiming and processing queued operations
// one at a time.
func (w *Worker) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := w.processNext(ctx); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// No queued operation; wait before retrying.
				select {
				case <-ctx.Done():
					return
				case <-time.After(2 * time.Second):
				}
				continue
			}
			// Transient error; back off briefly.
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
}

// processNext atomically claims the oldest queued operation, processes all
// items, and writes results back. Returns sql.ErrNoRows if nothing is queued.
func (w *Worker) processNext(ctx context.Context) error {
	var op BulkOperation
	err := w.db.Write(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `
			SELECT id, operation_type, total_items, results_json
			FROM bulk_operations
			WHERE status = 'queued'
			ORDER BY created_at ASC
			LIMIT 1
		`).Scan(&op.ID, &op.OperationType, &op.TotalItems, &op.ResultsJSON)
		if err != nil {
			return err // sql.ErrNoRows propagates unchanged
		}

		now := time.Now().Unix()
		_, err = tx.ExecContext(ctx, `
			UPDATE bulk_operations SET status = 'running', started_at = ? WHERE id = ?
		`, now, op.ID)
		return err
	})
	if err != nil {
		return err
	}

	// Decode items that were serialised at creation time.
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(op.ResultsJSON), &items); err != nil {
		return w.markFailed(ctx, op.ID, fmt.Sprintf("unmarshal items: %v", err))
	}

	results := make([]ItemResult, 0, len(items))
	for i, item := range items {
		res := ItemResult{Index: i, Input: item}
		out, err := w.processFn(ctx, op.OperationType, item)
		if err != nil {
			res.Error = err.Error()
			res.Success = false
		} else {
			res.Output = out
			res.Success = true
		}
		results = append(results, res)
	}

	return w.writeResults(ctx, op.ID, results)
}

// writeResults persists the ItemResult array and updates counters / final status.
func (w *Worker) writeResults(ctx context.Context, id int64, results []ItemResult) error {
	raw, err := json.Marshal(results)
	if err != nil {
		return fmt.Errorf("marshal results: %w", err)
	}

	completed := 0
	failed := 0
	for _, r := range results {
		if r.Success {
			completed++
		} else {
			failed++
		}
	}

	finalStatus := StatusCompleted
	if failed > 0 && completed == 0 {
		finalStatus = StatusFailed
	}

	now := time.Now().Unix()
	return w.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE bulk_operations
			SET status = ?, results_json = ?, completed_items = ?, failed_items = ?, completed_at = ?
			WHERE id = ?
		`, string(finalStatus), string(raw), completed, failed, now, id)
		return err
	})
}

// markFailed transitions an operation to 'failed' when a structural error
// prevents processing (e.g. unmarshal failure).
func (w *Worker) markFailed(ctx context.Context, id int64, reason string) error {
	now := time.Now().Unix()
	raw, _ := json.Marshal([]ItemResult{{Error: reason, Success: false}})
	return w.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE bulk_operations
			SET status = 'failed', completed_at = ?, results_json = ?
			WHERE id = ?
		`, now, string(raw), id)
		return err
	})
}

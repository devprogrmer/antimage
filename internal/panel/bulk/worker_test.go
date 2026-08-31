package bulk

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/rbac"
)

func alwaysSucceed(_ context.Context, _ string, item json.RawMessage) (string, error) {
	return "item-id", nil
}

func alwaysFail(_ context.Context, _ string, item json.RawMessage) (string, error) {
	return "", errors.New("processing failed")
}

func TestWorker_ProcessesQueuedOperation(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, IsSuper: true}

	op, err := CreateBulkOperation(context.Background(), db, actor, "subjects_create", mustItems(t, 3))
	if err != nil {
		t.Fatalf("CreateBulkOperation: %v", err)
	}

	w := NewWorker(db, alwaysSucceed)
	if err := w.processNext(context.Background()); err != nil {
		t.Fatalf("processNext: %v", err)
	}

	got, err := GetBulkOperation(context.Background(), db, actor, op.ID)
	if err != nil {
		t.Fatalf("GetBulkOperation: %v", err)
	}
	if got.Status != StatusCompleted {
		t.Errorf("expected completed, got %s", got.Status)
	}
	if got.CompletedItems != 3 {
		t.Errorf("expected 3 completed items, got %d", got.CompletedItems)
	}
	if got.FailedItems != 0 {
		t.Errorf("expected 0 failed items, got %d", got.FailedItems)
	}
}

func TestWorker_AllItemsFail(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, IsSuper: true}

	op, err := CreateBulkOperation(context.Background(), db, actor, "subjects_delete", mustItems(t, 2))
	if err != nil {
		t.Fatalf("CreateBulkOperation: %v", err)
	}

	w := NewWorker(db, alwaysFail)
	if err := w.processNext(context.Background()); err != nil {
		t.Fatalf("processNext: %v", err)
	}

	got, err := GetBulkOperation(context.Background(), db, actor, op.ID)
	if err != nil {
		t.Fatalf("GetBulkOperation: %v", err)
	}
	if got.Status != StatusFailed {
		t.Errorf("expected failed, got %s", got.Status)
	}
	if got.FailedItems != 2 {
		t.Errorf("expected 2 failed items, got %d", got.FailedItems)
	}
}

func TestWorker_MixedResults(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, IsSuper: true}

	// 3 items; alternate success/fail/success
	calls := 0
	mixedFn := func(_ context.Context, _ string, _ json.RawMessage) (string, error) {
		calls++
		if calls%2 == 0 {
			return "", errors.New("even item failed")
		}
		return "item-id", nil
	}

	op, err := CreateBulkOperation(context.Background(), db, actor, "subjects_update", mustItems(t, 3))
	if err != nil {
		t.Fatalf("CreateBulkOperation: %v", err)
	}

	w := NewWorker(db, mixedFn)
	if err := w.processNext(context.Background()); err != nil {
		t.Fatalf("processNext: %v", err)
	}

	got, err := GetBulkOperation(context.Background(), db, actor, op.ID)
	if err != nil {
		t.Fatalf("GetBulkOperation: %v", err)
	}
	// 2 succeed, 1 fails → status is completed (partial success)
	if got.Status != StatusCompleted {
		t.Errorf("expected completed (partial), got %s", got.Status)
	}
	if got.CompletedItems != 2 {
		t.Errorf("expected 2 completed, got %d", got.CompletedItems)
	}
	if got.FailedItems != 1 {
		t.Errorf("expected 1 failed, got %d", got.FailedItems)
	}
}

func TestWorker_NoQueuedOp(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)

	w := NewWorker(db, alwaysSucceed)
	err := w.processNext(context.Background())
	if err == nil {
		t.Error("expected error (no rows), got nil")
	}
}

func TestWorker_ProcessesOldestFirst(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, IsSuper: true}

	op1, err := CreateBulkOperation(context.Background(), db, actor, "subjects_freeze", mustItems(t, 1))
	if err != nil {
		t.Fatalf("CreateBulkOperation 1: %v", err)
	}
	op2, err := CreateBulkOperation(context.Background(), db, actor, "subjects_unfreeze", mustItems(t, 1))
	if err != nil {
		t.Fatalf("CreateBulkOperation 2: %v", err)
	}

	w := NewWorker(db, alwaysSucceed)
	if err := w.processNext(context.Background()); err != nil {
		t.Fatalf("processNext: %v", err)
	}

	// op1 should be completed, op2 still queued
	got1, err := GetBulkOperation(context.Background(), db, actor, op1.ID)
	if err != nil {
		t.Fatalf("GetBulkOperation op1: %v", err)
	}
	if got1.Status != StatusCompleted {
		t.Errorf("expected op1 completed, got %s", got1.Status)
	}

	got2, err := GetBulkOperation(context.Background(), db, actor, op2.ID)
	if err != nil {
		t.Fatalf("GetBulkOperation op2: %v", err)
	}
	if got2.Status != StatusQueued {
		t.Errorf("expected op2 queued, got %s", got2.Status)
	}
}

func TestWorker_RunStopsOnContextCancel(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)

	w := NewWorker(db, alwaysSucceed)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// ok
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}
}

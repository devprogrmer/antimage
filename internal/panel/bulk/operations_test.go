package bulk

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/testutil/storetest"
)

func openTestDB(t *testing.T) *store.Store {
	t.Helper()
	s, err := storetest.OpenCopy(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func createTestAdmins(t *testing.T, db *store.Store) {
	t.Helper()
	err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `
			INSERT OR IGNORE INTO roles (id, name, is_builtin, permissions)
			VALUES (1, 'super_admin', 1, '["node:read","node:write","node:enroll","service:read","service:write","subject:read","subject:write","credential:reveal","admin:manage","role:manage","audit:read","settings:write","alert:read"]')
		`)
		if err != nil {
			return err
		}
		for _, id := range []int64{1, 2} {
			_, err := tx.ExecContext(context.Background(), `
				INSERT OR IGNORE INTO admins (id, username, password_hash, role_id, status, created_at)
				VALUES (?, ?, 'dummy_hash', 1, 'active', 0)
			`, id, "admin"+string(rune(48+id)))
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("createTestAdmins: %v", err)
	}
}

func mustItems(t *testing.T, n int) []interface{} {
	t.Helper()
	items := make([]interface{}, n)
	for i := range items {
		items[i] = json.RawMessage(`{"id":1}`)
	}
	return items
}

func TestCreateBulkOperation_Valid(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, IsSuper: true}

	op, err := CreateBulkOperation(context.Background(), db, actor, "subjects_create", mustItems(t, 3))
	if err != nil {
		t.Fatalf("CreateBulkOperation: %v", err)
	}
	if op.ID == 0 {
		t.Error("expected non-zero ID")
	}
	if op.Status != StatusQueued {
		t.Errorf("expected queued, got %s", op.Status)
	}
	if op.TotalItems != 3 {
		t.Errorf("expected 3 total items, got %d", op.TotalItems)
	}
}

func TestCreateBulkOperation_EmptyItems(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, IsSuper: true}

	_, err := CreateBulkOperation(context.Background(), db, actor, "subjects_create", nil)
	if err == nil {
		t.Error("expected error for empty items, got nil")
	}
}

func TestGetBulkOperation_SuperSeesAny(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	creator := rbac.Actor{AdminID: 1, IsSuper: false}
	super := rbac.Actor{AdminID: 2, IsSuper: true}

	op, err := CreateBulkOperation(context.Background(), db, creator, "subjects_create", mustItems(t, 1))
	if err != nil {
		t.Fatalf("CreateBulkOperation: %v", err)
	}

	got, err := GetBulkOperation(context.Background(), db, super, op.ID)
	if err != nil {
		t.Fatalf("GetBulkOperation (super): %v", err)
	}
	if got.ID != op.ID {
		t.Errorf("expected ID %d, got %d", op.ID, got.ID)
	}
}

func TestGetBulkOperation_NonSuperDenied(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	creator := rbac.Actor{AdminID: 1, IsSuper: false}
	other := rbac.Actor{AdminID: 2, IsSuper: false}

	op, err := CreateBulkOperation(context.Background(), db, creator, "subjects_create", mustItems(t, 1))
	if err != nil {
		t.Fatalf("CreateBulkOperation: %v", err)
	}

	_, err = GetBulkOperation(context.Background(), db, other, op.ID)
	if err == nil {
		t.Error("expected access denied for other non-super admin, got nil")
	}
}

func TestGetBulkOperation_OwnVisible(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, IsSuper: false}

	op, err := CreateBulkOperation(context.Background(), db, actor, "subjects_freeze", mustItems(t, 2))
	if err != nil {
		t.Fatalf("CreateBulkOperation: %v", err)
	}

	got, err := GetBulkOperation(context.Background(), db, actor, op.ID)
	if err != nil {
		t.Fatalf("GetBulkOperation: %v", err)
	}
	if got.ID != op.ID {
		t.Errorf("expected ID %d, got %d", op.ID, got.ID)
	}
}

func TestGetBulkOperation_NotFound(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, IsSuper: true}

	_, err := GetBulkOperation(context.Background(), db, actor, 9999)
	if err == nil {
		t.Error("expected not-found error, got nil")
	}
}

func TestListBulkOperations_SuperSeesAll(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	admin1 := rbac.Actor{AdminID: 1, IsSuper: false}
	admin2 := rbac.Actor{AdminID: 2, IsSuper: false}
	super := rbac.Actor{AdminID: 2, IsSuper: true}

	_, err := CreateBulkOperation(context.Background(), db, admin1, "subjects_create", mustItems(t, 1))
	if err != nil {
		t.Fatalf("CreateBulkOperation admin1: %v", err)
	}
	_, err = CreateBulkOperation(context.Background(), db, admin2, "subjects_delete", mustItems(t, 1))
	if err != nil {
		t.Fatalf("CreateBulkOperation admin2: %v", err)
	}

	all, err := ListBulkOperations(context.Background(), db, super)
	if err != nil {
		t.Fatalf("ListBulkOperations: %v", err)
	}
	if len(all) < 2 {
		t.Errorf("expected at least 2, got %d", len(all))
	}
}

func TestListBulkOperations_NonSuperSeesOwn(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	admin1 := rbac.Actor{AdminID: 1, IsSuper: false}
	admin2 := rbac.Actor{AdminID: 2, IsSuper: false}

	_, err := CreateBulkOperation(context.Background(), db, admin1, "subjects_create", mustItems(t, 1))
	if err != nil {
		t.Fatalf("CreateBulkOperation admin1: %v", err)
	}
	_, err = CreateBulkOperation(context.Background(), db, admin2, "subjects_delete", mustItems(t, 1))
	if err != nil {
		t.Fatalf("CreateBulkOperation admin2: %v", err)
	}

	own, err := ListBulkOperations(context.Background(), db, admin1)
	if err != nil {
		t.Fatalf("ListBulkOperations: %v", err)
	}
	if len(own) != 1 {
		t.Errorf("expected 1 for admin1, got %d", len(own))
	}
}

func TestCancelBulkOperation_Queued(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, IsSuper: true}

	op, err := CreateBulkOperation(context.Background(), db, actor, "subjects_create", mustItems(t, 2))
	if err != nil {
		t.Fatalf("CreateBulkOperation: %v", err)
	}

	err = CancelBulkOperation(context.Background(), db, actor, op.ID)
	if err != nil {
		t.Fatalf("CancelBulkOperation: %v", err)
	}

	got, err := GetBulkOperation(context.Background(), db, actor, op.ID)
	if err != nil {
		t.Fatalf("GetBulkOperation after cancel: %v", err)
	}
	if got.Status != StatusCancelled {
		t.Errorf("expected cancelled, got %s", got.Status)
	}
}

func TestCancelBulkOperation_NonSuperDenied(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	creator := rbac.Actor{AdminID: 1, IsSuper: false}
	other := rbac.Actor{AdminID: 2, IsSuper: false}

	op, err := CreateBulkOperation(context.Background(), db, creator, "subjects_create", mustItems(t, 1))
	if err != nil {
		t.Fatalf("CreateBulkOperation: %v", err)
	}

	err = CancelBulkOperation(context.Background(), db, other, op.ID)
	if err == nil {
		t.Errorf("expected access denied, got nil; op.ID=%d", op.ID)
	}
}

func TestCancelBulkOperation_NotQueued(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, IsSuper: true}

	op, err := CreateBulkOperation(context.Background(), db, actor, "subjects_create", mustItems(t, 1))
	if err != nil {
		t.Fatalf("CreateBulkOperation: %v", err)
	}

	// Cancel once.
	if err := CancelBulkOperation(context.Background(), db, actor, op.ID); err != nil {
		t.Fatalf("first cancel: %v", err)
	}
	// Cancelling again must fail.
	err = CancelBulkOperation(context.Background(), db, actor, op.ID)
	if err == nil {
		t.Error("expected error cancelling a non-queued operation, got nil")
	}
}

func TestCancelBulkOperation_NotFound(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, IsSuper: true}

	err := CancelBulkOperation(context.Background(), db, actor, 9999)
	if err == nil {
		t.Error("expected not-found error, got nil")
	}
}

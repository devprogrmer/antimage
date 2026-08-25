package dashboard_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/amyrm/antimage/internal/panel/dashboard"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/testutil/storetest"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := storetest.OpenCopy(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// createTestAdmin inserts a role + admin row and returns the admin's ID.
// This is needed for per-admin cache tests, since dashboard_stats.admin_id
// has a FK reference to admins(id).
func createTestAdmin(t *testing.T, s *store.Store) int64 {
	t.Helper()
	var adminID int64
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		res, e := tx.ExecContext(context.Background(),
			`INSERT INTO roles (name, is_builtin, permissions) VALUES ('super', 1, '[]')`)
		if e != nil {
			return e
		}
		roleID, _ := res.LastInsertId()
		res, e = tx.ExecContext(context.Background(),
			`INSERT INTO admins (username, password_hash, role_id, created_at) VALUES ('root', 'x', ?, 1)`,
			roleID)
		if e != nil {
			return e
		}
		adminID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("createTestAdmin: %v", err)
	}
	return adminID
}

func TestComputeStats_EmptyDB(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	stats, err := dashboard.ComputeStats(ctx, s, nil)
	if err != nil {
		t.Fatalf("ComputeStats: %v", err)
	}
	if stats.NodesTotal != 0 {
		t.Errorf("NodesTotal: want 0, got %d", stats.NodesTotal)
	}
	if stats.SubjectsTotal != 0 {
		t.Errorf("SubjectsTotal: want 0, got %d", stats.SubjectsTotal)
	}
	if stats.Traffic24hUplink != 0 {
		t.Errorf("Traffic24hUplink: want 0, got %d", stats.Traffic24hUplink)
	}
	if stats.Traffic24hDownlink != 0 {
		t.Errorf("Traffic24hDownlink: want 0, got %d", stats.Traffic24hDownlink)
	}
	if stats.QuotaTotalBytes != nil {
		t.Errorf("QuotaTotalBytes: want nil, got %v", *stats.QuotaTotalBytes)
	}
	if stats.QuotaUsedBytes != nil {
		t.Errorf("QuotaUsedBytes: want nil, got %v", *stats.QuotaUsedBytes)
	}
	if stats.ComputedAt == 0 {
		t.Error("ComputedAt should not be zero")
	}
}

func TestComputeStats_AdminIDPassedThrough(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	id := int64(99)
	stats, err := dashboard.ComputeStats(ctx, s, &id)
	if err != nil {
		t.Fatalf("ComputeStats: %v", err)
	}
	if stats.AdminID == nil || *stats.AdminID != 99 {
		t.Errorf("AdminID: want 99, got %v", stats.AdminID)
	}
}

func TestGetStats_SuperAdminComputesFresh(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	actor := rbac.Actor{AdminID: 1, IsSuper: true}

	// Super admin stats are always computed fresh (global cache skipped due to
	// SQLite STRICT FK constraint on nullable PK).
	stats, err := dashboard.GetStats(ctx, s, actor)
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if stats.ComputedAt == 0 {
		t.Error("ComputedAt should not be zero")
	}
	// Super admin gets global stats (no admin_id).
	if stats.AdminID != nil {
		t.Errorf("AdminID: want nil for super admin, got %v", *stats.AdminID)
	}
}

func TestGetStats_CacheHit_PerAdmin(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// A real admin row is required for the per-admin cache insert (FK).
	adminID := createTestAdmin(t, s)
	actor := rbac.Actor{AdminID: adminID, IsSuper: false}

	// First call populates the cache.
	first, err := dashboard.GetStats(ctx, s, actor)
	if err != nil {
		t.Fatalf("first GetStats: %v", err)
	}

	// Second call within the staleness window must hit the cache.
	second, err := dashboard.GetStats(ctx, s, actor)
	if err != nil {
		t.Fatalf("second GetStats: %v", err)
	}

	// ComputedAt is stored at Unix second precision. The cached row's timestamp
	// must match the first call's.
	if second.ComputedAt != first.ComputedAt {
		t.Errorf("cache hit expected: first=%d second=%d", first.ComputedAt, second.ComputedAt)
	}
}

func TestGetStats_NonSuperAdminUsesOwnID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// Non-super actor without a matching admin row — cache write is silently
	// ignored, but fresh stats are still returned without error.
	actor := rbac.Actor{AdminID: 999, IsSuper: false}

	stats, err := dashboard.GetStats(ctx, s, actor)
	if err != nil {
		t.Fatalf("GetStats for non-super: %v", err)
	}
	if stats.AdminID == nil || *stats.AdminID != 999 {
		t.Errorf("AdminID: want 999, got %v", stats.AdminID)
	}
}

func TestGetStats_ZeroFields(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	actor := rbac.Actor{AdminID: 1, IsSuper: true}

	stats, err := dashboard.GetStats(ctx, s, actor)
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	for _, tc := range []struct {
		name string
		got  int64
	}{
		{"NodesTotal", stats.NodesTotal},
		{"NodesOnline", stats.NodesOnline},
		{"NodesDegraded", stats.NodesDegraded},
		{"NodesOffline", stats.NodesOffline},
		{"SubjectsTotal", stats.SubjectsTotal},
		{"SubjectsActive", stats.SubjectsActive},
		{"SubjectsExpired", stats.SubjectsExpired},
		{"SubjectsFrozen", stats.SubjectsFrozen},
	} {
		if tc.got != 0 {
			t.Errorf("%s: want 0, got %d", tc.name, tc.got)
		}
	}
}

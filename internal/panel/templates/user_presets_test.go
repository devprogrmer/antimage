package templates

import (
	"context"
	"testing"

	"github.com/amyrm/antimage/internal/panel/rbac"
)

func TestListPresets_Empty(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, RoleName: "super_admin", IsSuper: true}

	presets, err := ListPresets(context.Background(), db, actor)
	if err != nil {
		t.Fatalf("ListPresets failed: %v", err)
	}
	if len(presets) != 0 {
		t.Errorf("expected 0 presets, got %d", len(presets))
	}
}

func TestCreatePreset_ValidStandard(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, RoleName: "super_admin", IsSuper: true}

	quota := int64(53687091200) // 50GB
	validity := 30

	input := CreatePresetInput{
		Name:         "Standard User",
		Description:  "50GB quota, 30 days",
		QuotaBytes:   &quota,
		ValidityDays: &validity,
		AutoAssignServices: []int64{1, 2},
		AutoAssignNodeTags: []string{"premium"},
		IsPublic:     true,
	}

	preset, err := CreatePreset(context.Background(), db, actor, input)
	if err != nil {
		t.Fatalf("CreatePreset failed: %v", err)
	}
	if preset.ID == 0 {
		t.Error("expected non-zero ID")
	}
	if preset.QuotaBytes == nil || *preset.QuotaBytes != quota {
		t.Errorf("expected quota %d, got %v", quota, preset.QuotaBytes)
	}
	if len(preset.AutoAssignServices) != 2 {
		t.Errorf("expected 2 auto-assign services, got %d", len(preset.AutoAssignServices))
	}
}

func TestCreatePreset_UnlimitedQuota(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, RoleName: "super_admin", IsSuper: true}

	input := CreatePresetInput{
		Name:       "Unlimited User",
		QuotaBytes: nil, // unlimited
	}

	preset, err := CreatePreset(context.Background(), db, actor, input)
	if err != nil {
		t.Fatalf("CreatePreset failed: %v", err)
	}
	if preset.QuotaBytes != nil {
		t.Errorf("expected nil quota (unlimited), got %v", preset.QuotaBytes)
	}
}

func TestCreatePreset_MissingName(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, RoleName: "super_admin", IsSuper: true}

	input := CreatePresetInput{
		Name:       "",
		QuotaBytes: nil,
	}

	_, err := CreatePreset(context.Background(), db, actor, input)
	if err == nil {
		t.Error("expected error for missing name, got nil")
	}
}

func TestGetPreset_NotFound(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, RoleName: "super_admin", IsSuper: true}

	_, err := GetPreset(context.Background(), db, actor, 9999)
	if err == nil {
		t.Error("expected error for not found, got nil")
	}
}

func TestGetPreset_PermissionDenied(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	creator := rbac.Actor{AdminID: 1, RoleName: "admin", IsSuper: false}
	other := rbac.Actor{AdminID: 2, RoleName: "admin", IsSuper: false}

	input := CreatePresetInput{
		Name:       "Private Preset",
		IsPublic:   false,
	}

	preset, err := CreatePreset(context.Background(), db, creator, input)
	if err != nil {
		t.Fatalf("CreatePreset failed: %v", err)
	}

	_, err = GetPreset(context.Background(), db, other, preset.ID)
	if err == nil {
		t.Error("expected permission denied, got nil")
	}
}

func TestGetPreset_PublicPreset(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	creator := rbac.Actor{AdminID: 1, RoleName: "admin", IsSuper: false}
	other := rbac.Actor{AdminID: 2, RoleName: "admin", IsSuper: false}

	input := CreatePresetInput{
		Name:       "Public Preset",
		IsPublic:   true,
	}

	created, err := CreatePreset(context.Background(), db, creator, input)
	if err != nil {
		t.Fatalf("CreatePreset failed: %v", err)
	}

	retrieved, err := GetPreset(context.Background(), db, other, created.ID)
	if err != nil {
		t.Fatalf("GetPreset failed: %v", err)
	}
	if retrieved.ID != created.ID {
		t.Errorf("expected ID %d, got %d", created.ID, retrieved.ID)
	}
}

func TestUpdatePreset_OwnPreset(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, RoleName: "admin", IsSuper: false}

	created, err := CreatePreset(context.Background(), db, actor, CreatePresetInput{
		Name: "Original",
	})
	if err != nil {
		t.Fatalf("CreatePreset failed: %v", err)
	}

	newName := "Updated"
	err = UpdatePreset(context.Background(), db, actor, created.ID, UpdatePresetInput{
		Name: &newName,
	})
	if err != nil {
		t.Fatalf("UpdatePreset failed: %v", err)
	}

	updated, err := GetPreset(context.Background(), db, actor, created.ID)
	if err != nil {
		t.Fatalf("GetPreset failed: %v", err)
	}
	if updated.Name != newName {
		t.Errorf("expected name %q, got %q", newName, updated.Name)
	}
}

func TestUpdatePreset_DifferentAdminForbidden(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	creator := rbac.Actor{AdminID: 1, RoleName: "admin", IsSuper: false}
	other := rbac.Actor{AdminID: 2, RoleName: "admin", IsSuper: false}

	created, err := CreatePreset(context.Background(), db, creator, CreatePresetInput{
		Name: "Original",
	})
	if err != nil {
		t.Fatalf("CreatePreset failed: %v", err)
	}

	newName := "Hacked"
	err = UpdatePreset(context.Background(), db, other, created.ID, UpdatePresetInput{
		Name: &newName,
	})
	if err == nil {
		t.Error("expected permission denied, got nil")
	}
}

func TestUpdatePreset_SuperAdminCanModify(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	creator := rbac.Actor{AdminID: 1, RoleName: "admin", IsSuper: false}
	superAdmin := rbac.Actor{AdminID: 2, RoleName: "super_admin", IsSuper: true}

	created, err := CreatePreset(context.Background(), db, creator, CreatePresetInput{
		Name: "Original",
	})
	if err != nil {
		t.Fatalf("CreatePreset failed: %v", err)
	}

	newName := "Modified by SuperAdmin"
	err = UpdatePreset(context.Background(), db, superAdmin, created.ID, UpdatePresetInput{
		Name: &newName,
	})
	if err != nil {
		t.Fatalf("UpdatePreset failed: %v", err)
	}

	updated, err := GetPreset(context.Background(), db, creator, created.ID)
	if err != nil {
		t.Fatalf("GetPreset failed: %v", err)
	}
	if updated.Name != newName {
		t.Errorf("expected name %q, got %q", newName, updated.Name)
	}
}

func TestDeletePreset_OwnPreset(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, RoleName: "admin", IsSuper: false}

	created, err := CreatePreset(context.Background(), db, actor, CreatePresetInput{
		Name: "ToDelete",
	})
	if err != nil {
		t.Fatalf("CreatePreset failed: %v", err)
	}

	err = DeletePreset(context.Background(), db, actor, created.ID)
	if err != nil {
		t.Fatalf("DeletePreset failed: %v", err)
	}

	_, err = GetPreset(context.Background(), db, actor, created.ID)
	if err == nil {
		t.Error("expected not found after delete, got nil")
	}
}

func TestDeletePreset_DifferentAdminForbidden(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	creator := rbac.Actor{AdminID: 1, RoleName: "admin", IsSuper: false}
	other := rbac.Actor{AdminID: 2, RoleName: "admin", IsSuper: false}

	created, err := CreatePreset(context.Background(), db, creator, CreatePresetInput{
		Name: "Protected",
	})
	if err != nil {
		t.Fatalf("CreatePreset failed: %v", err)
	}

	err = DeletePreset(context.Background(), db, other, created.ID)
	if err == nil {
		t.Error("expected permission denied, got nil")
	}
}

func TestListPresets_ScopeFiltering(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	admin1 := rbac.Actor{AdminID: 1, RoleName: "admin", IsSuper: false}
	admin2 := rbac.Actor{AdminID: 2, RoleName: "admin", IsSuper: false}

	// Create private preset by admin1
	_, err := CreatePreset(context.Background(), db, admin1, CreatePresetInput{
		Name:     "Private1",
		IsPublic: false,
	})
	if err != nil {
		t.Fatalf("CreatePreset failed: %v", err)
	}

	// Create public preset by admin2
	_, err = CreatePreset(context.Background(), db, admin2, CreatePresetInput{
		Name:     "Public2",
		IsPublic: true,
	})
	if err != nil {
		t.Fatalf("CreatePreset failed: %v", err)
	}

	// Admin1 should see their own private + the public one
	presets1, err := ListPresets(context.Background(), db, admin1)
	if err != nil {
		t.Fatalf("ListPresets failed: %v", err)
	}
	if len(presets1) != 2 {
		t.Errorf("expected 2 presets for admin1, got %d", len(presets1))
	}

	// Admin2 should see only their own public preset
	presets2, err := ListPresets(context.Background(), db, admin2)
	if err != nil {
		t.Fatalf("ListPresets failed: %v", err)
	}
	if len(presets2) != 1 {
		t.Errorf("expected 1 preset for admin2, got %d", len(presets2))
	}
}


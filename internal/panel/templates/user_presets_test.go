package templates

import (
	"context"
	"encoding/json"
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

	servicesJSON := json.RawMessage(`[1,2]`)
	tagsJSON := json.RawMessage(`["premium"]`)

	input := CreatePresetInput{
		Name:                   "Standard User",
		Description:            "50GB quota, 30 days",
		QuotaBytes:             &quota,
		ValidityDays:           &validity,
		AutoAssignServicesJSON: servicesJSON,
		AutoAssignNodeTagsJSON: tagsJSON,
		IsPublic:               true,
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
	var services []int64
	if err := json.Unmarshal(preset.AutoAssignServicesJSON, &services); err != nil {
		t.Fatalf("unmarshal services: %v", err)
	}
	if len(services) != 2 {
		t.Errorf("expected 2 auto-assign services, got %d", len(services))
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
		Name: "",
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

	_, err := GetPreset(context.Background(), db, actor, 999)
	if err == nil {
		t.Error("expected error for non-existent preset, got nil")
	}
}

func TestUpdatePreset_Success(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, RoleName: "super_admin", IsSuper: true}

	// Create a preset first
	input := CreatePresetInput{
		Name:        "Test Preset",
		Description: "Original description",
	}

	preset, err := CreatePreset(context.Background(), db, actor, input)
	if err != nil {
		t.Fatalf("CreatePreset failed: %v", err)
	}

	// Update it
	newDesc := "Updated description"
	updateInput := UpdatePresetInput{
		Description: &newDesc,
	}

	updated, err := UpdatePreset(context.Background(), db, actor, preset.ID, updateInput)
	if err != nil {
		t.Fatalf("UpdatePreset failed: %v", err)
	}

	if updated.Description != newDesc {
		t.Errorf("expected description %q, got %q", newDesc, updated.Description)
	}
}

func TestUpdatePreset_NoFieldsToUpdate(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, RoleName: "super_admin", IsSuper: true}

	// Create a preset first
	input := CreatePresetInput{
		Name: "Test Preset",
	}

	preset, err := CreatePreset(context.Background(), db, actor, input)
	if err != nil {
		t.Fatalf("CreatePreset failed: %v", err)
	}

	// Try to update with no fields
	updateInput := UpdatePresetInput{}

	_, err = UpdatePreset(context.Background(), db, actor, preset.ID, updateInput)
	if err == nil {
		t.Error("expected error for no fields to update, got nil")
	}
}

func TestDeletePreset_Success(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, RoleName: "super_admin", IsSuper: true}

	// Create a preset first
	input := CreatePresetInput{
		Name: "Test Preset",
	}

	preset, err := CreatePreset(context.Background(), db, actor, input)
	if err != nil {
		t.Fatalf("CreatePreset failed: %v", err)
	}

	// Delete it
	if err := DeletePreset(context.Background(), db, actor, preset.ID); err != nil {
		t.Fatalf("DeletePreset failed: %v", err)
	}

	// Verify it's gone
	_, err = GetPreset(context.Background(), db, actor, preset.ID)
	if err == nil {
		t.Error("expected error for deleted preset, got nil")
	}
}

func TestDeletePreset_NotFound(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, RoleName: "super_admin", IsSuper: true}

	err := DeletePreset(context.Background(), db, actor, 999)
	if err == nil {
		t.Error("expected error for non-existent preset, got nil")
	}
}

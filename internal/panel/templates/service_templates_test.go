package templates

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

func openTestDB(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func createTestAdmins(t *testing.T, db *store.Store) {
	t.Helper()
	err := db.Write(context.Background(), func(tx *sql.Tx) error {
		// Create super_admin role
		_, err := tx.ExecContext(context.Background(), `
			INSERT INTO roles (id, name, is_builtin, permissions)
			VALUES (1, 'super_admin', 1, '["node:read","node:write","node:enroll","service:read","service:write","subject:read","subject:write","credential:reveal","admin:manage","role:manage","audit:read","settings:write","alert:read"]')
		`)
		if err != nil && err.Error() != "UNIQUE constraint failed: roles.name" {
			return err
		}

		// Create admin role
		_, err = tx.ExecContext(context.Background(), `
			INSERT INTO roles (id, name, is_builtin, permissions)
			VALUES (2, 'admin', 1, '["node:read","node:write","node:enroll","service:read","service:write","subject:read","subject:write","credential:reveal","audit:read","alert:read"]')
		`)
		if err != nil && err.Error() != "UNIQUE constraint failed: roles.name" {
			return err
		}

		// Create test admins
		for i := int64(1); i <= 2; i++ {
			_, err := tx.ExecContext(context.Background(), `
				INSERT OR IGNORE INTO admins (id, username, password_hash, role_id, status, created_at)
				VALUES (?, ?, ?, ?, 'active', ?)
			`, i, "admin"+string(rune(48+i)), "dummy_hash", 1, 0)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to create test admins: %v", err)
	}
}

func TestListTemplates_Empty(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, RoleName: "super_admin", IsSuper: true}

	templates, err := ListTemplates(context.Background(), db, actor, TemplateFilters{})
	if err != nil {
		t.Fatalf("ListTemplates failed: %v", err)
	}
	if len(templates) != 0 {
		t.Errorf("expected 0 templates, got %d", len(templates))
	}
}

func TestCreateTemplate_ValidXray(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, RoleName: "super_admin", IsSuper: true}

	input := CreateTemplateInput{
		Name:        "Xray VLESS Production",
		AdapterKind: "xray",
		ParamsJSON:  `{"listen_port":443,"protocol":"vless"}`,
		Description: "Production VLESS config",
		Tags:        []string{"production", "vless"},
		IsPublic:    true,
	}

	tmpl, err := CreateTemplate(context.Background(), db, actor, input)
	if err != nil {
		t.Fatalf("CreateTemplate failed: %v", err)
	}
	if tmpl.ID == 0 {
		t.Error("expected non-zero ID")
	}
	if tmpl.Name != input.Name {
		t.Errorf("expected name %q, got %q", input.Name, tmpl.Name)
	}
	if len(tmpl.Tags) != 2 {
		t.Errorf("expected 2 tags, got %d", len(tmpl.Tags))
	}
}

func TestCreateTemplate_DuplicateName(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, RoleName: "super_admin", IsSuper: true}

	input := CreateTemplateInput{
		Name:        "Duplicate Template",
		AdapterKind: "xray",
		ParamsJSON:  `{"listen_port":443}`,
	}

	_, err := CreateTemplate(context.Background(), db, actor, input)
	if err != nil {
		t.Fatalf("first CreateTemplate failed: %v", err)
	}

	_, err = CreateTemplate(context.Background(), db, actor, input)
	if err == nil {
		t.Error("expected error for duplicate name, got nil")
	}
}

func TestListTemplates_ScopeFiltering(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	admin1 := rbac.Actor{AdminID: 1, RoleName: "admin", IsSuper: false}
	admin2 := rbac.Actor{AdminID: 2, RoleName: "admin", IsSuper: false}

	// Admin 1 creates a private template
	input := CreateTemplateInput{
		Name:        "Private Template",
		AdapterKind: "xray",
		ParamsJSON:  `{"listen_port":8080}`,
		IsPublic:    false,
	}
	_, err := CreateTemplate(context.Background(), db, admin1, input)
	if err != nil {
		t.Fatalf("CreateTemplate failed: %v", err)
	}

	// Admin 2 should NOT see it
	templates, err := ListTemplates(context.Background(), db, admin2, TemplateFilters{})
	if err != nil {
		t.Fatalf("ListTemplates failed: %v", err)
	}
	if len(templates) != 0 {
		t.Errorf("admin2 should not see admin1's private template, got %d templates", len(templates))
	}

	// Admin 1 should see it
	templates, err = ListTemplates(context.Background(), db, admin1, TemplateFilters{})
	if err != nil {
		t.Fatalf("ListTemplates failed: %v", err)
	}
	if len(templates) != 1 {
		t.Errorf("admin1 should see own template, got %d templates", len(templates))
	}
}

func TestGetTemplate_PermissionDenied(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	admin1 := rbac.Actor{AdminID: 1, RoleName: "admin", IsSuper: false}
	admin2 := rbac.Actor{AdminID: 2, RoleName: "admin", IsSuper: false}

	input := CreateTemplateInput{
		Name:        "Private Template",
		AdapterKind: "xray",
		ParamsJSON:  `{"listen_port":8080}`,
		IsPublic:    false,
	}
	tmpl, err := CreateTemplate(context.Background(), db, admin1, input)
	if err != nil {
		t.Fatalf("CreateTemplate failed: %v", err)
	}

	// Admin 2 should not be able to get it
	_, err = GetTemplate(context.Background(), db, admin2, tmpl.ID)
	if err == nil {
		t.Error("expected error getting private template from different admin, got nil")
	}

	// Admin 1 should be able to get it
	_, err = GetTemplate(context.Background(), db, admin1, tmpl.ID)
	if err != nil {
		t.Errorf("admin1 should be able to get own template: %v", err)
	}
}

func TestGetTemplate_PublicTemplate(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	admin1 := rbac.Actor{AdminID: 1, RoleName: "admin", IsSuper: false}
	admin2 := rbac.Actor{AdminID: 2, RoleName: "admin", IsSuper: false}

	input := CreateTemplateInput{
		Name:        "Public Template",
		AdapterKind: "xray",
		ParamsJSON:  `{"listen_port":8080}`,
		IsPublic:    true,
	}
	tmpl, err := CreateTemplate(context.Background(), db, admin1, input)
	if err != nil {
		t.Fatalf("CreateTemplate failed: %v", err)
	}

	// Admin 2 should be able to get a public template
	got, err := GetTemplate(context.Background(), db, admin2, tmpl.ID)
	if err != nil {
		t.Errorf("admin2 should be able to get public template: %v", err)
	}
	if got.ID != tmpl.ID {
		t.Errorf("expected template ID %d, got %d", tmpl.ID, got.ID)
	}
}

func TestUpdateTemplate_OwnTemplate(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	admin1 := rbac.Actor{AdminID: 1, RoleName: "admin", IsSuper: false}

	input := CreateTemplateInput{
		Name:        "Update Test",
		AdapterKind: "xray",
		ParamsJSON:  `{"listen_port":8080}`,
		Description: "Original",
		IsPublic:    false,
	}
	tmpl, err := CreateTemplate(context.Background(), db, admin1, input)
	if err != nil {
		t.Fatalf("CreateTemplate failed: %v", err)
	}

	// Update the template
	newDesc := "Updated"
	updateInput := UpdateTemplateInput{
		Description: &newDesc,
	}
	err = UpdateTemplate(context.Background(), db, admin1, tmpl.ID, updateInput)
	if err != nil {
		t.Fatalf("UpdateTemplate failed: %v", err)
	}

	// Verify update
	updated, err := GetTemplate(context.Background(), db, admin1, tmpl.ID)
	if err != nil {
		t.Fatalf("GetTemplate failed: %v", err)
	}
	if updated.Description != newDesc {
		t.Errorf("expected description %q, got %q", newDesc, updated.Description)
	}
}

func TestUpdateTemplate_DifferentAdminForbidden(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	admin1 := rbac.Actor{AdminID: 1, RoleName: "admin", IsSuper: false}
	admin2 := rbac.Actor{AdminID: 2, RoleName: "admin", IsSuper: false}

	input := CreateTemplateInput{
		Name:        "Update Test",
		AdapterKind: "xray",
		ParamsJSON:  `{"listen_port":8080}`,
		IsPublic:    true,
	}
	tmpl, err := CreateTemplate(context.Background(), db, admin1, input)
	if err != nil {
		t.Fatalf("CreateTemplate failed: %v", err)
	}

	// Admin 2 should not be able to update admin 1's template
	newDesc := "Hacked"
	updateInput := UpdateTemplateInput{
		Description: &newDesc,
	}
	err = UpdateTemplate(context.Background(), db, admin2, tmpl.ID, updateInput)
	if err == nil {
		t.Error("expected error updating another admin's template, got nil")
	}
}

func TestDeleteTemplate_OwnTemplate(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	admin1 := rbac.Actor{AdminID: 1, RoleName: "admin", IsSuper: false}

	input := CreateTemplateInput{
		Name:        "Delete Test",
		AdapterKind: "xray",
		ParamsJSON:  `{"listen_port":8080}`,
	}
	tmpl, err := CreateTemplate(context.Background(), db, admin1, input)
	if err != nil {
		t.Fatalf("CreateTemplate failed: %v", err)
	}

	// Delete the template
	err = DeleteTemplate(context.Background(), db, admin1, tmpl.ID)
	if err != nil {
		t.Fatalf("DeleteTemplate failed: %v", err)
	}

	// Verify deletion
	_, err = GetTemplate(context.Background(), db, admin1, tmpl.ID)
	if err == nil {
		t.Error("expected error getting deleted template, got nil")
	}
}

func TestDeleteTemplate_DifferentAdminForbidden(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	admin1 := rbac.Actor{AdminID: 1, RoleName: "admin", IsSuper: false}
	admin2 := rbac.Actor{AdminID: 2, RoleName: "admin", IsSuper: false}

	input := CreateTemplateInput{
		Name:        "Delete Test",
		AdapterKind: "xray",
		ParamsJSON:  `{"listen_port":8080}`,
		IsPublic:    true,
	}
	tmpl, err := CreateTemplate(context.Background(), db, admin1, input)
	if err != nil {
		t.Fatalf("CreateTemplate failed: %v", err)
	}

	// Admin 2 should not be able to delete admin 1's template
	err = DeleteTemplate(context.Background(), db, admin2, tmpl.ID)
	if err == nil {
		t.Error("expected error deleting another admin's template, got nil")
	}
}

func TestListTemplates_AdapterKindFilter(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, RoleName: "super_admin", IsSuper: true}

	// Create templates with different adapter kinds
	xrayInput := CreateTemplateInput{
		Name:        "Xray Template",
		AdapterKind: "xray",
		ParamsJSON:  `{"listen_port":443}`,
		IsPublic:    true,
	}
	_, err := CreateTemplate(context.Background(), db, actor, xrayInput)
	if err != nil {
		t.Fatalf("CreateTemplate xray failed: %v", err)
	}

	vmessInput := CreateTemplateInput{
		Name:        "Vmess Template",
		AdapterKind: "singbox",
		ParamsJSON:  `{"listen_port":8080}`,
		IsPublic:    true,
	}
	_, err = CreateTemplate(context.Background(), db, actor, vmessInput)
	if err != nil {
		t.Fatalf("CreateTemplate singbox failed: %v", err)
	}

	// List with xray filter
	templates, err := ListTemplates(context.Background(), db, actor, TemplateFilters{AdapterKind: "xray"})
	if err != nil {
		t.Fatalf("ListTemplates failed: %v", err)
	}
	if len(templates) != 1 {
		t.Errorf("expected 1 xray template, got %d", len(templates))
	}
	if templates[0].AdapterKind != "xray" {
		t.Errorf("expected adapter kind xray, got %q", templates[0].AdapterKind)
	}

	// List with singbox filter
	templates, err = ListTemplates(context.Background(), db, actor, TemplateFilters{AdapterKind: "singbox"})
	if err != nil {
		t.Fatalf("ListTemplates failed: %v", err)
	}
	if len(templates) != 1 {
		t.Errorf("expected 1 singbox template, got %d", len(templates))
	}
	if templates[0].AdapterKind != "singbox" {
		t.Errorf("expected adapter kind singbox, got %q", templates[0].AdapterKind)
	}
}

func TestCreateTemplate_InvalidJSON(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, RoleName: "super_admin", IsSuper: true}

	input := CreateTemplateInput{
		Name:        "Bad JSON",
		AdapterKind: "xray",
		ParamsJSON:  `{invalid json}`,
	}

	_, err := CreateTemplate(context.Background(), db, actor, input)
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestCreateTemplate_MissingName(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, RoleName: "super_admin", IsSuper: true}

	input := CreateTemplateInput{
		AdapterKind: "xray",
		ParamsJSON:  `{"listen_port":443}`,
	}

	_, err := CreateTemplate(context.Background(), db, actor, input)
	if err == nil {
		t.Error("expected error for missing name, got nil")
	}
}

func TestCreateTemplate_MissingAdapterKind(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, RoleName: "super_admin", IsSuper: true}

	input := CreateTemplateInput{
		Name:       "Test",
		ParamsJSON: `{"listen_port":443}`,
	}

	_, err := CreateTemplate(context.Background(), db, actor, input)
	if err == nil {
		t.Error("expected error for missing adapter kind, got nil")
	}
}

func TestCreateTemplate_MissingParamsJSON(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, RoleName: "super_admin", IsSuper: true}

	input := CreateTemplateInput{
		Name:        "Test",
		AdapterKind: "xray",
	}

	_, err := CreateTemplate(context.Background(), db, actor, input)
	if err == nil {
		t.Error("expected error for missing params_json, got nil")
	}
}

func TestListByAdapterKind(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, RoleName: "super_admin", IsSuper: true}

	// Create templates with different adapter kinds
	xrayInput := CreateTemplateInput{
		Name:        "Xray Template",
		AdapterKind: "xray",
		ParamsJSON:  `{"listen_port":443}`,
		IsPublic:    true,
	}
	_, err := CreateTemplate(context.Background(), db, actor, xrayInput)
	if err != nil {
		t.Fatalf("CreateTemplate failed: %v", err)
	}

	singboxInput := CreateTemplateInput{
		Name:        "Singbox Template",
		AdapterKind: "singbox",
		ParamsJSON:  `{"listen_port":8080}`,
		IsPublic:    true,
	}
	_, err = CreateTemplate(context.Background(), db, actor, singboxInput)
	if err != nil {
		t.Fatalf("CreateTemplate failed: %v", err)
	}

	// List only xray templates
	templates, err := ListByAdapterKind(context.Background(), db, actor, "xray")
	if err != nil {
		t.Fatalf("ListByAdapterKind failed: %v", err)
	}

	if len(templates) != 1 {
		t.Errorf("expected 1 xray template, got %d", len(templates))
	}

	if len(templates) > 0 && templates[0].AdapterKind != "xray" {
		t.Errorf("expected adapter_kind 'xray', got %q", templates[0].AdapterKind)
	}
}

func TestApplyToNode(t *testing.T) {
	db := openTestDB(t)
	createTestAdmins(t, db)
	actor := rbac.Actor{AdminID: 1, RoleName: "super_admin", IsSuper: true}

	// Create a template with variables
	input := CreateTemplateInput{
		Name:        "Template With Variables",
		AdapterKind: "xray",
		ParamsJSON:  `{"uuid":"{{GENERATE_UUID}}","password":"{{GENERATE_PASSWORD}}"}`,
		IsPublic:    true,
	}
	tmpl, err := CreateTemplate(context.Background(), db, actor, input)
	if err != nil {
		t.Fatalf("CreateTemplate failed: %v", err)
	}

	// Try to apply to a node (should return "not yet implemented" error)
	err = ApplyToNode(context.Background(), db, actor, tmpl.ID, 1)
	if err == nil {
		t.Error("expected error (not implemented), got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("expected 'not yet implemented' error, got: %v", err)
	}
}

func TestExpandTemplateVariables(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		validate func(string) bool
	}{
		{
			name:  "UUID generation",
			input: `{"uuid":"{{GENERATE_UUID}}"}`,
			validate: func(result string) bool {
				return strings.Contains(result, `"uuid":"`) &&
					!strings.Contains(result, "{{GENERATE_UUID}}")
			},
		},
		{
			name:  "Password generation",
			input: `{"password":"{{GENERATE_PASSWORD}}"}`,
			validate: func(result string) bool {
				return strings.Contains(result, `"password":"`) &&
					!strings.Contains(result, "{{GENERATE_PASSWORD}}")
			},
		},
		{
			name:  "Secret generation",
			input: `{"secret":"{{GENERATE_SECRET}}"}`,
			validate: func(result string) bool {
				return strings.Contains(result, `"secret":"`) &&
					!strings.Contains(result, "{{GENERATE_SECRET}}")
			},
		},
		{
			name:  "Multiple variables",
			input: `{"uuid":"{{GENERATE_UUID}}","password":"{{GENERATE_PASSWORD}}","secret":"{{GENERATE_SECRET}}"}`,
			validate: func(result string) bool {
				return !strings.Contains(result, "{{GENERATE_") &&
					strings.Contains(result, `"uuid":"`) &&
					strings.Contains(result, `"password":"`) &&
					strings.Contains(result, `"secret":"`)
			},
		},
	}

		for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := expandTemplateVariables(tt.input)
			if !tt.validate(result) {
				t.Errorf("expandTemplateVariables(%q) = %q, validation failed", tt.input, result)
			}
		})
	}
}

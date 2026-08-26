package templates

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

// ServiceTemplate is a saved inbound configuration.
//
// Tagged for the same reason UserPreset is: these marshalled with Go field
// names while the rest of the API is snake_case, and nothing consumed them yet.
type ServiceTemplate struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	AdapterKind string   `json:"adapter_kind"`
	ParamsJSON  string   `json:"params_json"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	IsPublic    bool     `json:"is_public"`
	CreatedBy   *int64   `json:"created_by"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
}

type TemplateFilters struct {
	AdapterKind string
	Tags        []string
}

type CreateTemplateInput struct {
	Name        string
	AdapterKind string
	ParamsJSON  string
	Description string
	Tags        []string
	IsPublic    bool
}

type UpdateTemplateInput struct {
	Name        *string
	ParamsJSON  *string
	Description *string
	Tags        *[]string
	IsPublic    *bool
}

func ListTemplates(ctx context.Context, db *store.Store, actor rbac.Actor, filters TemplateFilters) ([]ServiceTemplate, error) {
	query := `
		SELECT id, name, adapter_kind, params_json, description, tags_json, is_public, created_by, created_at, updated_at
		FROM service_templates
		WHERE (is_public = 1 OR created_by = ?)
	`
	args := []interface{}{actor.AdminID}

	if filters.AdapterKind != "" {
		query += " AND adapter_kind = ?"
		args = append(args, filters.AdapterKind)
	}

	query += " ORDER BY name"

	rows, err := db.Read().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query templates: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var templates []ServiceTemplate
	for rows.Next() {
		var t ServiceTemplate
		var tagsJSON string
		if err := rows.Scan(&t.ID, &t.Name, &t.AdapterKind, &t.ParamsJSON, &t.Description, &tagsJSON, &t.IsPublic, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan template: %w", err)
		}
		if err := json.Unmarshal([]byte(tagsJSON), &t.Tags); err != nil {
			return nil, fmt.Errorf("unmarshal tags: %w", err)
		}
		templates = append(templates, t)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate templates: %w", err)
	}

	return templates, nil
}

func CreateTemplate(ctx context.Context, db *store.Store, actor rbac.Actor, input CreateTemplateInput) (ServiceTemplate, error) {
	if input.Name == "" {
		return ServiceTemplate{}, errors.New("template name is required")
	}
	if input.AdapterKind == "" {
		return ServiceTemplate{}, errors.New("adapter kind is required")
	}
	if input.ParamsJSON == "" {
		return ServiceTemplate{}, errors.New("params_json is required")
	}

	// Validate JSON
	var params map[string]interface{}
	if err := json.Unmarshal([]byte(input.ParamsJSON), &params); err != nil {
		return ServiceTemplate{}, fmt.Errorf("invalid params_json: %w", err)
	}

	tagsJSON, err := json.Marshal(input.Tags)
	if err != nil {
		return ServiceTemplate{}, fmt.Errorf("marshal tags: %w", err)
	}

	now := time.Now().Unix()

	var result ServiceTemplate
	err = db.Write(ctx, func(tx *sql.Tx) error {
		// Check for duplicate name
		var existing int64
		err := tx.QueryRowContext(ctx, "SELECT id FROM service_templates WHERE name = ?", input.Name).Scan(&existing)
		if err == nil {
			return errors.New("template name already exists")
		}
		if err != sql.ErrNoRows {
			return fmt.Errorf("check duplicate: %w", err)
		}

		// Insert and return the full record
		err = tx.QueryRowContext(ctx, `
			INSERT INTO service_templates (name, adapter_kind, params_json, description, tags_json, is_public, created_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			RETURNING id, name, adapter_kind, params_json, description, tags_json, is_public, created_by, created_at, updated_at
		`, input.Name, input.AdapterKind, input.ParamsJSON, input.Description, string(tagsJSON), input.IsPublic, actor.AdminID, now, now).
			Scan(&result.ID, &result.Name, &result.AdapterKind, &result.ParamsJSON, &result.Description, &tagsJSON, &result.IsPublic, &result.CreatedBy, &result.CreatedAt, &result.UpdatedAt)
		if err != nil {
			return fmt.Errorf("insert template: %w", err)
		}

		// Unmarshal tags
		if err := json.Unmarshal([]byte(tagsJSON), &result.Tags); err != nil {
			return fmt.Errorf("unmarshal tags: %w", err)
		}

		return nil
	})
	if err != nil {
		return ServiceTemplate{}, err
	}

	return result, nil
}

func GetTemplate(ctx context.Context, db *store.Store, actor rbac.Actor, id int64) (ServiceTemplate, error) {
	var t ServiceTemplate
	var tagsJSON string
	err := db.Read().QueryRowContext(ctx, `
		SELECT id, name, adapter_kind, params_json, description, tags_json, is_public, created_by, created_at, updated_at
		FROM service_templates
		WHERE id = ? AND (is_public = 1 OR created_by = ?)
	`, id, actor.AdminID).Scan(&t.ID, &t.Name, &t.AdapterKind, &t.ParamsJSON, &t.Description, &tagsJSON, &t.IsPublic, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt)

	if errors.Is(err, sql.ErrNoRows) {
		return ServiceTemplate{}, fmt.Errorf("template not found or access denied")
	}
	if err != nil {
		return ServiceTemplate{}, fmt.Errorf("query template: %w", err)
	}

	if err := json.Unmarshal([]byte(tagsJSON), &t.Tags); err != nil {
		return ServiceTemplate{}, fmt.Errorf("unmarshal tags: %w", err)
	}

	return t, nil
}

func UpdateTemplate(ctx context.Context, db *store.Store, actor rbac.Actor, id int64, input UpdateTemplateInput) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		// Check ownership
		var createdBy sql.NullInt64
		err := tx.QueryRowContext(ctx, "SELECT created_by FROM service_templates WHERE id = ?", id).Scan(&createdBy)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("template not found")
		}
		if err != nil {
			return fmt.Errorf("query template: %w", err)
		}

		if !createdBy.Valid || createdBy.Int64 != actor.AdminID {
			// Only super admin can modify another admin's template
			if !actor.IsSuper {
				return fmt.Errorf("cannot modify another admin's template")
			}
		}

		updates := []string{}
		args := []interface{}{}

		if input.Name != nil {
			updates = append(updates, "name = ?")
			args = append(args, *input.Name)
		}
		if input.ParamsJSON != nil {
			// Validate JSON
			var params map[string]interface{}
			if err := json.Unmarshal([]byte(*input.ParamsJSON), &params); err != nil {
				return fmt.Errorf("invalid params_json: %w", err)
			}
			updates = append(updates, "params_json = ?")
			args = append(args, *input.ParamsJSON)
		}
		if input.Description != nil {
			updates = append(updates, "description = ?")
			args = append(args, *input.Description)
		}
		if input.Tags != nil {
			tagsJSON, err := json.Marshal(*input.Tags)
			if err != nil {
				return fmt.Errorf("marshal tags: %w", err)
			}
			updates = append(updates, "tags_json = ?")
			args = append(args, string(tagsJSON))
		}
		if input.IsPublic != nil {
			updates = append(updates, "is_public = ?")
			args = append(args, *input.IsPublic)
		}

		if len(updates) == 0 {
			return nil // no-op
		}

		updates = append(updates, "updated_at = ?")
		args = append(args, time.Now().Unix())
		args = append(args, id)

		query := "UPDATE service_templates SET " + updates[0]
		for i := 1; i < len(updates); i++ {
			query += ", " + updates[i]
		}
		query += " WHERE id = ?"

		_, err = tx.ExecContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("update template: %w", err)
		}

		return nil
	})
}

func DeleteTemplate(ctx context.Context, db *store.Store, actor rbac.Actor, id int64) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		// Check ownership
		var createdBy sql.NullInt64
		err := tx.QueryRowContext(ctx, "SELECT created_by FROM service_templates WHERE id = ?", id).Scan(&createdBy)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("template not found")
		}
		if err != nil {
			return fmt.Errorf("query template: %w", err)
		}

		if !createdBy.Valid || createdBy.Int64 != actor.AdminID {
			// Only super admin can delete another admin's template
			if !actor.IsSuper {
				return fmt.Errorf("cannot delete another admin's template")
			}
		}

		_, err = tx.ExecContext(ctx, "DELETE FROM service_templates WHERE id = ?", id)
		if err != nil {
			return fmt.Errorf("delete template: %w", err)
		}

		return nil
	})
}

// ListByAdapterKind returns templates filtered by adapter kind.
func ListByAdapterKind(ctx context.Context, db *store.Store, actor rbac.Actor, adapterKind string) ([]ServiceTemplate, error) {
	filters := TemplateFilters{AdapterKind: adapterKind}
	return ListTemplates(ctx, db, actor, filters)
}

// ApplyToNode applies a template to a node, expanding template variables.
// Supported variables: {{GENERATE_UUID}}, {{GENERATE_PASSWORD}}, {{GENERATE_SECRET}}
func ApplyToNode(ctx context.Context, db *store.Store, actor rbac.Actor, templateID, nodeID int64) error {
	// Get the template
	template, err := GetTemplate(ctx, db, actor, templateID)
	if err != nil {
		return fmt.Errorf("get template: %w", err)
	}

	// Expand template variables
	expandedParams := expandTemplateVariables(template.ParamsJSON)

	// TODO: Apply the expanded params to the node
	// This would involve updating the node's service configuration
	// For now, we'll store it as a placeholder implementation
	_ = expandedParams
	_ = nodeID

	return fmt.Errorf("ApplyToNode not yet implemented")
}

// expandTemplateVariables replaces template variables with generated values.
func expandTemplateVariables(paramsJSON string) string {
	result := paramsJSON

	// Replace {{GENERATE_UUID}} with a new UUID
	for strings.Contains(result, "{{GENERATE_UUID}}") {
		result = strings.Replace(result, "{{GENERATE_UUID}}", generateUUID(), 1)
	}

	// Replace {{GENERATE_PASSWORD}} with a 16-character random password
	for strings.Contains(result, "{{GENERATE_PASSWORD}}") {
		password := generateRandomString(16)
		result = strings.Replace(result, "{{GENERATE_PASSWORD}}", password, 1)
	}

	// Replace {{GENERATE_SECRET}} with a 32-character random secret
	for strings.Contains(result, "{{GENERATE_SECRET}}") {
		secret := generateRandomString(32)
		result = strings.Replace(result, "{{GENERATE_SECRET}}", secret, 1)
	}

	return result
}

// generateUUID generates a new UUID v4.
func generateUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // Version 4
	b[8] = (b[8] & 0x3f) | 0x80 // Variant RFC4122
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// generateRandomString generates a random hex string of the specified length.
func generateRandomString(length int) string {
	bytes := make([]byte, (length+1)/2)
	_, _ = rand.Read(bytes)
	return hex.EncodeToString(bytes)[:length]
}

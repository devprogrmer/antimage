package templates

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

type UserPreset struct {
	ID                  int64
	Name                string
	Description         string
	QuotaBytes          *int64
	ValidityDays        *int
	AutoAssignServices  []int64
	AutoAssignNodeTags  []string
	IsPublic            bool
	CreatedBy           *int64
	CreatedAt           int64
	UpdatedAt           int64
}

type CreatePresetInput struct {
	Name                string
	Description         string
	QuotaBytes          *int64
	ValidityDays        *int
	AutoAssignServices  []int64
	AutoAssignNodeTags  []string
	IsPublic            bool
}

type UpdatePresetInput struct {
	Name                *string
	Description         *string
	QuotaBytes          **int64
	ValidityDays        **int
	AutoAssignServices  *[]int64
	AutoAssignNodeTags  *[]string
	IsPublic            *bool
}

func ListPresets(ctx context.Context, db *store.Store, actor rbac.Actor) ([]UserPreset, error) {
	query := `
		SELECT id, name, description, quota_bytes, validity_days, auto_assign_services_json, auto_assign_node_tags_json, is_public, created_by, created_at, updated_at
		FROM user_presets
		WHERE is_public = 1 OR created_by = ?
		ORDER BY name
	`

	rows, err := db.Read().QueryContext(ctx, query, actor.AdminID)
	if err != nil {
		return nil, fmt.Errorf("query presets: %w", err)
	}
	defer rows.Close()

	var presets []UserPreset
	for rows.Next() {
		var p UserPreset
		var servicesJSON, tagsJSON string
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.QuotaBytes, &p.ValidityDays, &servicesJSON, &tagsJSON, &p.IsPublic, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan preset: %w", err)
		}
		if err := json.Unmarshal([]byte(servicesJSON), &p.AutoAssignServices); err != nil {
			return nil, fmt.Errorf("unmarshal services: %w", err)
		}
		if err := json.Unmarshal([]byte(tagsJSON), &p.AutoAssignNodeTags); err != nil {
			return nil, fmt.Errorf("unmarshal tags: %w", err)
		}
		presets = append(presets, p)
	}

	return presets, rows.Err()
}

func CreatePreset(ctx context.Context, db *store.Store, actor rbac.Actor, input CreatePresetInput) (UserPreset, error) {
	if input.Name == "" {
		return UserPreset{}, errors.New("preset name is required")
	}

	servicesJSON, err := json.Marshal(input.AutoAssignServices)
	if err != nil {
		return UserPreset{}, fmt.Errorf("marshal services: %w", err)
	}

	tagsJSON, err := json.Marshal(input.AutoAssignNodeTags)
	if err != nil {
		return UserPreset{}, fmt.Errorf("marshal tags: %w", err)
	}

	now := time.Now().Unix()

	result, err := db.WriteDB().ExecContext(ctx, `
		INSERT INTO user_presets (name, description, quota_bytes, validity_days, auto_assign_services_json, auto_assign_node_tags_json, is_public, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, input.Name, input.Description, input.QuotaBytes, input.ValidityDays, string(servicesJSON), string(tagsJSON), input.IsPublic, actor.AdminID, now, now)
	if err != nil {
		return UserPreset{}, fmt.Errorf("insert preset: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return UserPreset{}, fmt.Errorf("get insert id: %w", err)
	}

	return UserPreset{
		ID:                 id,
		Name:               input.Name,
		Description:        input.Description,
		QuotaBytes:         input.QuotaBytes,
		ValidityDays:       input.ValidityDays,
		AutoAssignServices: input.AutoAssignServices,
		AutoAssignNodeTags: input.AutoAssignNodeTags,
		IsPublic:           input.IsPublic,
		CreatedBy:          &actor.AdminID,
		CreatedAt:          now,
		UpdatedAt:          now,
	}, nil
}

func GetPreset(ctx context.Context, db *store.Store, actor rbac.Actor, id int64) (UserPreset, error) {
	var p UserPreset
	var servicesJSON, tagsJSON string
	err := db.Read().QueryRowContext(ctx, `
		SELECT id, name, description, quota_bytes, validity_days, auto_assign_services_json, auto_assign_node_tags_json, is_public, created_by, created_at, updated_at
		FROM user_presets
		WHERE id = ? AND (is_public = 1 OR created_by = ?)
	`, id, actor.AdminID).Scan(&p.ID, &p.Name, &p.Description, &p.QuotaBytes, &p.ValidityDays, &servicesJSON, &tagsJSON, &p.IsPublic, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt)

	if errors.Is(err, sql.ErrNoRows) {
		return UserPreset{}, fmt.Errorf("preset not found or access denied")
	}
	if err != nil {
		return UserPreset{}, fmt.Errorf("query preset: %w", err)
	}

	if err := json.Unmarshal([]byte(servicesJSON), &p.AutoAssignServices); err != nil {
		return UserPreset{}, fmt.Errorf("unmarshal services: %w", err)
	}
	if err := json.Unmarshal([]byte(tagsJSON), &p.AutoAssignNodeTags); err != nil {
		return UserPreset{}, fmt.Errorf("unmarshal tags: %w", err)
	}

	return p, nil
}

func UpdatePreset(ctx context.Context, db *store.Store, actor rbac.Actor, id int64, input UpdatePresetInput) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		// Check ownership
		var createdBy sql.NullInt64
		err := tx.QueryRowContext(ctx, "SELECT created_by FROM user_presets WHERE id = ?", id).Scan(&createdBy)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("preset not found")
		}
		if err != nil {
			return fmt.Errorf("query preset: %w", err)
		}

		if !createdBy.Valid || createdBy.Int64 != actor.AdminID {
			// Only super admin can modify another admin's preset
			if !actor.IsSuper {
				return fmt.Errorf("cannot modify another admin's preset")
			}
		}

		// Build dynamic update query
		updates := []string{}
		args := []interface{}{}

		if input.Name != nil {
			updates = append(updates, "name = ?")
			args = append(args, *input.Name)
		}
		if input.Description != nil {
			updates = append(updates, "description = ?")
			args = append(args, *input.Description)
		}
		// Handle double pointers for nullable fields
		if input.QuotaBytes != nil {
			updates = append(updates, "quota_bytes = ?")
			args = append(args, *input.QuotaBytes)
		}
		if input.ValidityDays != nil {
			updates = append(updates, "validity_days = ?")
			args = append(args, *input.ValidityDays)
		}
		if input.AutoAssignServices != nil {
			servicesJSON, err := json.Marshal(*input.AutoAssignServices)
			if err != nil {
				return fmt.Errorf("marshal services: %w", err)
			}
			updates = append(updates, "auto_assign_services_json = ?")
			args = append(args, string(servicesJSON))
		}
		if input.AutoAssignNodeTags != nil {
			tagsJSON, err := json.Marshal(*input.AutoAssignNodeTags)
			if err != nil {
				return fmt.Errorf("marshal tags: %w", err)
			}
			updates = append(updates, "auto_assign_node_tags_json = ?")
			args = append(args, string(tagsJSON))
		}
		if input.IsPublic != nil {
			updates = append(updates, "is_public = ?")
			args = append(args, *input.IsPublic)
		}

		if len(updates) == 0 {
			return nil
		}

		now := time.Now().Unix()
		updates = append(updates, "updated_at = ?")
		args = append(args, now, id)

		query := "UPDATE user_presets SET " + strings.Join(updates, ", ") + " WHERE id = ?"

		_, err = tx.ExecContext(ctx, query, args...)
		return err
	})
}

func DeletePreset(ctx context.Context, db *store.Store, actor rbac.Actor, id int64) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		// Check ownership
		var createdBy sql.NullInt64
		err := tx.QueryRowContext(ctx, "SELECT created_by FROM user_presets WHERE id = ?", id).Scan(&createdBy)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("preset not found")
		}
		if err != nil {
			return fmt.Errorf("query preset: %w", err)
		}

		if !createdBy.Valid || createdBy.Int64 != actor.AdminID {
			// Only super admin can delete another admin's preset
			if !actor.IsSuper {
				return fmt.Errorf("cannot delete another admin's preset")
			}
		}

		_, err = tx.ExecContext(ctx, "DELETE FROM user_presets WHERE id = ?", id)
		return err
	})
}

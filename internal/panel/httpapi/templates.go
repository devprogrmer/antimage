package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/amyrm/antimage/internal/panel/templates"
)

// ---------------------------------------------------------------------------
// Service Templates
// ---------------------------------------------------------------------------

func (d Deps) handleListServiceTemplates(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}

	filters := templates.TemplateFilters{
		AdapterKind: r.URL.Query().Get("adapter_kind"),
	}

	list, err := templates.ListTemplates(r.Context(), d.Store, *actor, filters)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to list service templates")
		return
	}

	if list == nil {
		list = []templates.ServiceTemplate{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"templates": list})
}

func (d Deps) handleCreateServiceTemplate(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}

	var req struct {
		Name        string   `json:"name"`
		AdapterKind string   `json:"adapter_kind"`
		ParamsJSON  string   `json:"params_json"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
		IsPublic    bool     `json:"is_public"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		WriteError(w, http.StatusUnprocessableEntity, "validation", "name is required")
		return
	}

	input := templates.CreateTemplateInput{
		Name:        req.Name,
		AdapterKind: req.AdapterKind,
		ParamsJSON:  req.ParamsJSON,
		Description: req.Description,
		Tags:        req.Tags,
		IsPublic:    req.IsPublic,
	}

	tmpl, err := templates.CreateTemplate(r.Context(), d.Store, *actor, input)
	if err != nil {
		if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "invalid") {
			WriteError(w, http.StatusUnprocessableEntity, "validation", err.Error())
			return
		}
		if strings.Contains(err.Error(), "already exists") {
			WriteError(w, http.StatusConflict, "conflict", "a template with that name already exists")
			return
		}
		WriteError(w, http.StatusInternalServerError, "internal", "failed to create service template")
		return
	}

	WriteJSON(w, http.StatusCreated, map[string]any{"id": tmpl.ID, "template": tmpl})
}

func (d Deps) handleGetServiceTemplate(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}

	id, err := pathInt64(r, "id")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid template id")
		return
	}

	tmpl, err := templates.GetTemplate(r.Context(), d.Store, *actor, id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "access denied") {
			WriteError(w, http.StatusNotFound, "not_found", "template not found")
			return
		}
		WriteError(w, http.StatusInternalServerError, "internal", "failed to retrieve service template")
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{"template": tmpl})
}

func (d Deps) handleUpdateServiceTemplate(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}

	id, err := pathInt64(r, "id")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid template id")
		return
	}

	var req struct {
		Name        *string   `json:"name"`
		ParamsJSON  *string   `json:"params_json"`
		Description *string   `json:"description"`
		Tags        *[]string `json:"tags"`
		IsPublic    *bool     `json:"is_public"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}

	input := templates.UpdateTemplateInput{
		Name:        req.Name,
		ParamsJSON:  req.ParamsJSON,
		Description: req.Description,
		Tags:        req.Tags,
		IsPublic:    req.IsPublic,
	}

	if err := templates.UpdateTemplate(r.Context(), d.Store, *actor, id, input); err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteError(w, http.StatusNotFound, "not_found", "template not found")
			return
		}
		if strings.Contains(err.Error(), "cannot modify") {
			WriteError(w, http.StatusForbidden, "forbidden", "permission denied")
			return
		}
		WriteError(w, http.StatusInternalServerError, "internal", "failed to update service template")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (d Deps) handleDeleteServiceTemplate(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}

	id, err := pathInt64(r, "id")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid template id")
		return
	}

	if err := templates.DeleteTemplate(r.Context(), d.Store, *actor, id); err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteError(w, http.StatusNotFound, "not_found", "template not found")
			return
		}
		if strings.Contains(err.Error(), "cannot delete") {
			WriteError(w, http.StatusForbidden, "forbidden", "permission denied")
			return
		}
		WriteError(w, http.StatusInternalServerError, "internal", "failed to delete service template")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// User Presets
// ---------------------------------------------------------------------------

func (d Deps) handleListUserPresets(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}

	list, err := templates.ListPresets(r.Context(), d.Store, *actor)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to list user presets")
		return
	}

	if list == nil {
		list = []templates.UserPreset{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"presets": list})
}

func (d Deps) handleCreateUserPreset(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}

	var req struct {
		Name         string          `json:"name"`
		Description  string          `json:"description"`
		QuotaBytes   *int64          `json:"quota_bytes"`
		ValidityDays *int            `json:"validity_days"`
		AutoAssignServicesJSON json.RawMessage `json:"auto_assign_services_json"`
		AutoAssignNodeTagsJSON json.RawMessage `json:"auto_assign_node_tags_json"`
		IsPublic     bool            `json:"is_public"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		WriteError(w, http.StatusUnprocessableEntity, "validation", "name is required")
		return
	}

	input := templates.CreatePresetInput{
		Name:                   req.Name,
		Description:            req.Description,
		QuotaBytes:             req.QuotaBytes,
		ValidityDays:           req.ValidityDays,
		AutoAssignServicesJSON: req.AutoAssignServicesJSON,
		AutoAssignNodeTagsJSON: req.AutoAssignNodeTagsJSON,
		IsPublic:               req.IsPublic,
	}

	preset, err := templates.CreatePreset(r.Context(), d.Store, *actor, input)
	if err != nil {
		if strings.Contains(err.Error(), "required") {
			WriteError(w, http.StatusUnprocessableEntity, "validation", err.Error())
			return
		}
		WriteError(w, http.StatusInternalServerError, "internal", "failed to create user preset")
		return
	}

	WriteJSON(w, http.StatusCreated, map[string]any{"id": preset.ID, "preset": preset})
}

func (d Deps) handleGetUserPreset(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}

	id, err := pathInt64(r, "id")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid preset id")
		return
	}

	preset, err := templates.GetPreset(r.Context(), d.Store, *actor, id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "access denied") {
			WriteError(w, http.StatusNotFound, "not_found", "preset not found")
			return
		}
		WriteError(w, http.StatusInternalServerError, "internal", "failed to retrieve user preset")
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{"preset": preset})
}

func (d Deps) handleUpdateUserPreset(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}

	id, err := pathInt64(r, "id")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid preset id")
		return
	}

	var req struct {
		Name                   *string          `json:"name"`
		Description            *string          `json:"description"`
		QuotaBytes             **int64          `json:"quota_bytes"`
		ValidityDays           **int            `json:"validity_days"`
		AutoAssignServicesJSON *json.RawMessage `json:"auto_assign_services_json"`
		AutoAssignNodeTagsJSON *json.RawMessage `json:"auto_assign_node_tags_json"`
		IsPublic               *bool            `json:"is_public"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}

	input := templates.UpdatePresetInput{
		Name:                   req.Name,
		Description:            req.Description,
		QuotaBytes:             req.QuotaBytes,
		ValidityDays:           req.ValidityDays,
		AutoAssignServicesJSON: req.AutoAssignServicesJSON,
		AutoAssignNodeTagsJSON: req.AutoAssignNodeTagsJSON,
		IsPublic:               req.IsPublic,
	}

	_, err = templates.UpdatePreset(r.Context(), d.Store, *actor, id, input)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteError(w, http.StatusNotFound, "not_found", "preset not found")
			return
		}
		if strings.Contains(err.Error(), "cannot modify") || strings.Contains(err.Error(), "no fields") {
			WriteError(w, http.StatusForbidden, "forbidden", "permission denied")
			return
		}
		WriteError(w, http.StatusInternalServerError, "internal", "failed to update user preset")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (d Deps) handleDeleteUserPreset(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}

	id, err := pathInt64(r, "id")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid preset id")
		return
	}

	if err := templates.DeletePreset(r.Context(), d.Store, *actor, id); err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteError(w, http.StatusNotFound, "not_found", "preset not found")
			return
		}
		if strings.Contains(err.Error(), "cannot delete") {
			WriteError(w, http.StatusForbidden, "forbidden", "permission denied")
			return
		}
		WriteError(w, http.StatusInternalServerError, "internal", "failed to delete user preset")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// errors sentinel kept for compile-time import check
var _ = errors.New

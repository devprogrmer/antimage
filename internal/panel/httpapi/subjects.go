package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/resellers"
	"github.com/amyrm/antimage/internal/panel/service"
	"github.com/amyrm/antimage/internal/panel/subjects"
)

// subjectDTO is the wire shape of a subject.
//
// Credentials are deliberately absent. A list endpoint that returned them
// would put every user's credential in one response, in every log that
// captures response bodies, and in every browser cache. They are fetched one
// at a time through an explicitly authorized, audited reveal.
type subjectDTO struct {
	ID                     int64   `json:"id"`
	Name                   string  `json:"name"`
	Enabled                bool    `json:"enabled"`
	ExpiresAt              *int64  `json:"expires_at"`
	ExpiredAt              *int64  `json:"expired_at"`
	CreatedAt              int64   `json:"created_at"`
	Note                   string  `json:"note"`
	QuotaBytes             *int64  `json:"quota_bytes"`
	QuotaUsedBytes         int64   `json:"quota_used_bytes"`
	Frozen                 bool    `json:"frozen"`
	MaxDevices             *int64  `json:"max_devices"`
	MaxIPs                 *int64  `json:"max_ips"`
	MaxConnections         *int64  `json:"max_connections"`
	AutoDeleteInDays       *int64  `json:"auto_delete_in_days"`
	DataLimitResetStrategy string  `json:"data_limit_reset_strategy"`
	OnHoldExpireDuration   *int64  `json:"on_hold_expire_duration"`
	OnHoldExpiresAt        *int64  `json:"on_hold_expires_at"`
	Status                 *string `json:"status"`
	LifetimeUsedBytes      int64   `json:"lifetime_used_bytes"`
	TelegramID             *string `json:"telegram_id"`
	ContactNumber          *string `json:"contact_number"`
	LastOnlineAt           *int64  `json:"last_online_at"`
	IsOnline               bool    `json:"is_online"`
	OwnerAdminID           *int64  `json:"owner_admin_id"`
	PrimaryServiceID       *int64  `json:"primary_service_id"`
	RemainingBytes         *int64  `json:"remaining_bytes"`
	RemainingDays          *int64  `json:"remaining_days"`
	// Enriched fields for professional UI
	ServiceIDs []int64 `json:"service_ids,omitempty"`
	NodeIDs    []int64 `json:"node_ids,omitempty"`
}

func toSubjectDTO(s subjects.Subject) subjectDTO {
	dto := subjectDTO{
		ID: s.ID, Name: s.Name, Enabled: s.Enabled,
		CreatedAt: s.CreatedAt.Unix(), Note: s.Note,
		QuotaBytes: s.QuotaBytes, QuotaUsedBytes: s.QuotaUsedBytes,
		Frozen: s.FrozenAt != nil,
		MaxDevices: s.MaxDevices, MaxIPs: s.MaxIPs, MaxConnections: s.MaxConnections,
		AutoDeleteInDays: s.AutoDeleteInDays, DataLimitResetStrategy: s.DataLimitResetStrategy,
		OnHoldExpireDuration: s.OnHoldExpireDuration, Status: s.Status,
		LifetimeUsedBytes: s.LifetimeUsedBytes, TelegramID: s.TelegramID, ContactNumber: s.ContactNumber,
		IsOnline: s.IsOnline, OwnerAdminID: s.OwnerAdminID, PrimaryServiceID: s.PrimaryServiceID,
	}
	if s.ExpiresAt != nil {
		v := s.ExpiresAt.Unix()
		dto.ExpiresAt = &v
		now := time.Now().Unix()
		rem := (v - now) / 86400
		dto.RemainingDays = &rem
	}
	if s.ExpiredAt != nil {
		v := s.ExpiredAt.Unix()
		dto.ExpiredAt = &v
	}
	if s.OnHoldExpiresAt != nil {
		v := s.OnHoldExpiresAt.Unix()
		dto.OnHoldExpiresAt = &v
	}
	if s.LastOnlineAt != nil {
		v := s.LastOnlineAt.Unix()
		dto.LastOnlineAt = &v
	}
	if s.QuotaBytes != nil {
		rem := *s.QuotaBytes - s.QuotaUsedBytes
		if rem < 0 {
			rem = 0
		}
		dto.RemainingBytes = &rem
	}
	return dto
}

// subjectStore builds the store lazily so a panel with no master key still
// serves every other endpoint. Creating a subject without one fails, which is
// correct: storing credential material unsealed would put it in every backup.
func (d Deps) subjectStore() *subjects.Store {
	return subjects.NewStore(d.Store, d.Box, d.now)
}

// subjectService is the single orchestration path.
//
// Handlers own HTTP concerns -- decoding, status codes, headers -- and nothing
// else. Permission checks, tenant scope, transactions, node republishing and
// audit all live in the service, so the Telegram bot and the CLI get identical
// behaviour by calling the same methods rather than reimplementing them.
//
// Built lazily like subjectStore, so no Deps construction site has to change.
func (d Deps) subjectService() *service.Subjects {
	subj := d.subjectStore()
	return service.NewSubjects(
		d.Store, subj,
		resellers.NewStore(d.Store, subj, d.now),
		d.Hub, d.now, d.snapshotOpts()...,
	)
}

// svcActor bundles identity and request context for the service layer.
func (d Deps) svcActor(r *http.Request, actor *rbac.Actor) service.Actor {
	return service.Actor{
		RBAC:      actor,
		Audit:     d.actorAudit(actor, r),
		RequestID: RequestID(r.Context()),
		Via:       "http",
	}
}

// writeServiceError maps service errors onto status codes in ONE place, so
// every handler reports the same outcome for the same cause.
//
// service.ErrNotFound covers both "no such subject" and "not yours", and both
// become 404: a 403 would confirm the id is real and let one tenant count
// another's customers by walking the id space.
func (d Deps) writeServiceError(
	w http.ResponseWriter, r *http.Request, actor *rbac.Actor, action string, err error,
) {
	switch {
	case errors.Is(err, service.ErrNotFound), errors.Is(err, sql.ErrNoRows):
		WriteError(w, http.StatusNotFound, "not_found", "subject not found")
	case errors.Is(err, subjects.ErrNameTaken):
		WriteError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, rbac.ErrForbidden):
		WriteError(w, http.StatusForbidden, "forbidden", "insufficient permissions")
	default:
		// Denied attempts are audited here, per invariant 9.
		d.rejectSubject(w, r, actor, action, err)
	}
}

// requireSubjectInScope is the second enforcement layer for every handler that
// acts on a subject by id.
//
// authorize() decides whether this actor may perform the operation at all;
// this decides whether the subject exists as far as they are concerned. Both
// must run: PermSubjectWrite says a reseller may edit customers, not that they
// may edit ANY customer.
//
// Denials are 404, never 403. A 403 would confirm the id is real, which lets
// one tenant walk the id space and count a competitor's customers. That is the
// same reason GetSubjectScoped collapses out-of-scope into sql.ErrNoRows.
func (d Deps) requireSubjectInScope(
	w http.ResponseWriter, r *http.Request, actor *rbac.Actor, id int64,
) bool {
	inScope, err := d.Store.SubjectInScope(r.Context(), rbac.ScopeOf(actor), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not check subject")
		return false
	}
	if !inScope {
		WriteError(w, http.StatusNotFound, "not_found", "subject not found")
		return false
	}
	return true
}

// scopeFilterSubjectIDs drops the ids this caller may not act on.
//
// Bulk operations filter rather than reject. Rejecting a batch because one id
// is out of scope would tell the caller that id exists, which is the same
// enumeration oracle the 404-instead-of-403 rule exists to close. A dropped id
// is indistinguishable from one that was never there, and shows up in the
// response counts exactly as a nonexistent id would.
func (d Deps) scopeFilterSubjectIDs(
	r *http.Request, ids []int64,
) ([]int64, error) {
	sc := rbac.ScopeOf(ActorFrom(r.Context()))
	if sc.IsSuper {
		return ids, nil
	}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		ok, err := d.Store.SubjectInScope(r.Context(), sc, id)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, id)
		}
	}
	return out, nil
}

func (d Deps) handleListSubjects(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	list, err := d.subjectService().List(r.Context(), d.svcActor(r, actor))
	if err != nil {
		d.writeServiceError(w, r, actor, "subject.list", err)
		return
	}
	out := make([]subjectDTO, 0, len(list))
	for _, s := range list {
		dto := toSubjectDTO(s)
		// Enrich with service_ids and node_ids for UI
		svcIDs, _ := d.subjectStore().NodeIDsForRead(r.Context(), s.ID)
		_ = svcIDs
		// We need actual service IDs, not node IDs — query separately
		rows, err := d.Store.Read().QueryContext(r.Context(),
			`SELECT service_id FROM subject_services WHERE subject_id = ?`, s.ID)
		if err == nil {
			defer func() {
				// close in loop is tricky; collect first then close
			}()
			var ids []int64
			for rows.Next() {
				var sid int64
				if err := rows.Scan(&sid); err == nil {
					ids = append(ids, sid)
				}
			}
			_ = rows.Close()
			dto.ServiceIDs = ids
		}
		nodeIDs, _ := d.subjectStore().NodeIDsForRead(r.Context(), s.ID)
		dto.NodeIDs = nodeIDs
		out = append(out, dto)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"subjects": out})
}

func (d Deps) handleGetSubject(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "subjectID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid subject id")
		return
	}
	s, err := d.subjectService().Get(r.Context(), d.svcActor(r, actor), id)
	if err != nil {
		d.writeServiceError(w, r, actor, "subject.get", err)
		return
	}
	dto := toSubjectDTO(*s)
	// Enrich
	rows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT service_id FROM subject_services WHERE subject_id = ?`, s.ID)
	if err == nil {
		var ids []int64
		for rows.Next() {
			var sid int64
			if err := rows.Scan(&sid); err == nil {
				ids = append(ids, sid)
			}
		}
		_ = rows.Close()
		dto.ServiceIDs = ids
	}
	nodeIDs, _ := d.subjectStore().NodeIDsForRead(r.Context(), s.ID)
	dto.NodeIDs = nodeIDs
	WriteJSON(w, http.StatusOK, dto)
}

type createSubjectRequest struct {
	Name string `json:"name"`
	Note string `json:"note"`
	// ExpiresAt is a unix timestamp. Null means the subject never expires.
	ExpiresAt *int64 `json:"expires_at"`
	// ExpireDays is a convenience for the UI: N days from now.
	ExpireDays *int `json:"expire_days"`
	// QuotaBytes is nil/0 for unlimited.
	QuotaBytes             *int64  `json:"quota_bytes"`
	MaxDevices             *int64  `json:"max_devices"`
	MaxIPs                 *int64  `json:"max_ips"`
	MaxConnections         *int64  `json:"max_connections"`
	AutoDeleteInDays       *int64  `json:"auto_delete_in_days"`
	DataLimitResetStrategy *string `json:"data_limit_reset_strategy"`
	OnHoldExpireDuration   *int64  `json:"on_hold_expire_duration"`
	TelegramID             *string `json:"telegram_id"`
	ContactNumber          *string `json:"contact_number"`
	OwnerAdminID           *int64  `json:"owner_admin_id"`
	PrimaryServiceID       *int64  `json:"primary_service_id"`
	// ServiceIDs the subject may use. Each one's node has its revision bumped.
	ServiceIDs []int64 `json:"service_ids"`
	// Credentials to import from an existing deployment, keyed by kind.
	// Absent kinds are generated.
	Credentials map[string]string `json:"credentials"`
}

// handleCreateSubject creates the subject and bumps every affected node's
// revision, each through CommitNodeChange.
//
// The subject is written inside the FIRST node's commit rather than in its own
// transaction, so a failure to publish leaves no orphan subject that exists in
// the panel but on no node.
func (d Deps) handleCreateSubject(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}

	var req createSubjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}

	in := subjects.CreateInput{
		Name: req.Name, Note: req.Note,
		ServiceIDs:             req.ServiceIDs,
		Credentials:            map[subjects.CredentialKind]string{},
		QuotaBytes:             nilIfZero(req.QuotaBytes),
		MaxDevices:             nilIfZero(req.MaxDevices),
		MaxIPs:                 nilIfZero(req.MaxIPs),
		MaxConnections:         nilIfZero(req.MaxConnections),
		AutoDeleteInDays:       nilIfZero(req.AutoDeleteInDays),
		DataLimitResetStrategy: req.DataLimitResetStrategy,
		OnHoldExpireDuration:   nilIfZero(req.OnHoldExpireDuration),
		TelegramID:             req.TelegramID,
		ContactNumber:          req.ContactNumber,
		OwnerAdminID:           req.OwnerAdminID,
		PrimaryServiceID:       req.PrimaryServiceID,
	}
	if in.PrimaryServiceID == nil && len(req.ServiceIDs) > 0 {
		v := req.ServiceIDs[0]
		in.PrimaryServiceID = &v
	}
	switch {
	case req.ExpiresAt != nil:
		t := time.Unix(*req.ExpiresAt, 0).UTC()
		in.ExpiresAt = &t
	case req.ExpireDays != nil && *req.ExpireDays > 0:
		t := d.now().Add(time.Duration(*req.ExpireDays) * 24 * time.Hour)
		in.ExpiresAt = &t
	}
	for kind, value := range req.Credentials {
		k := subjects.CredentialKind(kind)
		if err := subjects.ValidateCredential(k, value); err != nil {
			d.rejectSubject(w, r, actor, "subject.create", err)
			return
		}
		in.Credentials[k] = value
	}

	subjectID, err := d.subjectService().Create(r.Context(), d.svcActor(r, actor), in)
	if err != nil {
		d.writeServiceError(w, r, actor, "subject.create", err)
		return
	}
	WriteJSON(w, http.StatusCreated, map[string]any{"id": subjectID})
}

type updateSubjectRequest struct {
	Name                   *string  `json:"name"`
	Note                   *string  `json:"note"`
	Enabled                *bool    `json:"enabled"`
	ExpiresAt              *int64   `json:"expires_at"`
	ClearExpiry            bool     `json:"clear_expiry"`
	ServiceIDs             *[]int64 `json:"service_ids"`
	QuotaBytes             *int64   `json:"quota_bytes"`
	ClearQuota             bool     `json:"clear_quota"`
	MaxIPs                 *int64   `json:"max_ips"`
	MaxDevices             *int64   `json:"max_devices"`
	MaxConnections         *int64   `json:"max_connections"`
	AutoDeleteInDays       *int64   `json:"auto_delete_in_days"`
	ClearAutoDelete        bool     `json:"clear_auto_delete"`
	DataLimitResetStrategy *string  `json:"data_limit_reset_strategy"`
	OnHoldExpireDuration   *int64   `json:"on_hold_expire_duration"`
	TelegramID             *string  `json:"telegram_id"`
	ContactNumber          *string  `json:"contact_number"`
	OwnerAdminID           *int64   `json:"owner_admin_id"`
	PrimaryServiceID       *int64   `json:"primary_service_id"`
	Status                 *string  `json:"status"`
	ClearStatus            bool     `json:"clear_status"`
}

// handleUpdateSubject changes a subject and republishes it.
//
// Republishing happens for the union of the node sets before AND after the
// change: revoking a service must bump the node that is losing the subject,
// or that node would keep serving them until something else happened to it.
func (d Deps) handleUpdateSubject(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "subjectID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid subject id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	if !d.requireSubjectInScope(w, r, actor, id) {
		return
	}

	var req updateSubjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}

	up := subjects.UpdateInput{
		Name: req.Name, Note: req.Note, Enabled: req.Enabled,
		ClearExpiry: req.ClearExpiry, ServiceIDs: req.ServiceIDs,
		QuotaBytes: req.QuotaBytes, ClearQuota: req.ClearQuota,
		MaxIPs: req.MaxIPs, MaxDevices: req.MaxDevices, MaxConnections: req.MaxConnections,
		AutoDeleteInDays: req.AutoDeleteInDays, ClearAutoDelete: req.ClearAutoDelete,
		DataLimitResetStrategy: req.DataLimitResetStrategy, OnHoldExpireDuration: req.OnHoldExpireDuration,
		TelegramID: req.TelegramID, ContactNumber: req.ContactNumber,
		OwnerAdminID: req.OwnerAdminID, PrimaryServiceID: req.PrimaryServiceID,
		Status: req.Status, ClearStatus: req.ClearStatus,
	}
	if req.ExpiresAt != nil {
		t := time.Unix(*req.ExpiresAt, 0).UTC()
		up.ExpiresAt = &t
	}
	if err := d.subjectService().Update(r.Context(), d.svcActor(r, actor), id, up); err != nil {
		d.writeServiceError(w, r, actor, "subject.update", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteSubject removes a subject and republishes every node that served
// them, so the credential stops working rather than merely disappearing from
// the panel.
func (d Deps) handleDeleteSubject(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "subjectID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid subject id")
		return
	}
	if err := d.subjectService().Delete(r.Context(), d.svcActor(r, actor), id); err != nil {
		d.writeServiceError(w, r, actor, "subject.delete", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRevealCredential returns one credential in plaintext.
//
// This is the only endpoint that ever does. It requires its own permission,
// separate from subject:read, and is audited with the KIND revealed but never
// the value.
func (d Deps) handleRevealCredential(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "subjectID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid subject id")
		return
	}
	kind := subjects.CredentialKind(chi.URLParam(r, "kind"))

	value, err := d.subjectService().Credential(r.Context(), d.svcActor(r, actor), id, kind)
	if err != nil {
		d.writeServiceError(w, r, actor, "credential.reveal", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, http.StatusOK, map[string]any{"kind": string(kind), "value": value})
}

// handleRotateCredential replaces one credential and republishes.
func (d Deps) handleRotateCredential(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "subjectID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid subject id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	if !d.requireSubjectInScope(w, r, actor, id) {
		return
	}
	kind := subjects.CredentialKind(chi.URLParam(r, "kind"))

	fresh, err := d.subjectService().RotateCredential(
		r.Context(), d.svcActor(r, actor), id, kind)
	if err != nil {
		d.writeServiceError(w, r, actor, "credential.rotate", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, http.StatusOK, map[string]any{"kind": string(kind), "value": fresh})
}

func (d Deps) rejectSubject(
	w http.ResponseWriter, r *http.Request, actor *rbac.Actor, action string, cause error,
) {
	ctx := r.Context()
	audit.BestEffort(ctx, d.Store, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
		Action: action, TargetType: "subject", Result: "denied",
		After: map[string]any{"reason": cause.Error()},
	})
	WriteError(w, http.StatusUnprocessableEntity, "validation", cause.Error())
}

func nilIfZero(v *int64) *int64 {
	if v == nil || *v == 0 {
		return nil
	}
	return v
}

func (d Deps) handleSubjectSubscription(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "subjectID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid subject id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	if !d.requireSubjectInScope(w, r, actor, id) {
		return
	}
	token, err := subjects.EnsureToken(r.Context(), d.Store, id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not issue subscription")
		return
	}
	base := d.publicBaseURL(r)
	url := base + "/api/v1/subscribe/" + token
	audit.BestEffort(r.Context(), d.Store, RequestID(r.Context()), d.actorAudit(actor, r), audit.Record{
		Action: "subject.subscription", TargetType: "subject",
		TargetID: sql.NullInt64{Int64: id, Valid: true}, Result: "ok",
	})
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, http.StatusOK, map[string]any{
		"url":         url,
		"clash_url":   url + "?format=clash",
		"singbox_url": url + "?format=singbox",
		"v2ray_url":   url + "?format=v2ray",
		"qr_url":      "/api/v1/subscribe/" + token + "/qr",
	})
}

func (d Deps) handleRevokeSubjectSubscription(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "subjectID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid subject id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	if !d.requireSubjectInScope(w, r, actor, id) {
		return
	}
	if _, err := subjects.RevokeToken(r.Context(), d.Store, id); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not rotate subscription")
		return
	}
	audit.BestEffort(r.Context(), d.Store, RequestID(r.Context()), d.actorAudit(actor, r), audit.Record{
		Action: "subject.subscription_revoke", TargetType: "subject",
		TargetID: sql.NullInt64{Int64: id, Valid: true}, Result: "ok",
	})
	w.WriteHeader(http.StatusNoContent)
}

// Additional quick-action handlers

func (d Deps) handleAddTraffic(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "subjectID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid subject id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	if !d.requireSubjectInScope(w, r, actor, id) {
		return
	}
	var req struct {
		Bytes int64 `json:"bytes"`
		GB    *int64 `json:"gb"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed body")
		return
	}
	delta := req.Bytes
	if req.GB != nil {
		delta = *req.GB * 1024 * 1024 * 1024
	}
	if delta == 0 {
		WriteError(w, http.StatusBadRequest, "bad_request", "bytes or gb required")
		return
	}
	if err := d.subjectService().AddTraffic(r.Context(), d.svcActor(r, actor), id, delta); err != nil {
		d.writeServiceError(w, r, actor, "subject.add_traffic", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d Deps) handleAddDays(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "subjectID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid subject id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	if !d.requireSubjectInScope(w, r, actor, id) {
		return
	}
	var req struct {
		Days int `json:"days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed body")
		return
	}
	if req.Days == 0 {
		WriteError(w, http.StatusBadRequest, "bad_request", "days required")
		return
	}
	if err := d.subjectService().AddDays(r.Context(), d.svcActor(r, actor), id, req.Days); err != nil {
		d.writeServiceError(w, r, actor, "subject.add_days", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d Deps) handleResetTraffic(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "subjectID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid subject id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	if !d.requireSubjectInScope(w, r, actor, id) {
		return
	}
	if err := d.subjectService().ResetTraffic(r.Context(), d.svcActor(r, actor), id); err != nil {
		d.writeServiceError(w, r, actor, "subject.reset_traffic", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

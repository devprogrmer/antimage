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
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Enabled        bool   `json:"enabled"`
	ExpiresAt      *int64 `json:"expires_at"`
	ExpiredAt      *int64 `json:"expired_at"`
	CreatedAt      int64  `json:"created_at"`
	Note           string `json:"note"`
	QuotaBytes     *int64 `json:"quota_bytes"`
	QuotaUsedBytes int64  `json:"quota_used_bytes"`
	Frozen         bool   `json:"frozen"`
	MaxDevices     *int64 `json:"max_devices"`
	MaxIPs         *int64 `json:"max_ips"`
	MaxConnections *int64 `json:"max_connections"`
}

func toSubjectDTO(s subjects.Subject) subjectDTO {
	dto := subjectDTO{
		ID: s.ID, Name: s.Name, Enabled: s.Enabled,
		CreatedAt: s.CreatedAt.Unix(), Note: s.Note,
		QuotaBytes: s.QuotaBytes, QuotaUsedBytes: s.QuotaUsedBytes,
		Frozen:     s.FrozenAt != nil,
		MaxDevices: s.MaxDevices, MaxIPs: s.MaxIPs, MaxConnections: s.MaxConnections,
	}
	if s.ExpiresAt != nil {
		v := s.ExpiresAt.Unix()
		dto.ExpiresAt = &v
	}
	if s.ExpiredAt != nil {
		v := s.ExpiredAt.Unix()
		dto.ExpiredAt = &v
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
	// Permission and tenant scope are both enforced inside the service, so this
	// handler cannot forget either one.
	list, err := d.subjectService().List(r.Context(), d.svcActor(r, actor))
	if err != nil {
		d.writeServiceError(w, r, actor, "subject.list", err)
		return
	}
	out := make([]subjectDTO, 0, len(list))
	for _, s := range list {
		out = append(out, toSubjectDTO(s))
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
	WriteJSON(w, http.StatusOK, toSubjectDTO(*s))
}

type createSubjectRequest struct {
	Name string `json:"name"`
	Note string `json:"note"`
	// ExpiresAt is a unix timestamp. Null means the subject never expires.
	ExpiresAt *int64 `json:"expires_at"`
	// ExpireDays is a convenience for the UI: N days from now.
	ExpireDays *int `json:"expire_days"`
	// QuotaBytes is nil/0 for unlimited.
	QuotaBytes     *int64 `json:"quota_bytes"`
	MaxDevices     *int64 `json:"max_devices"`
	MaxIPs         *int64 `json:"max_ips"`
	MaxConnections *int64 `json:"max_connections"`
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
		ServiceIDs:     req.ServiceIDs,
		Credentials:    map[subjects.CredentialKind]string{},
		QuotaBytes:     nilIfZero(req.QuotaBytes),
		MaxDevices:     nilIfZero(req.MaxDevices),
		MaxIPs:         nilIfZero(req.MaxIPs),
		MaxConnections: nilIfZero(req.MaxConnections),
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
	Name        *string  `json:"name"`
	Note        *string  `json:"note"`
	Enabled     *bool    `json:"enabled"`
	ExpiresAt   *int64   `json:"expires_at"`
	ClearExpiry bool     `json:"clear_expiry"`
	ServiceIDs  *[]int64 `json:"service_ids"`
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
	}
	if req.ExpiresAt != nil {
		t := time.Unix(*req.ExpiresAt, 0).UTC()
		up.ExpiresAt = &t
	}
	// The service republishes the union of the node sets before and after, so
	// moving a subject between services stops the losing node serving them.
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
	// The service captures the affected nodes BEFORE deleting, since the
	// cascade removes the grants that name them.
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

	// The highest-value read in the panel. Permission, tenant scope and the
	// by-kind audit record all live in the service, so the bot gets the same
	// three guarantees without repeating them.
	value, err := d.subjectService().Credential(r.Context(), d.svcActor(r, actor), id, kind)
	if err != nil {
		d.writeServiceError(w, r, actor, "credential.reveal", err)
		return
	}
	// no-store: a credential must not sit in a browser or proxy cache.
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
	// Rotation is as sensitive as reveal in the other direction: rotating a
	// foreign customer's credential would cut off a competitor's user.
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

// republishNodes commits an empty change to each node, which rebuilds its
// desired document and bumps the revision only if the document actually
// changed. CommitNodeChange remains the single path that may do so.
// rejectSubject records a refused write, per spec invariant 9.
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

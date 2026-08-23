package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/subjects"
)

// subjectDTO is the wire shape of a subject.
//
// Credentials are deliberately absent. A list endpoint that returned them
// would put every user's credential in one response, in every log that
// captures response bodies, and in every browser cache. They are fetched one
// at a time through an explicitly authorized, audited reveal.
type subjectDTO struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	ExpiresAt *int64 `json:"expires_at"`
	ExpiredAt *int64 `json:"expired_at"`
	CreatedAt int64  `json:"created_at"`
	Note      string `json:"note"`
}

func toSubjectDTO(s subjects.Subject) subjectDTO {
	dto := subjectDTO{
		ID: s.ID, Name: s.Name, Enabled: s.Enabled,
		CreatedAt: s.CreatedAt.Unix(), Note: s.Note,
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
	if !d.authorize(w, r, actor, rbac.PermSubjectRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	// Scope is the SECOND enforcement layer. The authorize() call above decided
	// that this actor may read subjects at all; this decides which subjects
	// exist as far as they are concerned. A reseller passes their own scope and
	// sees only their own customers.
	list, err := d.subjectStore().List(r.Context(), rbac.ScopeOf(actor))
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list subjects")
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
	if !d.authorize(w, r, actor, rbac.PermSubjectRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	// Out-of-scope and missing both arrive here as sql.ErrNoRows and both
	// become 404. Returning 403 for the former would let one reseller probe
	// the id space and count a competitor's customers.
	s, err := d.subjectStore().Get(r.Context(), rbac.ScopeOf(actor), id)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "not_found", "subject not found")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not load subject")
		return
	}
	WriteJSON(w, http.StatusOK, toSubjectDTO(*s))
}

type createSubjectRequest struct {
	Name string `json:"name"`
	Note string `json:"note"`
	// ExpiresAt is a unix timestamp. Null means the subject never expires.
	ExpiresAt *int64 `json:"expires_at"`
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
		ServiceIDs:  req.ServiceIDs,
		Credentials: map[subjects.CredentialKind]string{},
	}
	if req.ExpiresAt != nil {
		t := time.Unix(*req.ExpiresAt, 0).UTC()
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

	ctx := r.Context()
	store := d.subjectStore()

	var subjectID int64
	err := d.Store.Write(ctx, func(tx *sql.Tx) error {
		id, err := store.Create(ctx, tx, in)
		if err != nil {
			return err
		}
		subjectID = id
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action: "subject.create", TargetType: "subject",
			TargetID: sql.NullInt64{Int64: id, Valid: true},
			// The name and expiry are recorded; the credentials never are.
			After:  map[string]any{"name": in.Name, "services": len(in.ServiceIDs)},
			Result: "ok",
		})
	})
	if err != nil {
		if errors.Is(err, subjects.ErrNameTaken) {
			WriteError(w, http.StatusConflict, "conflict", err.Error())
			return
		}
		d.rejectSubject(w, r, actor, "subject.create", err)
		return
	}

	if err := d.republishSubject(ctx, r, actor, subjectID, "subject created"); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal",
			"the subject was created but could not be published to every node")
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

	ctx := r.Context()
	store := d.subjectStore()

	before, err := store.NodeIDsForRead(ctx, id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read subject")
		return
	}

	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		up := subjects.UpdateInput{
			Name: req.Name, Note: req.Note, Enabled: req.Enabled,
			ClearExpiry: req.ClearExpiry, ServiceIDs: req.ServiceIDs,
		}
		if req.ExpiresAt != nil {
			t := time.Unix(*req.ExpiresAt, 0).UTC()
			up.ExpiresAt = &t
		}
		if err := store.Update(ctx, tx, id, up); err != nil {
			return err
		}
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action: "subject.update", TargetType: "subject",
			TargetID: sql.NullInt64{Int64: id, Valid: true},
			After:    map[string]any{"enabled": req.Enabled, "clear_expiry": req.ClearExpiry},
			Result:   "ok",
		})
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			WriteError(w, http.StatusNotFound, "not_found", "subject not found")
			return
		}
		if errors.Is(err, subjects.ErrNameTaken) {
			WriteError(w, http.StatusConflict, "conflict", err.Error())
			return
		}
		d.rejectSubject(w, r, actor, "subject.update", err)
		return
	}

	after, _ := store.NodeIDsForRead(ctx, id)
	if err := d.republishNodes(ctx, r, actor, union(before, after), "subject updated"); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal",
			"the subject was updated but could not be published to every node")
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
	if !d.authorize(w, r, actor, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	if !d.requireSubjectInScope(w, r, actor, id) {
		return
	}

	ctx := r.Context()
	store := d.subjectStore()

	// Captured BEFORE the delete: the cascade removes the grants that name
	// these nodes, so afterwards there is nothing left to ask.
	affected, err := store.NodeIDsForRead(ctx, id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read subject")
		return
	}

	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		if err := store.Delete(ctx, tx, id); err != nil {
			return err
		}
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action: "subject.delete", TargetType: "subject",
			TargetID: sql.NullInt64{Int64: id, Valid: true}, Result: "ok",
		})
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			WriteError(w, http.StatusNotFound, "not_found", "subject not found")
			return
		}
		WriteError(w, http.StatusInternalServerError, "internal", "could not delete subject")
		return
	}

	if err := d.republishNodes(ctx, r, actor, affected, "subject deleted"); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal",
			"the subject was deleted but some nodes still serve them")
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
	if !d.authorize(w, r, actor, rbac.PermCredReveal, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	// The highest-value endpoint in the panel: it returns the secret a user
	// connects with. Without this gate, credential:reveal on any subject id
	// disclosed any tenant's customer credential to any other tenant.
	if !d.requireSubjectInScope(w, r, actor, id) {
		return
	}
	kind := subjects.CredentialKind(chi.URLParam(r, "kind"))

	value, err := d.subjectStore().Credential(r.Context(), id, kind)
	if err != nil {
		WriteError(w, http.StatusNotFound, "not_found", "no such credential")
		return
	}

	ctx := r.Context()
	audit.BestEffort(ctx, d.Store, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
		Action: "credential.reveal", TargetType: "subject",
		TargetID: sql.NullInt64{Int64: id, Valid: true},
		// The KIND is recorded so an operator can see what was exposed. The
		// VALUE never is: the audit log is readable by every audit:read holder.
		After:  map[string]any{"kind": string(kind)},
		Result: "ok",
	})
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

	ctx := r.Context()
	store := d.subjectStore()

	var fresh string
	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		v, err := store.Rotate(ctx, tx, id, kind)
		if err != nil {
			return err
		}
		fresh = v
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action: "credential.rotate", TargetType: "subject",
			TargetID: sql.NullInt64{Int64: id, Valid: true},
			After:    map[string]any{"kind": string(kind)},
			Result:   "ok",
		})
	})
	if err != nil {
		d.rejectSubject(w, r, actor, "credential.rotate", err)
		return
	}

	if err := d.republishSubject(ctx, r, actor, id, "credential rotated"); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal",
			"the credential was rotated but could not be published to every node")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, http.StatusOK, map[string]any{"kind": string(kind), "value": fresh})
}

// republishSubject bumps the revision of every node this subject reaches.
func (d Deps) republishSubject(
	ctx context.Context, r *http.Request, actor *rbac.Actor, subjectID int64, reason string,
) error {
	ids, err := d.subjectStore().NodeIDsForRead(ctx, subjectID)
	if err != nil {
		return err
	}
	return d.republishNodes(ctx, r, actor, ids, reason)
}

// republishNodes commits an empty change to each node, which rebuilds its
// desired document and bumps the revision only if the document actually
// changed. CommitNodeChange remains the single path that may do so.
func (d Deps) republishNodes(
	ctx context.Context, r *http.Request, actor *rbac.Actor, nodeIDs []int64, reason string,
) error {
	for _, nodeID := range nodeIDs {
		result, err := nodes.CommitNodeChange(ctx, d.Store, nodeID,
			d.actorAudit(actor, r), RequestID(ctx), reason,
			func(*sql.Tx) error { return nil }, d.snapshotOpts()...)
		if err != nil {
			return err
		}
		// Only signal when something actually moved, so a no-op edit does not
		// wake every agent in the fleet.
		if result.Changed {
			d.Hub.Notify(nodeID, result.Revision)
		}
	}
	return nil
}

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

func union(a, b []int64) []int64 {
	seen := make(map[int64]struct{}, len(a)+len(b))
	var out []int64
	for _, list := range [][]int64{a, b} {
		for _, id := range list {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

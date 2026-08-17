package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

type serviceRequest struct {
	AdapterKind string          `json:"adapter_kind"`
	Params      json.RawMessage `json:"params"`
	Enabled     *bool           `json:"enabled"`
}

// validateService checks the adapter exists and its params satisfy the schema
// that adapter publishes. The panel holds no protocol knowledge of its own:
// adding a protocol means registering a descriptor, not editing this file.
func validateService(req serviceRequest) error {
	desc, ok := nodes.KnownAdapters()[req.AdapterKind]
	if !ok {
		return errors.New("unknown adapter kind")
	}
	if len(req.Params) == 0 {
		req.Params = json.RawMessage(`{}`)
	}
	return nodes.ValidateServiceParams(desc.Caps.ServiceSchema, req.Params)
}

// paramsOf returns the params to store, normalising an omitted body field to
// an empty object so the column never holds the empty string. It is only
// called after validateService has accepted the request.
func paramsOf(req serviceRequest) string {
	if len(req.Params) == 0 {
		return "{}"
	}
	return string(req.Params)
}

func enabledOf(req serviceRequest) int {
	if req.Enabled != nil && !*req.Enabled {
		return 0
	}
	return 1
}

func (d Deps) handleCreateService(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	nodeID, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	if !authorize(w, actor, rbac.PermServiceWrite, rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}
	var req serviceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if err := validateService(req); err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "validation", err.Error())
		return
	}

	ctx := r.Context()
	var serviceID int64
	// CommitNodeChange owns the revision bump: it is the only path allowed to
	// move desired_revision, and it never touches applied_revision, so the
	// node stays visibly unconverged until the agent reports back.
	result, err := nodes.CommitNodeChange(ctx, d.Store, nodeID,
		d.actorAudit(actor, r), RequestID(ctx), "create service",
		func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx,
				`INSERT INTO services (node_id, adapter_kind, params, enabled, created_at)
				 VALUES (?,?,?,?,?)`,
				nodeID, req.AdapterKind, paramsOf(req), enabledOf(req), d.now().Unix())
			if err != nil {
				return err
			}
			serviceID, err = res.LastInsertId()
			return err
		})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not create service")
		return
	}

	// control owns the stream; the handler only signals that state moved.
	if result.Changed {
		d.Hub.Notify(nodeID, result.Revision)
	}
	WriteJSON(w, http.StatusCreated, map[string]any{
		"id": serviceID, "revision": result.Revision, "changed": result.Changed,
	})
}

func (d Deps) handleUpdateService(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	serviceID, err := pathInt64(r, "serviceID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid service id")
		return
	}
	ctx := r.Context()

	nodeID, ok := d.serviceNode(w, r, serviceID)
	if !ok {
		return
	}
	if !authorize(w, actor, rbac.PermServiceWrite, rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	var req serviceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if err := validateService(req); err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "validation", err.Error())
		return
	}

	result, err := nodes.CommitNodeChange(ctx, d.Store, nodeID,
		d.actorAudit(actor, r), RequestID(ctx), "update service",
		func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx,
				`UPDATE services SET adapter_kind = ?, params = ?, enabled = ? WHERE id = ?`,
				req.AdapterKind, paramsOf(req), enabledOf(req), serviceID)
			return err
		})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not update service")
		return
	}
	if result.Changed {
		d.Hub.Notify(nodeID, result.Revision)
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"revision": result.Revision, "changed": result.Changed,
	})
}

func (d Deps) handleDeleteService(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	serviceID, err := pathInt64(r, "serviceID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid service id")
		return
	}
	ctx := r.Context()

	nodeID, ok := d.serviceNode(w, r, serviceID)
	if !ok {
		return
	}
	if !authorize(w, actor, rbac.PermServiceWrite, rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	result, err := nodes.CommitNodeChange(ctx, d.Store, nodeID,
		d.actorAudit(actor, r), RequestID(ctx), "delete service",
		func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `DELETE FROM services WHERE id = ?`, serviceID)
			return err
		})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not delete service")
		return
	}
	if result.Changed {
		d.Hub.Notify(nodeID, result.Revision)
	}
	w.WriteHeader(http.StatusNoContent)
}

// serviceNode resolves the node a service belongs to, which is the target the
// permission check applies to. A service is only ever reachable through its
// node, so there is no separate service-level scope to consult here.
func (d Deps) serviceNode(w http.ResponseWriter, r *http.Request, serviceID int64) (int64, bool) {
	var nodeID int64
	err := d.Store.Read().QueryRowContext(r.Context(),
		`SELECT node_id FROM services WHERE id = ?`, serviceID).Scan(&nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "not_found", "service not found")
		return 0, false
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not load service")
		return 0, false
	}
	return nodeID, true
}

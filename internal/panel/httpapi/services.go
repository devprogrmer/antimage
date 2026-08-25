package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

type serviceRequest struct {
	AdapterKind string          `json:"adapter_kind"`
	Params      json.RawMessage `json:"params"`
	Enabled     *bool           `json:"enabled"`
}

// validateService checks the params against the schema for this adapter ON
// THIS NODE. The panel holds no protocol knowledge of its own.
//
// The schema comes from the node when the node has told us one, and from the
// panel's compiled-in descriptor otherwise. That order matters: a node reports
// the schema its installed adapter actually publishes, while KnownAdapters
// reports what this build of the panel was compiled with. When they differ the
// node is right, because the node is the thing that has to apply the result --
// and an editor built from the node's schema would otherwise offer fields the
// panel then rejects.
//
// A kind the node does not run is refused outright, once the node has reported
// anything at all. Creating a service no adapter on that host can apply is the
// fake feature layer AD-3 names, and it fails at reconcile time where it is
// hardest to diagnose rather than here where the reason is obvious.
//
// A node that has never connected has reported nothing, and is not held to that
// rule: preparing a node's services before its agent first calls home is a real
// workflow, and there is nothing yet to check against. "We do not know" is not
// "it cannot".
func (d Deps) validateService(ctx context.Context, nodeID int64, req serviceRequest) error {
	schema, err := d.resolveServiceSchema(ctx, nodeID, req.AdapterKind)
	if err != nil {
		return err
	}
	if len(req.Params) == 0 {
		req.Params = json.RawMessage(`{}`)
	}
	return nodes.ValidateServiceParams(schema, req.Params)
}

// resolveServiceSchema picks the schema this node's adapter publishes.
func (d Deps) resolveServiceSchema(
	ctx context.Context, nodeID int64, kind string,
) (json.RawMessage, error) {
	reported, err := nodes.ListAdapters(ctx, d.Store, nodeID)
	if err != nil {
		return nil, fmt.Errorf("read node adapters: %w", err)
	}

	if len(reported) > 0 {
		for _, entry := range reported {
			if entry.Kind != kind {
				continue
			}
			if len(entry.ServiceSchema) > 0 {
				return entry.ServiceSchema, nil
			}
			// The node runs this adapter but is too old to publish its schema.
			// Fall back rather than refuse: the protocol demonstrably works on
			// that host, and refusing would break it on an agent upgrade path.
			break
		}
		if !runsKind(reported, kind) {
			return nil, fmt.Errorf(
				"this node does not run a %q adapter; it reports %s",
				kind, strings.Join(kindsOf(reported), ", "))
		}
	}

	desc, ok := nodes.KnownAdapters()[kind]
	if !ok {
		return nil, errors.New("unknown adapter kind")
	}
	return desc.Caps.ServiceSchema, nil
}

func runsKind(entries []nodes.AdapterRegistryEntry, kind string) bool {
	for _, e := range entries {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

func kindsOf(entries []nodes.AdapterRegistryEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Kind)
	}
	return out
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

// rejectInvalidService answers a schema violation with 422 and records it,
// per spec invariant 9: a validation rejection deliberately never commits, so
// there is no transaction for an audit row to ride along in, and it is one of
// the three cases audit.BestEffort exists for.
//
// Only the adapter kind and the validator's own message are recorded. The
// submitted params are not: an adapter is free to publish a schema with a
// credential field, and an audit row is the wrong place to preserve one.
//
// The call is safe because both callers reject before opening a transaction —
// BestEffort needs the store's single write connection, which a caller inside
// store.Write would already be holding.
func (d Deps) rejectInvalidService(w http.ResponseWriter, r *http.Request,
	actor *rbac.Actor, action string, target rbac.Target, req serviceRequest, cause error) {
	ctx := r.Context()
	audit.BestEffort(ctx, d.Store, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
		Action:     action,
		TargetType: targetTypeName(target.Kind),
		TargetID:   sql.NullInt64{Int64: target.ID, Valid: true},
		Result:     "denied",
		After: map[string]any{
			"adapter_kind": req.AdapterKind,
			"reason":       cause.Error(),
		},
	})
	WriteError(w, http.StatusUnprocessableEntity, "validation", cause.Error())
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
	if !d.authorize(w, r, actor, rbac.PermServiceWrite, rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}
	var req serviceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if err := d.validateService(r.Context(), nodeID, req); err != nil {
		d.rejectInvalidService(w, r, actor, "service.create",
			rbac.Target{Kind: rbac.TargetNode, ID: nodeID}, req, err)
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
		}, d.snapshotOpts()...)
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
	if !d.authorize(w, r, actor, rbac.PermServiceWrite, rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	var req serviceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if err := d.validateService(r.Context(), nodeID, req); err != nil {
		d.rejectInvalidService(w, r, actor, "service.update",
			rbac.Target{Kind: rbac.TargetService, ID: serviceID}, req, err)
		return
	}

	result, err := nodes.CommitNodeChange(ctx, d.Store, nodeID,
		d.actorAudit(actor, r), RequestID(ctx), "update service",
		func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx,
				`UPDATE services SET adapter_kind = ?, params = ?, enabled = ? WHERE id = ?`,
				req.AdapterKind, paramsOf(req), enabledOf(req), serviceID)
			return err
		}, d.snapshotOpts()...)
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
	if !d.authorize(w, r, actor, rbac.PermServiceWrite, rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	result, err := nodes.CommitNodeChange(ctx, d.Store, nodeID,
		d.actorAudit(actor, r), RequestID(ctx), "delete service",
		func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `DELETE FROM services WHERE id = ?`, serviceID)
			return err
		}, d.snapshotOpts()...)
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

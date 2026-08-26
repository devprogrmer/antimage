package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

// Egress is node-scoped, so every route here hangs off a node id and every
// mutation goes through CommitNodeChange -- the only path allowed to move
// desired_revision. Writing an outbound row without bumping the revision would
// leave the panel holding egress configuration no node had been told about.

type outboundRequest struct {
	Tag     string          `json:"tag"`
	Kind    string          `json:"kind"`
	Params  json.RawMessage `json:"params"`
	Enabled *bool           `json:"enabled"`
}

type outboundDTO struct {
	ID      int64           `json:"id"`
	NodeID  int64           `json:"node_id"`
	Tag     string          `json:"tag"`
	Kind    string          `json:"kind"`
	Params  json.RawMessage `json:"params"`
	Enabled bool            `json:"enabled"`
}

// egressCapableAdapter returns an adapter on this node that can apply
// outbounds, or an error naming why none can.
//
// Fail closed, per the schema v3 decision. An outbound stored against a node
// whose adapters have no routing engine is not a harmless no-op: the panel
// would show an egress policy, report the node converged, and the node would
// send traffic wherever it did before. WireGuard, Hysteria2 and L2TP have no
// routing engine at all -- only the multiplexing proxies do.
func (d Deps) egressCapableAdapter(ctx context.Context, nodeID int64) (string, error) {
	var kindsJSON string
	err := d.Store.Read().QueryRowContext(ctx,
		`SELECT adapter_kinds FROM nodes WHERE id = ?`, nodeID).Scan(&kindsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errNodeNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read node adapters: %w", err)
	}

	var kinds []string
	if err := json.Unmarshal([]byte(kindsJSON), &kinds); err != nil {
		return "", fmt.Errorf("decode node adapters: %w", err)
	}

	known := nodes.KnownAdapters()
	for _, k := range kinds {
		if desc, ok := known[k]; ok && desc.Caps.SupportsOutbounds {
			return k, nil
		}
	}
	return "", fmt.Errorf(
		"no adapter on this node can apply outbounds (node runs %s); "+
			"only the multiplexing proxies have a routing engine",
		strings.Join(kinds, ", "))
}

// validateOutbound checks the kind is one the node's adapter renders and the
// params satisfy the schema that adapter publishes.
//
// The panel holds no protocol knowledge of its own here, exactly as it holds
// none for services: adding an outbound kind is an adapter change.
func validateOutbound(adapterKind string, req outboundRequest) error {
	if strings.TrimSpace(req.Tag) == "" {
		return errors.New("tag is required; routing rules select outbounds by tag")
	}
	if strings.TrimSpace(req.Kind) == "" {
		return errors.New("kind is required")
	}
	desc, ok := nodes.KnownAdapters()[adapterKind]
	if !ok {
		return errors.New("unknown adapter kind")
	}
	params := req.Params
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	return nodes.ValidateServiceParams(desc.Caps.OutboundSchema, params)
}

// outboundParamsBytes is outboundParamsOf as bytes, for the redaction merge.
func outboundParamsBytes(req outboundRequest) []byte {
	return []byte(outboundParamsOf(req))
}

func outboundParamsOf(req outboundRequest) string {
	if len(req.Params) == 0 {
		return "{}"
	}
	return string(req.Params)
}

func outboundEnabledOf(req outboundRequest) int {
	if req.Enabled != nil && !*req.Enabled {
		return 0
	}
	return 1
}

var errNodeNotFound = errors.New("node not found")

func (d Deps) handleListOutbounds(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	nodeID, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermOutboundRead,
		rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	// Which fields are credentials comes from the adapter's schema. The error
	// is deliberately ignored: a node with no egress-capable adapter has no
	// outbounds to list, and if the kind cannot be resolved the empty schema
	// makes redactParams fail CLOSED rather than disclose.
	adapterKind, _ := d.egressCapableAdapter(r.Context(), nodeID)

	rows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT id, node_id, tag, kind, params, enabled
		   FROM outbounds WHERE node_id = ? ORDER BY id`, nodeID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list outbounds")
		return
	}
	defer func() { _ = rows.Close() }()

	out := []outboundDTO{}
	for rows.Next() {
		var o outboundDTO
		var params string
		var enabled int
		if err := rows.Scan(&o.ID, &o.NodeID, &o.Tag, &o.Kind, &params, &enabled); err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not read outbounds")
			return
		}
		// Credential fields never leave the panel. Which ones they are comes
		// from the adapter's own schema; see secret_params.go.
		o.Params = redactParams(outboundSchemaFor(adapterKind), []byte(params))
		o.Enabled = enabled == 1
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		// A truncated list served as complete is the failure mode worth
		// guarding: nobody checks a row count against a number they do not have.
		WriteError(w, http.StatusInternalServerError, "internal", "could not read outbounds")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"outbounds": out})
}

func (d Deps) handleCreateOutbound(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	nodeID, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermOutboundWrite,
		rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	var req outboundRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}

	adapterKind, err := d.egressCapableAdapter(r.Context(), nodeID)
	if errors.Is(err, errNodeNotFound) {
		WriteError(w, http.StatusNotFound, "not_found", "node not found")
		return
	}
	if err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "unsupported", err.Error())
		return
	}
	if err := validateOutbound(adapterKind, req); err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}

	ctx := r.Context()
	var id int64

	// Seal params before storage
	sealedParams, err := nodes.SealOutboundParams(d.Box, json.RawMessage(outboundParamsOf(req)))
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not seal outbound params")
		return
	}

	_, err = nodes.CommitNodeChange(ctx, d.Store, nodeID,
		d.actorAudit(actor, r), RequestID(ctx), "create outbound",
		func(tx *sql.Tx) error {
			res, execErr := tx.ExecContext(ctx,
				`INSERT INTO outbounds (node_id, tag, kind, params, enabled, created_at, updated_at)
				 VALUES (?,?,?,?,?,?,?)`,
				nodeID, req.Tag, req.Kind, sealedParams, outboundEnabledOf(req),
				d.now().Unix(), d.now().Unix())
			if execErr != nil {
				return execErr
			}
			id, execErr = res.LastInsertId()
			return execErr
		}, d.snapshotOpts()...)
	if err != nil {
		// The unique index on (node_id, tag) is the panel's half of the rule
		// the adapters enforce at render time. Reporting it as a conflict tells
		// the operator now, rather than letting them find out when a routing
		// rule silently selects the wrong one.
		if isUniqueViolation(err) {
			WriteError(w, http.StatusConflict, "conflict",
				"an outbound with that tag already exists on this node")
			return
		}
		WriteError(w, http.StatusInternalServerError, "internal", "could not create outbound")
		return
	}

	WriteJSON(w, http.StatusCreated, outboundDTO{
		ID: id, NodeID: nodeID, Tag: req.Tag, Kind: req.Kind,
		Params: json.RawMessage(outboundParamsOf(req)), Enabled: outboundEnabledOf(req) == 1,
	})
}

func (d Deps) handleUpdateOutbound(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	nodeID, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	outboundID, err := pathInt64(r, "outboundID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid outbound id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermOutboundWrite,
		rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	var req outboundRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}

	adapterKind, err := d.egressCapableAdapter(r.Context(), nodeID)
	if errors.Is(err, errNodeNotFound) {
		WriteError(w, http.StatusNotFound, "not_found", "node not found")
		return
	}
	if err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "unsupported", err.Error())
		return
	}

	// A client that read this outbound saw its credentials as the sentinel, so
	// an unmodified round trip sends the sentinel back. Restore the stored
	// value BEFORE validating: without this an ordinary "change the port" edit
	// overwrites a working upstream key with the literal "__redacted__", the
	// outbound stops connecting, and the real key is gone.
	var storedParams string
	if err := d.Store.Read().QueryRowContext(r.Context(),
		`SELECT params FROM outbounds WHERE id = ? AND node_id = ?`,
		outboundID, nodeID).Scan(&storedParams); err != nil && !errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read outbound")
		return
	}

	// Unseal stored params before merging with the incoming request
	unsealedStored, err := nodes.OpenOutboundParams(d.Box, storedParams)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not unseal stored params")
		return
	}

	merged, err := unredactParams(outboundParamsBytes(req), unsealedStored)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed params")
		return
	}
	req.Params = merged

	if err := validateOutbound(adapterKind, req); err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}

	ctx := r.Context()
	var found bool

	// Seal params before storage
	sealedParams, err := nodes.SealOutboundParams(d.Box, json.RawMessage(outboundParamsOf(req)))
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not seal outbound params")
		return
	}

	_, err = nodes.CommitNodeChange(ctx, d.Store, nodeID,
		d.actorAudit(actor, r), RequestID(ctx), "update outbound",
		func(tx *sql.Tx) error {
			// node_id in the WHERE clause, not just the id: without it, an
			// outbound belonging to another node could be edited through this
			// node's route, and the wrong node's revision would be bumped.
			res, execErr := tx.ExecContext(ctx,
				`UPDATE outbounds SET tag = ?, kind = ?, params = ?, enabled = ?, updated_at = ?
				  WHERE id = ? AND node_id = ?`,
				req.Tag, req.Kind, sealedParams, outboundEnabledOf(req),
				d.now().Unix(), outboundID, nodeID)
			if execErr != nil {
				return execErr
			}
			n, _ := res.RowsAffected()
			found = n > 0
			return nil
		}, d.snapshotOpts()...)
	if err != nil {
		if isUniqueViolation(err) {
			WriteError(w, http.StatusConflict, "conflict",
				"an outbound with that tag already exists on this node")
			return
		}
		WriteError(w, http.StatusInternalServerError, "internal", "could not update outbound")
		return
	}
	if !found {
		WriteError(w, http.StatusNotFound, "not_found", "outbound not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d Deps) handleDeleteOutbound(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	nodeID, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	outboundID, err := pathInt64(r, "outboundID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid outbound id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermOutboundWrite,
		rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	ctx := r.Context()
	var found bool
	_, err = nodes.CommitNodeChange(ctx, d.Store, nodeID,
		d.actorAudit(actor, r), RequestID(ctx), "delete outbound",
		func(tx *sql.Tx) error {
			res, execErr := tx.ExecContext(ctx,
				`DELETE FROM outbounds WHERE id = ? AND node_id = ?`, outboundID, nodeID)
			if execErr != nil {
				return execErr
			}
			n, _ := res.RowsAffected()
			found = n > 0
			return nil
		}, d.snapshotOpts()...)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not delete outbound")
		return
	}
	if !found {
		WriteError(w, http.StatusNotFound, "not_found", "outbound not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// egressCapabilitiesDTO tells the UI what this node can actually do.
//
// Exists so the frontend holds no protocol knowledge. Without it the UI would
// need its own list of outbound kinds, which drifts from the adapters the
// moment one gains or loses support -- and the failure mode is an operator
// picking a kind the adapter refuses, which is not a validation error on the
// node but a proxy that will not start.
type egressCapabilitiesDTO struct {
	Supported     bool     `json:"supported"`
	AdapterKind   string   `json:"adapter_kind,omitempty"`
	OutboundKinds []string `json:"outbound_kinds"`
	// BuiltinTags are outbounds the adapter provides itself, which a rule may
	// reference although no row exists for them. The UI lists them alongside
	// the configured outbounds when choosing a rule's target.
	BuiltinTags []string `json:"builtin_tags"`
	// Reason explains an unsupported node, so the UI can say why rather than
	// simply hiding the feature with no account of itself.
	Reason string `json:"reason,omitempty"`
}

func (d Deps) handleGetEgressCapabilities(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	nodeID, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermOutboundRead,
		rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	adapterKind, err := d.egressCapableAdapter(r.Context(), nodeID)
	if errors.Is(err, errNodeNotFound) {
		WriteError(w, http.StatusNotFound, "not_found", "node not found")
		return
	}
	if err != nil {
		// Not an error to the caller: "this node cannot route" is a fact about
		// the node, and the UI needs it to decide what to show.
		WriteJSON(w, http.StatusOK, egressCapabilitiesDTO{
			Supported:     false,
			OutboundKinds: []string{},
			BuiltinTags:   []string{},
			Reason:        err.Error(),
		})
		return
	}

	caps := nodes.KnownAdapters()[adapterKind].Caps
	out := egressCapabilitiesDTO{
		Supported:     true,
		AdapterKind:   adapterKind,
		OutboundKinds: caps.OutboundKinds,
		BuiltinTags:   caps.BuiltinOutboundTags,
	}
	// Never nil: the UI iterates these, and a null would need a guard at every
	// call site.
	if out.OutboundKinds == nil {
		out.OutboundKinds = []string{}
	}
	if out.BuiltinTags == nil {
		out.BuiltinTags = []string{}
	}
	WriteJSON(w, http.StatusOK, out)
}

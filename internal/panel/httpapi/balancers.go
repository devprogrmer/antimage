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

// Balancers are node-scoped, independently addressable rows like outbounds
// and routing rules -- an operator adds, edits, and deletes individual
// balancers, unlike DNS's single whole-object config. Gated on the same
// outbound:* permission as egress and routing: a balancer only ever selects
// among outbounds, so it sits inside that same "where does traffic go"
// trust boundary. Every mutation goes through CommitNodeChange, so defining
// a balancer bumps the node's revision like any other desired-state change.

type balancerRequest struct {
	Tag      string   `json:"tag"`
	Selector []string `json:"selector"`
	Strategy string   `json:"strategy"`
	Enabled  *bool    `json:"enabled"`
}

type balancerDTO struct {
	ID       int64    `json:"id"`
	NodeID   int64    `json:"node_id"`
	Tag      string   `json:"tag"`
	Selector []string `json:"selector"`
	Strategy string   `json:"strategy"`
	Enabled  bool     `json:"enabled"`
}

// balancerCapableAdapter returns an adapter on this node that can apply
// balancers, or an error naming why none can.
//
// Fail closed, the same reasoning egressCapableAdapter documents: a
// balancer stored against a node whose adapters have no balancer concept is
// not a harmless no-op, it is a policy the panel shows an operator that the
// node never applies.
func (d Deps) balancerCapableAdapter(ctx context.Context, nodeID int64) (string, error) {
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
		if desc, ok := known[k]; ok && desc.Caps.SupportsBalancer {
			return k, nil
		}
	}
	return "", fmt.Errorf(
		"no adapter on this node can apply balancers (node runs %s)",
		strings.Join(kinds, ", "))
}

// validateBalancer refuses a balancer before it can reach a node.
//
// The adapter refuses these too at render time (see renderBalancers), which
// is the check that catches everything -- refusing here as well means the
// operator is told when they submit, not after watching the node fail to
// converge.
func validateBalancer(req balancerRequest) error {
	if strings.TrimSpace(req.Tag) == "" {
		return errors.New("tag is required; routing rules select balancers by tag")
	}
	if len(req.Selector) == 0 {
		return errors.New("selector is required; a balancer with none would match no outbound")
	}
	switch req.Strategy {
	case "", "random", "least_ping":
	default:
		return fmt.Errorf("strategy must be random, least_ping, or empty, got %q", req.Strategy)
	}
	return nil
}

func balancerEnabledOf(req balancerRequest) int {
	if req.Enabled != nil && !*req.Enabled {
		return 0
	}
	return 1
}

func selectorJSON(selector []string) string {
	if selector == nil {
		selector = []string{}
	}
	b, err := json.Marshal(selector)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func (d Deps) handleListBalancers(w http.ResponseWriter, r *http.Request) {
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

	rows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT id, node_id, tag, selector, strategy, enabled
		   FROM balancers WHERE node_id = ? ORDER BY id`, nodeID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list balancers")
		return
	}
	defer func() { _ = rows.Close() }()

	out := []balancerDTO{}
	for rows.Next() {
		var b balancerDTO
		var selector string
		var enabled int
		if err := rows.Scan(&b.ID, &b.NodeID, &b.Tag, &selector, &b.Strategy, &enabled); err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not read balancers")
			return
		}
		if err := json.Unmarshal([]byte(selector), &b.Selector); err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not decode balancer selector")
			return
		}
		b.Enabled = enabled == 1
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list balancers")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"balancers": out})
}

func (d Deps) handleCreateBalancer(w http.ResponseWriter, r *http.Request) {
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

	var req balancerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if err := validateBalancer(req); err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}

	_, err = d.balancerCapableAdapter(r.Context(), nodeID)
	if errors.Is(err, errNodeNotFound) {
		WriteError(w, http.StatusNotFound, "not_found", "node not found")
		return
	}
	if err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "unsupported", err.Error())
		return
	}

	ctx := r.Context()
	var id int64
	_, err = nodes.CommitNodeChange(ctx, d.Store, nodeID,
		d.actorAudit(actor, r), RequestID(ctx), "create balancer",
		func(tx *sql.Tx) error {
			res, execErr := tx.ExecContext(ctx,
				`INSERT INTO balancers (node_id, tag, selector, strategy, enabled, created_at, updated_at)
				 VALUES (?,?,?,?,?,?,?)`,
				nodeID, req.Tag, selectorJSON(req.Selector), balancerStrategyOf(req),
				balancerEnabledOf(req), d.now().Unix(), d.now().Unix())
			if execErr != nil {
				return execErr
			}
			id, execErr = res.LastInsertId()
			return execErr
		}, d.snapshotOpts()...)
	if err != nil {
		// The unique index on (node_id, tag) is the panel's half of the rule
		// the adapter enforces at render time -- reporting it as a conflict
		// tells the operator now, not after a routing rule silently
		// resolves to the wrong balancer.
		if isUniqueViolation(err) {
			WriteError(w, http.StatusConflict, "conflict",
				"a balancer with that tag already exists on this node")
			return
		}
		WriteError(w, http.StatusInternalServerError, "internal", "could not create balancer")
		return
	}

	WriteJSON(w, http.StatusCreated, balancerDTO{
		ID: id, NodeID: nodeID, Tag: req.Tag, Selector: req.Selector,
		Strategy: balancerStrategyOf(req), Enabled: balancerEnabledOf(req) == 1,
	})
}

func (d Deps) handleUpdateBalancer(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	nodeID, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	balancerID, err := pathInt64(r, "balancerID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid balancer id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermOutboundWrite,
		rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	var req balancerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if err := validateBalancer(req); err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}

	ctx := r.Context()
	var found bool
	_, err = nodes.CommitNodeChange(ctx, d.Store, nodeID,
		d.actorAudit(actor, r), RequestID(ctx), "update balancer",
		func(tx *sql.Tx) error {
			res, execErr := tx.ExecContext(ctx,
				`UPDATE balancers
				    SET tag = ?, selector = ?, strategy = ?, enabled = ?, updated_at = ?
				  WHERE id = ? AND node_id = ?`,
				req.Tag, selectorJSON(req.Selector), balancerStrategyOf(req),
				balancerEnabledOf(req), d.now().Unix(), balancerID, nodeID)
			if execErr != nil {
				return execErr
			}
			n, execErr := res.RowsAffected()
			if execErr != nil {
				return execErr
			}
			// node_id is in the WHERE clause, so a balancer belonging to a
			// different node reads as missing rather than being updated
			// through this node's URL.
			found = n > 0
			return nil
		}, d.snapshotOpts()...)
	if err != nil {
		if isUniqueViolation(err) {
			WriteError(w, http.StatusConflict, "conflict",
				"a balancer with that tag already exists on this node")
			return
		}
		WriteError(w, http.StatusInternalServerError, "internal", "could not update balancer")
		return
	}
	if !found {
		WriteError(w, http.StatusNotFound, "not_found", "balancer not found")
		return
	}

	WriteJSON(w, http.StatusOK, balancerDTO{
		ID: balancerID, NodeID: nodeID, Tag: req.Tag, Selector: req.Selector,
		Strategy: balancerStrategyOf(req), Enabled: balancerEnabledOf(req) == 1,
	})
}

func (d Deps) handleDeleteBalancer(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	nodeID, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	balancerID, err := pathInt64(r, "balancerID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid balancer id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermOutboundWrite,
		rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	ctx := r.Context()
	var found bool
	_, err = nodes.CommitNodeChange(ctx, d.Store, nodeID,
		d.actorAudit(actor, r), RequestID(ctx), "delete balancer",
		func(tx *sql.Tx) error {
			res, execErr := tx.ExecContext(ctx,
				`DELETE FROM balancers WHERE id = ? AND node_id = ?`, balancerID, nodeID)
			if execErr != nil {
				return execErr
			}
			n, execErr := res.RowsAffected()
			if execErr != nil {
				return execErr
			}
			found = n > 0
			return nil
		}, d.snapshotOpts()...)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not delete balancer")
		return
	}
	if !found {
		WriteError(w, http.StatusNotFound, "not_found", "balancer not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func balancerStrategyOf(req balancerRequest) string {
	if req.Strategy == "" {
		return "random"
	}
	return req.Strategy
}

// knownBalancerTags is every tag a routing rule on this node may reference,
// mirroring knownOutboundTags -- unlike outbounds, no adapter supplies a
// balancer tag on its own, so this is exactly the enabled rows.
func (d Deps) knownBalancerTags(ctx context.Context, nodeID int64) (map[string]bool, error) {
	tags := map[string]bool{}
	rows, err := d.Store.Read().QueryContext(ctx,
		`SELECT tag FROM balancers WHERE node_id = ? AND enabled = 1`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("read balancer tags: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, fmt.Errorf("scan balancer tag: %w", err)
		}
		tags[tag] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate balancer tags: %w", err)
	}
	return tags, nil
}

// requireKnownBalancer refuses a tag that resolves nowhere, mirroring
// requireKnownOutbound.
func (d Deps) requireKnownBalancer(ctx context.Context, nodeID int64, tag string) error {
	known, err := d.knownBalancerTags(ctx, nodeID)
	if err != nil {
		return err
	}
	if !known[tag] {
		return fmt.Errorf(
			"no enabled balancer tagged %q on this node; a rule naming a tag that "+
				"resolves nowhere makes the node refuse its whole configuration", tag)
	}
	return nil
}

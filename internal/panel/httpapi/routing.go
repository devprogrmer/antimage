package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

// Routing rules decide where a node sends matched traffic. Like outbounds they
// are node-scoped, gated on outbound:* rather than service:*, and every
// mutation goes through CommitNodeChange -- the only path allowed to move
// desired_revision -- so configuring routing bumps the node's revision like any
// other desired-state change.

type routingRuleRequest struct {
	Priority    *int     `json:"priority"`
	Domains     []string `json:"domains"`
	IPCIDRs     []string `json:"ip_cidrs"`
	GeoIP       []string `json:"geoip"`
	GeoSite     []string `json:"geosite"`
	Ports       []string `json:"ports"`
	InboundTags []string `json:"inbound_tags"`
	SubjectIDs  []int64  `json:"subject_ids"`
	Network     string   `json:"network"`
	OutboundTag string   `json:"outbound_tag"`
	Enabled     *bool    `json:"enabled"`
}

type routingRuleDTO struct {
	ID          int64    `json:"id"`
	NodeID      int64    `json:"node_id"`
	Priority    int      `json:"priority"`
	Domains     []string `json:"domains"`
	IPCIDRs     []string `json:"ip_cidrs"`
	GeoIP       []string `json:"geoip"`
	GeoSite     []string `json:"geosite"`
	Ports       []string `json:"ports"`
	InboundTags []string `json:"inbound_tags"`
	SubjectIDs  []int64  `json:"subject_ids"`
	Network     string   `json:"network"`
	OutboundTag string   `json:"outbound_tag"`
	Enabled     bool     `json:"enabled"`
}

// validateRoutingRule refuses a rule before it can reach a node.
//
// Both adapters refuse these at render time, which is the check that catches
// everything. Refusing here as well means the operator is told when they submit
// the rule, rather than watching the node fail to converge afterwards with an
// error attached to a revision instead of to the thing they just typed.
func validateRoutingRule(req routingRuleRequest) error {
	if strings.TrimSpace(req.OutboundTag) == "" {
		return errors.New("outbound_tag is required; a rule that matches but selects nothing silently drops traffic")
	}
	switch req.Network {
	case "", "tcp", "udp":
	default:
		return fmt.Errorf("network must be tcp, udp, or empty for both, got %q", req.Network)
	}
	// sing-box takes ports as numbers and has a separate field for ranges, so a
	// range here would be refused by that adapter at render time. Catching it
	// now keeps the two adapters from disagreeing about what the panel accepts.
	for _, p := range req.Ports {
		if _, err := strconv.Atoi(strings.TrimSpace(p)); err != nil {
			return fmt.Errorf("port %q is not a number; ranges are not supported", p)
		}
	}
	if countMatchers(req) == 0 {
		return errors.New(
			"a rule needs at least one matcher; both proxies apply a rule with none to ALL traffic")
	}
	return nil
}

func countMatchers(req routingRuleRequest) int {
	n := 0
	for _, set := range [][]string{
		req.Domains, req.IPCIDRs, req.GeoIP, req.GeoSite, req.Ports, req.InboundTags,
	} {
		if len(set) > 0 {
			n++
		}
	}
	if len(req.SubjectIDs) > 0 {
		n++
	}
	if req.Network != "" {
		n++
	}
	return n
}

// jsonArray renders a matcher for storage, normalising nil to an empty array so
// the column never holds NULL or the empty string.
func jsonArray[T any](in []T) string {
	if in == nil {
		in = []T{}
	}
	b, err := json.Marshal(in)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func routingPriorityOf(req routingRuleRequest) int {
	if req.Priority == nil {
		return 0
	}
	return *req.Priority
}

func routingEnabledOf(req routingRuleRequest) int {
	if req.Enabled != nil && !*req.Enabled {
		return 0
	}
	return 1
}

func (d Deps) handleListRoutingRules(w http.ResponseWriter, r *http.Request) {
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
		`SELECT id, priority, domains, ip_cidrs, geoip, geosite, ports,
		        inbound_tags, subject_ids, network, outbound_tag, enabled
		   FROM routing_rules
		  WHERE node_id = ?
		  ORDER BY priority, id`, nodeID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list routing rules")
		return
	}
	defer func() { _ = rows.Close() }()

	out := []routingRuleDTO{}
	for rows.Next() {
		dto := routingRuleDTO{NodeID: nodeID}
		var domains, ipCIDRs, geoIP, geoSite, ports, inboundTags, subjectIDs string
		var enabled int
		if err := rows.Scan(
			&dto.ID, &dto.Priority, &domains, &ipCIDRs, &geoIP, &geoSite, &ports,
			&inboundTags, &subjectIDs, &dto.Network, &dto.OutboundTag, &enabled,
		); err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not read routing rule")
			return
		}
		dto.Enabled = enabled == 1
		for _, f := range []struct {
			raw  string
			into any
		}{
			{domains, &dto.Domains}, {ipCIDRs, &dto.IPCIDRs}, {geoIP, &dto.GeoIP},
			{geoSite, &dto.GeoSite}, {ports, &dto.Ports}, {inboundTags, &dto.InboundTags},
			{subjectIDs, &dto.SubjectIDs},
		} {
			if err := json.Unmarshal([]byte(f.raw), f.into); err != nil {
				WriteError(w, http.StatusInternalServerError, "internal", "could not decode routing rule")
				return
			}
		}
		out = append(out, dto)
	}
	if err := rows.Err(); err != nil {
		// A truncated rule list read as complete is worse than an error: an
		// operator would conclude a rule they expected is missing and add it
		// again.
		WriteError(w, http.StatusInternalServerError, "internal", "could not list routing rules")
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{"rules": out})
}

func (d Deps) handleCreateRoutingRule(w http.ResponseWriter, r *http.Request) {
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

	var req routingRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}

	// Same capability gate as outbounds: a rule is meaningless on a node whose
	// adapters have no routing engine, and accepting one would show an operator
	// a policy the node is not enforcing.
	adapterKind, err := d.egressCapableAdapter(r.Context(), nodeID)
	if errors.Is(err, errNodeNotFound) {
		WriteError(w, http.StatusNotFound, "not_found", "node not found")
		return
	}
	if err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "unsupported", err.Error())
		return
	}
	if err := validateRoutingRule(req); err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}
	if err := d.requireKnownOutbound(r.Context(), nodeID, adapterKind, req.OutboundTag); err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}

	ctx := r.Context()
	var id int64
	_, err = nodes.CommitNodeChange(ctx, d.Store, nodeID,
		d.actorAudit(actor, r), RequestID(ctx), "create routing rule",
		func(tx *sql.Tx) error {
			res, execErr := tx.ExecContext(ctx,
				`INSERT INTO routing_rules
				   (node_id, priority, domains, ip_cidrs, geoip, geosite, ports,
				    inbound_tags, subject_ids, network, outbound_tag, enabled,
				    created_at, updated_at)
				 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				nodeID, routingPriorityOf(req),
				jsonArray(req.Domains), jsonArray(req.IPCIDRs), jsonArray(req.GeoIP),
				jsonArray(req.GeoSite), jsonArray(req.Ports), jsonArray(req.InboundTags),
				jsonArray(req.SubjectIDs), req.Network, req.OutboundTag,
				routingEnabledOf(req), d.now().Unix(), d.now().Unix())
			if execErr != nil {
				return execErr
			}
			id, execErr = res.LastInsertId()
			return execErr
		}, d.snapshotOpts()...)
	if err != nil {
		// The adapters refuse a rule naming a tag the node does not have. That
		// check sees both panel-defined outbounds and the ones an adapter
		// supplies on its own, so it is the authoritative one and its message
		// is worth surfacing rather than replacing.
		WriteError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}

	WriteJSON(w, http.StatusCreated, routingRuleDTO{
		ID: id, NodeID: nodeID, Priority: routingPriorityOf(req),
		Domains: req.Domains, IPCIDRs: req.IPCIDRs, GeoIP: req.GeoIP, GeoSite: req.GeoSite,
		Ports: req.Ports, InboundTags: req.InboundTags, SubjectIDs: req.SubjectIDs,
		Network: req.Network, OutboundTag: req.OutboundTag,
		Enabled: routingEnabledOf(req) == 1,
	})
}

func (d Deps) handleUpdateRoutingRule(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	nodeID, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	ruleID, err := pathInt64(r, "ruleID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid rule id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermOutboundWrite,
		rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	var req routingRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if err := validateRoutingRule(req); err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}
	adapterKind, err := d.egressCapableAdapter(r.Context(), nodeID)
	if err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "unsupported", err.Error())
		return
	}
	if err := d.requireKnownOutbound(r.Context(), nodeID, adapterKind, req.OutboundTag); err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}

	ctx := r.Context()
	var found bool
	_, err = nodes.CommitNodeChange(ctx, d.Store, nodeID,
		d.actorAudit(actor, r), RequestID(ctx), "update routing rule",
		func(tx *sql.Tx) error {
			res, execErr := tx.ExecContext(ctx,
				`UPDATE routing_rules
				    SET priority = ?, domains = ?, ip_cidrs = ?, geoip = ?, geosite = ?,
				        ports = ?, inbound_tags = ?, subject_ids = ?, network = ?,
				        outbound_tag = ?, enabled = ?, updated_at = ?
				  WHERE id = ? AND node_id = ?`,
				routingPriorityOf(req),
				jsonArray(req.Domains), jsonArray(req.IPCIDRs), jsonArray(req.GeoIP),
				jsonArray(req.GeoSite), jsonArray(req.Ports), jsonArray(req.InboundTags),
				jsonArray(req.SubjectIDs), req.Network, req.OutboundTag,
				routingEnabledOf(req), d.now().Unix(), ruleID, nodeID)
			if execErr != nil {
				return execErr
			}
			n, execErr := res.RowsAffected()
			if execErr != nil {
				return execErr
			}
			// node_id is in the WHERE clause, so a rule belonging to a
			// different node reads as missing rather than being updated
			// through this node's URL.
			found = n > 0
			return nil
		}, d.snapshotOpts()...)
	if err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}
	if !found {
		WriteError(w, http.StatusNotFound, "not_found", "routing rule not found")
		return
	}

	WriteJSON(w, http.StatusOK, routingRuleDTO{
		ID: ruleID, NodeID: nodeID, Priority: routingPriorityOf(req),
		Domains: req.Domains, IPCIDRs: req.IPCIDRs, GeoIP: req.GeoIP, GeoSite: req.GeoSite,
		Ports: req.Ports, InboundTags: req.InboundTags, SubjectIDs: req.SubjectIDs,
		Network: req.Network, OutboundTag: req.OutboundTag,
		Enabled: routingEnabledOf(req) == 1,
	})
}

func (d Deps) handleDeleteRoutingRule(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	nodeID, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	ruleID, err := pathInt64(r, "ruleID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid rule id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermOutboundWrite,
		rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	ctx := r.Context()
	var found bool
	_, err = nodes.CommitNodeChange(ctx, d.Store, nodeID,
		d.actorAudit(actor, r), RequestID(ctx), "delete routing rule",
		func(tx *sql.Tx) error {
			res, execErr := tx.ExecContext(ctx,
				`DELETE FROM routing_rules WHERE id = ? AND node_id = ?`, ruleID, nodeID)
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
		WriteError(w, http.StatusInternalServerError, "internal", "could not delete routing rule")
		return
	}
	if !found {
		WriteError(w, http.StatusNotFound, "not_found", "routing rule not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// defaultOutboundRequest sets where unmatched traffic goes.
type defaultOutboundRequest struct {
	OutboundTag string `json:"outbound_tag"`
}

// handleSetDefaultOutbound sets the node's fallback egress path.
//
// Empty clears it, which returns the node to the proxy's own default. That is a
// meaningful state rather than an absent one, so it is settable rather than
// only clearable by deleting something.
func (d Deps) handleSetDefaultOutbound(w http.ResponseWriter, r *http.Request) {
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

	var req defaultOutboundRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}

	if req.OutboundTag != "" {
		adapterKind, capErr := d.egressCapableAdapter(r.Context(), nodeID)
		if errors.Is(capErr, errNodeNotFound) {
			WriteError(w, http.StatusNotFound, "not_found", "node not found")
			return
		}
		if capErr != nil {
			WriteError(w, http.StatusUnprocessableEntity, "unsupported", capErr.Error())
			return
		}
		// Same reason as a rule: a default naming a tag that resolves nowhere
		// makes the adapter refuse the whole document.
		if tagErr := d.requireKnownOutbound(r.Context(), nodeID, adapterKind, req.OutboundTag); tagErr != nil {
			WriteError(w, http.StatusUnprocessableEntity, "invalid", tagErr.Error())
			return
		}
	}

	ctx := r.Context()
	_, err = nodes.CommitNodeChange(ctx, d.Store, nodeID,
		d.actorAudit(actor, r), RequestID(ctx), "set default outbound",
		func(tx *sql.Tx) error {
			_, execErr := tx.ExecContext(ctx,
				`UPDATE nodes SET default_outbound_tag = ? WHERE id = ?`,
				req.OutboundTag, nodeID)
			return execErr
		}, d.snapshotOpts()...)
	if err != nil {
		// The adapters refuse a default naming a tag the node does not define.
		WriteError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{"outbound_tag": req.OutboundTag})
}

// knownOutboundTags is every tag a routing rule on this node may reference.
//
// Two sources, and both matter. The outbounds table holds what an operator
// created; the adapter contributes tags it provides on its own, which for Xray
// means "direct" is routable without anybody creating an outbound for it.
//
// This exists because the adapters refuse to RENDER a rule naming a tag that
// resolves nowhere, and that refusal fails Plan for the whole node -- one bad
// rule would stop it converging on anything, including its inbounds. Catching
// it here turns a node-wide outage into a 422 on the request that caused it.
func (d Deps) knownOutboundTags(ctx context.Context, nodeID int64, adapterKind string) (map[string]bool, error) {
	tags := map[string]bool{}

	if desc, ok := nodes.KnownAdapters()[adapterKind]; ok {
		for _, t := range desc.Caps.BuiltinOutboundTags {
			tags[t] = true
		}
	}

	rows, err := d.Store.Read().QueryContext(ctx,
		`SELECT tag FROM outbounds WHERE node_id = ? AND enabled = 1`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("read outbound tags: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, fmt.Errorf("scan outbound tag: %w", err)
		}
		tags[tag] = true
	}
	if err := rows.Err(); err != nil {
		// A truncated tag set would reject a rule that is actually valid, which
		// is the safe direction but still wrong; report it rather than guess.
		return nil, fmt.Errorf("iterate outbound tags: %w", err)
	}
	return tags, nil
}

// requireKnownOutbound refuses a tag that resolves nowhere.
//
// Disabled outbounds do not count. A rule pointing at one would render against
// a document that omits it -- buildOutbounds drops disabled rows -- so the
// adapter would refuse exactly as if it never existed.
func (d Deps) requireKnownOutbound(
	ctx context.Context, nodeID int64, adapterKind, tag string,
) error {
	known, err := d.knownOutboundTags(ctx, nodeID, adapterKind)
	if err != nil {
		return err
	}
	if !known[tag] {
		return fmt.Errorf(
			"no enabled outbound tagged %q on this node; a rule naming a tag that "+
				"resolves nowhere makes the node refuse its whole configuration", tag)
	}
	return nil
}

package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

// serviceSchemaDTO is one protocol a node can serve, and what it needs.
type serviceSchemaDTO struct {
	Kind    string `json:"kind"`
	Version string `json:"version"`
	// Schema is the adapter's JSON Schema for service params, verbatim. It is
	// what the panel validates against and what an editor builds a form from,
	// so the two cannot disagree about what a valid service is.
	Schema json.RawMessage `json:"schema"`
	// Offerable is false when this node has reported no schema. Such a
	// protocol must not be offered: without a schema the panel cannot validate
	// params, so anything created would be unvalidated at best and unappliable
	// at worst.
	Offerable bool   `json:"offerable"`
	Reason    string `json:"reason,omitempty"`
	// Capabilities as THIS node reports them. HotUserAdd in particular differs
	// between two nodes running the same adapter, because it depends on that
	// host having configured a management API.
	HotUserAdd     bool     `json:"hot_user_add"`
	SelfAccounting bool     `json:"self_accounting"`
	RequiresPKI    bool     `json:"requires_pki"`
	Capabilities   []string `json:"capabilities"`
	ReportedAt     int64    `json:"reported_at"`
}

// handleListServiceSchemas serves GET /nodes/{nodeID}/service-schemas.
//
// This is what an inbound editor is built from, and everything in it comes from
// the NODE, not from what the panel was compiled to know. The distinction is
// the whole point: nodes.KnownAdapters() says what this build of the panel
// understands, while a node's Hello says what that host can actually execute.
// Offering a protocol the panel knows and the node does not produces a service
// that can never be applied -- the fake feature layer AD-3 names.
//
// A node that has never connected reports nothing, and the response is an empty
// list rather than a guess. "This node has not told us yet" and "this node runs
// nothing" are both better answers than a list of protocols it may not have.
func (d Deps) handleListServiceSchemas(w http.ResponseWriter, r *http.Request) {
	nodeID, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	// Same gate as every other node read: the permission plus TargetNode, so a
	// caller holding node:read still cannot read a node outside their scope.
	if !d.requirePermission(w, r, rbac.PermNodeRead,
		rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	entries, err := nodes.ListAdapters(r.Context(), d.Store, nodeID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal",
			"could not list adapters")
		return
	}

	out := make([]serviceSchemaDTO, 0, len(entries))
	for _, e := range entries {
		dto := serviceSchemaDTO{
			Kind: e.Kind, Version: e.Version,
			HotUserAdd:     e.HotUserAdd,
			SelfAccounting: e.SelfAccounting,
			RequiresPKI:    e.RequiresPKI,
			Capabilities:   e.Capabilities,
			ReportedAt:     e.ReportedAt.Unix(),
		}
		if len(e.ServiceSchema) > 0 {
			dto.Schema = json.RawMessage(e.ServiceSchema)
			dto.Offerable = true
		} else {
			// Said plainly rather than left as an empty field, because the UI
			// has to explain to an operator why a protocol their node runs is
			// not on offer.
			dto.Schema = json.RawMessage(`{}`)
			dto.Reason = "this node reported no service schema for " + e.Kind +
				"; upgrade the agent so it publishes one"
		}
		if dto.Capabilities == nil {
			dto.Capabilities = []string{}
		}
		out = append(out, dto)
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"node_id":  nodeID,
		"adapters": out,
	})
}

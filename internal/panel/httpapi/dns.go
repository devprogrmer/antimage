package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

// DNS is node-scoped and whole-object, like default_outbound_tag: there is
// exactly one DNS config per node, edited and read as a single form rather
// than as independently addressable rows the way outbounds and routing
// rules are. Every mutation still goes through CommitNodeChange -- the only
// path allowed to move desired_revision -- so saving a DNS config bumps the
// node's revision like any other desired-state change.
//
// Gated on outbound:*, not a new dns:* permission: DNS servers decide where
// a node's OWN lookups go, which is the same trust boundary
// PermOutboundRead/Write already draws around "where this node's traffic
// goes" -- an operator who can redirect a node's traffic through a proxy
// they control can just as easily redirect its DNS resolution.

type dnsServerRequest struct {
	Address      string   `json:"address"`
	Domains      []string `json:"domains"`
	SkipFallback bool     `json:"skip_fallback"`
}

type fakeDNSPoolRequest struct {
	IPPool   string `json:"ip_pool"`
	PoolSize int    `json:"pool_size"`
}

type dnsRequest struct {
	Servers       []dnsServerRequest   `json:"servers"`
	Hosts         map[string][]string  `json:"hosts"`
	FakeDNS       []fakeDNSPoolRequest `json:"fakedns"`
	QueryStrategy string               `json:"query_strategy"`
	DisableCache  bool                 `json:"disable_cache"`
}

// dnsResponse doubles as the GET response (with Supported/AdapterKind/Reason
// describing the node's capability) and the PUT response (echoing what was
// saved). A node whose adapters cannot apply DNS gets Supported=false and
// every config field omitted, the same shape EgressCapabilities uses for
// egress -- not an error, because a node with no routing engine is a normal
// node and saying so is more use than a 4xx the frontend has to special-case.
type dnsResponse struct {
	Supported     bool                 `json:"supported"`
	AdapterKind   string               `json:"adapter_kind,omitempty"`
	Reason        string               `json:"reason,omitempty"`
	Servers       []dnsServerRequest   `json:"servers,omitempty"`
	Hosts         map[string][]string  `json:"hosts,omitempty"`
	FakeDNS       []fakeDNSPoolRequest `json:"fakedns,omitempty"`
	QueryStrategy string               `json:"query_strategy,omitempty"`
	DisableCache  bool                 `json:"disable_cache,omitempty"`
}

// dnsCapableAdapter returns an adapter on this node that can apply DNS
// config, or an error naming why none can.
//
// Fail closed, the same reasoning egressCapableAdapter documents: a DNS
// config stored against a node whose adapters have no DNS concept is not a
// harmless no-op, it is a policy the panel shows an operator that the node
// never applies.
func (d Deps) dnsCapableAdapter(ctx context.Context, nodeID int64) (string, error) {
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
		if desc, ok := known[k]; ok && desc.Caps.SupportsDNS {
			return k, nil
		}
	}
	return "", fmt.Errorf(
		"no adapter on this node can apply DNS config (node runs %s)",
		strings.Join(kinds, ", "))
}

// validateDNS refuses a config before it can reach a node.
//
// The adapter refuses these too at render time, which is the check that
// catches everything -- refusing here as well means the operator is told
// when they submit, rather than discovering it later against a revision
// instead of the form they just filled in.
func validateDNS(req dnsRequest) error {
	for i, s := range req.Servers {
		if strings.TrimSpace(s.Address) == "" {
			return fmt.Errorf("server %d has no address", i)
		}
	}
	for domain, ips := range req.Hosts {
		if strings.TrimSpace(domain) == "" {
			return errors.New("a host override has an empty domain")
		}
		if len(ips) == 0 {
			return fmt.Errorf("host %q has no addresses", domain)
		}
	}
	for i, p := range req.FakeDNS {
		if _, _, err := net.ParseCIDR(p.IPPool); err != nil {
			return fmt.Errorf("fakedns pool %d: %q is not a valid CIDR", i, p.IPPool)
		}
		if p.PoolSize <= 0 {
			return fmt.Errorf("fakedns pool %d (%s): pool_size must be positive", i, p.IPPool)
		}
	}
	switch req.QueryStrategy {
	case "", "UseIP", "UseIPv4", "UseIPv6":
	default:
		return fmt.Errorf("query_strategy must be UseIP, UseIPv4, UseIPv6, or empty, got %q", req.QueryStrategy)
	}
	return nil
}

// dnsRequestFromStored decodes the stored JSON blob into the wire shape,
// so GET and a successful PUT return identically shaped responses.
func dnsRequestFromStored(raw string) (dnsRequest, error) {
	var req dnsRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return dnsRequest{}, err
	}
	return req, nil
}

func (d Deps) handleGetNodeDNS(w http.ResponseWriter, r *http.Request) {
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

	adapterKind, capErr := d.dnsCapableAdapter(r.Context(), nodeID)
	if errors.Is(capErr, errNodeNotFound) {
		WriteError(w, http.StatusNotFound, "not_found", "node not found")
		return
	}
	if capErr != nil {
		WriteJSON(w, http.StatusOK, dnsResponse{Supported: false, Reason: capErr.Error()})
		return
	}

	var raw string
	if err := d.Store.Read().QueryRowContext(r.Context(),
		`SELECT dns_config FROM nodes WHERE id = ?`, nodeID).Scan(&raw); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read dns config")
		return
	}
	req, err := dnsRequestFromStored(raw)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not decode dns config")
		return
	}

	WriteJSON(w, http.StatusOK, dnsResponse{
		Supported: true, AdapterKind: adapterKind,
		Servers: req.Servers, Hosts: req.Hosts, FakeDNS: req.FakeDNS,
		QueryStrategy: req.QueryStrategy, DisableCache: req.DisableCache,
	})
}

func (d Deps) handleSetNodeDNS(w http.ResponseWriter, r *http.Request) {
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

	var req dnsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if err := validateDNS(req); err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}

	adapterKind, capErr := d.dnsCapableAdapter(r.Context(), nodeID)
	if errors.Is(capErr, errNodeNotFound) {
		WriteError(w, http.StatusNotFound, "not_found", "node not found")
		return
	}
	if capErr != nil {
		WriteError(w, http.StatusUnprocessableEntity, "unsupported", capErr.Error())
		return
	}

	stored, err := json.Marshal(req)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not encode dns config")
		return
	}

	ctx := r.Context()
	_, err = nodes.CommitNodeChange(ctx, d.Store, nodeID,
		d.actorAudit(actor, r), RequestID(ctx), "set dns config",
		func(tx *sql.Tx) error {
			_, execErr := tx.ExecContext(ctx,
				`UPDATE nodes SET dns_config = ? WHERE id = ?`, string(stored), nodeID)
			return execErr
		}, d.snapshotOpts()...)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not save dns config")
		return
	}

	WriteJSON(w, http.StatusOK, dnsResponse{
		Supported: true, AdapterKind: adapterKind,
		Servers: req.Servers, Hosts: req.Hosts, FakeDNS: req.FakeDNS,
		QueryStrategy: req.QueryStrategy, DisableCache: req.DisableCache,
	})
}

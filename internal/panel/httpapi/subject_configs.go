package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/subjects"
	"github.com/amyrm/antimage/internal/panel/subscriptions"
)

// configsResponse is what the browser needs to show a subject everything they
// can connect with.
type configsResponse struct {
	SubjectID int64  `json:"subject_id"`
	Name      string `json:"name"`
	// SubscriptionURL is the aggregated link, which only carries the protocols
	// an aggregated format can represent. Empty when the subject has no token
	// yet, which is not an error.
	SubscriptionURL string `json:"subscription_url,omitempty"`
	// Status, expiry and quota are here so the operator can see whether the
	// thing they are about to hand over will actually work. A subscription for
	// a frozen subject is a support ticket waiting to happen.
	Status     string `json:"status"`
	ExpiresAt  *int64 `json:"expires_at"`
	QuotaBytes *int64 `json:"quota_bytes"`
	QuotaUsed  int64  `json:"quota_used_bytes"`

	// GroupFilter is the protocol selection the subject's subscription group
	// imposes. Empty means no group, or a group that carries everything --
	// the UI states which rather than leaving an empty list ambiguous.
	GroupFilter []string `json:"group_filter"`

	Configs []subscriptions.ClientConfig `json:"configs"`
	// Skipped names inbounds that produced no client configuration, with the
	// reason. Silence here is what let a WireGuard inbound vanish from a
	// subscription with nothing to say it had.
	Skipped []skippedInbound `json:"skipped"`
}

type skippedInbound struct {
	ServiceID   int64  `json:"service_id"`
	NodeName    string `json:"node_name"`
	AdapterKind string `json:"adapter_kind"`
	Reason      string `json:"reason"`
}

// handleSubjectConfigs returns every client configuration a subject can use.
//
// GET /api/v1/subjects/{subjectID}/configs
//
// Distinct from the public /subscribe/{token} endpoint in two ways that
// matter. That one serves a CLIENT one aggregated document in whatever format
// the client's user agent implies; this serves an OPERATOR the per-inbound
// truth, including the protocols no aggregated format can carry. Without it
// the panel could hand out a WireGuard tunnel it had no way to display.
func (d Deps) handleSubjectConfigs(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	// Permission first, then scope. These configurations embed the subject's
	// credentials, so this is as sensitive a read as a credential reveal and
	// is gated the same way.
	if !d.authorize(w, r, actor, rbac.PermCredReveal, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	subjectID, err := pathInt64(r, "subjectID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid subject id")
		return
	}
	if !d.requireSubjectInScope(w, r, actor, subjectID) {
		return
	}

	ctx := r.Context()
	subj, err := d.subjectService().Get(ctx, d.svcActor(r, actor), subjectID)
	if err != nil {
		d.writeServiceError(w, r, actor, "subject.configs", err)
		return
	}

	resp := configsResponse{
		SubjectID: subj.ID,
		Name:      subj.Name,
		Status:    string(subj.Status(d.now())),
		QuotaUsed: 0,
	}
	if subj.ExpiresAt != nil {
		v := subj.ExpiresAt.Unix()
		resp.ExpiresAt = &v
	}
	if err := d.Store.Read().QueryRowContext(ctx,
		`SELECT quota_bytes, quota_used_bytes FROM subjects WHERE id = ?`, subjectID).
		Scan(&resp.QuotaBytes, &resp.QuotaUsed); err != nil && !errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read quota")
		return
	}

	creds, err := d.subjectCredentials(ctx, subjectID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read credentials")
		return
	}

	configs, skipped, err := d.buildSubjectConfigs(ctx, subjectID, subj.Name, creds)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not build configurations")
		return
	}
	// The subject's subscription group. Entries it excludes are moved into
	// Skipped rather than dropped: an operator looking at what a customer was
	// sold needs to see that the group is why an inbound is absent, not
	// discover it from a support ticket.
	filter, err := subscriptions.FilterForSubject(ctx, d.Store, subjectID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read subscription group")
		return
	}
	kept := filter.ApplyConfigs(configs)
	if len(kept) != len(configs) {
		in := make(map[int64]bool, len(kept))
		for _, c := range kept {
			in[c.ServiceID] = true
		}
		for _, c := range configs {
			if !in[c.ServiceID] {
				skipped = append(skipped, skippedInbound{
					ServiceID: c.ServiceID, NodeName: c.NodeName,
					AdapterKind: c.AdapterKind, Reason: "excludedByGroup",
				})
			}
		}
	}
	resp.Configs = kept
	resp.Skipped = skipped
	resp.GroupFilter = filter.Protocols

	// The token is read, never minted here. Creating one as a side effect of
	// looking would mean a read endpoint changed state, and a token handed out
	// by an accidental page load is one nobody meant to issue.
	token, err := subjects.PeekToken(ctx, d.Store, subjectID)
	if err == nil && token != "" {
		resp.SubscriptionURL = "/subscribe/" + token
	}

	// The credentials are in this body. It must not sit in a shared cache.
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, http.StatusOK, resp)
}

// subjectCredentials unseals the secrets a client configuration needs.
func (d Deps) subjectCredentials(
	ctx context.Context, subjectID int64,
) (subscriptions.Credentials, error) {
	var out subscriptions.Credentials
	if d.Box == nil {
		// No master key: the panel serves everything else, but it cannot
		// produce a configuration without the secrets that go in it.
		return out, fmt.Errorf("no secret box configured")
	}
	rows, err := d.Store.Read().QueryContext(ctx,
		`SELECT kind, value_enc FROM subject_credentials WHERE subject_id = ?`, subjectID)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var kind string
		var enc []byte
		if err := rows.Scan(&kind, &enc); err != nil {
			return out, err
		}
		plain, err := d.Box.Open(enc)
		if err != nil {
			// One unreadable credential must not lose the others: a subject
			// with a corrupt password can still be given their VLESS link.
			continue
		}
		switch kind {
		case "uuid":
			out.UUID = string(plain)
		case "password":
			out.Password = string(plain)
		}
	}
	return out, rows.Err()
}

// buildSubjectConfigs turns every inbound this subject is granted into the
// configuration its protocol actually uses.
//
// Node status is NOT filtered on. The public subscription endpoint serves only
// online nodes, which is right for a client fetching something to connect
// with; an operator looking at what a customer has been sold needs to see the
// inbound on a node that is currently down, or the entry silently disappears
// and reappears as the fleet flaps.
func (d Deps) buildSubjectConfigs(
	ctx context.Context, subjectID int64, subjectName string,
	creds subscriptions.Credentials,
) ([]subscriptions.ClientConfig, []skippedInbound, error) {
	rows, err := d.Store.Read().QueryContext(ctx, `
		SELECT n.id, n.name, n.address, s.id, s.adapter_kind, s.params, s.enabled
		  FROM subject_services ss
		  JOIN services s ON s.id = ss.service_id
		  JOIN nodes n    ON n.id = s.node_id
		 WHERE ss.subject_id = ?
		 ORDER BY n.name, s.id`, subjectID)
	if err != nil {
		return nil, nil, fmt.Errorf("query inbounds: %w", err)
	}
	defer func() { _ = rows.Close() }()

	configs := []subscriptions.ClientConfig{}
	skipped := []skippedInbound{}

	for rows.Next() {
		var (
			nodeID      int64
			nodeName    string
			nodeAddr    string
			serviceID   int64
			adapterKind string
			paramsJSON  string
			enabled     int
		)
		if err := rows.Scan(&nodeID, &nodeName, &nodeAddr, &serviceID,
			&adapterKind, &paramsJSON, &enabled); err != nil {
			return nil, nil, fmt.Errorf("scan inbound: %w", err)
		}

		if enabled == 0 {
			// Reported rather than dropped: a disabled inbound is why a
			// customer's config stopped working, and an operator comparing
			// what they sold against what they see needs to find it.
			skipped = append(skipped, skippedInbound{
				ServiceID: serviceID, NodeName: nodeName,
				AdapterKind: adapterKind, Reason: "inboundDisabled",
			})
			continue
		}

		var params map[string]any
		if err := json.Unmarshal([]byte(paramsJSON), &params); err != nil {
			skipped = append(skipped, skippedInbound{
				ServiceID: serviceID, NodeName: nodeName,
				AdapterKind: adapterKind, Reason: "paramsUnreadable",
			})
			continue
		}

		cfg, err := subscriptions.BuildClientConfig(
			subscriptions.Inbound{
				ServiceID: serviceID, AdapterKind: adapterKind, Params: params,
			},
			subscriptions.NodeRef{ID: nodeID, Name: nodeName, Address: nodeAddr},
			subjectName, creds,
		)
		if err != nil {
			// The reason is carried through verbatim. "This protocol has no
			// client configuration" and "this inbound has no port" are
			// different problems and the operator has to be able to tell them
			// apart.
			skipped = append(skipped, skippedInbound{
				ServiceID: serviceID, NodeName: nodeName,
				AdapterKind: adapterKind, Reason: err.Error(),
			})
			continue
		}
		configs = append(configs, cfg)
	}
	return configs, skipped, rows.Err()
}

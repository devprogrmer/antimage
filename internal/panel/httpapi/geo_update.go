package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/control"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
)

// Default geo database source. Loyalsoldier/v2ray-rules-dat is the
// community compilation most third-party Xray/V2Ray panels already default
// to, and it publishes a companion `.sha256sum` file per asset -- which is
// what UpdateGeoData's verify-before-install design requires; a source with
// no published checksum would leave nothing to verify against, and this
// feature does not silently skip verification for a source that lacks one.
//
// Overridable per request specifically because this is a claim about a
// third party's continued behavior, not a fact this codebase controls: an
// operator behind a mirror, a fork, or a network policy that blocks GitHub
// needs a way to point elsewhere without an agent rebuild.
const (
	defaultGeoIPURL         = "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geoip.dat"
	defaultGeoIPSHA256URL   = "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geoip.dat.sha256sum"
	defaultGeoSiteURL       = "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat"
	defaultGeoSiteSHA256URL = "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat.sha256sum"
)

// geoUpdateCommandTimeout is longer than restartCommandTimeout: this
// involves two downloads (a few MB each) plus a restart afterward, not
// just the restart.
const geoUpdateCommandTimeout = 3 * time.Minute

type geoUpdateRequest struct {
	GeoIPURL         string `json:"geoip_url"`
	GeoIPSHA256URL   string `json:"geoip_sha256_url"`
	GeoSiteURL       string `json:"geosite_url"`
	GeoSiteSHA256URL string `json:"geosite_sha256_url"`
}

func (req geoUpdateRequest) orDefaults() geoUpdateRequest {
	if strings.TrimSpace(req.GeoIPURL) == "" {
		req.GeoIPURL = defaultGeoIPURL
	}
	if strings.TrimSpace(req.GeoIPSHA256URL) == "" {
		req.GeoIPSHA256URL = defaultGeoIPSHA256URL
	}
	if strings.TrimSpace(req.GeoSiteURL) == "" {
		req.GeoSiteURL = defaultGeoSiteURL
	}
	if strings.TrimSpace(req.GeoSiteSHA256URL) == "" {
		req.GeoSiteSHA256URL = defaultGeoSiteSHA256URL
	}
	return req
}

// POST /api/v1/nodes/:id/geo-update
//
// Goes through the same on-demand command channel restart uses: a real
// UpdateGeoData command reaches a connected agent, which downloads and
// verifies both files against their published checksums, installs them
// only if both verify, and restarts whichever adapter (today: xray) reads
// them -- reported back per adapter kind, never collapsed to one boolean
// for "the node".
func (d Deps) handleUpdateNodeGeoData(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	nodeID, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node ID")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermNodeWrite, rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	var nodeName string
	err = d.Store.Read().QueryRowContext(ctx,
		`SELECT name FROM nodes WHERE id = ?`, nodeID).Scan(&nodeName)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "not_found", "node not found")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not load node")
		return
	}

	var req geoUpdateRequest
	if r.Body != nil {
		// A missing or empty body is the common case (an operator who just
		// clicked "update" with no source override), not a request error --
		// json.NewDecoder on an empty body returns io.EOF, which orDefaults
		// then fills in completely, so it is deliberately not treated as
		// malformed.
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	req = req.orDefaults()

	cmd := &pb.AgentCommand{
		CommandId: uuid.NewString(),
		Body: &pb.AgentCommand_UpdateGeoData{
			UpdateGeoData: &pb.UpdateGeoData{
				GeoipUrl: req.GeoIPURL, GeoipSha256Url: req.GeoIPSHA256URL,
				GeositeUrl: req.GeoSiteURL, GeositeSha256Url: req.GeoSiteSHA256URL,
			},
		},
	}

	var (
		delivered bool
		outcomes  []map[string]any
		cmdErr    string
	)
	if d.Hub != nil {
		result, sendErr := d.Hub.SendCommand(ctx, nodeID, cmd, geoUpdateCommandTimeout)
		switch {
		case sendErr == nil:
			delivered = true
			if geo, ok := result.Body.(*pb.AgentCommandResult_UpdateGeoData); ok {
				for _, o := range geo.UpdateGeoData.Outcomes {
					outcomes = append(outcomes, map[string]any{
						"kind": o.Kind, "ok": o.Ok, "error": o.Error,
						"geoip_sha256": o.GeoipSha256, "geosite_sha256": o.GeositeSha256,
					})
					if o.Ok {
						if _, err := nodes.RecordGeoUpdate(ctx, d.Store, nodeID, o.Kind,
							o.GeoipSha256, o.GeositeSha256, d.now()); err != nil {
							// The update itself already succeeded on the node; a
							// failure to RECORD that must not be reported to the
							// operator as the update having failed -- it did not.
							// It is audited below regardless, so the fact is not
							// lost, only the browser's "last updated" display is
							// stale until the next successful update.
							audit.BestEffort(ctx, d.Store, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
								Action: "node.geo_update.record_failed", TargetType: "node",
								TargetID: sql.NullInt64{Int64: nodeID, Valid: true},
								Result:   "failed",
								After:    map[string]any{"kind": o.Kind, "error": err.Error()},
							})
						}
					}
				}
			}
			// An empty Body means an agent too old to understand this
			// command (see handleCommand's default case) -- reported as an
			// error, not as a silent no-op success.
			if result.Body == nil {
				delivered = false
				cmdErr = "the agent did not recognise this command; it may need an upgrade"
			}
		case errors.Is(sendErr, control.ErrCommandNotDelivered):
			// Not an error state: the node is offline. Reported as
			// delivered=false so the browser can say so honestly.
		case errors.Is(sendErr, control.ErrCommandTimeout):
			cmdErr = "the agent did not reply before the deadline"
		default:
			cmdErr = sendErr.Error()
		}
	}

	if err := nodes.RecordNodeEvent(ctx, d.Store, nodeID, "geo_update_requested", "info", map[string]interface{}{
		"action": "geo_update", "admin_id": actor.AdminID, "node_name": nodeName,
		"delivered": delivered, "outcomes": outcomes,
	}, &actor.AdminID); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to record event")
		return
	}
	if err := d.Store.Write(ctx, func(tx *sql.Tx) error {
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action:     "node.geo_update",
			TargetType: "node",
			TargetID:   sql.NullInt64{Int64: nodeID, Valid: true},
			Result:     "ok",
			After:      map[string]any{"node": nodeName, "delivered": delivered},
		})
	}); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "audit failed")
		return
	}

	message := "the node is offline; nothing was updated"
	switch {
	case cmdErr != "":
		message = cmdErr
	case delivered && len(outcomes) == 0:
		message = "no adapter on this node has geo data to update"
	case delivered:
		message = "geo data update delivered"
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"node_id":   nodeID,
		"node_name": nodeName,
		"action":    "geo_update",
		"delivered": delivered,
		"outcomes":  outcomes,
		"message":   message,
	})
}

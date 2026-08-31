package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/amyrm/antimage/internal/node/adapter/wireguard"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

// Cloudflare WARP: a free WireGuard tunnel into Cloudflare's network.
// Registration needs no Cloudflare account -- the same anonymous flow the
// official 1.1.1.1 app and warp-cli use -- so "register" here means
// "generate a fresh WireGuard keypair and ask Cloudflare's client API to
// pair a device with it," not authenticating as anyone.
const warpAPIBase = "https://api.cloudflareclient.com/v0a2158"
const warpClientVersion = "a-7.21-0721"
const warpUserAgent = "okhttp/3.12.1"

// warpCredentials is what gets sealed into proxy_provider_accounts.
// PrivateKey is generated locally and never leaves the panel except sealed
// into an outbound's own sealed params at provisioning time; DeviceID and
// AccessToken are Cloudflare's, needed on every later RemoteConfig call.
type warpCredentials struct {
	PrivateKey  string `json:"private_key"`
	DeviceID    string `json:"device_id"`
	AccessToken string `json:"access_token"`
}

type warpClient struct {
	baseURL string
	http    *http.Client
}

func newWARPClient(baseURL string) warpClient {
	if baseURL == "" {
		baseURL = warpAPIBase
	}
	return warpClient{baseURL: baseURL, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c warpClient) do(ctx context.Context, method, path, token string, payload any) (map[string]any, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("CF-Client-Version", warpClientVersion)
	req.Header.Set("User-Agent", warpUserAgent)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("cloudflare: %s", msg)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("cloudflare returned a non-JSON response")
	}
	return result, nil
}

// register pairs a freshly generated device with Cloudflare's client API.
// The payload shape (key/tos/type/model/name) matches what the official
// client sends; Cloudflare has no other way to tell a "new device" request
// apart from a malformed one.
func (c warpClient) register(ctx context.Context, publicKey string) (deviceID, accessToken, license string, err error) {
	hostname, _ := os.Hostname()
	if strings.TrimSpace(hostname) == "" {
		hostname = "antimage-panel"
	}
	resp, err := c.do(ctx, http.MethodPost, "/reg", "", map[string]any{
		"key":   publicKey,
		"tos":   time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"type":  "PC",
		"model": "antimage-panel",
		"name":  hostname,
	})
	if err != nil {
		return "", "", "", err
	}
	deviceID = mapString(resp, "id")
	accessToken = mapString(resp, "token")
	if deviceID == "" || accessToken == "" {
		return "", "", "", fmt.Errorf("cloudflare response is missing device id or access token")
	}
	if account, ok := resp["account"].(map[string]any); ok {
		license = mapString(account, "license")
	}
	return deviceID, accessToken, license, nil
}

// warpPeer is what provisioning an outbound actually needs out of
// RemoteConfig's response: the endpoint and public key of the WARP edge
// this device was assigned, and the tunnel-internal address Cloudflare
// wants the client to use as its own. Cloudflare's response nests these
// under config.peers[0] and config.interface.addresses -- the same shape
// every independent WARP client reimplementation (wgcf, warp-plus, the
// official apps) has documented, since Rebecca's own client (which this
// mirrors) returns the response raw without parsing it itself.
type warpPeer struct {
	PublicKey      string
	Endpoint       string
	LocalAddresses []string
}

func (c warpClient) remoteConfig(ctx context.Context, deviceID, accessToken string) (warpPeer, error) {
	resp, err := c.do(ctx, http.MethodGet, "/reg/"+deviceID, accessToken, nil)
	if err != nil {
		return warpPeer{}, err
	}
	config, _ := resp["config"].(map[string]any)
	if config == nil {
		return warpPeer{}, fmt.Errorf("cloudflare response has no config block")
	}
	peers, _ := config["peers"].([]any)
	if len(peers) == 0 {
		return warpPeer{}, fmt.Errorf("cloudflare response lists no peers")
	}
	peer, _ := peers[0].(map[string]any)
	publicKey := mapString(peer, "public_key")
	if publicKey == "" {
		return warpPeer{}, fmt.Errorf("cloudflare peer has no public_key")
	}

	endpointBlock, _ := peer["endpoint"].(map[string]any)
	endpoint := mapString(endpointBlock, "host")
	if endpoint == "" {
		endpoint = mapString(endpointBlock, "v4")
	}
	if endpoint == "" {
		return warpPeer{}, fmt.Errorf("cloudflare peer has no usable endpoint")
	}

	var local []string
	if iface, ok := config["interface"].(map[string]any); ok {
		if addrs, ok := iface["addresses"].(map[string]any); ok {
			if v4 := mapString(addrs, "v4"); v4 != "" {
				local = append(local, v4+"/32")
			}
			if v6 := mapString(addrs, "v6"); v6 != "" {
				local = append(local, v6+"/128")
			}
		}
	}
	if len(local) == 0 {
		return warpPeer{}, fmt.Errorf("cloudflare response has no interface addresses")
	}

	return warpPeer{PublicKey: publicKey, Endpoint: endpoint, LocalAddresses: local}, nil
}

func mapString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}

type warpRegisterRequest struct {
	Label string `json:"label"`
}

// handleRegisterWarpAccount generates a WireGuard keypair locally and
// registers it with Cloudflare as a new anonymous device, storing the
// resulting credentials for later outbound provisioning.
func (d Deps) handleRegisterWarpAccount(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermOutboundWrite, rbac.Target{}) {
		return
	}
	var req warpRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if strings.TrimSpace(req.Label) == "" {
		req.Label = "WARP"
	}

	privateKey, err := wireguard.GeneratePrivateKey()
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not generate a WireGuard key")
		return
	}
	publicKey, err := wireguard.PublicKeyFromPrivate(privateKey)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not derive a WireGuard public key")
		return
	}

	client := d.warpClient()
	deviceID, accessToken, license, err := client.register(r.Context(), publicKey)
	if err != nil {
		WriteError(w, http.StatusBadGateway, "provider_error", "Cloudflare registration failed: "+err.Error())
		return
	}

	dto, err := d.storeProxyProviderAccount(r, "warp", req.Label,
		warpCredentials{PrivateKey: privateKey, DeviceID: deviceID, AccessToken: accessToken},
		map[string]any{"device_type": "PC", "has_license": license != ""})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not save WARP account")
		return
	}
	WriteJSON(w, http.StatusCreated, dto)
}

type warpOutboundRequest struct {
	NodeID int64  `json:"node_id"`
	Tag    string `json:"tag"`
}

// handleProvisionWarpOutbound fetches this device's CURRENT peer assignment
// from Cloudflare (not cached -- Cloudflare can reassign the edge a device
// talks to) and creates a real wireguard outbound from it on the chosen
// node, through the exact same insert path a manually-entered outbound uses.
func (d Deps) handleProvisionWarpOutbound(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	accountID, err := pathInt64(r, "accountID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid account id")
		return
	}
	var req warpOutboundRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermOutboundWrite, rbac.Target{Kind: rbac.TargetNode, ID: req.NodeID}) {
		return
	}
	if strings.TrimSpace(req.Tag) == "" {
		WriteError(w, http.StatusBadRequest, "bad_request", "tag is required")
		return
	}

	var creds warpCredentials
	_, err = d.loadProxyProviderAccount(r.Context(), accountID, "warp", &creds)
	if errors.Is(err, errProxyProviderAccountNotFound) {
		WriteError(w, http.StatusNotFound, "not_found", "WARP account not found")
		return
	}
	if errors.Is(err, errProxyProviderAccountWrongKind) {
		WriteError(w, http.StatusUnprocessableEntity, "invalid", "that account is not a WARP account")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read WARP account")
		return
	}

	peer, err := d.warpClient().remoteConfig(r.Context(), creds.DeviceID, creds.AccessToken)
	if err != nil {
		WriteError(w, http.StatusBadGateway, "provider_error", "Cloudflare rejected this device: "+err.Error())
		return
	}

	params, err := json.Marshal(map[string]any{
		"private_key":     creds.PrivateKey,
		"peer_public_key": peer.PublicKey,
		"endpoint":        peer.Endpoint,
		"local_addresses": peer.LocalAddresses,
	})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not encode outbound params")
		return
	}

	id, err := d.createOutboundFromProvider(r, actor, req.NodeID, req.Tag, "wireguard", params)
	if err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}
	WriteJSON(w, http.StatusCreated, map[string]any{"outbound_id": id, "node_id": req.NodeID, "tag": req.Tag})
}

// warpClient lets tests point registration/provisioning at a fake server
// instead of the real Cloudflare API. Deps.WARPAPIBase is empty in
// production, which newWARPClient resolves to the real base URL.
func (d Deps) warpClient() warpClient { return newWARPClient(d.WARPAPIBase) }

// createOutboundFromProvider is handleCreateOutbound's insert step, shared
// with every Proxy Hub provider: validate against the node's adapter
// capability, seal the params, insert exactly as a manually-entered
// outbound would be. Provider handlers build kind/params themselves because
// each provider's shape and validation differs; only the "make it a real
// outbound row" tail is common.
func (d Deps) createOutboundFromProvider(
	r *http.Request, actor *rbac.Actor, nodeID int64, tag, kind string, params json.RawMessage,
) (int64, error) {
	ctx := r.Context()
	adapterKind, err := d.egressCapableAdapter(ctx, nodeID)
	if err != nil {
		return 0, err
	}
	if err := validateOutbound(adapterKind, outboundRequest{Tag: tag, Kind: kind, Params: params}); err != nil {
		return 0, err
	}
	sealedParams, err := nodes.SealOutboundParams(d.Box, params)
	if err != nil {
		return 0, err
	}

	var id int64
	_, err = nodes.CommitNodeChange(ctx, d.Store, nodeID,
		d.actorAudit(actor, r), RequestID(ctx), "create outbound from proxy provider",
		func(tx *sql.Tx) error {
			res, execErr := tx.ExecContext(ctx,
				`INSERT INTO outbounds (node_id, tag, kind, params, enabled, created_at, updated_at)
				 VALUES (?,?,?,?,1,?,?)`,
				nodeID, tag, kind, sealedParams, d.now().Unix(), d.now().Unix())
			if execErr != nil {
				return execErr
			}
			id, execErr = res.LastInsertId()
			return execErr
		}, d.snapshotOpts()...)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, fmt.Errorf("an outbound with that tag already exists on this node")
		}
		return 0, err
	}
	return id, nil
}

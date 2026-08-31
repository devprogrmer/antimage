package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/amyrm/antimage/internal/panel/rbac"
)

// NordVPN: NordLynx (NordVPN's name for its WireGuard implementation) needs
// a NordVPN access token the operator already has (Account -> NordLynx
// setup on nordvpn.com), exchanged once for a WireGuard private key that
// NordVPN's own API hands back -- unlike WARP, antimage never generates
// this key itself, because NordVPN's servers are keyed to the specific
// private key their API returns for that token.
const nordVPNAPIBase = "https://api.nordvpn.com"

// nordVPNWireguardTechID is NordVPN's own numeric id for the "Wireguard"
// entry in every server's technologies list -- confirmed against the real
// API (GET /v2/servers), not guessed: each entry with this id carries a
// metadata pair {"name":"public_key","value":"<base64 key>"}.
const nordVPNWireguardTechID = 35

// nordVPNPort is NordLynx's fixed WireGuard listen port. NordVPN's API
// nowhere returns a per-server port because it never varies.
const nordVPNPort = 51820

// nordVPNDefaultLocalAddress is the client-side tunnel address NordVPN's
// own official Linux client (nordvpn-linux, open source) uses for every
// NordLynx connection -- NordVPN's server side NATs all clients through
// this same address rather than routing a per-account subnet, so it is not
// account-specific. NordVPN's credentials API does not return this value
// (there is nothing to return; it does not vary), so it is hardcoded here
// rather than fetched -- but the operator can override it per outbound if
// their account ever needs something else.
const nordVPNDefaultLocalAddress = "10.5.0.2/32"

type nordVPNCredentials struct {
	Token      string `json:"token"`
	PrivateKey string `json:"private_key"`
}

type nordVPNCountry struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Code string `json:"code"`
}

type nordVPNServer struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Station   string `json:"station"`
	Load      int    `json:"load"`
	PublicKey string `json:"public_key"`
}

type nordVPNClient struct {
	baseURL string
	http    *http.Client
}

func newNordVPNClient(baseURL string) nordVPNClient {
	if baseURL == "" {
		baseURL = nordVPNAPIBase
	}
	return nordVPNClient{baseURL: baseURL, http: &http.Client{Timeout: 15 * time.Second}}
}

func (c nordVPNClient) get(ctx context.Context, path, basicToken string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "antimage-panel")
	if basicToken != "" {
		req.SetBasicAuth("token", basicToken)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("nordvpn: %s", msg)
	}
	return raw, nil
}

func (c nordVPNClient) countries(ctx context.Context) ([]nordVPNCountry, error) {
	raw, err := c.get(ctx, "/v1/countries", "")
	if err != nil {
		return nil, err
	}
	var out []nordVPNCountry
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("nordvpn: could not decode countries: %w", err)
	}
	return out, nil
}

// servers lists NordLynx-capable servers in a country, best (lowest load)
// first. NordVPN's own filter only narrows by technology and country; the
// load-based ordering is this handler's own choice, not NordVPN's.
func (c nordVPNClient) servers(ctx context.Context, countryID int) ([]nordVPNServer, error) {
	if countryID <= 0 {
		return nil, errors.New("country_id is required")
	}
	path := fmt.Sprintf(
		"/v2/servers?limit=50&filters[servers_technologies][id]=%d&filters[country_id]=%s",
		nordVPNWireguardTechID, url.QueryEscape(strconv.Itoa(countryID)))
	raw, err := c.get(ctx, path, "")
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Servers []struct {
			ID           int    `json:"id"`
			Name         string `json:"name"`
			Station      string `json:"station"`
			Load         int    `json:"load"`
			Technologies []struct {
				ID       int `json:"id"`
				Metadata []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"metadata"`
			} `json:"technologies"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("nordvpn: could not decode servers: %w", err)
	}

	out := make([]nordVPNServer, 0, len(parsed.Servers))
	for _, s := range parsed.Servers {
		var publicKey string
		for _, tech := range s.Technologies {
			if tech.ID != nordVPNWireguardTechID {
				continue
			}
			for _, meta := range tech.Metadata {
				if meta.Name == "public_key" {
					publicKey = meta.Value
				}
			}
		}
		if publicKey == "" {
			// A server this filter returned but with no WireGuard public
			// key is not usable as a NordLynx peer; skip rather than offer
			// a choice that will fail at provisioning time.
			continue
		}
		out = append(out, nordVPNServer{
			ID: s.ID, Name: s.Name, Station: s.Station, Load: s.Load, PublicKey: publicKey,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Load < out[j].Load })
	return out, nil
}

// credentials exchanges a NordVPN access token for the account's NordLynx
// private key. The same key every time for a given token -- NordVPN's API
// is idempotent here, not a "create a new key" call.
func (c nordVPNClient) credentials(ctx context.Context, token string) (privateKey string, err error) {
	raw, err := c.get(ctx, "/v1/users/services/credentials", token)
	if err != nil {
		return "", err
	}
	var parsed struct {
		NordlynxPrivateKey string `json:"nordlynx_private_key"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("nordvpn: could not decode credentials: %w", err)
	}
	if parsed.NordlynxPrivateKey == "" {
		return "", errors.New("nordvpn: this account has no NordLynx private key -- enable NordLynx for it at nordvpn.com/account first")
	}
	return parsed.NordlynxPrivateKey, nil
}

func (d Deps) nordVPNClient() nordVPNClient { return newNordVPNClient(d.NordVPNAPIBase) }

func (d Deps) handleListNordVPNCountries(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermOutboundRead, rbac.Target{}) {
		return
	}
	countries, err := d.nordVPNClient().countries(r.Context())
	if err != nil {
		WriteError(w, http.StatusBadGateway, "provider_error", err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"countries": countries})
}

func (d Deps) handleListNordVPNServers(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermOutboundRead, rbac.Target{}) {
		return
	}
	countryID, err := strconv.Atoi(r.URL.Query().Get("country_id"))
	if err != nil || countryID <= 0 {
		WriteError(w, http.StatusBadRequest, "bad_request", "country_id is required and must be a positive integer")
		return
	}
	servers, err := d.nordVPNClient().servers(r.Context(), countryID)
	if err != nil {
		WriteError(w, http.StatusBadGateway, "provider_error", err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"servers": servers})
}

type nordVPNRegisterRequest struct {
	Label string `json:"label"`
	Token string `json:"token"`
}

func (d Deps) handleRegisterNordVPNAccount(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermOutboundWrite, rbac.Target{}) {
		return
	}
	var req nordVPNRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if strings.TrimSpace(req.Token) == "" {
		WriteError(w, http.StatusBadRequest, "bad_request", "token is required")
		return
	}
	if strings.TrimSpace(req.Label) == "" {
		req.Label = "NordVPN"
	}

	privateKey, err := d.nordVPNClient().credentials(r.Context(), req.Token)
	if err != nil {
		WriteError(w, http.StatusBadGateway, "provider_error", err.Error())
		return
	}

	dto, err := d.storeProxyProviderAccount(r, "nordvpn", req.Label,
		nordVPNCredentials{Token: req.Token, PrivateKey: privateKey}, map[string]any{})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not save NordVPN account")
		return
	}
	WriteJSON(w, http.StatusCreated, dto)
}

type nordVPNOutboundRequest struct {
	NodeID    int64  `json:"node_id"`
	Tag       string `json:"tag"`
	Station   string `json:"station"`
	PublicKey string `json:"public_key"`
	// LocalAddress overrides nordVPNDefaultLocalAddress. Optional: almost
	// no operator needs this, since NordVPN's own client uses the default
	// for every account.
	LocalAddress string `json:"local_address"`
}

// handleProvisionNordVPNOutbound builds a wireguard outbound from the
// account's stored private key and a server the operator picked from
// GET .../nordvpn/servers -- trusted the same way a manually-typed
// wireguard outbound's peer fields already are, since the operator chose
// it from a list this same panel just fetched from NordVPN.
func (d Deps) handleProvisionNordVPNOutbound(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	accountID, err := pathInt64(r, "accountID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid account id")
		return
	}
	var req nordVPNOutboundRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermOutboundWrite, rbac.Target{Kind: rbac.TargetNode, ID: req.NodeID}) {
		return
	}
	if strings.TrimSpace(req.Tag) == "" || strings.TrimSpace(req.Station) == "" || strings.TrimSpace(req.PublicKey) == "" {
		WriteError(w, http.StatusBadRequest, "bad_request", "tag, station and public_key are all required")
		return
	}

	var creds nordVPNCredentials
	_, err = d.loadProxyProviderAccount(r.Context(), accountID, "nordvpn", &creds)
	if errors.Is(err, errProxyProviderAccountNotFound) {
		WriteError(w, http.StatusNotFound, "not_found", "NordVPN account not found")
		return
	}
	if errors.Is(err, errProxyProviderAccountWrongKind) {
		WriteError(w, http.StatusUnprocessableEntity, "invalid", "that account is not a NordVPN account")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read NordVPN account")
		return
	}

	localAddress := strings.TrimSpace(req.LocalAddress)
	if localAddress == "" {
		localAddress = nordVPNDefaultLocalAddress
	}
	params, err := json.Marshal(map[string]any{
		"private_key":     creds.PrivateKey,
		"peer_public_key": req.PublicKey,
		"endpoint":        fmt.Sprintf("%s:%d", req.Station, nordVPNPort),
		"local_addresses": []string{localAddress},
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

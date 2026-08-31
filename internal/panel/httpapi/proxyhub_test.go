package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/shared/secrets"
)

// proxyHubDeps returns setupTestDeps plus a sealing box, since every
// registration handler seals credentials before storing them and
// setupTestDeps leaves Box nil (most tests never touch sealed storage).
func proxyHubDeps(t *testing.T) (Deps, *store.Store, func(method, path, body string) *http.Request) {
	t.Helper()
	deps, s, actor := setupTestDeps(t)
	grantOutboundPerms(actor)
	box, err := secrets.NewBox(bytes.Repeat([]byte{7}, secrets.KeySize))
	if err != nil {
		t.Fatalf("secrets.NewBox: %v", err)
	}
	deps.Box = box

	newReq := func(method, path, body string) *http.Request {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(withActor(req.Context(), actor))
		return req
	}
	return deps, s, newReq
}

func withRouteParams(req *http.Request, params map[string]string) *http.Request {
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// ---- WARP ----

func fakeCloudflare(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/reg", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s on /reg", r.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "device-123",
			"token": "access-token-abc",
			"account": map[string]any{
				"license": "",
			},
		})
	})
	mux.HandleFunc("/reg/device-123", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access-token-abc" {
			t.Errorf("Authorization = %q, want Bearer access-token-abc", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"config": map[string]any{
				"peers": []any{
					map[string]any{
						"public_key": "peerPublicKeyBase64==",
						"endpoint":   map[string]any{"host": "engage.cloudflareclient.com:2408", "v4": "162.159.192.1:2408"},
					},
				},
				"interface": map[string]any{
					"addresses": map[string]any{"v4": "172.16.0.2", "v6": "2606:4700:110::1"},
				},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestHandleRegisterWarpAccount_StoresSealedCredentialsAndNeverReturnsThem(t *testing.T) {
	deps, _, newReq := proxyHubDeps(t)
	deps.WARPAPIBase = fakeCloudflare(t).URL

	req := newReq("POST", "/api/v1/proxy-providers/warp/register", `{"label":"My WARP"}`)
	w := httptest.NewRecorder()
	deps.handleRegisterWarpAccount(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "access-token-abc") || strings.Contains(body, "private") {
		t.Errorf("response leaked credential material: %s", body)
	}
	var dto proxyProviderAccountDTO
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if dto.Provider != "warp" || dto.Label != "My WARP" {
		t.Errorf("dto = %+v", dto)
	}

	// The stored row itself must be sealed, not plaintext JSON.
	var stored string
	if err := deps.Store.Read().QueryRowContext(context.Background(),
		`SELECT credentials FROM proxy_provider_accounts WHERE id = ?`, dto.ID).Scan(&stored); err != nil {
		t.Fatalf("read stored row: %v", err)
	}
	if strings.HasPrefix(stored, "{") {
		t.Error("credentials stored as plaintext JSON, want sealed")
	}
}

func TestHandleListProxyProviderAccounts_NeverReturnsCredentials(t *testing.T) {
	deps, _, newReq := proxyHubDeps(t)
	deps.WARPAPIBase = fakeCloudflare(t).URL

	registerReq := newReq("POST", "/api/v1/proxy-providers/warp/register", `{}`)
	deps.handleRegisterWarpAccount(httptest.NewRecorder(), registerReq)

	listReq := newReq("GET", "/api/v1/proxy-providers", "")
	w := httptest.NewRecorder()
	deps.handleListProxyProviderAccounts(w, listReq)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "access-token-abc") {
		t.Errorf("list response leaked the access token: %s", w.Body.String())
	}
}

func TestHandleProvisionWarpOutbound_CreatesARealOutboundAndBumpsRevision(t *testing.T) {
	deps, s, newReq := proxyHubDeps(t)
	deps.WARPAPIBase = fakeCloudflare(t).URL
	nodeID := int64(801)
	createTestNode(t, s, nodeID, "warp-node", "online")
	setAdapterKinds(t, s, nodeID, `["xray"]`)

	registerReq := newReq("POST", "/api/v1/proxy-providers/warp/register", `{}`)
	registerW := httptest.NewRecorder()
	deps.handleRegisterWarpAccount(registerW, registerReq)
	var account proxyProviderAccountDTO
	_ = json.Unmarshal(registerW.Body.Bytes(), &account)

	var before int64
	_ = deps.Store.Read().QueryRowContext(context.Background(),
		`SELECT desired_revision FROM nodes WHERE id = ?`, nodeID).Scan(&before)

	body, _ := json.Marshal(map[string]any{"node_id": nodeID, "tag": "warp-out"})
	outReq := newReq("POST", "/api/v1/proxy-providers/"+strconv.FormatInt(account.ID, 10)+"/warp/outbound", string(body))
	outReq = withRouteParams(outReq, map[string]string{"accountID": strconv.FormatInt(account.ID, 10)})
	outW := httptest.NewRecorder()
	deps.handleProvisionWarpOutbound(outW, outReq)

	if outW.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", outW.Code, outW.Body.String())
	}

	var after int64
	_ = deps.Store.Read().QueryRowContext(context.Background(),
		`SELECT desired_revision FROM nodes WHERE id = ?`, nodeID).Scan(&after)
	if after <= before {
		t.Errorf("desired_revision = %d, want it to have moved past %d", after, before)
	}

	var kind, sealedParams string
	if err := deps.Store.Read().QueryRowContext(context.Background(),
		`SELECT kind, params FROM outbounds WHERE node_id = ? AND tag = 'warp-out'`, nodeID).
		Scan(&kind, &sealedParams); err != nil {
		t.Fatalf("read created outbound: %v", err)
	}
	if kind != "wireguard" {
		t.Errorf("kind = %q, want wireguard", kind)
	}
	if strings.HasPrefix(sealedParams, "{") {
		t.Error("outbound params stored as plaintext, want sealed like any other outbound")
	}
}

// ---- NordVPN ----

func fakeNordVPN(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/countries", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": 228, "name": "United States", "code": "US"},
		})
	})
	mux.HandleFunc("/v2/servers", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("filters[country_id]"); got != "228" {
			t.Errorf("country_id = %q, want 228", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"servers": []any{
				map[string]any{
					"id": 1, "name": "US #1", "station": "1.2.3.4", "load": 5,
					"technologies": []any{
						map[string]any{"id": 35, "metadata": []any{
							map[string]any{"name": "public_key", "value": "lowLoadKey=="},
						}},
					},
				},
				map[string]any{
					"id": 2, "name": "US #2", "station": "5.6.7.8", "load": 1,
					"technologies": []any{
						map[string]any{"id": 35, "metadata": []any{
							map[string]any{"name": "public_key", "value": "veryLowLoadKey=="},
						}},
					},
				},
				map[string]any{
					// No WireGuard technology entry -- must be excluded, not
					// offered as a choice that would fail at provisioning.
					"id": 3, "name": "US #3 (OpenVPN only)", "station": "9.9.9.9", "load": 1,
					"technologies": []any{map[string]any{"id": 3}},
				},
			},
		})
	})
	mux.HandleFunc("/v1/users/services/credentials", func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "token" || pass != "real-nord-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"nordlynx_private_key": "nordPrivateKey=="})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestHandleListNordVPNServers_SortsByLoadAscendingAndExcludesNonWireGuard(t *testing.T) {
	deps, _, newReq := proxyHubDeps(t)
	deps.NordVPNAPIBase = fakeNordVPN(t).URL

	req := newReq("GET", "/api/v1/proxy-providers/nordvpn/servers?country_id=228", "")
	w := httptest.NewRecorder()
	deps.handleListNordVPNServers(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Servers []nordVPNServer `json:"servers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(resp.Servers) != 2 {
		t.Fatalf("got %d servers, want 2 (the OpenVPN-only one must be excluded): %+v", len(resp.Servers), resp.Servers)
	}
	if resp.Servers[0].Load != 1 || resp.Servers[1].Load != 5 {
		t.Errorf("servers not sorted by load ascending: %+v", resp.Servers)
	}
}

func TestHandleRegisterNordVPNAccount_RequiresAToken(t *testing.T) {
	deps, _, newReq := proxyHubDeps(t)
	deps.NordVPNAPIBase = fakeNordVPN(t).URL

	req := newReq("POST", "/api/v1/proxy-providers/nordvpn/register", `{}`)
	w := httptest.NewRecorder()
	deps.handleRegisterNordVPNAccount(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a missing token", w.Code)
	}
}

func TestHandleProvisionNordVPNOutbound_UsesDefaultLocalAddressAndFixedPort(t *testing.T) {
	deps, s, newReq := proxyHubDeps(t)
	deps.NordVPNAPIBase = fakeNordVPN(t).URL
	nodeID := int64(802)
	createTestNode(t, s, nodeID, "nord-node", "online")
	setAdapterKinds(t, s, nodeID, `["xray"]`)

	regReq := newReq("POST", "/api/v1/proxy-providers/nordvpn/register", `{"token":"real-nord-token"}`)
	regW := httptest.NewRecorder()
	deps.handleRegisterNordVPNAccount(regW, regReq)
	if regW.Code != http.StatusCreated {
		t.Fatalf("register status = %d: %s", regW.Code, regW.Body.String())
	}
	var account proxyProviderAccountDTO
	_ = json.Unmarshal(regW.Body.Bytes(), &account)

	body, _ := json.Marshal(map[string]any{
		"node_id": nodeID, "tag": "nord-out", "station": "5.6.7.8", "public_key": "veryLowLoadKey==",
	})
	outReq := newReq("POST", "/api/v1/proxy-providers/"+strconv.FormatInt(account.ID, 10)+"/nordvpn/outbound", string(body))
	outReq = withRouteParams(outReq, map[string]string{"accountID": strconv.FormatInt(account.ID, 10)})
	outW := httptest.NewRecorder()
	deps.handleProvisionNordVPNOutbound(outW, outReq)

	if outW.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", outW.Code, outW.Body.String())
	}

	var sealedParams string
	if err := deps.Store.Read().QueryRowContext(context.Background(),
		`SELECT params FROM outbounds WHERE node_id = ? AND tag = 'nord-out'`, nodeID).
		Scan(&sealedParams); err != nil {
		t.Fatalf("read created outbound: %v", err)
	}
	// Round-trip through the same unsealer the panel itself uses, rather
	// than asserting on the ciphertext.
	raw, err := nodes.OpenOutboundParams(deps.Box, sealedParams)
	if err != nil {
		t.Fatalf("unseal params: %v", err)
	}
	var params struct {
		PrivateKey     string   `json:"private_key"`
		PeerPublicKey  string   `json:"peer_public_key"`
		Endpoint       string   `json:"endpoint"`
		LocalAddresses []string `json:"local_addresses"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	if params.PrivateKey != "nordPrivateKey==" {
		t.Errorf("private_key = %q, want the key fetched from NordVPN's credentials endpoint", params.PrivateKey)
	}
	if params.PeerPublicKey != "veryLowLoadKey==" {
		t.Errorf("peer_public_key = %q", params.PeerPublicKey)
	}
	if params.Endpoint != "5.6.7.8:51820" {
		t.Errorf("endpoint = %q, want 5.6.7.8:51820 (NordLynx's fixed port)", params.Endpoint)
	}
	if len(params.LocalAddresses) != 1 || params.LocalAddresses[0] != nordVPNDefaultLocalAddress {
		t.Errorf("local_addresses = %+v, want [%q]", params.LocalAddresses, nordVPNDefaultLocalAddress)
	}
}

func TestProvisionOutbound_RefusesWrongProviderAccount(t *testing.T) {
	deps, s, newReq := proxyHubDeps(t)
	deps.WARPAPIBase = fakeCloudflare(t).URL
	deps.NordVPNAPIBase = fakeNordVPN(t).URL
	nodeID := int64(803)
	createTestNode(t, s, nodeID, "mixed-node", "online")
	setAdapterKinds(t, s, nodeID, `["xray"]`)

	// Register a WARP account, then try to provision a NordVPN outbound from it.
	warpReq := newReq("POST", "/api/v1/proxy-providers/warp/register", `{}`)
	warpW := httptest.NewRecorder()
	deps.handleRegisterWarpAccount(warpW, warpReq)
	var warpAccount proxyProviderAccountDTO
	_ = json.Unmarshal(warpW.Body.Bytes(), &warpAccount)

	body, _ := json.Marshal(map[string]any{
		"node_id": nodeID, "tag": "x", "station": "1.1.1.1", "public_key": "k",
	})
	req := newReq("POST", "/api/v1/proxy-providers/"+strconv.FormatInt(warpAccount.ID, 10)+"/nordvpn/outbound", string(body))
	req = withRouteParams(req, map[string]string{"accountID": strconv.FormatInt(warpAccount.ID, 10)})
	w := httptest.NewRecorder()
	deps.handleProvisionNordVPNOutbound(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 for a WARP account id used against the NordVPN endpoint: %s", w.Code, w.Body.String())
	}
}

func TestHandleDeleteProxyProviderAccount_RemovesTheRow(t *testing.T) {
	deps, _, newReq := proxyHubDeps(t)
	deps.WARPAPIBase = fakeCloudflare(t).URL

	regReq := newReq("POST", "/api/v1/proxy-providers/warp/register", `{}`)
	regW := httptest.NewRecorder()
	deps.handleRegisterWarpAccount(regW, regReq)
	var account proxyProviderAccountDTO
	_ = json.Unmarshal(regW.Body.Bytes(), &account)

	delReq := newReq("DELETE", "/api/v1/proxy-providers/"+strconv.FormatInt(account.ID, 10), "")
	delReq = withRouteParams(delReq, map[string]string{"accountID": strconv.FormatInt(account.ID, 10)})
	delW := httptest.NewRecorder()
	deps.handleDeleteProxyProviderAccount(delW, delReq)
	if delW.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d: %s", delW.Code, delW.Body.String())
	}

	var count int
	_ = deps.Store.Read().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM proxy_provider_accounts WHERE id = ?`, account.ID).Scan(&count)
	if count != 0 {
		t.Error("account row still present after delete")
	}
}

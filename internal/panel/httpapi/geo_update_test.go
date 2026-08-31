package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/amyrm/antimage/internal/panel/control"
	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
)

// seedAdapterRow inserts the row Hello would have created, so
// RecordGeoUpdate has something to UPDATE. Real code never calls
// RecordGeoUpdate for an adapter kind that hasn't reported in via Hello.
func seedAdapterRow(t *testing.T, s interface {
	Write(ctx context.Context, fn func(tx *sql.Tx) error) error
}, nodeID int64, kind string) {
	t.Helper()
	if err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO adapter_registry (node_id, kind, version, capabilities, reported_at)
			 VALUES (?, ?, '1.0', '[]', ?)`, nodeID, kind, time.Now().Unix())
		return err
	}); err != nil {
		t.Fatalf("seed adapter_registry: %v", err)
	}
}

// TestHandleUpdateNodeGeoData_DeliversAndRecordsSuccess proves the whole
// chain: HTTP request -> Hub -> simulated agent reply -> per-adapter
// outcome in the response AND the checksums+timestamp actually written to
// adapter_registry, which is what the browser's "last updated" reads.
func TestHandleUpdateNodeGeoData_DeliversAndRecordsSuccess(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	deps.Hub = control.NewHub()
	deps.Now = func() time.Time { return time.Unix(1700000000, 0) }
	nodeID := int64(201)
	createTestNode(t, s, nodeID, "geo-node", "online")
	seedAdapterRow(t, s, nodeID, "xray")

	_, cmds, release := deps.Hub.Register(nodeID)
	defer release()

	var sentURLs pb.UpdateGeoData
	go func() {
		cmd := <-cmds
		sentURLs = *cmd.GetUpdateGeoData()
		deps.Hub.DeliverResult(&pb.AgentCommandResult{
			CommandId: cmd.CommandId,
			Body: &pb.AgentCommandResult_UpdateGeoData{
				UpdateGeoData: &pb.UpdateGeoDataResult{
					Outcomes: []*pb.AdapterGeoUpdateOutcome{
						{Kind: "xray", Ok: true, GeoipSha256: "aaa111", GeositeSha256: "bbb222"},
					},
				},
			},
		})
	}()

	req := httptest.NewRequest("POST", "/api/v1/nodes/201/geo-update", nil)
	req = req.WithContext(withActor(req.Context(), actor))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "201")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleUpdateNodeGeoData(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	// The handler must have supplied the default source when the request
	// body named none.
	if !strings.Contains(sentURLs.GeoipUrl, "v2ray-rules-dat") {
		t.Errorf("geoip_url = %q, expected the default source", sentURLs.GeoipUrl)
	}
	if sentURLs.GeoipSha256Url == "" || sentURLs.GeositeUrl == "" || sentURLs.GeositeSha256Url == "" {
		t.Errorf("not all four URLs were populated: geoip=%q geoipSum=%q geosite=%q geositeSum=%q",
			sentURLs.GeoipUrl, sentURLs.GeoipSha256Url, sentURLs.GeositeUrl, sentURLs.GeositeSha256Url)
	}

	var response struct {
		Delivered bool `json:"delivered"`
		Outcomes  []struct {
			Kind          string `json:"kind"`
			OK            bool   `json:"ok"`
			GeoIPSHA256   string `json:"geoip_sha256"`
			GeoSiteSHA256 string `json:"geosite_sha256"`
		} `json:"outcomes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if !response.Delivered {
		t.Fatal("delivered = false, want true")
	}
	if len(response.Outcomes) != 1 || !response.Outcomes[0].OK {
		t.Fatalf("outcomes = %+v, want one OK xray outcome", response.Outcomes)
	}
	if response.Outcomes[0].GeoIPSHA256 != "aaa111" {
		t.Errorf("geoip checksum in response = %q, want aaa111", response.Outcomes[0].GeoIPSHA256)
	}

	// The database row is what the browser actually reads back later --
	// proving the response looked right is not enough on its own.
	var geoUpdatedAt sql.NullInt64
	var geoip, geosite string
	if err := s.Read().QueryRowContext(context.Background(),
		`SELECT geo_updated_at, geo_geoip_sha256, geo_geosite_sha256
		   FROM adapter_registry WHERE node_id = ? AND kind = 'xray'`, nodeID,
	).Scan(&geoUpdatedAt, &geoip, &geosite); err != nil {
		t.Fatalf("read adapter_registry: %v", err)
	}
	if !geoUpdatedAt.Valid || geoUpdatedAt.Int64 != 1700000000 {
		t.Errorf("geo_updated_at = %+v, want 1700000000", geoUpdatedAt)
	}
	if geoip != "aaa111" || geosite != "bbb222" {
		t.Errorf("stored checksums = %q/%q, want aaa111/bbb222", geoip, geosite)
	}
}

// TestHandleUpdateNodeGeoData_CustomSourceOverridesDefault proves an
// operator behind a mirror or fork can actually use one -- the request
// body's URLs must reach the agent unchanged, not just validate and get
// discarded in favor of the default.
func TestHandleUpdateNodeGeoData_CustomSourceOverridesDefault(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	deps.Hub = control.NewHub()
	nodeID := int64(202)
	createTestNode(t, s, nodeID, "geo-node-2", "online")

	_, cmds, release := deps.Hub.Register(nodeID)
	defer release()

	var sentURLs pb.UpdateGeoData
	done := make(chan struct{})
	go func() {
		defer close(done)
		cmd := <-cmds
		sentURLs = *cmd.GetUpdateGeoData()
		deps.Hub.DeliverResult(&pb.AgentCommandResult{
			CommandId: cmd.CommandId,
			Body: &pb.AgentCommandResult_UpdateGeoData{
				UpdateGeoData: &pb.UpdateGeoDataResult{},
			},
		})
	}()

	body := `{"geoip_url":"https://mirror.example/geoip.dat","geoip_sha256_url":"https://mirror.example/geoip.dat.sha256sum","geosite_url":"https://mirror.example/geosite.dat","geosite_sha256_url":"https://mirror.example/geosite.dat.sha256sum"}`
	req := httptest.NewRequest("POST", "/api/v1/nodes/202/geo-update", strings.NewReader(body))
	req = req.WithContext(withActor(req.Context(), actor))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "202")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleUpdateNodeGeoData(w, req)
	<-done

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if sentURLs.GeoipUrl != "https://mirror.example/geoip.dat" {
		t.Errorf("geoip_url = %q, want the custom mirror", sentURLs.GeoipUrl)
	}
}

func TestHandleUpdateNodeGeoData_OfflineNodeReportsNotDelivered(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	deps.Hub = control.NewHub() // node never registered
	nodeID := int64(203)
	createTestNode(t, s, nodeID, "offline-node", "offline")

	req := httptest.NewRequest("POST", "/api/v1/nodes/203/geo-update", nil)
	req = req.WithContext(withActor(req.Context(), actor))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "203")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleUpdateNodeGeoData(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Delivered bool `json:"delivered"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if response.Delivered {
		t.Error("delivered = true for a node that was never connected")
	}
}

func TestHandleUpdateNodeGeoData_NotFound(t *testing.T) {
	deps, _, actor := setupTestDeps(t)

	req := httptest.NewRequest("POST", "/api/v1/nodes/999/geo-update", nil)
	req = req.WithContext(withActor(req.Context(), actor))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "999")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleUpdateNodeGeoData(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

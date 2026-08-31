package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeGitHub serves a releases list at /releases and per-asset .dgst files,
// standing in for api.github.com and github.com's release-asset host --
// real code fetches two different hosts, but nothing in
// fetchXrayCoreVersions cares that they're the same server in a test.
// Because httptest.NewServer starts listening synchronously, srv.URL is
// already known before the release fixture below is built, so asset URLs
// can point at it directly with no placeholder-rewriting step needed.
func fakeGitHub(t *testing.T, dgstByAsset map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for name, content := range dgstByAsset {
		content := content
		mux.HandleFunc("/dgst/"+name, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(content))
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	releases := []ghRelease{
		{
			TagName: "v1.9.0",
			Assets: []ghAsset{
				{Name: "Xray-linux-64.zip", BrowserDownloadURL: srv.URL + "/Xray-linux-64.zip"},
				{Name: "Xray-linux-64.zip.dgst", BrowserDownloadURL: srv.URL + "/dgst/Xray-linux-64.zip.dgst"},
			},
		},
	}
	mux.HandleFunc("/releases", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("request to GitHub API sent with no User-Agent; GitHub refuses those in production")
		}
		_ = json.NewEncoder(w).Encode(releases)
	})
	return srv
}

func TestFetchXrayCoreVersions_ParsesReleasesAndDGST(t *testing.T) {
	wantSum := fmt.Sprintf("%064s", "abc123")
	srv := fakeGitHub(t, map[string]string{
		"Xray-linux-64.zip.dgst": "MD5(Xray-linux-64.zip)= deadbeef\n" +
			"SHA256(Xray-linux-64.zip)= " + wantSum + "\n" +
			"SHA512(Xray-linux-64.zip)= ffffffff\n",
	})

	versions, err := fetchXrayCoreVersions(t.Context(), srv.URL+"/releases")
	if err != nil {
		t.Fatalf("fetchXrayCoreVersions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("got %d versions, want 1: %+v", len(versions), versions)
	}
	v := versions[0]
	if v.Version != "1.9.0" {
		t.Errorf("Version = %q, want 1.9.0 (v-prefix stripped)", v.Version)
	}
	if v.BinarySHA256 != wantSum {
		t.Errorf("BinarySHA256 = %q, want %q -- must pick the SHA256 line specifically, not the first line in the file", v.BinarySHA256, wantSum)
	}
	if v.BinaryURL != srv.URL+"/Xray-linux-64.zip" {
		t.Errorf("BinaryURL = %q, want the release asset's own URL", v.BinaryURL)
	}
}

func TestFetchXrayCoreVersions_SkipsReleaseMissingTheAsset(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/releases", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]ghRelease{
			{TagName: "v1.9.0", Assets: []ghAsset{{Name: "Xray-linux-arm64-v8a.zip", BrowserDownloadURL: "x"}}},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	versions, err := fetchXrayCoreVersions(t.Context(), srv.URL+"/releases")
	if err != nil {
		t.Fatalf("fetchXrayCoreVersions: %v", err)
	}
	if len(versions) != 0 {
		t.Errorf("got %d versions, want 0 -- the release has no verifiable linux-64 asset", len(versions))
	}
}

func TestHandleListXrayCoreVersions_UsesCacheWithinTTL(t *testing.T) {
	deps, _, actor := setupTestDeps(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode([]ghRelease{})
	}))
	defer srv.Close()

	now := time.Unix(1_700_000_000, 0)
	deps.Now = func() time.Time { return now }
	deps.CoreVersions = &xrayCoreVersionCache{releasesURL: srv.URL}

	req1 := httptest.NewRequest("GET", "/api/v1/xray-core-versions", nil)
	req1 = req1.WithContext(withActor(req1.Context(), actor))
	w1 := httptest.NewRecorder()
	deps.handleListXrayCoreVersions(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("first call status = %d", w1.Code)
	}

	// Same instant, second request: must NOT hit the upstream again.
	req2 := httptest.NewRequest("GET", "/api/v1/xray-core-versions", nil)
	req2 = req2.WithContext(withActor(req2.Context(), actor))
	w2 := httptest.NewRecorder()
	deps.handleListXrayCoreVersions(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("second call status = %d", w2.Code)
	}

	if calls != 1 {
		t.Errorf("upstream called %d times for two requests inside the TTL, want 1", calls)
	}
}

func TestHandleListXrayCoreVersions_RefreshesAfterTTLExpires(t *testing.T) {
	deps, _, actor := setupTestDeps(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode([]ghRelease{})
	}))
	defer srv.Close()

	current := time.Unix(1_700_000_000, 0)
	deps.Now = func() time.Time { return current }
	deps.CoreVersions = &xrayCoreVersionCache{releasesURL: srv.URL}

	req := func() *http.Request {
		r := httptest.NewRequest("GET", "/api/v1/xray-core-versions", nil)
		return r.WithContext(withActor(r.Context(), actor))
	}
	deps.handleListXrayCoreVersions(httptest.NewRecorder(), req())

	current = current.Add(coreVersionCacheTTL + time.Second)
	deps.handleListXrayCoreVersions(httptest.NewRecorder(), req())

	if calls != 2 {
		t.Errorf("upstream called %d times across the TTL boundary, want 2", calls)
	}
}

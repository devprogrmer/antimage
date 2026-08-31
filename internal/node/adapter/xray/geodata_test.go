package xray

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// geoServer serves a fake geoip.dat/geosite.dat pair plus their checksum
// files, so the test proves the adapter actually verifies what it downloads
// rather than trusting the transport.
type geoServer struct {
	geoip, geosite   []byte
	geoipSum         string
	geositeSum       string
	corruptGeoSite   bool // serves a body that does NOT match geositeSum
	geositeTooSmall  bool
	failGeositeFetch bool
}

func newGeoServer() *geoServer {
	geoip := make([]byte, 8192)
	for i := range geoip {
		geoip[i] = byte(i)
	}
	geosite := make([]byte, 8192)
	for i := range geosite {
		geosite[i] = byte(255 - i)
	}
	sumOf := func(b []byte) string {
		s := sha256.Sum256(b)
		return hex.EncodeToString(s[:])
	}
	return &geoServer{
		geoip: geoip, geosite: geosite,
		geoipSum: sumOf(geoip), geositeSum: sumOf(geosite),
	}
}

func (g *geoServer) start(t *testing.T) (srv *httptest.Server, geoipURL, geoipSumURL, geositeURL, geositeSumURL string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/geoip.dat", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(g.geoip)
	})
	mux.HandleFunc("/geoip.dat.sha256sum", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  geoip.dat\n", g.geoipSum)
	})
	mux.HandleFunc("/geosite.dat", func(w http.ResponseWriter, r *http.Request) {
		if g.failGeositeFetch {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if g.geositeTooSmall {
			_, _ = w.Write([]byte("nope"))
			return
		}
		if g.corruptGeoSite {
			_, _ = w.Write([]byte("this is not the file the checksum below describes"))
			return
		}
		_, _ = w.Write(g.geosite)
	})
	mux.HandleFunc("/geosite.dat.sha256sum", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  geosite.dat\n", g.geositeSum)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base := srv.URL
	return srv, base + "/geoip.dat", base + "/geoip.dat.sha256sum",
		base + "/geosite.dat", base + "/geosite.dat.sha256sum"
}

// TestUpdateGeoData_InstallsVerifiedFilesAndRestarts is the whole feature:
// both files are fetched, both checksums match, both land in assetDir, and
// the adapter restarts through the same Restart() path an operator's own
// click uses -- because Xray only reads geo data at process start.
func TestUpdateGeoData_InstallsVerifiedFilesAndRestarts(t *testing.T) {
	dir := t.TempDir()
	assetDir := t.TempDir()
	rt := newFakeRuntime()
	a := NewWithAssetDir(dir, rt, false, assetDir)

	g := newGeoServer()
	_, geoipURL, geoipSumURL, geositeURL, geositeSumURL := g.start(t)

	result, err := a.UpdateGeoData(context.Background(), geoipURL, geoipSumURL, geositeURL, geositeSumURL)
	if err != nil {
		t.Fatalf("UpdateGeoData: %v", err)
	}
	if result.GeoIPSHA256 != g.geoipSum {
		t.Errorf("GeoIPSHA256 = %s, want %s", result.GeoIPSHA256, g.geoipSum)
	}
	if result.GeoSiteSHA256 != g.geositeSum {
		t.Errorf("GeoSiteSHA256 = %s, want %s", result.GeoSiteSHA256, g.geositeSum)
	}

	installed, err := os.ReadFile(filepath.Join(assetDir, "geoip.dat"))
	if err != nil {
		t.Fatalf("read installed geoip.dat: %v", err)
	}
	if string(installed) != string(g.geoip) {
		t.Errorf("installed geoip.dat does not match what the server served")
	}
	installedSite, err := os.ReadFile(filepath.Join(assetDir, "geosite.dat"))
	if err != nil {
		t.Fatalf("read installed geosite.dat: %v", err)
	}
	if string(installedSite) != string(g.geosite) {
		t.Errorf("installed geosite.dat does not match what the server served")
	}

	restarts, _, _, _ := rt.counts()
	if restarts != 1 {
		t.Errorf("restarts = %d, want 1 -- Xray only reads geo data at startup", restarts)
	}

	// No leftover temp files: both renames succeeded, and the defers guard
	// only the failure paths.
	entries, _ := os.ReadDir(assetDir)
	if len(entries) != 2 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("assetDir has %d entries, want exactly geoip.dat and geosite.dat: %v", len(entries), names)
	}
}

// TestUpdateGeoData_ChecksumMismatchLeavesExistingFilesAlone is the
// safety property the whole design exists for: a corrupt or tampered
// download must not silently replace working geo data, and must not
// restart Xray onto files that were never verified.
func TestUpdateGeoData_ChecksumMismatchLeavesExistingFilesAlone(t *testing.T) {
	dir := t.TempDir()
	assetDir := t.TempDir()
	rt := newFakeRuntime()
	a := NewWithAssetDir(dir, rt, false, assetDir)

	// Seed an existing, known-good geoip.dat as if a prior update had
	// already installed it.
	priorGeoip := []byte("previously-installed-geoip-data-untouched")
	if err := os.WriteFile(filepath.Join(assetDir, "geoip.dat"), priorGeoip, 0o644); err != nil {
		t.Fatalf("seed prior geoip.dat: %v", err)
	}

	g := newGeoServer()
	g.corruptGeoSite = true
	_, geoipURL, geoipSumURL, geositeURL, geositeSumURL := g.start(t)

	_, err := a.UpdateGeoData(context.Background(), geoipURL, geoipSumURL, geositeURL, geositeSumURL)
	if err == nil {
		t.Fatal("expected a checksum mismatch error, got nil")
	}

	// geoip.dat verified fine on its own, but geosite.dat's mismatch must
	// stop the whole update -- an inconsistent pair is worse than no
	// update at all.
	got, err := os.ReadFile(filepath.Join(assetDir, "geoip.dat"))
	if err != nil {
		t.Fatalf("read geoip.dat: %v", err)
	}
	if string(got) != string(priorGeoip) {
		t.Error("geoip.dat was overwritten despite geosite.dat failing verification")
	}
	if _, err := os.Stat(filepath.Join(assetDir, "geosite.dat")); !os.IsNotExist(err) {
		t.Error("geosite.dat should not exist -- it never verified")
	}

	restarts, _, _, _ := rt.counts()
	if restarts != 0 {
		t.Errorf("restarts = %d, want 0 -- must not restart onto unverified data", restarts)
	}

	// No temp files left behind after the failure.
	entries, _ := os.ReadDir(assetDir)
	if len(entries) != 1 {
		t.Errorf("assetDir has %d entries after a failed update, want exactly the untouched geoip.dat", len(entries))
	}
}

func TestUpdateGeoData_RejectsImplausiblySmallDownload(t *testing.T) {
	dir := t.TempDir()
	assetDir := t.TempDir()
	a := NewWithAssetDir(dir, newFakeRuntime(), false, assetDir)

	g := newGeoServer()
	g.geositeTooSmall = true
	_, geoipURL, geoipSumURL, geositeURL, geositeSumURL := g.start(t)

	_, err := a.UpdateGeoData(context.Background(), geoipURL, geoipSumURL, geositeURL, geositeSumURL)
	if err == nil {
		t.Fatal("expected an error for an implausibly small download")
	}
}

func TestUpdateGeoData_FetchFailureIsReported(t *testing.T) {
	dir := t.TempDir()
	assetDir := t.TempDir()
	a := NewWithAssetDir(dir, newFakeRuntime(), false, assetDir)

	g := newGeoServer()
	g.failGeositeFetch = true
	_, geoipURL, geoipSumURL, geositeURL, geositeSumURL := g.start(t)

	_, err := a.UpdateGeoData(context.Background(), geoipURL, geoipSumURL, geositeURL, geositeSumURL)
	if err == nil {
		t.Fatal("expected an error when the geosite download itself fails")
	}
}

func TestUpdateGeoData_RequiresBothURLs(t *testing.T) {
	dir := t.TempDir()
	assetDir := t.TempDir()
	a := NewWithAssetDir(dir, newFakeRuntime(), false, assetDir)

	if _, err := a.UpdateGeoData(context.Background(), "", "x", "https://example.com/geosite.dat", "x"); err == nil {
		t.Error("expected an error for a missing geoip URL")
	}
	if _, err := a.UpdateGeoData(context.Background(), "https://example.com/geoip.dat", "x", "", "x"); err == nil {
		t.Error("expected an error for a missing geosite URL")
	}
}

// TestUpdateGeoData_RestartFailureIsReportedWithChecksums proves that a
// restart failure after both files verified still reports what was
// actually installed -- the operator needs to know the files ARE new even
// though Xray has not picked them up yet, rather than being told nothing
// happened at all.
func TestUpdateGeoData_RestartFailureIsReportedWithChecksums(t *testing.T) {
	dir := t.TempDir()
	assetDir := t.TempDir()
	rt := newFakeRuntime()
	rt.failRst = fmt.Errorf("systemctl restart xray: unit failed")
	a := NewWithAssetDir(dir, rt, false, assetDir)

	g := newGeoServer()
	_, geoipURL, geoipSumURL, geositeURL, geositeSumURL := g.start(t)

	result, err := a.UpdateGeoData(context.Background(), geoipURL, geoipSumURL, geositeURL, geositeSumURL)
	if err == nil {
		t.Fatal("expected the restart failure to surface as an error")
	}
	if result.GeoIPSHA256 != g.geoipSum || result.GeoSiteSHA256 != g.geositeSum {
		t.Errorf("checksums should still be reported when restart fails: %+v", result)
	}
	if _, statErr := os.Stat(filepath.Join(assetDir, "geoip.dat")); statErr != nil {
		t.Error("geoip.dat should be installed even though the restart afterward failed")
	}
}

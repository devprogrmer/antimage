package xray

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// geoFetchTimeout bounds one download. A geoip.dat is a few MB; over a slow
// or stalled connection this must give up rather than let a "restart this
// node's geo data" command hang the agent's stream loop, which handles
// commands inline and would otherwise stall heartbeats and reconciliation
// behind it too.
const geoFetchTimeout = 2 * time.Minute

// geoMinPlausibleSize guards against a fetch that "succeeded" by saving an
// HTML error page (a redirect to a login wall, a 404 page served as 200 by a
// misconfigured mirror) as if it were the binary database. A real
// geoip.dat/geosite.dat is multiple megabytes; nothing legitimate is this
// small.
const geoMinPlausibleSize = 4096

var _ adapter.GeoDataUpdater = (*Adapter)(nil)

// UpdateGeoData implements adapter.GeoDataUpdater.
//
// Both files are fetched and verified BEFORE either replaces the adapter's
// existing copy. A checksum mismatch on geosite.dat after geoip.dat had
// already been swapped in would leave the pair inconsistent -- and inside a
// download that failed partway, "inconsistent" is exactly the state an
// operator clicking one button must never be left in.
func (a *Adapter) UpdateGeoData(
	ctx context.Context, geoipURL, geoipSHA256URL, geositeURL, geositeSHA256URL string,
) (adapter.GeoDataResult, error) {
	if strings.TrimSpace(geoipURL) == "" || strings.TrimSpace(geositeURL) == "" {
		return adapter.GeoDataResult{}, fmt.Errorf("geoip and geosite URLs are both required")
	}
	if err := os.MkdirAll(a.assetDir, 0o755); err != nil {
		return adapter.GeoDataResult{}, fmt.Errorf("create asset dir: %w", err)
	}

	geoipTmp, geoipSum, err := fetchAndVerify(ctx, a.assetDir, "geoip", geoipURL, geoipSHA256URL)
	if err != nil {
		return adapter.GeoDataResult{}, fmt.Errorf("geoip.dat: %w", err)
	}
	defer func() { _ = os.Remove(geoipTmp) }() // no-op once renamed

	geositeTmp, geositeSum, err := fetchAndVerify(ctx, a.assetDir, "geosite", geositeURL, geositeSHA256URL)
	if err != nil {
		return adapter.GeoDataResult{}, fmt.Errorf("geosite.dat: %w", err)
	}
	defer func() { _ = os.Remove(geositeTmp) }() // no-op once renamed

	// Both verified. Now install both -- still two renames, not one atomic
	// operation across two files (the filesystem offers no such thing), but
	// each rename individually cannot produce a HALF-WRITTEN file, only a
	// point where one file is new and the other is not yet. Xray is not
	// restarted until after both renames succeed, so it never observes that
	// intermediate state.
	if err := os.Rename(geoipTmp, filepath.Join(a.assetDir, "geoip.dat")); err != nil {
		return adapter.GeoDataResult{}, fmt.Errorf("install geoip.dat: %w", err)
	}
	if err := os.Rename(geositeTmp, filepath.Join(a.assetDir, "geosite.dat")); err != nil {
		return adapter.GeoDataResult{}, fmt.Errorf("install geosite.dat: %w", err)
	}

	// Xray loads geo data once at process start; nothing short of a restart
	// makes it see the new files. Routed through Adapter.Restart rather than
	// a→rt.Restart directly, so this takes the exact same path (and any
	// future logic added to it) that an operator's own "restart" click does.
	if err := a.Restart(ctx); err != nil {
		return adapter.GeoDataResult{GeoIPSHA256: geoipSum, GeoSiteSHA256: geositeSum},
			fmt.Errorf("files installed but restart failed, still running old data: %w", err)
	}

	return adapter.GeoDataResult{GeoIPSHA256: geoipSum, GeoSiteSHA256: geositeSum}, nil
}

// fetchAndVerify downloads url into a temp file inside dir (so the later
// rename is same-filesystem and therefore atomic), hashes it, and compares
// against the hex digest published at sha256URL. It returns the temp file's
// path unrenamed -- the caller decides when it's safe to install, which is
// only after BOTH files in a pair have verified.
func fetchAndVerify(ctx context.Context, dir, label, url, sha256URL string) (tmpPath, sum string, err error) {
	ctx, cancel := context.WithTimeout(ctx, geoFetchTimeout)
	defer cancel()

	wantSum, err := fetchSHA256(ctx, sha256URL)
	if err != nil {
		return "", "", fmt.Errorf("fetch checksum: %w", err)
	}

	tmp, err := os.CreateTemp(dir, "antimage-"+label+"-*.tmp")
	if err != nil {
		return "", "", fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	// If verification fails below, the caller never renames this file and
	// its own defer removes it; this defer only guards the early-return
	// paths inside this function itself.
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpName)
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		_ = tmp.Close()
		return "", "", fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		_ = tmp.Close()
		return "", "", fmt.Errorf("download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_ = tmp.Close()
		return "", "", fmt.Errorf("download: unexpected status %s", resp.Status)
	}

	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hasher), resp.Body)
	if err != nil {
		_ = tmp.Close()
		return "", "", fmt.Errorf("download: %w", err)
	}
	if written < geoMinPlausibleSize {
		_ = tmp.Close()
		return "", "", fmt.Errorf("downloaded file is only %d bytes, too small to be real geo data", written)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", "", fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", "", fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return "", "", fmt.Errorf("chmod temp file: %w", err)
	}

	gotSum := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(gotSum, wantSum) {
		return "", "", fmt.Errorf("checksum mismatch: got %s, want %s (published source may be mid-update; try again)",
			gotSum, wantSum)
	}

	success = true
	return tmpName, gotSum, nil
}

// fetchSHA256 reads a sha256sum-format file (`<hex>  <filename>` or just
// `<hex>` on its own line, both of which real-world mirrors use
// inconsistently) and returns the hex digest.
//
// Fetched and checked BEFORE the data file downloads, not after: a source
// that cannot serve its own checksum file is not trustworthy enough to
// download several megabytes from in the first place, and failing fast here
// avoids that wasted transfer.
func fetchSHA256(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %s", resp.Status)
	}
	// A checksum file is a handful of bytes; capping the read defends
	// against a misconfigured URL that serves something else entirely (the
	// geoip.dat itself, say) under this address.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	fields := strings.Fields(string(body))
	if len(fields) == 0 {
		return "", fmt.Errorf("empty response")
	}
	sum := strings.ToLower(strings.TrimSpace(fields[0]))
	if len(sum) != 64 {
		return "", fmt.Errorf("does not look like a sha256 hex digest: %q", sum)
	}
	for _, c := range sum {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", fmt.Errorf("does not look like a sha256 hex digest: %q", sum)
		}
	}
	return sum, nil
}

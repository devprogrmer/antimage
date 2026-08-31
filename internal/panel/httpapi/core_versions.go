package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/amyrm/antimage/internal/panel/rbac"
)

// This is the convenience half of Core Version management: a browsable
// list of real, checksummed Xray releases, so an operator triggering
// POST /nodes/{id}/core-upgrade is picking from what actually exists
// rather than hand-typing a release tag into a URL template and hoping.
//
// Unlike Geo Update, there is no default binary_url/binary_sha256 baked
// into the upgrade handler itself -- a data file that turns out wrong
// degrades routing accuracy; an executable that turns out wrong can take a
// node offline, and that is not a choice this system makes silently on an
// operator's behalf. This endpoint exists so the operator does not have to
// make that choice blind, not so the upgrade endpoint can make it for them.

const (
	xrayReleasesAPIURL = "https://api.github.com/repos/XTLS/Xray-core/releases?per_page=10"
	// coreVersionCacheTTL respects GitHub's unauthenticated rate limit
	// (60 requests/hour/IP): a panel restarted repeatedly, or several
	// operators opening the upgrade dialog back to back, must not burn
	// through that budget on identical data.
	coreVersionCacheTTL     = 15 * time.Minute
	coreVersionFetchTimeout = 20 * time.Second
)

type xrayCoreVersion struct {
	Version      string `json:"version"`
	BinaryURL    string `json:"binary_url"`
	BinarySHA256 string `json:"binary_sha256"`
}

// xrayCoreVersionCache holds the last successful fetch. A pointer field on
// Deps (which is itself passed by value to every handler) so all requests
// share one cache rather than each handler call getting its own,
// unpopulated copy.
type xrayCoreVersionCache struct {
	mu          sync.Mutex
	versions    []xrayCoreVersion
	fetchedAt   time.Time
	releasesURL string // overridable in tests; empty means xrayReleasesAPIURL
}

func NewXrayCoreVersionCache() *xrayCoreVersionCache {
	return &xrayCoreVersionCache{}
}

// handleListXrayCoreVersions implements GET /api/v1/xray-core-versions.
//
// Not node-scoped: this codebase's agent only supports Linux (see
// adapterfactory and the xray/adapter_test.go comment on the same point),
// so the same linux-64 asset list applies fleet-wide. A per-node route
// would imply the answer could differ by node, which it cannot.
func (d Deps) handleListXrayCoreVersions(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermNodeRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}

	if d.CoreVersions == nil {
		// Degraded, not broken: an older Deps literal (or a test that does
		// not care about this feature) that never set the cache still gets
		// a real, uncached fetch rather than a nil-pointer panic.
		versions, err := fetchXrayCoreVersions(r.Context(), xrayReleasesAPIURL)
		if err != nil {
			WriteError(w, http.StatusBadGateway, "upstream_unavailable", err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"versions": versions})
		return
	}

	versions, err := d.CoreVersions.get(r.Context(), d.now())
	if err != nil {
		WriteError(w, http.StatusBadGateway, "upstream_unavailable", err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"versions": versions})
}

func (c *xrayCoreVersionCache) get(ctx context.Context, now time.Time) ([]xrayCoreVersion, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.fetchedAt.IsZero() && now.Sub(c.fetchedAt) < coreVersionCacheTTL {
		return c.versions, nil
	}

	url := c.releasesURL
	if url == "" {
		url = xrayReleasesAPIURL
	}
	versions, err := fetchXrayCoreVersions(ctx, url)
	if err != nil {
		// A stale cache is more useful than no answer at all: GitHub being
		// briefly unreachable must not blank out a list an operator was
		// just looking at, as long as SOMETHING was ever fetched.
		if len(c.versions) > 0 {
			return c.versions, nil
		}
		return nil, err
	}
	c.versions = versions
	c.fetchedAt = now
	return versions, nil
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}
type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

// sha256LineRE matches the SHA256 line inside a release's per-asset .dgst
// file, of the form `SHA256(Xray-linux-64.zip)= <hex>`. Xray-core has
// published this exact format for every release for years; matched by
// regex rather than assumed to be the first line of the file, because the
// file also carries MD5/SHA1/SHA512 lines in an order this code should not
// depend on.
var sha256LineRE = regexp.MustCompile(`(?i)SHA256\([^)]*\)\s*=\s*([0-9a-fA-F]{64})`)

// fetchXrayCoreVersions queries the GitHub releases API, and for each
// release that publishes a linux-64 asset, fetches that asset's .dgst
// companion to extract its published SHA256 -- the panel resolves the
// checksum here, once, so the agent's own verification code never has to
// parse a manifest format that could change out from under many nodes at
// once.
func fetchXrayCoreVersions(ctx context.Context, releasesURL string) ([]xrayCoreVersion, error) {
	releases, err := fetchGitHubReleases(ctx, releasesURL)
	if err != nil {
		return nil, err
	}

	out := make([]xrayCoreVersion, 0, len(releases))
	for _, rel := range releases {
		var binaryURL, dgstURL string
		for _, a := range rel.Assets {
			switch a.Name {
			case "Xray-linux-64.zip":
				binaryURL = a.BrowserDownloadURL
			case "Xray-linux-64.zip.dgst":
				dgstURL = a.BrowserDownloadURL
			}
		}
		if binaryURL == "" || dgstURL == "" {
			// A release that does not publish this asset (a source-only
			// release, a draft) is skipped rather than listed with a
			// blank/guessed checksum -- an operator must never be offered
			// a version this code cannot actually verify.
			continue
		}
		sha256hex, err := fetchDGSTSha256(ctx, dgstURL)
		if err != nil {
			continue
		}
		out = append(out, xrayCoreVersion{
			Version: strings.TrimPrefix(rel.TagName, "v"), BinaryURL: binaryURL, BinarySHA256: sha256hex,
		})
	}
	return out, nil
}

func fetchGitHubReleases(ctx context.Context, url string) ([]ghRelease, error) {
	body, err := httpGetWithTimeout(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("fetch releases: %w", err)
	}
	var releases []ghRelease
	if err := json.Unmarshal(body, &releases); err != nil {
		return nil, fmt.Errorf("parse releases: %w", err)
	}
	return releases, nil
}

func fetchDGSTSha256(ctx context.Context, url string) (string, error) {
	body, err := httpGetWithTimeout(ctx, url)
	if err != nil {
		return "", err
	}
	m := sha256LineRE.FindSubmatch(body)
	if m == nil {
		return "", fmt.Errorf("no SHA256 line found in %s", url)
	}
	return strings.ToLower(string(m[1])), nil
}

func httpGetWithTimeout(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, coreVersionFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// GitHub's API refuses requests with no User-Agent at all.
	req.Header.Set("User-Agent", "antimage-panel")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s from %s", resp.Status, url)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

package xray

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// coreDownloadTimeout bounds the archive fetch. Xray release archives are a
// few MB, larger than geo data but not by much.
const coreDownloadTimeout = 3 * time.Minute

// corePreflightTimeout bounds running the freshly downloaded, not-yet-installed
// binary with -version. A binary that hangs here is not one this code should
// wait on indefinitely before deciding it is broken.
const corePreflightTimeout = 15 * time.Second

// coreHealthPollWindow and coreHealthPollInterval bound how long UpgradeCore
// waits after a restart before deciding the new binary did or did not come
// up. systemd's own restart is near-instant, but Xray's own startup
// (parsing config, binding listeners) is not, and checking Healthy() exactly
// once immediately after Restart returns would catch it mid-startup and
// report a false failure.
const (
	coreHealthPollWindow   = 20 * time.Second
	coreHealthPollInterval = 2 * time.Second
)

var _ adapter.CoreVersionManager = (*Adapter)(nil)

// UpgradeCore implements adapter.CoreVersionManager.
//
// Sequence, and why each step is ordered where it is:
//  1. Download + verify checksum. Nothing on disk changes yet.
//  2. Extract the xray binary from the archive into a temp file NEXT TO the
//     real install path (same filesystem => the later rename is atomic).
//  3. Preflight: run the temp binary's own `-version`. This is the last
//     point at which failure costs nothing -- the running installation has
//     not been touched.
//  4. Backup: rename the CURRENT binary to <path>.previous. Atomic. The
//     running process keeps its already-open file handle regardless (POSIX
//     rename does not affect a process that already exec'd the old inode),
//     so nothing is disrupted by this step alone.
//  5. Install: rename the temp binary into <path>. Atomic.
//  6. Restart, then poll Healthy() for up to coreHealthPollWindow.
//  7. If it never reports healthy: rename the new (bad) binary aside, restore
//     <path>.previous back to <path>, restart AGAIN, and report
//     RolledBack=true. The node is not left running a binary nothing
//     verified was ever the point of steps 1-3; that only handles a
//     corrupt/wrong DOWNLOAD, not a binary that runs `-version` fine but
//     fails against this node's actual config -- which is exactly what the
//     health check after a real restart catches instead.
func (a *Adapter) UpgradeCore(
	ctx context.Context, binaryURL, binarySHA256, expectedVersion string,
) (adapter.CoreVersionResult, error) {
	if strings.TrimSpace(binaryURL) == "" {
		return adapter.CoreVersionResult{}, fmt.Errorf("binary URL is required")
	}
	if strings.TrimSpace(binarySHA256) == "" {
		return adapter.CoreVersionResult{}, fmt.Errorf("binary sha256 is required")
	}

	installedPath, err := a.rt.BinaryPath(ctx)
	if err != nil {
		return adapter.CoreVersionResult{}, fmt.Errorf("resolve current binary: %w", err)
	}
	installDir := filepath.Dir(installedPath)

	// Step 1+2: download the archive, verify it, extract the executable --
	// all before anything real is touched.
	newBinaryTmp, err := downloadExtractVerify(ctx, installDir, binaryURL, binarySHA256)
	if err != nil {
		return adapter.CoreVersionResult{}, fmt.Errorf("download: %w", err)
	}
	// Removed unconditionally once this function returns; a successful
	// install renamed it away already, so this is a no-op in that case.
	defer func() { _ = os.Remove(newBinaryTmp) }()

	// Step 3: preflight. A binary that will not even report its own
	// version is not one worth stopping the current, working installation
	// for.
	preflightVersion, err := runVersion(ctx, newBinaryTmp, corePreflightTimeout)
	if err != nil {
		return adapter.CoreVersionResult{}, fmt.Errorf("preflight: downloaded binary did not run: %w", err)
	}
	if expectedVersion != "" && !strings.Contains(preflightVersion, expectedVersion) {
		return adapter.CoreVersionResult{}, fmt.Errorf(
			"preflight: downloaded binary reports %q, expected it to contain %q -- refusing to install what may be the wrong release",
			preflightVersion, expectedVersion)
	}

	// Step 4: backup the current binary.
	backupPath := installedPath + ".previous"
	if err := os.Rename(installedPath, backupPath); err != nil {
		return adapter.CoreVersionResult{}, fmt.Errorf("back up current binary: %w", err)
	}

	// Step 5: install.
	if err := os.Rename(newBinaryTmp, installedPath); err != nil {
		// The current binary is already gone from installedPath. Put it
		// back immediately -- a node with NEITHER binary at the path is a
		// worse failure than the upgrade simply not happening, and this is
		// the one step where that could otherwise occur.
		_ = os.Rename(backupPath, installedPath)
		return adapter.CoreVersionResult{}, fmt.Errorf("install new binary: %w", err)
	}

	// Step 6: restart and wait for a real health signal, not just a
	// restart call that returned without error.
	if err := a.Restart(ctx); err != nil {
		return a.rollback(ctx, installedPath, backupPath,
			fmt.Errorf("restart after install failed: %w", err))
	}
	if !a.waitHealthy(ctx) {
		return a.rollback(ctx, installedPath, backupPath,
			fmt.Errorf("the new binary did not become healthy within %s", coreHealthPollWindow))
	}

	// Success: the backup at backupPath is deliberately LEFT on disk as a
	// manual escape hatch (one generation, not accumulated) -- an operator
	// who finds the new version behaves differently under real load, after
	// this health check already passed, still has something to revert to
	// by hand without re-downloading.
	installedVersion, err := runVersion(ctx, installedPath, corePreflightTimeout)
	if err != nil {
		// The upgrade itself succeeded (health check passed); being unable
		// to re-read the version afterward is a cosmetic failure, not a
		// reason to report the upgrade as failed or roll it back.
		installedVersion = preflightVersion
	}
	return adapter.CoreVersionResult{InstalledVersion: installedVersion}, nil
}

// rollback restores backupPath over installedPath and restarts, reporting
// the ORIGINAL failure alongside whether the rollback itself worked. A
// rollback that also fails is the one scenario this whole design cannot
// make safe -- disk full, permissions changed mid-flight -- and the error
// says so explicitly rather than claiming RolledBack=true for a restore
// that did not actually happen.
func (a *Adapter) rollback(ctx context.Context, installedPath, backupPath string, cause error) (adapter.CoreVersionResult, error) {
	failedBinary := installedPath + ".failed"
	_ = os.Rename(installedPath, failedBinary) // best-effort; proceed regardless
	if err := os.Rename(backupPath, installedPath); err != nil {
		return adapter.CoreVersionResult{}, fmt.Errorf(
			"%w; additionally, restoring the previous binary FAILED (%v) -- this node may have no working core binary at %s",
			cause, err, installedPath)
	}
	_ = a.Restart(ctx) // best-effort: report the original cause regardless of this outcome
	healthy := a.waitHealthy(ctx)
	version, _ := runVersion(ctx, installedPath, corePreflightTimeout)
	result := adapter.CoreVersionResult{InstalledVersion: version, RolledBack: true}
	if !healthy {
		return result, fmt.Errorf("%w; rolled back, but the previous binary is ALSO not healthy after restart -- this node needs manual attention", cause)
	}
	return result, cause
}

func (a *Adapter) waitHealthy(ctx context.Context) bool {
	deadline := time.Now().Add(a.coreHealthPollWindow)
	for {
		if ok, _ := a.rt.Healthy(ctx); ok {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(a.coreHealthPollInterval):
		}
	}
}

// runVersion execs binaryPath with -version and returns its combined
// output, trimmed. Used both for the preflight (on a temp file, before
// anything is installed) and to read back what actually ended up in place
// afterward -- the same helper because the question is identical: what
// does THIS specific file report itself to be.
func runVersion(ctx context.Context, binaryPath string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, binaryPath, "-version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s -version: %w: %s", binaryPath, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// downloadExtractVerify fetches the zip at url, verifies it against
// wantSHA256 BEFORE opening it as an archive (an unverified file is not
// trusted enough to even parse), extracts the first regular file whose
// name is exactly "xray" (case-insensitive) or ends in "/xray", and returns
// its path as a new temp file in dir, executable, not yet renamed into
// place.
func downloadExtractVerify(ctx context.Context, dir, url, wantSHA256 string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, coreDownloadTimeout)
	defer cancel()

	archiveTmp, err := os.CreateTemp(dir, "antimage-xray-archive-*.zip")
	if err != nil {
		return "", fmt.Errorf("create temp archive: %w", err)
	}
	archiveName := archiveTmp.Name()
	defer func() { _ = os.Remove(archiveName) }()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		_ = archiveTmp.Close()
		return "", fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		_ = archiveTmp.Close()
		return "", fmt.Errorf("download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_ = archiveTmp.Close()
		return "", fmt.Errorf("download: unexpected status %s", resp.Status)
	}

	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(archiveTmp, hasher), resp.Body)
	if err != nil {
		_ = archiveTmp.Close()
		return "", fmt.Errorf("download: %w", err)
	}
	if size < geoMinPlausibleSize {
		_ = archiveTmp.Close()
		return "", fmt.Errorf("downloaded archive is only %d bytes, too small to be real", size)
	}
	if err := archiveTmp.Sync(); err != nil {
		_ = archiveTmp.Close()
		return "", fmt.Errorf("sync temp archive: %w", err)
	}
	if err := archiveTmp.Close(); err != nil {
		return "", fmt.Errorf("close temp archive: %w", err)
	}

	gotSHA256 := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(gotSHA256, wantSHA256) {
		return "", fmt.Errorf("checksum mismatch: got %s, want %s", gotSHA256, wantSHA256)
	}

	return extractBinary(archiveName, dir)
}

// extractBinary opens archivePath as a zip and copies the entry named
// "xray" (any directory depth, case-insensitive) into a new temp file in
// dir, executable. Xray-core's release archives put the binary at the
// zip root; matching on basename rather than the full entry path tolerates
// a release that nests it in a subdirectory instead, which has happened
// before across similar Go-core projects' packaging changes.
func extractBinary(archivePath, dir string) (string, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = zr.Close() }()

	var entry *zip.File
	for _, f := range zr.File {
		base := strings.ToLower(filepath.Base(f.Name))
		if base == "xray" {
			entry = f
			break
		}
	}
	if entry == nil {
		return "", fmt.Errorf("archive contains no file named xray")
	}

	rc, err := entry.Open()
	if err != nil {
		return "", fmt.Errorf("open xray entry: %w", err)
	}
	defer func() { _ = rc.Close() }()

	out, err := os.CreateTemp(dir, "antimage-xray-bin-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temp binary: %w", err)
	}
	outName := out.Name()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(outName)
		}
	}()

	// entry.UncompressedSize64 caps what LimitReader will ever hand to
	// Copy, so a maliciously crafted zip cannot decompress past what it
	// claims no matter what the compressed stream actually contains --
	// unlike io.Copy alone, which trusts the archive's own accounting for
	// nothing and would happily write however much the decompressor
	// produces. The read of one extra byte afterward is what CATCHES the
	// lie: if the entry's real content is larger than its declared size,
	// the underlying reader still has data once the capped copy is done.
	declared := int64(entry.UncompressedSize64)
	written, err := io.Copy(out, io.LimitReader(rc, declared))
	if err != nil {
		_ = out.Close()
		return "", fmt.Errorf("extract xray: %w", err)
	}
	if written != declared {
		_ = out.Close()
		return "", fmt.Errorf("extract xray: declared size %d, only %d bytes present", declared, written)
	}
	var probe [1]byte
	if n, _ := rc.Read(probe[:]); n > 0 {
		_ = out.Close()
		return "", fmt.Errorf("extract xray: archive entry exceeds its declared size %d (possible zip bomb)", declared)
	}
	if err := out.Close(); err != nil {
		return "", fmt.Errorf("close temp binary: %w", err)
	}
	if err := os.Chmod(outName, 0o755); err != nil {
		return "", fmt.Errorf("chmod temp binary: %w", err)
	}

	success = true
	return outName, nil
}

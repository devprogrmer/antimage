package xray

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// UpgradeCore execs the downloaded file directly (`<path> -version`), which
// on Windows means it must actually be a Windows-executable format -- a
// shell script with a shebang is not one. These tests build a real shell
// script "xray" stand-in and exec it for real, so they are gated to Linux,
// matching this codebase's existing convention (see
// TestWrittenConfigIsNotWorldReadable) of verifying exec/POSIX-permission
// behaviour on CI rather than faking the process boundary here -- faking
// it would test this file's own mock, not whether UpgradeCore's ordering
// guarantees (nothing touched until preflight passes, rollback on an
// unhealthy restart) hold against a real exec.Command call.
func skipUnlessLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("execs a real binary; verified on Linux CI")
	}
}

// fakeXrayScript is a shell script standing in for the xray binary. It
// prints a recognizable version string for `-version` and exits 0 for any
// other argument (so a[Restart-triggered health check via systemctl in
// production isn't exercised here, but has no complaint from this file
// existing where systemctl expects the unit's binary).
const fakeXrayScript = `#!/bin/sh
if [ "$1" = "-version" ]; then
  echo "Xray %s (Xray, Penetrates Everything.) Custom (go1.2 linux/amd64)"
  exit 0
fi
exit 0
`

// buildFakeXrayZip returns zip bytes containing one entry "xray" with mode
// 0755 running fakeXrayScript formatted with version.
func buildFakeXrayZip(t *testing.T, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	hdr := &zip.FileHeader{Name: "xray", Method: zip.Deflate}
	hdr.SetMode(0o755)
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := fmt.Fprintf(w, fakeXrayScript, version); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// coreTestServer serves a zip (and lets a test corrupt it after computing
// the checksum, to test checksum-mismatch handling) at /xray.zip.
func coreTestServer(t *testing.T, zipBytes []byte) (url string, teardown func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/xray.zip", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(zipBytes)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/xray.zip", func() {}
}

// installedBinary creates a directory containing a "previous" xray binary
// (also a real, exec-able script) at the path BinaryPath will report, so
// UpgradeCore has something real to back up and potentially roll back to.
func installedBinary(t *testing.T, dir, version string) string {
	t.Helper()
	path := filepath.Join(dir, "xray")
	script := fmt.Sprintf(fakeXrayScript, version)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write installed binary: %v", err)
	}
	return path
}

func TestUpgradeCore_HappyPath_InstallsAndBacksUpPrevious(t *testing.T) {
	skipUnlessLinux(t)
	dir := t.TempDir()
	installedPath := installedBinary(t, dir, "1.8.0")

	rt := newFakeRuntime()
	rt.binaryPath = installedPath
	a := NewWithAssetDir(t.TempDir(), rt, false, t.TempDir())

	zipBytes := buildFakeXrayZip(t, "1.9.0")
	url, _ := coreTestServer(t, zipBytes)
	sum := sha256Hex(zipBytes)

	result, err := a.UpgradeCore(context.Background(), url, sum, "1.9.0")
	if err != nil {
		t.Fatalf("UpgradeCore: %v", err)
	}
	if result.RolledBack {
		t.Error("RolledBack = true on the happy path")
	}
	if result.InstalledVersion == "" || !bytes.Contains([]byte(result.InstalledVersion), []byte("1.9.0")) {
		t.Errorf("InstalledVersion = %q, want it to mention 1.9.0", result.InstalledVersion)
	}

	// The new binary is in place and reports the new version.
	out, verErr := runVersion(context.Background(), installedPath, corePreflightTimeout)
	if verErr != nil {
		t.Fatalf("run installed binary: %v", verErr)
	}
	if !bytes.Contains([]byte(out), []byte("1.9.0")) {
		t.Errorf("installed binary reports %q, want 1.9.0", out)
	}

	// The previous binary is preserved, not deleted, as a manual escape
	// hatch -- and still reports the OLD version.
	prevOut, verErr := runVersion(context.Background(), installedPath+".previous", corePreflightTimeout)
	if verErr != nil {
		t.Fatalf("run backup binary: %v", verErr)
	}
	if !bytes.Contains([]byte(prevOut), []byte("1.8.0")) {
		t.Errorf("backup binary reports %q, want 1.8.0", prevOut)
	}

	restarts, _, _, _ := rt.counts()
	if restarts != 1 {
		t.Errorf("restarts = %d, want 1", restarts)
	}
}

func TestUpgradeCore_ChecksumMismatch_LeavesInstalledBinaryUntouched(t *testing.T) {
	skipUnlessLinux(t)
	dir := t.TempDir()
	installedPath := installedBinary(t, dir, "1.8.0")

	rt := newFakeRuntime()
	rt.binaryPath = installedPath
	a := NewWithAssetDir(t.TempDir(), rt, false, t.TempDir())

	zipBytes := buildFakeXrayZip(t, "1.9.0")
	url, _ := coreTestServer(t, zipBytes)

	_, err := a.UpgradeCore(context.Background(), url, "0000000000000000000000000000000000000000000000000000000000000000", "1.9.0")
	if err == nil {
		t.Fatal("expected a checksum mismatch error")
	}

	out, verErr := runVersion(context.Background(), installedPath, corePreflightTimeout)
	if verErr != nil {
		t.Fatalf("run installed binary: %v", verErr)
	}
	if !bytes.Contains([]byte(out), []byte("1.8.0")) {
		t.Errorf("installed binary reports %q, want it to still be 1.8.0 (untouched)", out)
	}
	if _, statErr := os.Stat(installedPath + ".previous"); !os.IsNotExist(statErr) {
		t.Error(".previous should not exist -- nothing was ever installed to back up from")
	}

	restarts, _, _, _ := rt.counts()
	if restarts != 0 {
		t.Errorf("restarts = %d, want 0 -- must not restart when the download never verified", restarts)
	}
}

func TestUpgradeCore_PreflightVersionMismatch_LeavesInstalledBinaryUntouched(t *testing.T) {
	skipUnlessLinux(t)
	dir := t.TempDir()
	installedPath := installedBinary(t, dir, "1.8.0")

	rt := newFakeRuntime()
	rt.binaryPath = installedPath
	a := NewWithAssetDir(t.TempDir(), rt, false, t.TempDir())

	// The archive genuinely contains 1.9.0, and its checksum is correct --
	// but the caller asked for 2.0.0. This is the "URL served a different
	// release than the operator chose" case the preflight exists for.
	zipBytes := buildFakeXrayZip(t, "1.9.0")
	url, _ := coreTestServer(t, zipBytes)
	sum := sha256Hex(zipBytes)

	_, err := a.UpgradeCore(context.Background(), url, sum, "2.0.0")
	if err == nil {
		t.Fatal("expected a preflight version-mismatch error")
	}

	out, _ := runVersion(context.Background(), installedPath, corePreflightTimeout)
	if !bytes.Contains([]byte(out), []byte("1.8.0")) {
		t.Errorf("installed binary reports %q, want it untouched at 1.8.0", out)
	}
	restarts, _, _, _ := rt.counts()
	if restarts != 0 {
		t.Errorf("restarts = %d, want 0 -- must not restart onto an unverified version", restarts)
	}
}

func TestUpgradeCore_ArchiveWithoutXrayEntry_IsRejected(t *testing.T) {
	skipUnlessLinux(t)
	dir := t.TempDir()
	installedPath := installedBinary(t, dir, "1.8.0")

	rt := newFakeRuntime()
	rt.binaryPath = installedPath
	a := NewWithAssetDir(t.TempDir(), rt, false, t.TempDir())

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("README.md")
	_, _ = w.Write([]byte("not a binary"))
	_ = zw.Close()

	url, _ := coreTestServer(t, buf.Bytes())
	sum := sha256Hex(buf.Bytes())

	_, err := a.UpgradeCore(context.Background(), url, sum, "")
	if err == nil {
		t.Fatal("expected an error for an archive with no xray entry")
	}
}

// TestUpgradeCore_UnhealthyAfterRestart_RollsBackToPreviousBinary is the
// safety property the entire design exists for: a new binary that installs
// and restarts without ERROR but never reports healthy must not be left in
// place. healthyAfterRestartN=2 simulates exactly that -- the first
// restart (the new binary) stays unhealthy; only a SECOND restart (the
// rolled-back previous binary) reports healthy.
func TestUpgradeCore_UnhealthyAfterRestart_RollsBackToPreviousBinary(t *testing.T) {
	skipUnlessLinux(t)
	dir := t.TempDir()
	installedPath := installedBinary(t, dir, "1.8.0")

	rt := newFakeRuntime()
	rt.binaryPath = installedPath
	rt.healthyAfterRestartN = 2
	a := NewWithAssetDir(t.TempDir(), rt, false, t.TempDir())
	// Shrunk from the production window (20s/2s) so this test proves the
	// give-up-and-roll-back path without spending real seconds on it.
	a.coreHealthPollWindow = 200 * time.Millisecond
	a.coreHealthPollInterval = 20 * time.Millisecond

	zipBytes := buildFakeXrayZip(t, "1.9.0")
	url, _ := coreTestServer(t, zipBytes)
	sum := sha256Hex(zipBytes)

	result, err := a.UpgradeCore(context.Background(), url, sum, "1.9.0")
	if err == nil {
		t.Fatal("expected an error: the new binary never became healthy")
	}
	if !result.RolledBack {
		t.Error("RolledBack = false, want true")
	}

	// The binary actually in place afterward must be the OLD one, not the
	// new one that failed its health check.
	out, verErr := runVersion(context.Background(), installedPath, corePreflightTimeout)
	if verErr != nil {
		t.Fatalf("run installed binary after rollback: %v", verErr)
	}
	if !bytes.Contains([]byte(out), []byte("1.8.0")) {
		t.Errorf("installed binary after rollback reports %q, want 1.8.0 (the restored original)", out)
	}
	if result.InstalledVersion != "" && !bytes.Contains([]byte(result.InstalledVersion), []byte("1.8.0")) {
		t.Errorf("result.InstalledVersion = %q, want it to reflect the restored 1.8.0", result.InstalledVersion)
	}

	restarts, _, _, _ := rt.counts()
	if restarts != 2 {
		t.Errorf("restarts = %d, want 2 (install attempt + rollback restart)", restarts)
	}
}

func TestUpgradeCore_RestartFailsAfterInstall_RollsBack(t *testing.T) {
	skipUnlessLinux(t)
	dir := t.TempDir()
	installedPath := installedBinary(t, dir, "1.8.0")

	rt := newFakeRuntime()
	rt.binaryPath = installedPath
	rt.failRst = errors.New("systemctl restart xray: timed out")
	a := NewWithAssetDir(t.TempDir(), rt, false, t.TempDir())

	zipBytes := buildFakeXrayZip(t, "1.9.0")
	url, _ := coreTestServer(t, zipBytes)
	sum := sha256Hex(zipBytes)

	result, err := a.UpgradeCore(context.Background(), url, sum, "1.9.0")
	if err == nil {
		t.Fatal("expected an error: restart itself failed")
	}
	if !result.RolledBack {
		t.Error("RolledBack = false, want true")
	}

	// The binary in place must still be the (never-actually-swapped-out
	// from the running process's perspective, but file-system-wise
	// restored) original.
	if _, statErr := os.Stat(installedPath); statErr != nil {
		t.Fatalf("installed path missing after rollback: %v", statErr)
	}
}

func TestUpgradeCore_RequiresBinaryURLAndChecksum(t *testing.T) {
	dir := t.TempDir()
	installedPath := filepath.Join(dir, "xray")
	_ = os.WriteFile(installedPath, []byte("x"), 0o755)

	rt := newFakeRuntime()
	rt.binaryPath = installedPath
	a := NewWithAssetDir(t.TempDir(), rt, false, t.TempDir())

	if _, err := a.UpgradeCore(context.Background(), "", "sum", "1.0"); err == nil {
		t.Error("expected an error for a missing binary URL")
	}
	if _, err := a.UpgradeCore(context.Background(), "https://example.com/x.zip", "", "1.0"); err == nil {
		t.Error("expected an error for a missing checksum")
	}
}

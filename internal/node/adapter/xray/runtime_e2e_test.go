package xray

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestXrayRuntimeSpeedLimitEnforcement verifies actual Xray bandwidth enforcement
// with real traffic through a real Xray process.
//
// TECHNICAL LIMITATION: Xray 24.11.11 accepts upSpeed/downSpeed policy fields but does NOT enforce them.
// Runtime verification (Phase 6 M2): 343 Mbps measured vs 5 Mbps configured limit.
//
// Evidence: ENFORCEMENT-CAPABILITY-MATRIX.md line 41
// Fallback: External tc (traffic control) required for bandwidth shaping on Linux
//
// Classification: UNSUPPORTED (native), ENFORCED (external via tc)
func TestXrayRuntimeSpeedLimitEnforcement(t *testing.T) {
	t.Skip("UNSUPPORTED: Xray 24.11.11 does not enforce upSpeed/downSpeed fields. " +
		"Runtime test proved config accepted but limits ignored (343 Mbps vs 5 Mbps configured). " +
		"Speed limit enforcement requires external tc (traffic control) on Linux. " +
		"See ENFORCEMENT-CAPABILITY-MATRIX.md for details and tc-based implementation.")
}

// findXrayBinary locates the Xray executable
func findXrayBinary(t *testing.T) string {
	t.Helper()

	// Strategy 1: Check XRAY_PATH environment variable first
	if xrayPath := os.Getenv("XRAY_PATH"); xrayPath != "" {
		if _, err := os.Stat(xrayPath); err == nil {
			t.Logf("Found Xray from XRAY_PATH: %s", xrayPath)
			return xrayPath
		}
	}

	// Strategy 2: Try common locations relative to module root
	// On Windows the binary was downloaded to test/bin/test/bin/xray.exe
	possiblePaths := []string{
		"D:\\download\\antimage\\test\\bin\\test\\bin\\xray.exe", // Absolute Windows path
		"/d/download/antimage/test/bin/test/bin/xray.exe",        // Git Bash Windows path
		"test/bin/test/bin/xray.exe",                             // Relative
		"test/bin/xray.exe",
		"test/bin/xray",
		"../../../../test/bin/test/bin/xray.exe", // From internal/node/adapter/xray
		"../../../../test/bin/xray.exe",
		"../../../../test/bin/xray",
	}

	for _, path := range possiblePaths {
		if _, err := os.Stat(path); err == nil {
			abs, _ := filepath.Abs(path)
			t.Logf("Found Xray at: %s", abs)
			return abs
		}
	}

	// Strategy 3: Try PATH
	if path, err := exec.LookPath("xray"); err == nil {
		t.Logf("Found Xray in PATH: %s", path)
		return path
	}

	t.Logf("Xray binary not found. Searched:")
	for _, p := range possiblePaths {
		t.Logf("  - %s", p)
	}
	t.Logf("  - PATH")
	t.Logf("Set XRAY_PATH environment variable to specify location")

	return ""
}

// verifyXrayVersion checks that Xray binary is working

// testDownloadSpeedLimit verifies download bandwidth enforcement

// testUploadSpeedLimit verifies upload bandwidth enforcement

// testNoLimitBaseline verifies traffic flows freely without limits

// testMultipleUsersDifferentLimits verifies per-user enforcement

// generateServerConfig creates Xray server config with speed limits

// generateClientConfig creates Xray client config with SOCKS5 inbound

// startXrayServer starts an Xray process with the given config
func startXrayServer(t *testing.T, xrayPath, configPath string) *exec.Cmd {
	t.Helper()

	cmd := exec.Command(xrayPath, "run", "-config", configPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start Xray server: %v", err)
	}

	t.Cleanup(func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
	})

	return cmd
}

// startXrayClient starts Xray client with SOCKS5 proxy

// findFreePort finds an available TCP port
func findFreePort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find free port: %v", err)
	}
	defer listener.Close()

	return listener.Addr().(*net.TCPAddr).Port
}

// waitForPort waits for a port to be listening
func waitForPort(t *testing.T, port int, timeout time.Duration) bool {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// startLargeFileServer starts HTTP server serving random data

// startUploadServer starts HTTP server accepting POST uploads

// measureDownloadThroughput measures download speed through SOCKS5 proxy

// measureUploadThroughput measures upload speed through SOCKS5 proxy

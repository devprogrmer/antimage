package xray

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
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
		"test/bin/test/bin/xray.exe",                              // Relative
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
func verifyXrayVersion(t *testing.T, xrayPath string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, xrayPath, "version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Xray version check failed: %v\nOutput: %s", err, output)
	}

	t.Logf("Xray version: %s", string(output))
}

// testDownloadSpeedLimit verifies download bandwidth enforcement
func testDownloadSpeedLimit(t *testing.T, xrayPath string, limitKbps int64, durationSec int) {
	// Create test directory
	testDir := t.TempDir()

	// Generate server config with speed limit
	serverPort := findFreePort(t)
	serverConfig := generateServerConfig(t, testDir, serverPort, limitKbps, limitKbps)

	// Start Xray server
	serverCmd := startXrayServer(t, xrayPath, serverConfig)
	defer serverCmd.Process.Kill()

	// Wait for server startup
	time.Sleep(2 * time.Second)
	if !waitForPort(t, serverPort, 5*time.Second) {
		t.Fatal("Xray server did not start listening on port")
	}

	// Create SOCKS5 client through Xray
	socksPort := findFreePort(t)
	clientConfig := generateClientConfig(t, testDir, serverPort, socksPort)

	clientCmd := startXrayClient(t, xrayPath, clientConfig)
	defer clientCmd.Process.Kill()

	// Wait for client startup
	time.Sleep(2 * time.Second)
	if !waitForPort(t, socksPort, 5*time.Second) {
		t.Fatal("Xray client SOCKS5 proxy did not start")
	}

	// Start HTTP server serving large data
	httpPort := findFreePort(t)
	httpServer := startLargeFileServer(t, httpPort)
	defer httpServer.Close()

	// Measure download throughput through Xray
	bytesTransferred, elapsed := measureDownloadThroughput(t, socksPort, httpPort, durationSec)

	// Calculate actual throughput in kbps
	actualKbps := float64(bytesTransferred*8) / elapsed.Seconds() / 1024

	// Expected limit with tolerance (15% for protocol overhead)
	expectedMaxKbps := float64(limitKbps) * 1.15

	t.Logf("Download test results:")
	t.Logf("  Configured limit: %d kbps (%.2f Mbps)", limitKbps, float64(limitKbps)/1024)
	t.Logf("  Measured throughput: %.0f kbps (%.2f Mbps)", actualKbps, actualKbps/1024)
	t.Logf("  Duration: %.1f seconds", elapsed.Seconds())
	t.Logf("  Bytes transferred: %d", bytesTransferred)
	t.Logf("  Tolerance: 15%%")
	t.Logf("  Max allowed: %.0f kbps", expectedMaxKbps)

	// Verify enforcement
	if actualKbps > expectedMaxKbps {
		t.Errorf("Speed limit violated: actual %.0f kbps > limit %.0f kbps (with tolerance)",
			actualKbps, expectedMaxKbps)
	}

	// Also verify we're not too far below (would indicate throttling failure)
	minExpectedKbps := float64(limitKbps) * 0.50 // At least 50% of limit
	if actualKbps < minExpectedKbps {
		t.Logf("WARNING: Throughput %.0f kbps is significantly below limit %d kbps (might indicate test issue)",
			actualKbps, limitKbps)
	}
}

// testUploadSpeedLimit verifies upload bandwidth enforcement
func testUploadSpeedLimit(t *testing.T, xrayPath string, limitKbps int64, durationSec int) {
	testDir := t.TempDir()

	// Generate server config with speed limit
	serverPort := findFreePort(t)
	serverConfig := generateServerConfig(t, testDir, serverPort, limitKbps, limitKbps)

	serverCmd := startXrayServer(t, xrayPath, serverConfig)
	defer serverCmd.Process.Kill()

	time.Sleep(2 * time.Second)
	if !waitForPort(t, serverPort, 5*time.Second) {
		t.Fatal("Xray server did not start")
	}

	socksPort := findFreePort(t)
	clientConfig := generateClientConfig(t, testDir, serverPort, socksPort)

	clientCmd := startXrayClient(t, xrayPath, clientConfig)
	defer clientCmd.Process.Kill()

	time.Sleep(2 * time.Second)
	if !waitForPort(t, socksPort, 5*time.Second) {
		t.Fatal("Xray client did not start")
	}

	// Start HTTP server that accepts uploads
	httpPort := findFreePort(t)
	httpServer := startUploadServer(t, httpPort)
	defer httpServer.Close()

	// Measure upload throughput
	bytesTransferred, elapsed := measureUploadThroughput(t, socksPort, httpPort, durationSec)

	actualKbps := float64(bytesTransferred*8) / elapsed.Seconds() / 1024
	expectedMaxKbps := float64(limitKbps) * 1.15

	t.Logf("Upload test results:")
	t.Logf("  Configured limit: %d kbps (%.2f Mbps)", limitKbps, float64(limitKbps)/1024)
	t.Logf("  Measured throughput: %.0f kbps (%.2f Mbps)", actualKbps, actualKbps/1024)
	t.Logf("  Duration: %.1f seconds", elapsed.Seconds())
	t.Logf("  Bytes transferred: %d", bytesTransferred)

	if actualKbps > expectedMaxKbps {
		t.Errorf("Upload speed limit violated: actual %.0f kbps > limit %.0f kbps",
			actualKbps, expectedMaxKbps)
	}
}

// testNoLimitBaseline verifies traffic flows freely without limits
func testNoLimitBaseline(t *testing.T, xrayPath string, durationSec int) {
	testDir := t.TempDir()

	serverPort := findFreePort(t)
	// No speed limit (0 = unlimited)
	serverConfig := generateServerConfig(t, testDir, serverPort, 0, 0)

	serverCmd := startXrayServer(t, xrayPath, serverConfig)
	defer serverCmd.Process.Kill()

	time.Sleep(2 * time.Second)

	socksPort := findFreePort(t)
	clientConfig := generateClientConfig(t, testDir, serverPort, socksPort)

	clientCmd := startXrayClient(t, xrayPath, clientConfig)
	defer clientCmd.Process.Kill()

	time.Sleep(2 * time.Second)

	httpPort := findFreePort(t)
	httpServer := startLargeFileServer(t, httpPort)
	defer httpServer.Close()

	bytesTransferred, elapsed := measureDownloadThroughput(t, socksPort, httpPort, durationSec)
	actualKbps := float64(bytesTransferred*8) / elapsed.Seconds() / 1024

	t.Logf("Baseline (no limit) test results:")
	t.Logf("  Measured throughput: %.0f kbps (%.2f Mbps)", actualKbps, actualKbps/1024)
	t.Logf("  Duration: %.1f seconds", elapsed.Seconds())

	// Verify throughput is reasonable (at least 10 Mbps for local loopback)
	minExpected := 10000.0 // 10 Mbps minimum for localhost
	if actualKbps < minExpected {
		t.Logf("WARNING: Baseline throughput %.0f kbps is lower than expected %.0f kbps",
			actualKbps, minExpected)
	}
}

// testMultipleUsersDifferentLimits verifies per-user enforcement
func testMultipleUsersDifferentLimits(t *testing.T, xrayPath string) {
	t.Skip("Multiple users test requires more complex setup - deferred")
}

// generateServerConfig creates Xray server config with speed limits
func generateServerConfig(t *testing.T, dir string, port int, upKbps, downKbps int64) string {
	t.Helper()

	// Convert kbps to bytes/sec for Xray policy
	var upSpeed, downSpeed int64
	if upKbps > 0 {
		upSpeed = upKbps * 1024 / 8
	}
	if downKbps > 0 {
		downSpeed = downKbps * 1024 / 8
	}

	config := map[string]interface{}{
		"log": map[string]interface{}{
			"loglevel": "warning",
		},
		"inbounds": []map[string]interface{}{
			{
				"port":     port,
				"protocol": "vless",
				"tag":      "test-inbound",
				"settings": map[string]interface{}{
					"clients": []map[string]interface{}{
						{
							"id":    "b831381d-6324-4d53-ad4f-8cda48b30811",
							"email": "test-user@antimage",
							"level": 1,
						},
					},
					"decryption": "none",
				},
				"streamSettings": map[string]interface{}{
					"network": "tcp",
				},
			},
			{
				"listen":   "127.0.0.1",
				"port":     findFreePort(t),
				"protocol": "dokodemo-door",
				"tag":      "api-in",
				"settings": map[string]interface{}{
					"address": "127.0.0.1",
				},
			},
		},
		"outbounds": []map[string]interface{}{
			{
				"protocol": "freedom",
				"tag":      "direct",
			},
		},
		"api": map[string]interface{}{
			"tag": "api",
			"services": []string{
				"HandlerService",
				"StatsService",
			},
		},
		"stats": map[string]interface{}{},
		"routing": map[string]interface{}{
			"rules": []map[string]interface{}{
				{
					"inboundTag": []string{"api-in"},
					"outboundTag": "api",
					"type":        "field",
				},
			},
		},
	}

	// Add policy if limits are set
	if upSpeed > 0 || downSpeed > 0 {
		levelConfig := map[string]interface{}{
			"statsUserUplink":   true,
			"statsUserDownlink": true,
		}

		// Xray expects direct integer values, not objects
		if upSpeed > 0 {
			levelConfig["upSpeed"] = upSpeed
		}
		if downSpeed > 0 {
			levelConfig["downSpeed"] = downSpeed
		}

		config["policy"] = map[string]interface{}{
			"levels": map[string]interface{}{
				"1": levelConfig,
			},
		}
	}

	configPath := filepath.Join(dir, "server-config.json")
	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("Failed to write server config: %v", err)
	}

	return configPath
}

// generateClientConfig creates Xray client config with SOCKS5 inbound
func generateClientConfig(t *testing.T, dir string, serverPort, socksPort int) string {
	t.Helper()

	config := map[string]interface{}{
		"log": map[string]interface{}{
			"loglevel": "warning",
		},
		"inbounds": []map[string]interface{}{
			{
				"port":     socksPort,
				"protocol": "socks",
				"tag":      "socks-in",
				"settings": map[string]interface{}{
					"auth": "noauth",
					"udp":  false,
				},
			},
		},
		"outbounds": []map[string]interface{}{
			{
				"protocol": "vless",
				"tag":      "proxy",
				"settings": map[string]interface{}{
					"vnext": []map[string]interface{}{
						{
							"address": "127.0.0.1",
							"port":    serverPort,
							"users": []map[string]interface{}{
								{
									"id":         "b831381d-6324-4d53-ad4f-8cda48b30811",
									"encryption": "none",
									"level":      1,
								},
							},
						},
					},
				},
				"streamSettings": map[string]interface{}{
					"network": "tcp",
				},
			},
		},
	}

	configPath := filepath.Join(dir, "client-config.json")
	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("Failed to write client config: %v", err)
	}

	return configPath
}

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
func startXrayClient(t *testing.T, xrayPath, configPath string) *exec.Cmd {
	return startXrayServer(t, xrayPath, configPath) // Same mechanism
}

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
func startLargeFileServer(t *testing.T, port int) *http.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/large", func(w http.ResponseWriter, r *http.Request) {
		// Stream 100 MB of zeros (fast generation, still tests bandwidth)
		w.Header().Set("Content-Type", "application/octet-stream")
		buf := make([]byte, 1024*1024) // 1 MB buffer
		for i := 0; i < 100; i++ {
			if _, err := w.Write(buf); err != nil {
				return
			}
		}
	})

	server := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", port),
		Handler: mux,
	}

	go func() {
		server.ListenAndServe()
	}()

	t.Cleanup(func() {
		server.Close()
	})

	// Wait for server to be ready
	time.Sleep(100 * time.Millisecond)

	return server
}

// startUploadServer starts HTTP server accepting POST uploads
func startUploadServer(t *testing.T, port int) *http.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		// Discard uploaded data
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	})

	server := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", port),
		Handler: mux,
	}

	go func() {
		server.ListenAndServe()
	}()

	t.Cleanup(func() {
		server.Close()
	})

	time.Sleep(100 * time.Millisecond)

	return server
}

// measureDownloadThroughput measures download speed through SOCKS5 proxy
func measureDownloadThroughput(t *testing.T, socksPort, httpPort, durationSec int) (int64, time.Duration) {
	t.Helper()

	// Create SOCKS5 connection
	proxyAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	targetAddr := fmt.Sprintf("127.0.0.1:%d", httpPort)

	// Connect to SOCKS5 proxy
	conn, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("Failed to connect to SOCKS5 proxy: %v", err)
	}
	defer conn.Close()

	// SOCKS5 handshake: no authentication
	// Send: [version=5, nmethods=1, method=0 (no auth)]
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("SOCKS5 handshake write failed: %v", err)
	}

	// Receive: [version=5, method=0]
	handshakeResp := make([]byte, 2)
	if _, err := io.ReadFull(conn, handshakeResp); err != nil {
		t.Fatalf("SOCKS5 handshake read failed: %v", err)
	}
	if handshakeResp[0] != 0x05 || handshakeResp[1] != 0x00 {
		t.Fatalf("SOCKS5 handshake failed: got %v", handshakeResp)
	}

	// SOCKS5 connect request
	// Parse target host and port
	host, portStr, _ := net.SplitHostPort(targetAddr)
	port, _ := net.LookupPort("tcp", portStr)

	// Build connect request: [version=5, cmd=1 (connect), reserved=0, atyp=1 (IPv4), dst.addr, dst.port]
	request := []byte{0x05, 0x01, 0x00, 0x01}
	// Add IPv4 address (127.0.0.1)
	ip := net.ParseIP(host).To4()
	request = append(request, ip...)
	// Add port (big endian)
	request = append(request, byte(port>>8), byte(port&0xff))

	if _, err := conn.Write(request); err != nil {
		t.Fatalf("SOCKS5 connect request failed: %v", err)
	}

	// Read connect response
	connectResp := make([]byte, 10)
	if _, err := io.ReadFull(conn, connectResp); err != nil {
		t.Fatalf("SOCKS5 connect response failed: %v", err)
	}
	if connectResp[1] != 0x00 {
		t.Fatalf("SOCKS5 connect failed with status: %d", connectResp[1])
	}

	// Now connected through SOCKS5 proxy to HTTP server
	// Send HTTP GET request
	httpRequest := fmt.Sprintf("GET /large HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", targetAddr)
	if _, err := conn.Write([]byte(httpRequest)); err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}

	// Measure download throughput
	startTime := time.Now()
	deadline := startTime.Add(time.Duration(durationSec) * time.Second)
	conn.SetReadDeadline(deadline)

	buf := make([]byte, 32*1024) // 32KB buffer
	totalBytes := int64(0)
	headerSkipped := false

	for time.Now().Before(deadline) {
		n, err := conn.Read(buf)
		if err != nil {
			if errors.Is(err, io.EOF) || os.IsTimeout(err) {
				break
			}
			t.Logf("Read error (may be timeout): %v", err)
			break
		}

		// Skip HTTP headers on first read
		if !headerSkipped {
			// Find end of headers (\r\n\r\n)
			headerEnd := -1
			for i := 0; i < n-3; i++ {
				if buf[i] == '\r' && buf[i+1] == '\n' && buf[i+2] == '\r' && buf[i+3] == '\n' {
					headerEnd = i + 4
					break
				}
			}
			if headerEnd > 0 {
				totalBytes += int64(n - headerEnd)
				headerSkipped = true
			}
			continue
		}

		totalBytes += int64(n)
	}

	elapsed := time.Since(startTime)

	return totalBytes, elapsed
}

// measureUploadThroughput measures upload speed through SOCKS5 proxy
func measureUploadThroughput(t *testing.T, socksPort, httpPort, durationSec int) (int64, time.Duration) {
	t.Helper()

	proxyAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	targetAddr := fmt.Sprintf("127.0.0.1:%d", httpPort)

	// Connect to SOCKS5 proxy
	conn, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("Failed to connect to SOCKS5 proxy: %v", err)
	}
	defer conn.Close()

	// SOCKS5 handshake
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("SOCKS5 handshake write failed: %v", err)
	}

	handshakeResp := make([]byte, 2)
	if _, err := io.ReadFull(conn, handshakeResp); err != nil {
		t.Fatalf("SOCKS5 handshake read failed: %v", err)
	}

	// SOCKS5 connect
	host, portStr, _ := net.SplitHostPort(targetAddr)
	port, _ := net.LookupPort("tcp", portStr)

	request := []byte{0x05, 0x01, 0x00, 0x01}
	ip := net.ParseIP(host).To4()
	request = append(request, ip...)
	request = append(request, byte(port>>8), byte(port&0xff))

	if _, err := conn.Write(request); err != nil {
		t.Fatalf("SOCKS5 connect request failed: %v", err)
	}

	connectResp := make([]byte, 10)
	if _, err := io.ReadFull(conn, connectResp); err != nil {
		t.Fatalf("SOCKS5 connect response failed: %v", err)
	}

	// Calculate content length for durationSec of upload
	// Target ~10MB per second for sustained upload
	contentLength := durationSec * 10 * 1024 * 1024

	// Send HTTP POST request with chunked encoding
	httpRequest := fmt.Sprintf("POST /upload HTTP/1.1\r\nHost: %s\r\nContent-Length: %d\r\n\r\n",
		targetAddr, contentLength)
	if _, err := conn.Write([]byte(httpRequest)); err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}

	// Upload data
	startTime := time.Now()
	deadline := startTime.Add(time.Duration(durationSec) * time.Second)
	conn.SetWriteDeadline(deadline)

	buf := make([]byte, 32*1024) // 32KB buffer
	totalBytes := int64(0)

	for totalBytes < int64(contentLength) && time.Now().Before(deadline) {
		n, err := conn.Write(buf)
		if err != nil {
			if os.IsTimeout(err) {
				break
			}
			t.Logf("Write error (may be timeout): %v", err)
			break
		}
		totalBytes += int64(n)
	}

	elapsed := time.Since(startTime)

	return totalBytes, elapsed
}

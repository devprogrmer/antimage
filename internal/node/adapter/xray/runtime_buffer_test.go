package xray

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestXraySpeedLimitDirect tests speed limits with direct Xray connection (no SOCKS5)
// This simplifies the test to isolate whether the issue is with policy or with SOCKS5 layer
func TestXraySpeedLimitDirect(t *testing.T) {
	xrayPath := findXrayBinary(t)
	if xrayPath == "" {
		t.Skip("Xray binary not available")
	}

	t.Logf("Testing speed limit enforcement with Xray %s", xrayPath)

	// Start a simple HTTP server on a known port
	httpPort := 8888
	httpServer := startSimpleHTTPServer(t, httpPort)
	defer httpServer.Close()

	// Test with bufferSize-based throttling (more reliable than upSpeed/downSpeed)
	testDir := t.TempDir()

	// Server with very small buffer size = slow throughput
	serverPort := findFreePort(t)
	serverConfig := generateServerConfigWithBuffer(t, testDir, serverPort, 1) // 1 KB buffer = ~8 Mbps max

	serverCmd := startXrayServer(t, xrayPath, serverConfig)
	defer serverCmd.Process.Kill()

	time.Sleep(2 * time.Second)
	if !waitForPort(t, serverPort, 5*time.Second) {
		t.Fatal("Xray server did not start")
	}

	t.Logf("Server started on port %d with bufferSize=1 (throttled)", serverPort)

	// For now, just verify Xray starts with the config
	// Full test requires VLESS client implementation
	t.Logf("Xray server running with policy config - manual verification needed")
}

// generateServerConfigWithBuffer creates config with bufferSize-based throttling
func generateServerConfigWithBuffer(t *testing.T, dir string, port int, bufferSizeKB int) string {
	t.Helper()

	config := map[string]interface{}{
		"log": map[string]interface{}{
			"loglevel": "warning",
		},
		"stats": map[string]interface{}{},
		"policy": map[string]interface{}{
			"levels": map[string]interface{}{
				"1": map[string]interface{}{
					"statsUserUplink":   true,
					"statsUserDownlink": true,
					"bufferSize":        bufferSizeKB,
				},
			},
			"system": map[string]interface{}{
				"statsInboundUplink":   true,
				"statsInboundDownlink": true,
			},
		},
		"api": map[string]interface{}{
			"tag": "api",
			"services": []string{
				"HandlerService",
				"StatsService",
			},
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
		"routing": map[string]interface{}{
			"rules": []map[string]interface{}{
				{
					"inboundTag":  []string{"api-in"},
					"outboundTag": "api",
					"type":        "field",
				},
			},
		},
	}

	configPath := filepath.Join(dir, "server-buffer-config.json")
	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("Failed to write server config: %v", err)
	}

	t.Logf("Generated config at %s with bufferSize=%d KB", configPath, bufferSizeKB)
	return configPath
}

// startSimpleHTTPServer starts a basic HTTP server for testing
func startSimpleHTTPServer(t *testing.T, port int) *http.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
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

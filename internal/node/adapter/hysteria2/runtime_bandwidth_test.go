package hysteria2

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

// TestHysteria2RuntimeBandwidthEnforcement verifies whether Hysteria2 actually
// enforces the bandwidth.up/down config fields at runtime.
//
// Context: Xray accepted upSpeed/downSpeed but silently ignored them (Phase 6 M2).
// Hysteria2 has bandwidth.up/down in config - we MUST verify actual enforcement
// before classifying as ENFORCED vs UNSUPPORTED.
//
// Test Strategy:
// 1. Generate config with 5 Mbps upload limit
// 2. Start Hysteria2 server (requires binary)
// 3. Connect client and upload sustained traffic
// 4. Measure actual throughput over 3-5 seconds
// 5. Verify measured rate ≈ configured limit (within 20% tolerance)
//
// Classification:
// - If enforced: upgrade bandwidth to ENFORCED
// - If ignored (like Xray): downgrade to UNSUPPORTED, document, use tc
func TestHysteria2RuntimeBandwidthEnforcement(t *testing.T) {
	t.Skip("Requires Hysteria2 binary, TLS certificates, and Linux environment - manual verification needed")

	// Test parameters
	const (
		uploadLimitMbps = 5
		testDurationSec = 5
		tolerancePct    = 20 // Allow ±20% variance
	)

	expectedBps := uploadLimitMbps * 1024 * 1024 / 8 // Convert Mbps to bytes/sec
	minBps := expectedBps * (100 - tolerancePct) / 100
	maxBps := expectedBps * (100 + tolerancePct) / 100

	t.Logf("Testing Hysteria2 upload bandwidth enforcement")
	t.Logf("Configured limit: %d Mbps (%d bytes/sec)", uploadLimitMbps, expectedBps)
	t.Logf("Acceptable range: %d - %d bytes/sec (±%d%%)", minBps, maxBps, tolerancePct)

	// TODO: Implementation steps (blocked by missing Hysteria2 binary):
	//
	// 1. Generate test TLS certificate (self-signed)
	//    - openssl req -x509 -newkey rsa:2048 -nodes -keyout key.pem -out cert.pem
	//
	// 2. Generate Hysteria2 server config with bandwidth limit:
	//    listen: :20001
	//    tls:
	//      cert: cert.pem
	//      key: key.pem
	//    auth:
	//      type: password
	//      password: testpass123
	//    bandwidth:
	//      up: 5 mbps
	//      down: 100 mbps  # High to isolate upload test
	//
	// 3. Start Hysteria2 server:
	//    hysteria server -c config.yaml
	//
	// 4. Connect Hysteria2 client (or implement QUIC client):
	//    - Authenticate with password
	//    - Establish QUIC stream
	//    - Upload random data for 5 seconds
	//
	// 5. Measure actual throughput:
	//    totalBytes / testDurationSec = measuredBps
	//
	// 6. Compare against limit:
	//    if measuredBps > maxBps:
	//        FAIL - bandwidth not enforced (like Xray)
	//    else:
	//        PASS - bandwidth enforced

	t.Error("Hysteria2 bandwidth verification NOT IMPLEMENTED - requires manual testing")
	t.Error("CRITICAL: Do NOT classify bandwidth as ENFORCED without this test passing")
	t.Error("Xray taught us: config acceptance ≠ runtime enforcement")
}

// TestHysteria2BandwidthConfigGeneration verifies config generation includes
// bandwidth fields correctly, but does NOT verify enforcement.
func TestHysteria2BandwidthConfigGeneration(t *testing.T) {
	params := ServiceParams{
		Port:       20001,
		Password:   "testpassword",
		CertFile:   "/tmp/cert.pem",
		KeyFile:    "/tmp/key.pem",
		UpMbps:     10,
		DownMbps:   50,
	}

	config, err := GenerateConfig(1, params, nil)
	if err != nil {
		t.Fatalf("GenerateConfig failed: %v", err)
	}

	// Verify bandwidth section present
	if !contains(config, "bandwidth:") {
		t.Error("Config missing 'bandwidth:' section")
	}
	if !contains(config, "up: 10 mbps") {
		t.Error("Config missing 'up: 10 mbps'")
	}
	if !contains(config, "down: 50 mbps") {
		t.Error("Config missing 'down: 50 mbps'")
	}

	t.Log("✅ Config generation: bandwidth fields present")
	t.Log("⚠️  Runtime enforcement: UNVERIFIED (requires runtime test)")
}

// contains checks if s contains substring.
func contains(s, substr string) bool {
	return len(s) >= len(substr) &&
		(s == substr || len(s) > len(substr) && containsAt(s, substr))
}

func containsAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// measureHysteria2Throughput connects to Hysteria2 server and measures upload speed.
// This is a placeholder - actual implementation requires Hysteria2 protocol client.
func measureHysteria2Throughput(ctx context.Context, serverAddr, password string, durationSec int) (bytesPerSec int64, err error) {
	// TODO: Implement Hysteria2 client
	//
	// Hysteria2 uses QUIC (UDP-based) with custom protocol:
	// 1. TLS handshake over QUIC
	// 2. Authentication frame with password
	// 3. Data frames for upload/download
	//
	// Options:
	// A. Use official Hysteria2 Go client library (if exists)
	// B. Implement minimal QUIC client with quic-go
	// C. Shell out to hysteria client binary
	// D. Use curl/wget if Hysteria2 supports HTTP proxy mode
	//
	// For now: document the requirement
	return 0, fmt.Errorf("Hysteria2 client not implemented - requires quic-go or official client")
}

// BenchmarkHysteria2BaselineThroughput measures unconstrained throughput.
// Useful for comparison against rate-limited test.
func BenchmarkHysteria2BaselineThroughput(b *testing.B) {
	b.Skip("Requires Hysteria2 server running without bandwidth limits")

	// Expected: Should achieve >100 Mbps on localhost
	// This establishes that Hysteria2 CAN go fast when unconstrained
}

// TestHysteria2IgnoreClientBandwidth verifies ignoreClientBandwidth config.
func TestHysteria2IgnoreClientBandwidth(t *testing.T) {
	params := ServiceParams{
		Port:                  20002,
		Password:              "testpass",
		CertFile:              "/tmp/cert.pem",
		KeyFile:               "/tmp/key.pem",
		IgnoreClientBandwidth: true,
	}

	config, err := GenerateConfig(2, params, nil)
	if err != nil {
		t.Fatalf("GenerateConfig failed: %v", err)
	}

	if !contains(config, "ignoreClientBandwidth: true") {
		t.Error("Config missing 'ignoreClientBandwidth: true'")
	}

	t.Log("✅ ignoreClientBandwidth config field present")
}

// Documentation for manual verification process
const hysteria2BandwidthVerificationGuide = `
# Hysteria2 Bandwidth Enforcement Verification Guide

## Prerequisites

1. **Hysteria2 Binary**:
   - Download from https://hysteria.network/
   - Or compile from source: github.com/apernet/hysteria
   - Place in test/bin/hysteria or add to PATH

2. **TLS Certificate**:
   openssl req -x509 -newkey rsa:2048 -nodes \
     -keyout /tmp/hysteria-test-key.pem \
     -out /tmp/hysteria-test-cert.pem \
     -days 1 -subj "/CN=localhost"

3. **Test Environment**:
   - Linux (Hysteria2 works on Windows/Mac but tc verification needs Linux)
   - Port 20001 UDP available
   - Network with low latency (localhost ideal)

## Server Config

Create /tmp/hysteria-test-server.yaml:

"""yaml
listen: :20001
tls:
  cert: /tmp/hysteria-test-cert.pem
  key: /tmp/hysteria-test-key.pem
auth:
  type: password
  password: testpassword123
bandwidth:
  up: 5 mbps
  down: 100 mbps
"""

## Client Config

Create /tmp/hysteria-test-client.yaml:

"""yaml
server: 127.0.0.1:20001
auth: testpassword123
tls:
  insecure: true  # Accept self-signed cert
bandwidth:
  up: 100 mbps
  down: 100 mbps
"""

## Test Procedure

### Step 1: Start Server
"""
hysteria server -c /tmp/hysteria-test-server.yaml
"""

### Step 2: Baseline Test (No Limit)
Edit server config, remove bandwidth section, restart server.
Upload 100MB file through Hysteria2 proxy, measure speed.

Expected: >100 Mbps (proves Hysteria2 can go fast)

### Step 3: Limited Test (5 Mbps)
Restore bandwidth.up: 5 mbps in server config, restart.
Upload 100MB file, measure speed.

Expected if ENFORCED: ~5 Mbps (±20% = 4-6 Mbps)
Expected if UNSUPPORTED: Same as baseline (like Xray)

### Step 4: Client Upload via SOCKS5
"""
# Start Hysteria2 in SOCKS5 proxy mode
hysteria client -c /tmp/hysteria-test-client.yaml

# Upload through proxy
time curl --socks5 127.0.0.1:1080 -T /dev/zero http://httpbin.org/post --limit-rate 10M
"""

Measure actual throughput from curl output or tcpdump.

## Verdict

- **Measured ~5 Mbps**: ENFORCED ✅
  - Upgrade classification to ENFORCED
  - Document that native bandwidth works
  - Still implement tc as fallback for flexibility

- **Measured >50 Mbps**: UNSUPPORTED ❌
  - Downgrade classification to UNSUPPORTED
  - Document like Xray (config accepted, runtime ignored)
  - Use tc external enforcement only

## Report Format

"""
Hysteria2 Bandwidth Verification Report
Date: YYYY-MM-DD
Binary Version: hysteria2 vX.Y.Z

Test 1: Baseline (no limit)
- Measured: XXX Mbps
- Result: Establishes capability

Test 2: Upload limit 5 Mbps
- Configured: 5 Mbps
- Measured: XXX Mbps
- Delta: YYY% over limit
- Verdict: [ENFORCED / UNSUPPORTED]

Conclusion: [Classification decision and rationale]
"""
`

func TestHysteria2VerificationGuide(t *testing.T) {
	t.Log(hysteria2BandwidthVerificationGuide)
}

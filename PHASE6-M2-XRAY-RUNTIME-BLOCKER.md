# Phase 6 Milestone 2: Xray Runtime Enforcement - Blocker Analysis

**Date**: 2026-08-22  
**Status**: BLOCKED - Xray binary not available  

---

## Objective

Verify Xray speed limit enforcement with **real runtime traffic tests**:
1. Start actual Xray binary
2. Configure speed limits
3. Establish client connection
4. Generate sustained traffic (20+ seconds)
5. Measure actual throughput
6. Verify: actual throughput ≤ configured limit × 1.10

---

## Current Environment Assessment

### Xray Binary Availability

```bash
$ which xray
which: no xray in PATH
```

**Result**: ❌ Xray binary NOT available

### Impact

**Cannot verify runtime enforcement without Xray binary**:
- ❌ Cannot start Xray process
- ❌ Cannot establish real connections
- ❌ Cannot generate real traffic
- ❌ Cannot measure actual throughput
- ❌ Cannot verify speed limit enforcement

---

## What Phase 5 Already Verified

### Configuration Generation ✅

**File**: `internal/node/adapter/xray/policy.go`

**Verified**:
- Speed limit config generation works
- kbps → bytes/sec conversion correct: `bytesPerSec = kbps * 1024 / 8`
- Policy levels correctly assigned
- JSON output valid

**Tests**:
- `TestEndToEndSpeedLimitEnforcement` (lines 148-254)
- Verifies policy config generation
- Verifies config included in plan
- Does NOT verify runtime throughput

**Example Generated Config**:
```json
{
  "policy": {
    "levels": {
      "1": {
        "statsUserUplink": true,
        "statsUserDownlink": true,
        "upSpeed": 640000,    // 5 Mbps = 5000 kbps * 1024 / 8
        "downSpeed": 1280000  // 10 Mbps = 10000 kbps * 1024 / 8
      }
    }
  }
}
```

### Enforcer Integration ✅

**Verified**:
- Enforcer receives policies with speed limits
- Speed limits stored in enforcement.Policy struct
- Atomic admission control works (13 tests)
- Subject isolation works (9 security tests)

**What's NOT Verified**:
- Xray actually reads the policy config
- Xray actually applies speed limits to connections
- Actual throughput matches configured limits
- Protocol overhead tolerance
- Multiple users with different limits
- Live policy updates affect throughput

---

## What Runtime Verification Requires

### Infrastructure Needed

1. **Xray Binary**
   - Download from: https://github.com/XTLS/Xray-core/releases
   - Or build from source
   - Platform: Windows/Linux/macOS
   - Version: Latest stable

2. **Xray Server Config**
   ```json
   {
     "inbounds": [{
       "port": 10086,
       "protocol": "vless",
       "settings": {
         "clients": [{
           "id": "test-uuid",
           "email": "test@example.com",
           "level": 1  // References policy.levels["1"]
         }]
       }
     }],
     "policy": {
       "levels": {
         "1": {
           "upSpeed": 640000,     // 5 Mbps upload limit
           "downSpeed": 1280000   // 10 Mbps download limit
         }
       }
     }
   }
   ```

3. **Xray Client**
   - Same binary, different config
   - Connects to server on localhost
   - Proxy traffic through connection

4. **Traffic Generator**
   - HTTP server serving large file (100+ MB)
   - Or iperf3 for raw throughput
   - Or custom TCP stream generator

5. **Throughput Measurement**
   - Track bytes transferred
   - Measure elapsed time
   - Calculate bytes/sec
   - Compare to configured limit

### Test Design

```go
func TestXraySpeedLimitRuntimeVerification(t *testing.T) {
    // Skip if Xray not available
    xrayPath, err := exec.LookPath("xray")
    if err != nil {
        t.Skip("Xray binary not available")
    }
    
    // 1. Start Xray server with speed limit
    serverConfig := generateServerConfig(t, uploadLimit: 5*1024*1024/8) // 5 Mbps
    serverCmd := exec.Command(xrayPath, "run", "-config", serverConfig)
    serverCmd.Start()
    defer serverCmd.Process.Kill()
    
    time.Sleep(2 * time.Second) // Wait for server startup
    
    // 2. Start Xray client
    clientConfig := generateClientConfig(t)
    clientCmd := exec.Command(xrayPath, "run", "-config", clientConfig)
    clientCmd.Start()
    defer clientCmd.Process.Kill()
    
    time.Sleep(2 * time.Second) // Wait for client startup
    
    // 3. Generate upload traffic through proxy
    proxyURL := "socks5://127.0.0.1:10808"
    startTime := time.Now()
    bytesTransferred := generateUploadTraffic(t, proxyURL, duration: 20*time.Second)
    elapsed := time.Since(startTime)
    
    // 4. Calculate actual throughput
    actualBytesPerSec := float64(bytesTransferred) / elapsed.Seconds()
    
    // 5. Verify within tolerance
    configuredLimit := 5 * 1024 * 1024 / 8 // 5 Mbps = 640000 bytes/sec
    tolerance := 1.10 // 10% for protocol overhead
    
    if actualBytesPerSec > float64(configuredLimit) * tolerance {
        t.Errorf("Speed limit violated: actual %.0f bytes/sec > limit %.0f bytes/sec",
            actualBytesPerSec, float64(configuredLimit) * tolerance)
    }
    
    t.Logf("Speed limit enforced: actual %.0f bytes/sec, limit %.0f bytes/sec",
        actualBytesPerSec, float64(configuredLimit))
}
```

### Test Scenarios Required

1. **Upload Speed Limit**
   - Configure 5 Mbps upload limit
   - Generate 20s sustained upload traffic
   - Measure actual upload throughput
   - Verify: actual ≤ 5 Mbps × 1.10

2. **Download Speed Limit**
   - Configure 10 Mbps download limit
   - Generate 20s sustained download traffic
   - Measure actual download throughput
   - Verify: actual ≤ 10 Mbps × 1.10

3. **No Limit Baseline**
   - Configure no speed limit
   - Generate 20s traffic
   - Measure baseline throughput
   - Verify: baseline significantly higher than limited

4. **Multiple Users**
   - User A: 5 Mbps limit
   - User B: 10 Mbps limit
   - User C: no limit
   - Concurrent traffic from all 3
   - Verify each respects their limit

5. **Live Limit Update**
   - Start with 10 Mbps limit
   - Measure throughput (should be ~10 Mbps)
   - Update limit to 2 Mbps
   - Reload Xray config
   - Measure throughput (should be ~2 Mbps)

---

## Honest Classification

### Current Status: CONFIGURED

**Evidence**:
- ✅ Config generation works (tested)
- ✅ kbps → bytes/sec conversion correct (tested)
- ✅ Policy levels assigned (tested)
- ✅ Config included in plan (tested)
- ❌ Runtime throughput UNVERIFIED (blocked by missing Xray binary)

### Cannot Claim ENFORCED Until

1. Xray binary available
2. Runtime tests implemented
3. Real traffic generated
4. Actual throughput measured
5. Throughput verified within tolerance
6. Tests pass in CI

---

## Alternative Approaches Considered

### Option 1: Mock Xray Binary ❌

**Idea**: Create fake "xray" binary that pretends to enforce limits

**Rejected**: This would be **dishonest** - we'd be faking enforcement verification

### Option 2: Assume Xray Works ❌

**Idea**: Trust Xray documentation, classify as ENFORCED without tests

**Rejected**: This violates Phase 6 requirement for **real runtime verification**

### Option 3: Docker-Based CI Tests ⚠️

**Idea**: Run tests in Docker container with Xray binary

**Status**: Feasible but requires:
- Docker environment
- Xray Docker image
- Network setup in container
- CI/CD integration

**Recommendation**: Implement in future CI pipeline

### Option 4: Manual Verification ⚠️

**Idea**: Manually test with Xray, document results

**Status**: Honest but not automated

**Process**:
1. Install Xray manually
2. Configure speed limits
3. Generate traffic manually
4. Measure throughput with tools
5. Document actual measurements
6. Classify as ENFORCED with manual verification note

---

## Recommended Path Forward

### Phase 6 M2 Deliverables (Without Xray Binary)

1. **Document Runtime Test Design** ✅ (this file)
   - Test infrastructure requirements
   - Test scenario design
   - Measurement approach
   - Tolerance calculation

2. **Document Current Limitations** ✅
   - Xray binary not available
   - Cannot run runtime tests
   - Classification remains CONFIGURED

3. **Document Manual Verification Process** ✅
   - Steps for manual testing
   - Tools required
   - Measurement procedure

4. **Implement CI Test Framework** (skeleton)
   - Test code that skips when Xray unavailable
   - Ready to run when Xray binary added
   - Documents exact requirements

5. **Honest Classification** ✅
   - Keep speed limits as CONFIGURED
   - Do NOT upgrade to ENFORCED without runtime tests
   - Document exactly what's missing

### Future Work (When Xray Available)

1. Add Xray binary to CI environment
2. Run automated runtime tests
3. Verify throughput enforcement
4. Upgrade classification to ENFORCED
5. Add to regression test suite

---

## Phase 6 M2 Status

**Milestone**: Xray Runtime Enforcement  
**Status**: ⏳ **BLOCKED** - Xray binary not available  
**Blocker**: Cannot verify runtime throughput without actual Xray process  

**What's Complete**:
- ✅ Configuration generation (Phase 5)
- ✅ Enforcer integration (Phase 5)
- ✅ Test design documented (Phase 6 M2)
- ✅ Infrastructure requirements documented (Phase 6 M2)
- ✅ Manual verification process documented (Phase 6 M2)

**What's Blocked**:
- ❌ Automated runtime traffic tests (need Xray binary)
- ❌ Throughput measurement (need Xray binary)
- ❌ Speed limit verification (need Xray binary)
- ❌ Classification upgrade to ENFORCED (need tests)

**Honest Assessment**:
Speed limits remain **CONFIGURED** (not **ENFORCED**) until runtime tests can verify actual throughput. Configuration generation is correct, but we have not proven Xray actually enforces the limits at runtime.

**Next Steps**:
1. Document this blocker in Phase 6 report
2. Move to M3: Real Bandwidth Enforcement (external tc/nftables approach)
3. Move to M4: Immediate Quota Enforcement (can implement without Xray binary)
4. Return to M2 when Xray binary becomes available

---

## Classification Decision

**Xray Speed Limits**: **CONFIGURED** (not ENFORCED)

**Reason**: Runtime behavior unverified due to missing Xray binary

**Evidence**: Configuration generation tests pass, but no runtime throughput tests

**To Upgrade to ENFORCED**:
1. Add Xray binary to environment
2. Implement runtime traffic tests
3. Verify throughput respects limits
4. Tests must pass in CI
5. Then and only then: upgrade classification

**Honest Reporting**: We will NOT fake enforcement verification. Speed limits remain CONFIGURED until actually verified with real traffic.

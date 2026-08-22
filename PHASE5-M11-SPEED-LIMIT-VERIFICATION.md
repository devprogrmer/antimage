# Phase 5 M11: Speed Limit Runtime Verification

**Status**: CONFIGURED - Runtime verification needed  
**Classification**: Cannot claim ENFORCED without real traffic tests  

---

## Current Implementation

### Xray Speed Limit Configuration

**File**: `internal/node/adapter/xray/policy.go`

**How It Works**:
1. Panel generates policy config with per-user speed limits
2. Speed limits converted from kbps to bytes/sec:
   ```go
   bytesPerSec := (*subj.SpeedLimitUpKbps * 1024) / 8
   ```
3. Each subject assigned to policy level = subject ID
4. Xray policy levels contain `upSpeed` and `downSpeed` fields
5. Config written to Xray runtime

**Example Generated Config**:
```json
{
  "policy": {
    "levels": {
      "0": {
        "statsUserUplink": true,
        "statsUserDownlink": true
      },
      "42": {
        "statsUserUplink": true,
        "statsUserDownlink": true,
        "upSpeed": 128000,      // 1 Mbps upload (1024 kbps * 1024 / 8)
        "downSpeed": 640000     // 5 Mbps download (5120 kbps * 1024 / 8)
      }
    },
    "system": {
      "statsInboundUplink": true,
      "statsInboundDownlink": true
    }
  }
}
```

**User-to-Level Assignment**:
```json
{
  "inbounds": [{
    "settings": {
      "clients": [
        {
          "id": "uuid-here",
          "email": "user@example.com",
          "level": 42  // References policy.levels["42"]
        }
      ]
    }
  }]
}
```

---

## What's Missing: Runtime Verification

### Problem

**We configure speed limits, but have no proof they're enforced at runtime.**

- Configuration is written ✅
- Xray accepts the config ✅
- **Actual bandwidth shaping?** ❓ (UNVERIFIED)

### Required Test

```go
func TestXraySpeedLimitEnforcement(t *testing.T) {
    // 1. Start Xray with speed limit (e.g., 5 Mbps down)
    xray := startXrayWithPolicy(t, speedLimitDown: 5*1024*1024/8) // 5 Mbps = 640000 bytes/sec
    
    // 2. Establish connection as limited user
    conn := connectAsUser(t, xray, "limited_user")
    
    // 3. Generate sustained download traffic (20 seconds minimum)
    traffic := generateDownloadTraffic(t, conn, duration: 20*time.Second)
    
    // 4. Measure actual throughput
    actualBytesPerSec := traffic.totalBytes / traffic.duration.Seconds()
    
    // 5. Verify throughput respects limit (with tolerance)
    configuredLimit := 640000 // bytes/sec
    tolerance := 1.10         // Allow 10% overhead for protocol framing
    
    if actualBytesPerSec > configuredLimit * tolerance {
        t.Errorf("Speed limit violated: actual %d bytes/sec > limit %d bytes/sec",
            actualBytesPerSec, configuredLimit)
    }
    
    // 6. Verify limit is enforced (not just slow network)
    // Generate traffic to unlimited user, should be much faster
    unlimitedConn := connectAsUser(t, xray, "unlimited_user")
    unlimitedTraffic := generateDownloadTraffic(t, unlimitedConn, duration: 10*time.Second)
    unlimitedBytesPerSec := unlimitedTraffic.totalBytes / unlimitedTraffic.duration.Seconds()
    
    if unlimitedBytesPerSec <= actualBytesPerSec * 1.5 {
        t.Errorf("Speed limit not working: unlimited user (%d bps) not significantly faster than limited (%d bps)",
            unlimitedBytesPerSec, actualBytesPerSec)
    }
}
```

### Required Infrastructure

1. **Xray Runtime**:
   - Start actual Xray process with test config
   - Generate policy config with known limits
   - Load config into Xray

2. **Traffic Generator**:
   - HTTP download server (large file response)
   - Sustained traffic (minimum 20 seconds for accurate measurement)
   - Protocol support (VLESS, VMess, Trojan)

3. **Throughput Measurement**:
   - Track bytes transferred and elapsed time
   - Calculate bytes/sec
   - Account for protocol overhead (TCP framing, encryption)

4. **Test Scenarios**:
   - ✅ Limited user respects download limit
   - ✅ Limited user respects upload limit
   - ✅ Unlimited user has no artificial limit
   - ✅ Limit change takes effect (update policy, reload Xray, verify new limit)
   - ✅ Multiple users with different limits don't interfere

---

## Measurement Tolerance

### Why Tolerance Matters

1. **Protocol Overhead**:
   - TCP headers: ~5% overhead
   - TLS encryption: ~2-3% overhead
   - Proxy protocol framing: ~1-2% overhead
   - **Total overhead**: ~10%

2. **Timing Precision**:
   - Network jitter: ±50ms typical
   - Measurement granularity: ±100ms
   - Xray internal buffering: variable

3. **Burst Behavior**:
   - TCP slow start: initial burst above limit
   - Buffer draining: may exceed limit briefly
   - Need sustained measurement (20+ seconds)

### Recommended Tolerance

**Download Limit**: 110% of configured (allow protocol overhead)  
**Upload Limit**: 110% of configured  
**Measurement Duration**: 20 seconds minimum  
**Violation Threshold**: Average over 10-second windows  

### Classification Criteria

| Measured Throughput | Classification |
|---------------------|----------------|
| ≤ limit × 1.10 | **ENFORCED** |
| > limit × 1.10 and ≤ limit × 1.50 | **BEST_EFFORT** |
| > limit × 1.50 | **NOT_ENFORCED** |

---

## Current Classification: CONFIGURED

**Reasoning**:
- Configuration generation: ✅ Working
- Xray accepts policy config: ✅ Assumed (no errors observed)
- Runtime enforcement: ❓ **NOT VERIFIED**

**Cannot claim ENFORCED until**:
1. Real traffic tests measure actual throughput
2. Measured throughput respects configured limits
3. Tests run in CI for regression detection

---

## Other Protocols

### Sing-box

**Status**: TODO  
**API**: Sing-box has similar policy system but different config schema  
**Verification**: Same runtime traffic tests needed  

### Hysteria2

**Status**: TODO  
**API**: Built-in bandwidth control (`up_mbps`, `down_mbps`)  
**Verification**: Same runtime traffic tests needed  

### WireGuard

**Status**: UNSUPPORTED  
**Reason**: WireGuard is kernel-level VPN with no application-layer speed limits  
**Workaround**: Could use tc (traffic control) on Linux, but not cross-platform  

### L2TP/IPsec

**Status**: UNSUPPORTED  
**Reason**: Kernel-level VPN, same issue as WireGuard  

---

## Blockers

1. **Xray Runtime**: Tests need actual Xray binary and runtime environment
2. **Traffic Generation**: Need HTTP server or other traffic source
3. **Time**: Each test needs 20+ seconds for accurate measurement
4. **Platform**: Tests may be OS-specific (network stack differences)

---

## Recommendation

**Mark as CONFIGURED, not ENFORCED**

Add to enforcement matrix:
```markdown
| Feature | Xray | Sing-box | Hysteria2 | WireGuard | L2TP/IPsec |
|---------|------|----------|-----------|-----------|------------|
| Upload Speed | CONFIGURED | TODO | TODO | UNSUPPORTED | UNSUPPORTED |
| Download Speed | CONFIGURED | TODO | TODO | UNSUPPORTED | UNSUPPORTED |
```

**Why CONFIGURED**:
- Config generation works
- Xray accepts config without errors
- **Runtime enforcement not verified with real traffic**

**To upgrade to ENFORCED**:
- Implement runtime traffic tests
- Verify throughput respects limits within tolerance
- Add to CI pipeline

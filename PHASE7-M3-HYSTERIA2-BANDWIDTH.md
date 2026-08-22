# Phase 7 M3 - Hysteria2 Bandwidth Verification

**Date**: 2026-08-22  
**Status**: Test framework created, runtime verification REQUIRED

---

## Critical Finding

**Hysteria2 has `bandwidth.up` and `bandwidth.down` config fields** (lines 111-120 in config.go):

```yaml
bandwidth:
  up: 5 mbps
  down: 100 mbps
```

**Classification Status**: **CONFIGURED** (not ENFORCED)

**Why CONFIGURED, not ENFORCED**: Xray taught us that config acceptance ≠ runtime enforcement
- Xray accepted `upSpeed`/`downSpeed` fields
- Xray validated config as "Configuration OK"
- Xray runtime test: 343 Mbps measured vs 5 Mbps configured (68.6x over limit)
- Verdict: Xray silently ignores speed fields

**Hysteria2 MUST be tested the same way** before claiming ENFORCED.

---

## What We Know

### ✅ Config Generation Working

**Evidence**: internal/node/adapter/hysteria2/config.go:111-120
```go
// Optional: Bandwidth
if params.UpMbps > 0 || params.DownMbps > 0 {
    bandwidth := make(map[string]string)
    if params.UpMbps > 0 {
        bandwidth["up"] = fmt.Sprintf("%d mbps", params.UpMbps)
    }
    if params.DownMbps > 0 {
        bandwidth["down"] = fmt.Sprintf("%d mbps", params.DownMbps)
    }
    config["bandwidth"] = bandwidth
}
```

**Test**: internal/node/adapter/hysteria2/runtime_bandwidth_test.go:TestHysteria2BandwidthConfigGeneration
- ✅ Verifies `bandwidth:` section present in YAML
- ✅ Verifies `up: 10 mbps` and `down: 50 mbps` formatted correctly
- ⚠️ Does NOT verify runtime enforcement

### ❌ Runtime Enforcement Unverified

**Blocker**: No Hysteria2 binary in test environment

**Required Test**: internal/node/adapter/hysteria2/runtime_bandwidth_test.go:TestHysteria2RuntimeBandwidthEnforcement
- Generate config with 5 Mbps upload limit
- Start Hysteria2 server with real binary
- Connect client and upload sustained traffic
- Measure actual throughput over 5 seconds
- Verify measured ≈ configured (within ±20%)

**Current Status**: Test skeleton created, marked `t.Skip()` pending binary

---

## Test Strategy

### Phase 1: Baseline (No Limit)

**Goal**: Prove Hysteria2 can achieve high throughput

**Config**:
```yaml
listen: :20001
tls:
  cert: /tmp/cert.pem
  key: /tmp/key.pem
auth:
  type: password
  password: testpass123
# NO bandwidth section
```

**Expected**: >100 Mbps on localhost

### Phase 2: Upload Limit (5 Mbps)

**Goal**: Test if bandwidth.up is enforced

**Config**:
```yaml
# Same as baseline, plus:
bandwidth:
  up: 5 mbps
  down: 100 mbps
```

**Test**:
1. Upload random data for 5 seconds
2. Measure: `totalBytes / 5 seconds = measuredBps`
3. Convert: `measuredBps * 8 / 1024 / 1024 = measuredMbps`

**Verdict**:
- **If measuredMbps ≈ 5** (within 4-6 Mbps): ✅ **ENFORCED**
  - Upgrade classification to ENFORCED
  - Document that Hysteria2 native bandwidth works
  - Still provide tc as alternative for flexibility

- **If measuredMbps >50**: ❌ **UNSUPPORTED**
  - Downgrade classification to UNSUPPORTED
  - Document like Xray (config accepted, ignored at runtime)
  - Use tc external enforcement exclusively

### Phase 3: Download Limit (if Upload passes)

**Config**:
```yaml
bandwidth:
  up: 100 mbps
  down: 5 mbps
```

**Test**: Download large file, measure throughput

---

## Implementation Blockers

### 1. Hysteria2 Binary

**Status**: Not in test environment, not in PATH

**Options**:
- A. Download from https://hysteria.network/ (official releases)
- B. Compile from source: github.com/apernet/hysteria
- C. Use Docker image: tobyxdd/hysteria
- D. Manual testing on separate Linux system

**Recommended**: Download official binary to test/bin/hysteria

### 2. Hysteria2 Protocol Client

**Challenge**: Need QUIC client to upload traffic through server

**Options**:
- A. Use official Hysteria2 client binary (SOCKS5 proxy mode)
- B. Implement minimal QUIC client with quic-go library
- C. Shell out to hysteria client binary from test
- D. Manual testing with curl through SOCKS5 proxy

**Recommended**: Use official client binary in SOCKS5 mode

### 3. TLS Certificates

**Solution**: Generate self-signed cert for testing
```bash
openssl req -x509 -newkey rsa:2048 -nodes \
  -keyout /tmp/hysteria-test-key.pem \
  -out /tmp/hysteria-test-cert.pem \
  -days 1 -subj "/CN=localhost"
```

---

## Manual Verification Guide

**Created**: internal/node/adapter/hysteria2/runtime_bandwidth_test.go:hysteria2BandwidthVerificationGuide

**Steps**:
1. Generate TLS certificate
2. Create server config with bandwidth limit
3. Create client config
4. Run baseline test (no limit) → measure >100 Mbps
5. Run limited test (5 Mbps) → measure actual throughput
6. Compare: measured vs configured
7. Document verdict: ENFORCED or UNSUPPORTED

**Time Required**: ~30 minutes (including binary download and setup)

---

## Honest Classification

**Current Status**: CONFIGURED (waiting for runtime verification)

| Scenario | Classification | Evidence |
|----------|----------------|----------|
| **Config generation** | ✅ CONFIGURED | bandwidth.up/down in YAML, test passes |
| **Runtime enforcement** | ⚠️ UNVERIFIED | No runtime test yet |
| **If test shows ~5 Mbps** | → ENFORCED | Native bandwidth working |
| **If test shows >50 Mbps** | → UNSUPPORTED | Config ignored (like Xray) |

**DO NOT classify as ENFORCED until runtime test passes**

---

## Comparison: Xray vs Hysteria2

| Protocol | Config Fields | Config Accepted | Runtime Test | Verdict |
|----------|---------------|-----------------|--------------|---------|
| **Xray** | upSpeed, downSpeed | ✅ YES | ❌ IGNORED (343 vs 5 Mbps) | UNSUPPORTED |
| **Hysteria2** | bandwidth.up, bandwidth.down | ✅ YES | ⚠️ NOT TESTED | UNKNOWN |

**Lesson from Xray**: Config validation means nothing without runtime verification

---

## Next Steps

### Option A: Runtime Test (Recommended)

1. Obtain Hysteria2 binary (download or compile)
2. Run test/bin/hysteria or add to PATH
3. Uncomment `t.Skip()` in TestHysteria2RuntimeBandwidthEnforcement
4. Run: `go test -v ./internal/node/adapter/hysteria2 -run RuntimeBandwidth`
5. Analyze results, update classification

**Effort**: 30-60 minutes  
**Impact**: Definitive ENFORCED vs UNSUPPORTED classification

### Option B: Manual Verification

1. Follow manual guide in runtime_bandwidth_test.go
2. Test on separate Linux system with Hysteria2 installed
3. Document results in PHASE7-M3-HYSTERIA2-RESULTS.md
4. Update ENFORCEMENT-CAPABILITY-MATRIX.md based on findings

**Effort**: 30-60 minutes  
**Impact**: Same as Option A, just manual

### Option C: Conservative Classification (If blocked)

1. Keep classification as CONFIGURED
2. Document in matrix: "Runtime unverified, assume UNSUPPORTED until tested"
3. Implement tc external enforcement only
4. Revisit when Hysteria2 binary available

**Effort**: 5 minutes  
**Impact**: Safe but incomplete

---

## Recommendation

**Do Option A or B before proceeding to M4**

**Rationale**:
- Hysteria2 is designed for high-speed circumvention with built-in bandwidth control
- Unlike Xray (general-purpose proxy), Hysteria2 bandwidth is a core feature
- Higher probability that it actually works
- If it works: huge value (native enforcement, no tc dependency)
- If it doesn't: critical to know early (avoid false claims)

**Estimated likelihood**:
- ENFORCED: 70% (Hysteria2 designed for this)
- UNSUPPORTED: 30% (like Xray, config cosmetic)

**Time investment**: 30-60 minutes for definitive answer

---

**Status**: M3 test framework complete, awaiting runtime verification  
**Next**: M4 - WireGuard production integration (can proceed in parallel)

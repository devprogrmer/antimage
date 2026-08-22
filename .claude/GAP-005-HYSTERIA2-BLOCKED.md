# GAP-005: Hysteria2 Verification - Blocked Status

**Date:** 2026-08-22  
**Status:** BLOCKED - Requires Hysteria2 Binary  
**Priority:** P1  
**Blocker:** Hysteria2 binary not available in test environment  

## Current Situation

The Hysteria2 adapter is **implemented** but **unverified** at runtime. Per Phase 9 M15 assessment:
- Configuration generation: IMPLEMENTED
- Service lifecycle: IMPLEMENTED (via systemd)
- Bandwidth enforcement: ❓ UNVERIFIED (classification unknown)
- Connection limits: ❓ UNVERIFIED
- Traffic accounting: ❓ UNVERIFIED

## Blocker Details

**Missing Component:** Hysteria2 server binary

```bash
$ which hysteria2
Hysteria2 binary not in PATH

$ which hysteria
Hysteria2 binary not in PATH
```

**Test Environment:** Windows 11 (dev machine)
- Hysteria2 is Linux/macOS binary
- Runtime tests require Linux VM or container
- Binary download: https://github.com/apernet/hysteria/releases

## Verification Required Before Production Classification

### Critical Tests (Must Pass)

1. **Bandwidth Enforcement Test**
   - Location: `internal/node/adapter/hysteria2/runtime_bandwidth_test.go`
   - Status: SKIPPED (requires binary)
   - Purpose: Verify bandwidth.up/down config actually enforced
   - Lesson from Xray: Config acceptance ≠ runtime enforcement

2. **Connection Limit Test**
   - Status: NOT IMPLEMENTED
   - Purpose: Verify concurrent connection limits work
   - Classification depends on this

3. **Traffic Accounting Test**
   - Status: NOT IMPLEMENTED
   - Purpose: Verify traffic metrics collected accurately
   - Integration with panel quota system

4. **Authentication Test**
   - Status: NOT IMPLEMENTED
   - Purpose: Verify password auth works correctly

## Manual Verification Procedure (When Binary Available)

### Prerequisites
```bash
# Download Hysteria2 binary
wget https://github.com/apernet/hysteria/releases/download/app/v2.x.x/hysteria-linux-amd64
chmod +x hysteria-linux-amd64
mv hysteria-linux-amd64 /usr/local/bin/hysteria

# Generate test TLS certificate
openssl req -x509 -newkey rsa:2048 -nodes -keyout /tmp/hy2-key.pem -out /tmp/hy2-cert.pem -days 365 -subj "/CN=test.local"
```

### Test 1: Bandwidth Enforcement (Upload)

```bash
# 1. Create server config with 5 Mbps upload limit
cat > /tmp/hy2-server.yaml <<EOF
listen: :20001
tls:
  cert: /tmp/hy2-cert.pem
  key: /tmp/hy2-key.pem
auth:
  type: password
  password: testpass123
bandwidth:
  up: 5 mbps
  down: 100 mbps
EOF

# 2. Start server
hysteria server -c /tmp/hy2-server.yaml &
SERVER_PID=$!

# 3. Create client config
cat > /tmp/hy2-client.yaml <<EOF
server: 127.0.0.1:20001
auth: testpass123
tls:
  insecure: true
bandwidth:
  up: 50 mbps
  down: 50 mbps
socks5:
  listen: 127.0.0.1:1080
EOF

# 4. Start client
hysteria client -c /tmp/hy2-client.yaml &
CLIENT_PID=$!

# 5. Upload test (via SOCKS5 proxy)
# Use curl with SOCKS5 + local HTTP server to measure throughput
# Expected: ~5 Mbps (640 KB/s) actual throughput
# If higher: bandwidth NOT enforced (classify as UNSUPPORTED)
# If correct: bandwidth ENFORCED (classify as ENFORCED)

# 6. Cleanup
kill $CLIENT_PID $SERVER_PID
```

### Test 2: Download Bandwidth

Same as above but reverse:
- Server: `bandwidth.down: 5 mbps`
- Download large file via SOCKS5
- Measure actual throughput
- Verify ≈ 5 Mbps

### Test 3: Connection Limits (If Supported)

Check Hysteria2 documentation for connection limit configuration. If available:
- Configure max 2 concurrent connections
- Attempt 3 concurrent connections
- Verify 3rd rejected

### Test 4: Traffic Accounting

- Upload known data (e.g., 50 MB)
- Check if Hysteria2 provides traffic stats API or logs
- Verify accuracy of reported bytes vs actual

## Classification Decision Tree

```
bandwidth.up/down enforced?
├─ YES → bandwidth: ENFORCED
└─ NO  → bandwidth: UNSUPPORTED (use tc fallback)

connection limits supported?
├─ YES → connection_limit: ENFORCED
├─ PARTIAL → connection_limit: BEST_EFFORT
└─ NO  → connection_limit: UNSUPPORTED

traffic stats available?
├─ YES + accurate → quota: ENFORCED
└─ NO/inaccurate → quota: UNSUPPORTED
```

## Current Classification (Provisional)

Until runtime verification completes:

| Feature | Classification | Confidence |
|---------|---------------|------------|
| Quota Enforcement | ❓ UNKNOWN | 0% - not tested |
| Bandwidth Limiting | ❓ UNKNOWN | 0% - not tested |
| Connection Limits | ❓ UNKNOWN | 0% - not tested |
| Device Limits | ❓ UNKNOWN | 0% - not tested |

**Phase 9 Verdict:** "❓ UNVERIFIED (requires manual testing)"

## Impact of Blocked Status

**Production Risk:**
- Cannot confidently deploy Hysteria2 in production
- Cannot claim bandwidth enforcement works
- Users may select protocol expecting features that don't work
- Potential quota bypass if accounting broken

**Recommended Action:**
- Document Hysteria2 as EXPERIMENTAL/UNVERIFIED
- Recommend Xray or WireGuard for production (verified)
- Block Hysteria2 selection in production until verified

## Unblocking GAP-005

**Option 1: Linux VM Testing (Recommended)**
- Provision Ubuntu 22.04 VM
- Install Hysteria2 binary
- Run runtime verification tests
- Document actual enforcement behavior
- Update protocol classification matrix
- Estimated: 4-6 hours

**Option 2: Accept Limitation**
- Document Hysteria2 as UNVERIFIED
- Mark as experimental-only
- Defer verification to future release
- Focus on verified protocols (Xray, WireGuard, L2TP)

**Option 3: Community Verification**
- Request Hysteria2 community test results
- Document known behavior from upstream
- Still requires local verification before production claim

## Next Steps (Blocked)

Since GAP-005 is BLOCKED, moving to next actionable gap per execution order:

**Recommended Next Gap:** GAP-003 (Failure Injection Framework)
- Priority: P0
- Blockers: None
- Can proceed immediately
- Estimated: 18 hours

**Alternative:** GAP-006 (TC Integration)
- Priority: P1
- Blockers: Requires Linux (same as Hysteria2)
- Can implement on Windows (code), test on Linux later

**Alternative:** GAP-004 (API Documentation)
- Priority: P1
- Blockers: None
- Can proceed immediately on Windows
- Estimated: 16 hours

---

**GAP-005 Status:** BLOCKED (requires Linux + Hysteria2 binary)  
**Recommended:** Defer to Linux testing phase, proceed with GAP-003 or GAP-004

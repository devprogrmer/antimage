# Phase 6 Milestone 1: Real Enforcement Baseline Audit

**Date**: 2026-08-22  
**Status**: Baseline assessment before Phase 6 implementation  

---

## Current Enforcement Status (From Phase 5)

### Classification Legend

- **ENFORCED**: Runtime behavior verified with passing tests
- **CONFIGURED**: Configuration written correctly, runtime behavior unverified
- **PROPAGATED**: Desired state propagated to nodes, enforcement unverified
- **OBSERVED**: Data collected but no enforcement action
- **BEST_EFFORT**: Retroactive enforcement with polling window
- **UNSUPPORTED**: Technical limitation prevents implementation

---

## Protocol-by-Protocol Current Status

### Xray

| Feature | Current Status | Evidence | Gap |
|---------|---------------|----------|-----|
| Authentication | ENFORCED | Protocol-level auth, users added via API | ✅ None |
| User Admission | BEST_EFFORT | Stats API polling (5-10s window), retroactive termination | ⚠️ Not proactive |
| Connection Tracking | BEST_EFFORT | Polls stats API every 5-10s | ⚠️ Polling window |
| MaxConnections | BEST_EFFORT | Enforcer checks, retroactive via RemoveUser() | ⚠️ Retroactive (5-10s) |
| MaxIPs | UNSUPPORTED | Stats API doesn't expose source IPs | ❌ API limitation |
| MaxDevices | UNSUPPORTED | Stats API doesn't expose device fingerprints | ❌ API limitation |
| Upload Speed Limit | CONFIGURED | Policy config written, kbps→bytes/sec correct | ❌ Runtime unverified |
| Download Speed Limit | CONFIGURED | Policy config written, kbps→bytes/sec correct | ❌ Runtime unverified |
| Traffic Accounting | OBSERVED | Stats API provides uplink/downlink bytes | ✅ None (read-only) |
| Quota | ENFORCED | Panel auto-freeze (5-min sweeper) | ⚠️ Not immediate |
| Revoke | ENFORCED | RemoveUser() API call, immediate | ✅ None |
| Live Disconnect | ENFORCED | RemoveUser() terminates session | ✅ None |
| Restart Reconciliation | ENFORCED | State rebuilt from desired state | ✅ None |

**Files**:
- `internal/node/adapter/xray/enforcement.go` (188 lines)
- `internal/node/adapter/xray/policy.go` (231 lines)

**Key Gap**: Speed limits CONFIGURED not ENFORCED (no runtime traffic tests)

---

### Sing-box

| Feature | Current Status | Evidence | Gap |
|---------|---------------|----------|-----|
| Authentication | CONFIGURED | Adapter generates user config | ❌ Runtime unverified |
| User Admission | TODO | Adapter exists, no enforcement integration | ❌ Not implemented |
| Connection Tracking | TODO | No implementation | ❌ Not implemented |
| MaxConnections | TODO | No implementation | ❌ Not implemented |
| MaxIPs | TODO | No implementation | ❌ Not implemented |
| MaxDevices | TODO | No implementation | ❌ Not implemented |
| Upload Speed Limit | TODO | No implementation | ❌ Not implemented |
| Download Speed Limit | TODO | No implementation | ❌ Not implemented |
| Traffic Accounting | TODO | No implementation | ❌ Not implemented |
| Quota | TODO | No implementation | ❌ Not implemented |
| Revoke | TODO | No implementation | ❌ Not implemented |
| Live Disconnect | TODO | No implementation | ❌ Not implemented |
| Restart Reconciliation | TODO | No implementation | ❌ Not implemented |

**Files**:
- `internal/node/adapter/singbox/adapter.go` (exists but minimal)

**Key Gap**: Adapter exists, but zero runtime enforcement

---

### Hysteria2

| Feature | Current Status | Evidence | Gap |
|---------|---------------|----------|-----|
| Authentication | CONFIGURED | Adapter generates config | ❌ Runtime unverified |
| User Admission | TODO | No implementation | ❌ Not implemented |
| Connection Tracking | TODO | No implementation | ❌ Not implemented |
| MaxConnections | TODO | No implementation | ❌ Not implemented |
| MaxIPs | TODO | No implementation | ❌ Not implemented |
| MaxDevices | TODO | No implementation | ❌ Not implemented |
| Upload Speed Limit | TODO | No implementation | ❌ Not implemented |
| Download Speed Limit | TODO | No implementation | ❌ Not implemented |
| Traffic Accounting | TODO | No implementation | ❌ Not implemented |
| Quota | TODO | No implementation | ❌ Not implemented |
| Revoke | TODO | No implementation | ❌ Not implemented |
| Live Disconnect | TODO | No implementation | ❌ Not implemented |
| Restart Reconciliation | TODO | No implementation | ❌ Not implemented |

**Files**:
- `internal/node/adapter/hysteria2/adapter.go` (exists but minimal)

**Key Gap**: Adapter exists, but zero runtime enforcement

---

### WireGuard

| Feature | Current Status | Evidence | Gap |
|---------|---------------|----------|-----|
| Authentication | CONFIGURED | Adapter generates peer config | ❌ Runtime unverified |
| Peer Tracking | TODO | No implementation | ❌ Not implemented |
| Connection Tracking | N/A | WireGuard has no "connections" (stateless) | ❌ Wrong abstraction |
| MaxConnections | N/A | WireGuard is peer-based, not connection-based | ❌ Wrong abstraction |
| MaxIPs | TODO | Could track allowed IPs per peer | ❌ Not implemented |
| MaxDevices | TODO | Could track peer count per subject | ❌ Not implemented |
| Upload Speed Limit | UNSUPPORTED | Kernel VPN, no application-layer control | ❌ Need tc/nftables |
| Download Speed Limit | UNSUPPORTED | Kernel VPN, no application-layer control | ❌ Need tc/nftables |
| Traffic Accounting | TODO | Could use wg show / kernel counters | ❌ Not implemented |
| Quota | TODO | No implementation | ❌ Not implemented |
| Revoke | TODO | Could remove peer | ❌ Not implemented |
| Live Disconnect | UNSUPPORTED | WireGuard has no "disconnect" (stateless) | ❌ Protocol limitation |
| Restart Reconciliation | TODO | No implementation | ❌ Not implemented |

**Files**:
- `internal/node/adapter/wireguard/adapter.go` (exists but minimal)

**Key Gap**: Wrong abstraction (connections vs peers), no enforcement, speed limits need external tools

---

### L2TP/IPsec

| Feature | Current Status | Evidence | Gap |
|---------|---------------|----------|-----|
| Authentication | CONFIGURED | Adapter generates config | ❌ Runtime unverified |
| Session Tracking | TODO | No implementation | ❌ Not implemented |
| Connection Tracking | TODO | No implementation | ❌ Not implemented |
| MaxConnections | TODO | No implementation | ❌ Not implemented |
| MaxIPs | TODO | No implementation | ❌ Not implemented |
| MaxDevices | TODO | No implementation | ❌ Not implemented |
| Upload Speed Limit | UNSUPPORTED | Kernel VPN, no application-layer control | ❌ Need tc/nftables |
| Download Speed Limit | UNSUPPORTED | Kernel VPN, no application-layer control | ❌ Need tc/nftables |
| Traffic Accounting | TODO | Could use xl2tpd/strongSwan logs | ❌ Not implemented |
| Quota | TODO | No implementation | ❌ Not implemented |
| Revoke | TODO | No implementation | ❌ Not implemented |
| Live Disconnect | TODO | No implementation | ❌ Not implemented |
| Restart Reconciliation | TODO | No implementation | ❌ Not implemented |

**Files**:
- `internal/node/adapter/l2tp/adapter.go` (exists but minimal)

**Key Gap**: Adapter exists, but zero runtime enforcement, speed limits need external tools

---

## Cross-Protocol Summary

### What's Actually ENFORCED (verified with tests)

1. **Atomic Admission Control** (enforcer layer)
   - CheckAndRegisterConnection() prevents TOCTOU
   - 13 race tests pass
   - Subject isolation verified

2. **Quota Auto-Freeze** (panel layer)
   - 5-minute sweeper freezes over-quota subjects
   - 3 tests pass
   - NOT immediate (5-min latency)

3. **Xray Revocation**
   - RemoveUser() API call
   - Immediate termination
   - Verified via existing tests

4. **Security Properties** (enforcer layer)
   - Integer overflow protection
   - Subject isolation
   - Device/IP spoofing prevention
   - Policy bypass prevention
   - 9 security tests pass

### What's CONFIGURED (not verified)

1. **Xray Speed Limits**
   - Config generation works
   - kbps → bytes/sec conversion correct
   - Policy levels assigned
   - **NO RUNTIME TRAFFIC TESTS**

2. **Other Protocol Configs**
   - Sing-box user config
   - Hysteria2 config
   - WireGuard peer config
   - L2TP/IPsec config
   - **ZERO RUNTIME ENFORCEMENT**

### What's BEST_EFFORT (polling/retroactive)

1. **Xray Connection Tracking**
   - Polls stats API every 5-10s
   - Retroactive termination via RemoveUser()
   - 5-10s enforcement window

2. **Xray MaxConnections**
   - Enforcer checks limit
   - Terminates retroactively if exceeded
   - 5-10s enforcement window

### What's OBSERVED (no enforcement)

1. **Xray Traffic Accounting**
   - Stats API provides bytes
   - Stored in database
   - No enforcement action

### What's UNSUPPORTED (technical limitations)

1. **Xray MaxDevices/MaxIPs**
   - Stats API doesn't expose device fingerprints
   - Stats API doesn't expose source IPs
   - Cannot retroactively detect violations

2. **WireGuard/L2TP Speed Limits**
   - Kernel-level VPN
   - No application-layer control
   - Need tc/nftables/eBPF

---

## Phase 6 Target State

### Xray (M2)

| Feature | Current | Target | Approach |
|---------|---------|--------|----------|
| Upload Speed | CONFIGURED | ENFORCED | Runtime traffic tests, measure throughput |
| Download Speed | CONFIGURED | ENFORCED | Runtime traffic tests, measure throughput |
| MaxConnections | BEST_EFFORT | ENFORCED | Proactive admission (before Xray accepts) |
| MaxIPs | UNSUPPORTED | BEST_EFFORT or EXTERNAL | Track at enforcer, or use nftables |
| MaxDevices | UNSUPPORTED | BEST_EFFORT or EXTERNAL | Track at enforcer, or use nftables |
| Quota | ENFORCED (5-min) | ENFORCED (immediate) | Check during admission |

### Sing-box (M5)

| Feature | Current | Target | Approach |
|---------|---------|--------|----------|
| All features | TODO/CONFIGURED | ENFORCED or UNSUPPORTED | Implement runtime enforcement, verify with tests |

### Hysteria2 (M6)

| Feature | Current | Target | Approach |
|---------|---------|--------|----------|
| All features | TODO/CONFIGURED | ENFORCED or UNSUPPORTED | Implement runtime enforcement, verify with tests |

### WireGuard (M7)

| Feature | Current | Target | Approach |
|---------|---------|--------|----------|
| Speed Limits | UNSUPPORTED | ENFORCED | External: tc/nftables/eBPF |
| Peer Tracking | TODO | ENFORCED | wg show / kernel counters |
| Traffic Accounting | TODO | OBSERVED | wg show / kernel counters |

### L2TP/IPsec (M8)

| Feature | Current | Target | Approach |
|---------|---------|--------|----------|
| Speed Limits | UNSUPPORTED | ENFORCED | External: tc/nftables/eBPF |
| Session Tracking | TODO | ENFORCED | xl2tpd/strongSwan integration |
| Traffic Accounting | TODO | OBSERVED | xl2tpd/strongSwan logs |

---

## Key Gaps to Close

### Gap 1: Speed Limits CONFIGURED → ENFORCED

**Problem**: Configuration is written, but runtime throughput is unverified.

**Solution**:
1. Start actual Xray runtime with test config
2. Establish real client connection
3. Generate sustained traffic (20+ seconds)
4. Measure actual throughput
5. Verify: actual ≤ configured × 1.10 (10% tolerance)
6. Repeat for upload and download
7. Test multiple users with different limits

**Required Infrastructure**:
- Xray binary
- Test client (Xray client or generic proxy client)
- Traffic generator (HTTP server, iperf, or custom)
- Throughput measurement tool

**Milestone**: M2 (Xray), M3 (bandwidth enforcement), M5-M8 (other protocols)

---

### Gap 2: Quota 5-Minute Sweeper → Immediate Enforcement

**Problem**: Subjects can exceed quota by up to 5 minutes of traffic before freeze.

**Solution**:
1. Add quota check to enforcer.CheckAndRegisterConnection()
2. Query current quota_used_bytes from database
3. Reject connection if quota_used_bytes >= quota_bytes
4. Keep 5-minute sweeper as backup
5. Test with real traffic crossing quota threshold

**Required Changes**:
- `internal/node/enforcement/enforcement.go`: Add quota check
- Pass quota context to enforcer (via gRPC desired state)
- Test immediate rejection

**Milestone**: M4 (Immediate Quota Enforcement)

---

### Gap 3: Xray BEST_EFFORT → ENFORCED

**Problem**: 5-10s polling window allows brief policy violations.

**Solution Options**:
1. **Proactive Admission**: Check enforcer BEFORE calling Xray AddUser()
   - Pros: No violations possible
   - Cons: Need to predict connection before protocol handshake
   
2. **Faster Polling**: Reduce polling interval to 1-2s
   - Pros: Smaller violation window
   - Cons: Higher CPU, still retroactive

3. **Accept BEST_EFFORT**: Document actual enforcement latency
   - Pros: Honest about limitations
   - Cons: Not true proactive enforcement

**Recommended**: Option 3 with option 2 (faster polling to 1-2s, honest classification as BEST_EFFORT with measured latency)

**Milestone**: M2 (Xray Runtime Enforcement)

---

### Gap 4: Other Protocols TODO → ENFORCED

**Problem**: Adapters exist but no runtime enforcement.

**Solution**: For each protocol:
1. Audit protocol API capabilities
2. Implement connection/session tracking
3. Implement traffic accounting
4. Implement revocation
5. Implement speed limits (native or external)
6. Write runtime tests
7. Verify with real traffic

**Milestones**: M5 (Sing-box), M6 (Hysteria2), M7 (WireGuard), M8 (L2TP/IPsec)

---

### Gap 5: UNSUPPORTED Features → External Enforcement

**Problem**: Some features cannot be implemented at application layer.

**Examples**:
- WireGuard speed limits (kernel VPN)
- L2TP/IPsec speed limits (kernel VPN)
- Xray MaxIPs/MaxDevices (stats API limitation)

**Solution**: External enforcement via:
- **tc (traffic control)**: Linux kernel traffic shaping
- **nftables**: Linux firewall, packet filtering/accounting
- **eBPF**: Kernel-level programmable enforcement
- **cgroups**: Resource isolation (where applicable)

**Approach**:
1. Evaluate which external tool fits best
2. Implement integration
3. Verify with runtime tests
4. Classify honestly (ENFORCED via external, not native)

**Milestone**: M3 (Real Bandwidth Enforcement), M7 (WireGuard), M8 (L2TP/IPsec)

---

## Before/After Enforcement Matrix

### Before Phase 6

| Feature | Xray | Sing-box | Hysteria2 | WireGuard | L2TP/IPsec |
|---------|------|----------|-----------|-----------|------------|
| Authentication | ENFORCED | CONFIGURED | CONFIGURED | CONFIGURED | CONFIGURED |
| Admission | BEST_EFFORT | TODO | TODO | TODO | TODO |
| MaxConnections | BEST_EFFORT | TODO | TODO | N/A | TODO |
| MaxIPs | UNSUPPORTED | TODO | TODO | TODO | TODO |
| MaxDevices | UNSUPPORTED | TODO | TODO | TODO | TODO |
| Upload Speed | CONFIGURED | TODO | TODO | UNSUPPORTED | UNSUPPORTED |
| Download Speed | CONFIGURED | TODO | TODO | UNSUPPORTED | UNSUPPORTED |
| Traffic Acct | OBSERVED | TODO | TODO | TODO | TODO |
| Quota | ENFORCED (5-min) | TODO | TODO | TODO | TODO |
| Revoke | ENFORCED | TODO | TODO | TODO | TODO |
| Disconnect | ENFORCED | TODO | TODO | UNSUPPORTED | TODO |
| Reconciliation | ENFORCED | TODO | TODO | TODO | TODO |

### After Phase 6 (Target)

| Feature | Xray | Sing-box | Hysteria2 | WireGuard | L2TP/IPsec |
|---------|------|----------|-----------|-----------|------------|
| Authentication | ENFORCED | ENFORCED | ENFORCED | ENFORCED | ENFORCED |
| Admission | ENFORCED | ENFORCED | ENFORCED | ENFORCED | ENFORCED |
| MaxConnections | ENFORCED | ENFORCED | ENFORCED | N/A (peer-based) | ENFORCED |
| MaxIPs | ENFORCED (external) | ENFORCED | ENFORCED | ENFORCED | ENFORCED |
| MaxDevices | ENFORCED (external) | ENFORCED | ENFORCED | ENFORCED (peers) | ENFORCED |
| Upload Speed | **ENFORCED** | ENFORCED | ENFORCED | ENFORCED (tc) | ENFORCED (tc) |
| Download Speed | **ENFORCED** | ENFORCED | ENFORCED | ENFORCED (tc) | ENFORCED (tc) |
| Traffic Acct | OBSERVED | OBSERVED | OBSERVED | OBSERVED | OBSERVED |
| Quota | **ENFORCED (immediate)** | ENFORCED | ENFORCED | ENFORCED | ENFORCED |
| Revoke | ENFORCED | ENFORCED | ENFORCED | ENFORCED | ENFORCED |
| Disconnect | ENFORCED | ENFORCED | ENFORCED | UNSUPPORTED | ENFORCED |
| Reconciliation | ENFORCED | ENFORCED | ENFORCED | ENFORCED | ENFORCED |

**Bold** = upgraded from CONFIGURED/BEST_EFFORT to ENFORCED

---

## Success Criteria

Phase 6 M1 complete when:

✅ Current status audited honestly  
✅ Every CONFIGURED/TODO/UNSUPPORTED identified  
✅ Target state defined  
✅ Gap closure approach documented  
✅ Before/after matrix created  

**Next**: M2 - Xray Runtime Enforcement (speed limits, immediate quota, proactive admission)

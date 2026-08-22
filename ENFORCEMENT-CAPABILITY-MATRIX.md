# Enforcement Capability Matrix - Production Audit

**Date:** 2026-08-22  
**Branch:** sp7-observability  
**Auditor:** Autonomous production verification  

---

## Executive Summary

**Critical Finding:** Enforcement is BEST_EFFORT, not real-time prevention.

**Classification System:**
- ✅ **ENFORCED** - Real-time admission control, cannot be bypassed
- ⚠️ **BEST_EFFORT** - Reactive termination within sync window (5-10s)
- 🔄 **PROPAGATED** - Configuration delivered to node, runtime integration incomplete
- 📋 **CONFIGURED** - Database/API exists, not yet propagated to runtime
- ❌ **UNSUPPORTED** - Protocol limitation, cannot be implemented

---

## Enforcement Architecture Analysis

### Current Flow (Xray)

```
User connects
    ↓
Xray accepts connection (NO admission control)
    ↓
Connection established and serving traffic
    ↓
Stats sync loop runs (every 5-10 seconds)
    ↓
Enforcer checks policy retroactively
    ↓
If violation: RemoveUser() terminates connection
```

**Gap:** 5-10 second window where policy violations can connect and use service.

### Ideal Flow (Not Implemented)

```
User connects
    ↓
Xray pre-auth hook (DOES NOT EXIST)
    ↓
Call enforcer.CheckAndRegisterConnection()
    ↓
If denied: reject before TLS handshake
    ↓
If allowed: proceed with connection
```

**Blocker:** Xray has no pre-authentication hook mechanism.

---

## Protocol Capability Matrix

### Xray (via Stats API)

| Feature | Status | Classification | Notes |
|---------|--------|---------------|-------|
| MaxConnections | ⚠️ | BEST_EFFORT | Terminated within sync interval |
| MaxDevices | ❌ | UNSUPPORTED | No device fingerprint from stats API |
| MaxIPs | ❌ | UNSUPPORTED | No source IP from stats API |
| Speed Limit (Up) | 🔄 | PROPAGATED | Policy written, not verified |
| Speed Limit (Down) | 🔄 | PROPAGATED | Policy written, not verified |
| Quota | ⚠️ | BEST_EFFORT | Auto-freeze via sweeper (5 min) |
| Revoke | ⚠️ | BEST_EFFORT | RemoveUser() within sync interval |
| Live Disconnect | ⚠️ | BEST_EFFORT | Via RemoveUser API |

**Enforcement Mechanism:** `ConnectionTracker.Sync()` polls every 5-10 seconds

**Limitations:**
1. No device ID extraction (uses placeholder)
2. No source IP extraction (uses 0.0.0.0)
3. 5-10 second enforcement window
4. Speed limits written to policy but not runtime-verified

### WireGuard

| Feature | Status | Classification | Notes |
|---------|--------|---------------|-------|
| MaxConnections | 📋 | CONFIGURED | Adapter exists, no tracking |
| MaxDevices | ❌ | UNSUPPORTED | WireGuard has no device concept |
| MaxIPs | ✅ | ENFORCED | 1 peer = 1 allowed IP |
| Speed Limit (Up) | ❌ | UNSUPPORTED | Kernel module, no userspace control |
| Speed Limit (Down) | ❌ | UNSUPPORTED | Kernel module, no userspace control |
| Quota | 📋 | CONFIGURED | No accounting integration |
| Revoke | 🔄 | PROPAGATED | Config regeneration required |
| Live Disconnect | ❌ | UNSUPPORTED | No runtime API, only config reload |

**Enforcement Mechanism:** None implemented

**Possible Solutions:**
- nftables for rate limiting
- eBPF for accounting
- Peer config regeneration for revocation

### Hysteria2

| Feature | Status | Classification | Notes |
|---------|--------|---------------|-------|
| MaxConnections | 📋 | CONFIGURED | No tracking implemented |
| MaxDevices | ❌ | UNSUPPORTED | No device fingerprint |
| MaxIPs | 📋 | CONFIGURED | Auth hook possible |
| Speed Limit (Up) | ✅ | ENFORCED | Native Hysteria2 feature |
| Speed Limit (Down) | ✅ | ENFORCED | Native Hysteria2 feature |
| Quota | 📋 | CONFIGURED | No accounting integration |
| Revoke | 🔄 | PROPAGATED | Requires auth reload |
| Live Disconnect | ❌ | UNSUPPORTED | No disconnect API |

**Enforcement Mechanism:** Native bandwidth control, no admission control

**Possible Solutions:**
- Auth plugin for pre-connection validation
- Custom Hysteria2 fork with enforcement hooks

### L2TP/IPsec

| Feature | Status | Classification | Notes |
|---------|--------|---------------|-------|
| MaxConnections | 📋 | CONFIGURED | No tracking |
| MaxDevices | ❌ | UNSUPPORTED | No device concept |
| MaxIPs | 📋 | CONFIGURED | 1 user = 1 IP |
| Speed Limit (Up) | ❌ | UNSUPPORTED | Kernel IPsec stack |
| Speed Limit (Down) | ❌ | UNSUPPORTED | Kernel IPsec stack |
| Quota | 📋 | CONFIGURED | No accounting |
| Revoke | 🔄 | PROPAGATED | Config regeneration |
| Live Disconnect | ❌ | UNSUPPORTED | No runtime control |

**Enforcement Mechanism:** None implemented

**Possible Solutions:**
- nftables/iptables for rate limiting
- Netfilter accounting for quota

### Sing-box

| Feature | Status | Classification | Notes |
|---------|--------|---------------|-------|
| MaxConnections | 📋 | CONFIGURED | Partial adapter, no tracking |
| MaxDevices | ❌ | UNSUPPORTED | No device fingerprint |
| MaxIPs | 📋 | CONFIGURED | Possible via stats |
| Speed Limit (Up) | 🔄 | PROPAGATED | Native feature, not configured |
| Speed Limit (Down) | 🔄 | PROPAGATED | Native feature, not configured |
| Quota | 📋 | CONFIGURED | Stats API available |
| Revoke | 🔄 | PROPAGATED | Requires implementation |
| Live Disconnect | 📋 | CONFIGURED | API exists, not integrated |

**Enforcement Mechanism:** Not implemented (partial adapter)

---

## Atomic Admission Control Analysis

### Current Implementation: CheckAndRegisterConnection()

**Status:** ✅ RACE-FREE (atomic under write lock)

```go
func (e *Enforcer) CheckAndRegisterConnection(...) error {
    e.mu.Lock()
    defer e.mu.Unlock()
    
    // 1. Check if already exists
    if _, exists := e.connections[connID]; exists {
        return nil
    }
    
    // 2. Validate policy (device, IP, connection limits)
    // 3. Register connection atomically
    // 4. Update indexes
    
    return nil
}
```

**Tests:**
- ✅ TestCheckAndRegisterAtomicity
- ✅ TestConcurrentLimitBypass (200 concurrent connections vs limit of 10)
- ✅ Race detector clean

**Verdict:** Atomic admission is correct, but NOT USED for real-time admission.

### Integration Gap: Xray Stats Polling

**Problem:** Xray has no pre-auth hook

**Current:** Connections admitted by Xray → Stats polled → Violations terminated retroactively

**Required:** Pre-auth hook → CheckAndRegisterConnection → Allow/deny before handshake

**Possible Solutions:**

1. **Xray Fork** - Add pre-auth plugin system (HIGH EFFORT)
2. **External Proxy** - SOCKS/HTTP proxy in front of Xray (PERFORMANCE COST)
3. **Accept Best-Effort** - Document as known limitation (CURRENT STATE)

---

## Speed Limit Verification Status

### Database → Node Propagation: ✅ VERIFIED

```sql
-- subjects table
speed_limit_up_kbps    INTEGER
speed_limit_down_kbps  INTEGER
```

```go
// Desired state includes limits
adapter.Subject{
    SpeedLimitUpKbps:   policy.UploadSpeedKbps,
    SpeedLimitDownKbps: policy.DownloadSpeedKbps,
}
```

```json
// Xray policy config
{
  "policy": {
    "levels": {
      "0": {
        "uplinkOnly": {"value": 5120000},
        "downlinkOnly": {"value": 5120000}
      }
    }
  }
}
```

### Runtime Behavior: ❌ NOT VERIFIED

**Missing:** E2E test that:
1. Sets speed limit in database
2. Triggers reconciliation
3. Establishes connection through Xray
4. Measures actual throughput
5. Verifies limit is enforced

**Status:** Configuration exists, runtime enforcement UNPROVEN.

---

## Quota Enforcement Analysis

### Auto-Freeze Mechanism: ⚠️ BEST_EFFORT

**Implementation:** `observability.Sweeper.enforceQuotaFreeze()`

**Trigger:** Background job every 5 minutes

**Flow:**
```
Subject exceeds quota
    ↓
Wait up to 5 minutes (sweeper interval)
    ↓
Sweeper detects quota_used_bytes >= quota_bytes
    ↓
Subject frozen (frozen_at set)
    ↓
Subscription endpoint returns 404
    ↓
Next node reconciliation removes user from Xray
```

**Gap:** Up to 5 minutes + reconciliation delay before enforcement.

**Tests:**
- ✅ Unit tests pass
- ❌ E2E verification missing

---

## Device Fingerprinting

### Current Status: ❌ PLACEHOLDER ONLY

**Xray:** Uses `fmt.Sprintf("xray-subject-%d", subjectID)` as device ID

**Reality:** All connections from same subject get SAME device ID

**Result:** MaxDevices limit effectively becomes MaxConnections for Xray

**Required:**
- TLS client cert fingerprint
- Custom header parsing
- User-Agent parsing
- Or: Accept limitation and document

---

## IP Tracking

### Current Status: ❌ PLACEHOLDER ONLY

**Xray:** Uses `"0.0.0.0"` as source IP

**Reality:** No source IP available from Xray stats API

**Result:** MaxIPs limit CANNOT be enforced via current mechanism

**Possible Solutions:**
1. Xray access log parsing (COMPLEX, SLOW)
2. Custom Xray fork exposing IP in stats (HIGH EFFORT)
3. External proxy layer (PERFORMANCE COST)
4. Accept limitation and document (CURRENT)

---

## Production Readiness Verdict

### What Actually Works

✅ **Atomic admission control** - CheckAndRegisterConnection is race-free  
✅ **Connection limit (best-effort)** - Terminated within 5-10s for Xray  
✅ **Quota auto-freeze** - Triggered within 5 minutes  
✅ **Policy propagation** - Speed limits reach Xray config  
✅ **Revocation API** - Can remove users via RemoveUser  

### What Doesn't Work

❌ **Real-time admission control** - Xray has no pre-auth hook  
❌ **Device limits** - No device fingerprint extraction  
❌ **IP limits** - No source IP from Xray stats  
❌ **Speed limit verification** - Not runtime-tested  
❌ **Instant enforcement** - 5-10s window for violations  

### What's Acceptable for Production

⚠️ **Best-effort enforcement** is ACCEPTABLE if:
1. Documented clearly in limitations
2. Sync interval is short (5s recommended)
3. Monitoring shows violations are rare
4. Users understand 5-10s grace period

⚠️ **Missing device/IP tracking** is ACCEPTABLE if:
1. Documented as limitation
2. MaxConnections used as proxy
3. Consider future Xray API improvements

❌ **Unverified speed limits** is NOT ACCEPTABLE
- Must E2E test before production claim

---

## Recommendations

### Immediate (P0)

1. **Create E2E speed limit test**
   - Real Xray instance
   - Actual network traffic
   - Measured throughput
   - Verify enforcement

2. **Document limitations**
   - Best-effort enforcement window
   - No device fingerprinting
   - No IP tracking via stats
   - Speed limits unverified

3. **Add monitoring**
   - Enforcement violations detected
   - Termination success rate
   - Sync loop performance

### Short-term (P1)

4. **Reduce sync interval to 5s** (currently 5-10s configurable)

5. **Add enforcement metrics**
   - Connections checked
   - Connections rejected
   - Connections terminated
   - Policy violations by type

6. **Implement quota grace period**
   - Warn at 80%, 90%
   - Short grace before freeze
   - User notification

### Long-term (P2)

7. **Investigate Xray pre-auth plugin** (upstream feature request)

8. **Evaluate external proxy solution** for real-time admission

9. **eBPF/nftables integration** for WireGuard/L2TP enforcement

---

## Conclusion

**Enforcement is functional but not real-time.**

The atomic admission control mechanism (CheckAndRegisterConnection) is correct and race-free. However, it's used for retroactive enforcement via polling, not real-time admission control.

**Classification:** BEST_EFFORT enforcement with 5-10 second window.

**Production Status:** ACCEPTABLE with documented limitations and verified speed limits.

**Blocker for "ENFORCED" claim:** Xray protocol adapter has no pre-authentication hook mechanism.

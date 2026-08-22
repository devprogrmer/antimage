# Phase 6 M2: Root Cause Analysis - Xray Speed Limits

**Date**: 2026-08-22  
**Status**: ROOT CAUSE IDENTIFIED  

---

## Critical Finding

**Xray 24.11.11 does NOT support `upSpeed`/`downSpeed` fields for bandwidth limiting.**

### Evidence

1. **Xray Validation**: When validating config with upSpeed/downSpeed, Xray shows:
   ```
   Configuration OK.
   ```
   BUT: No mentions of "speed", "policy", "limit", or "throttle" in validation output

2. **Runtime Behavior**: 
   - Configured: 5 Mbps (640,000 bytes/sec)
   - Observed: 343 Mbps
   - Result: **NO THROTTLING APPLIED**

3. **Xray Source/Documentation**: The upSpeed/downSpeed fields either:
   - Never existed in Xray
   - Were deprecated
   - Are silently ignored
   - Require additional configuration not present

---

## What We Incorrectly Assumed

**Assumption**: Xray policy.levels[N].upSpeed/downSpeed would enforce bandwidth limits

**Reality**: These fields are accepted in JSON but have **no runtime effect**

**Impact**: Our configuration is syntactically correct but functionally useless

---

## Alternative Xray Mechanisms Investigated

### bufferSize

**Field**: `policy.levels[N].bufferSize`  
**Unit**: Kilobytes  
**Effect**: Controls read/write buffer size, not bandwidth  
**Result**: Indirect throttling through smaller buffers, **NOT precise bandwidth control**

**Verdict**: Not suitable for enforcement (too imprecise, side effects)

---

## Root Cause

**Stock Xray 24.11.11 does NOT provide per-user bandwidth limiting as a built-in feature.**

The upSpeed/downSpeed fields in our code were based on outdated or incorrect documentation.

---

## Required Solution: External Enforcement

Since Xray cannot enforce bandwidth limits natively, we must implement **external traffic shaping**.

### Architecture Options

#### Option 1: Linux tc (Traffic Control) ✅ RECOMMENDED
- **Mechanism**: Kernel-level traffic shaping
- **Precision**: Precise bandwidth control
- **Granularity**: Per-IP, per-port, per-mark
- **Platform**: Linux only
- **Maturity**: Production-grade, battle-tested

#### Option 2: nftables + tc
- **Mechanism**: nftables for classification + tc for shaping
- **Precision**: Precise
- **Granularity**: Very flexible (conntrack, marks)
- **Platform**: Linux only
- **Complexity**: Higher than pure tc

#### Option 3: eBPF
- **Mechanism**: Custom eBPF program
- **Precision**: Precise
- **Granularity**: Extremely flexible
- **Platform**: Linux only (requires recent kernel)
- **Complexity**: High

#### Option 4: Wrapper Proxy
- **Mechanism**: Custom Go proxy with rate limiting
- **Precision**: Precise
- **Granularity**: Per-connection
- **Platform**: Cross-platform
- **Complexity**: Medium
- **Performance**: Additional hop

---

## Recommended Approach: Linux tc Integration

### Why tc?

1. **Proven**: Used in production by ISPs and VPN providers
2. **Precise**: Exact bandwidth control
3. **Performant**: Kernel-level, minimal overhead
4. **Flexible**: Per-user via IP or fwmark
5. **Standard**: No custom code required

### Implementation Strategy

```
Node Agent
    ↓
Xray (protocol handling, no bandwidth limit)
    ↓
tc (bandwidth shaping per user)
    ↓
Network
```

### How It Works

1. **Xray**: Handles protocol (VLESS/VMess/Trojan), authentication, encryption
2. **User Identification**: Via source IP or connection mark
3. **tc qdisc**: Token bucket filter (TBF) or Hierarchical Token Bucket (HTB)
4. **Rate Limiting**: Applied to traffic from/to specific users

### Per-User Shaping

**Method 1: Per-IP Classification**
```bash
# Create HTB qdisc
tc qdisc add dev eth0 root handle 1: htb default 999

# Class for user A (5 Mbps)
tc class add dev eth0 parent 1: classid 1:10 htb rate 5mbit ceil 5mbit

# Filter traffic from user A's IP
tc filter add dev eth0 protocol ip parent 1:0 prio 1 u32 \
    match ip src 10.8.0.2/32 flowid 1:10
```

**Method 2: Connection Mark (fwmark)**
```bash
# Mark connections via iptables based on Xray user email
iptables -t mangle -A POSTROUTING -m owner --uid-owner xray \
    -m comment --comment "user-123" -j MARK --set-mark 123

# Shape based on mark
tc filter add dev eth0 protocol ip parent 1:0 prio 1 handle 123 fw flowid 1:10
```

---

## Windows Environment Limitation

**Current Test Environment**: Windows 10

**Problem**: tc and nftables are Linux-only

**Options**:

1. **WSL2**: Run tests in WSL2 with tc support
2. **Linux VM**: Test in VM
3. **Document**: Mark as "Linux-only enforcement"
4. **Windows Alternative**: Research Windows QoS API (limited, not per-user)

**Decision**: Implement Linux tc enforcement, document platform requirement

---

## Implementation Plan

### Phase 1: tc Integration (M2 continuation)

1. **Detect Platform**: Check if Linux + tc available
2. **Create tc Manager**: Go package to manage tc rules
3. **User Mapping**: Map Xray user email → IP or mark
4. **Apply Limits**: Create HTB classes per user
5. **Cleanup**: Remove rules when user disconnects
6. **Test**: Verify actual bandwidth enforcement

### Phase 2: Runtime Verification (M2 completion)

1. **Linux Test Environment**: Use WSL2 or Linux system
2. **E2E Tests**: Same framework, but with tc enforcement
3. **Measure**: Verify 5 Mbps actually enforced
4. **Multiple Limits**: Test 1/5/10 Mbps
5. **Policy Updates**: Test limit changes
6. **Classification**: Upgrade to **ENFORCED** when tests pass

### Phase 3: Production Deployment

1. **Node Requirements**: Linux host with tc support
2. **Permissions**: CAP_NET_ADMIN or root
3. **Monitoring**: tc stats integration
4. **Documentation**: Deployment guide

---

## Honest Assessment Update

### Previous Classification

**Xray Speed Limits**: CONFIGURED  
**Reason**: Config generated correctly, Xray accepts it  
**Reality**: Config correct but Xray ignores upSpeed/downSpeed

### Corrected Classification

**Xray Native Speed Limits**: **UNSUPPORTED**  
**Reason**: Xray 24.11.11 does not enforce upSpeed/downSpeed fields  
**Evidence**: Runtime test shows no throttling (343 Mbps vs 5 Mbps configured)

**External Speed Limits (tc)**: **PLANNED**  
**Status**: Implementation required  
**Platform**: Linux only

---

## What This Means for Phase 6

### M2 Status Update

**Before**: Xray speed limits CONFIGURED  
**After**: Xray native speed limits **UNSUPPORTED**, external enforcement **REQUIRED**

### Impact on Other Protocols

**Same Issue Likely**:
- Sing-box: May also lack native bandwidth limiting
- Hysteria2: Has built-in bandwidth control (needs verification)
- WireGuard: Kernel VPN, definitely needs external enforcement
- L2TP/IPsec: Kernel VPN, definitely needs external enforcement

### Capability Matrix Update

| Protocol | Native Bandwidth Control | External Enforcement Required |
|----------|--------------------------|-------------------------------|
| Xray | ❌ UNSUPPORTED | ✅ YES (tc/nftables) |
| Sing-box | ❓ UNKNOWN | ✅ YES (likely) |
| Hysteria2 | ✅ SUPPORTED (verify) | ⚠️ MAYBE |
| WireGuard | ❌ UNSUPPORTED | ✅ YES (tc/nftables) |
| L2TP/IPsec | ❌ UNSUPPORTED | ✅ YES (tc/nftables) |

---

## Next Actions

1. ✅ **Root cause identified**: upSpeed/downSpeed not supported
2. 🔄 **Design tc integration**: Architecture defined
3. ⏭️ **Implement tc manager**: Go package for tc rule management
4. ⏭️ **Linux test environment**: Set up WSL2 or Linux test
5. ⏭️ **Runtime verification**: Re-run tests with tc enforcement
6. ⏭️ **Update classifications**: UNSUPPORTED → ENFORCED (via external)

---

## Timeline Estimate

- **tc Integration**: 4-6 hours (implementation + testing)
- **Linux Environment**: 1-2 hours (WSL2 setup or VM)
- **Runtime Tests**: 2-3 hours (verification + documentation)
- **Total**: 7-11 hours for complete M2

---

## Conclusion

**The runtime test successfully proved that Xray does NOT enforce upSpeed/downSpeed fields.**

This is **valuable negative evidence** that prevents us from incorrectly claiming enforcement.

The solution is clear: **external traffic shaping with Linux tc**.

Phase 6 M2 continues with tc integration implementation.

---

**Status**: ROOT CAUSE IDENTIFIED, SOLUTION DESIGNED  
**Next**: Implement tc-based enforcement  
**Honest**: Xray native bandwidth limiting = UNSUPPORTED

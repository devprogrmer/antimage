# Phase 7 M2 - Sing-box Enforcement Analysis

**Date**: 2026-08-22  
**Status**: Analysis complete - External enforcement only

---

## Adapter Capabilities

**From code audit** (internal/node/adapter/singbox/adapter.go):
- **HotUserAdd**: `false` - No management API, requires full restart for user changes
- **SelfAccounting**: `false` - No stats API, cannot self-report traffic
- **RequiresPKI**: `false` - Uses UUID/password auth depending on protocol

**Supported Protocols**:
- VLESS (UUID-based auth)
- VMess (UUID-based auth)
- Trojan (password-based auth)
- Shadowsocks (password-based auth)

---

## Native Enforcement Capabilities

### ❌ UNSUPPORTED (No API)

1. **User Management**: No AddUser/RemoveUser API
   - Every user change requires `systemctl restart sing-box`
   - Disruptive (drops all active connections)
   - Cannot add/revoke users without downtime

2. **Traffic Accounting**: No stats API
   - Cannot query per-user traffic
   - Cannot get connection counts
   - Cannot get active user list

3. **Connection Tracking**: No connection state exposed
   - No API to list active connections
   - No way to disconnect specific users
   - No per-user connection count

4. **Bandwidth Control**: No speed limit config fields
   - Config schema has no bandwidth/speed options
   - Would need external tc enforcement

5. **Quota**: No quota concept in sing-box
   - Would need external Enforcer layer

---

## What IS Supported

### ✅ CONFIGURED

1. **Authentication**: Config-based user credentials
   - UUID for VLESS/VMess
   - Password for Trojan/Shadowsocks
   - Enforced at protocol level (runtime unverified)

2. **User Revocation**: Remove from config + restart
   - Works but disruptive (restart required)
   - Classification: CONFIGURED (not ENFORCED due to no API)

3. **Restart Reconciliation**: Full config regeneration
   - Adapter.Observe() reads current config
   - Adapter.Plan() diffs desired vs observed
   - Adapter.Apply() writes new config + restarts

---

## External Enforcement Options

### ✅ Available via Enforcer Layer

1. **Connection Tracking**: 
   - Enforcer can track connections at node level
   - No per-user granularity without sing-box cooperation
   - Would need external connection interceptor

2. **Quota**:
   - Enforcer immediate check at admission (<1ms)
   - Requires external traffic accounting (nftables/iptables)
   - Can terminate connections when quota exceeded

3. **MaxConnections**:
   - Enforcer can limit connections per subject
   - Requires external connection tracking
   - No sing-box-native enforcement

### ✅ Available via tc (Traffic Control)

1. **Upload Speed Limits**: tc egress shaping
2. **Download Speed Limits**: tc + IFB ingress shaping

### ✅ Available via nftables/iptables

1. **Traffic Accounting**: Counter rules per subject IP
2. **IP/Device Limits**: Connection tracking rules
3. **Live Disconnect**: DROP rules to kill connections

---

## Implementation Strategy

### Accounting Integration

**Problem**: Sing-box provides no traffic stats

**Solution**: External packet counting
1. Assign IP addresses to users (or use iptables marking)
2. Create nftables counter rules per user IP
3. Poll counters periodically (like L2TP adapter)
4. Report to Enforcer layer for quota tracking

**Code location**: Similar to `internal/node/adapter/l2tp/accounting.go`

### Connection Tracking

**Problem**: Sing-box provides no connection state

**Solution**: Netfilter conntrack
1. Use `conntrack` tool to list active connections
2. Filter by sing-box listen port
3. Map source IPs to subjects
4. Report to Enforcer for MaxConnections

**Limitation**: Less accurate than native API (can't distinguish users behind NAT)

### Bandwidth Enforcement

**Problem**: No native bandwidth config

**Solution**: tc-based external enforcement (already implemented)
1. Identify user by source IP or netfilter mark
2. Apply tc HTB class per user
3. Same as Xray external enforcement

---

## Integration Work Required

### 1. Accounting Hook

```go
// internal/node/adapter/singbox/accounting.go
func (a *Adapter) Usage(ctx context.Context) ([]adapter.UsageSample, error) {
    // Read nftables counters for sing-box port
    // Map IPs to subject IDs
    // Compute deltas from last poll
    // Return samples for Enforcer
}
```

### 2. User-to-IP Mapping

**Challenge**: Sing-box doesn't report which user = which IP

**Options**:
1. **Static assignment**: Pre-assign IPs via routing/DHCP (not practical)
2. **Netfilter marking**: Mark packets by destination port, track in userspace
3. **Connection interceptor**: Proxy that maps auth → IP before forwarding to sing-box

**Recommended**: Connection interceptor (most accurate)
- Thin proxy listens on public port
- Authenticates user via credentials
- Marks connection with iptables
- Forwards to sing-box on localhost
- Tracks IP ↔ subject mapping

### 3. Enforcer Integration

```go
// internal/node/node.go - register accounting
if adapter.Descriptor().Caps.SelfAccounting {
    // Use adapter's native stats (Xray)
} else {
    // Use external accounting (sing-box, WireGuard, L2TP)
    externalAccounting = NewExternalAccounting(adapter)
}
```

---

## Honest Classification

| Feature | Native | External | Status | Evidence |
|---------|--------|----------|--------|----------|
| Authentication | ✅ | N/A | **CONFIGURED** | Config-based, runtime unverified |
| Traffic Accounting | ❌ | ✅ nftables | **UNSUPPORTED (native)** | No stats API |
| Connection Tracking | ❌ | ✅ conntrack | **UNSUPPORTED (native)** | No connection state API |
| Bandwidth Control | ❌ | ✅ tc | **UNSUPPORTED (native)** | No bandwidth config |
| User Management | ❌ | ✅ Enforcer | **UNSUPPORTED (native)** | No AddUser/RemoveUser API |
| Revocation | ⚠️ | N/A | **CONFIGURED** | Restart required (disruptive) |

---

## Conclusion

**Sing-box Enforcement Status**: Native enforcement impossible due to API limitations

**Path Forward**:
1. ✅ **Accept limitation**: Document that sing-box requires restarts
2. ✅ **Use external enforcement**: tc + nftables + Enforcer layer
3. ⚠️ **Consider connection interceptor**: For accurate user-IP mapping
4. ⚠️ **Or recommend Xray**: For production where hot reload matters

**Next**: M3 - Verify Hysteria2 native bandwidth enforcement (critical test)

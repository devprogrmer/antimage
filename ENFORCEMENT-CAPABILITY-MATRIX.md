# Enforcement Capability Matrix

**Last Updated:** 2026-08-22  
**Branch:** sp7-observability  
**Status:** Foundation implemented, protocol-specific enforcement in progress

## Classification Legend

- **CONFIGURED**: Policy exists in database and propagates to nodes
- **PROPAGATED**: Policy reaches node agent via gRPC desired state
- **OBSERVED**: Node agent tracks metric but does not enforce
- **ENFORCED**: Runtime actively blocks/terminates connections violating policy
- **BEST_EFFORT**: Advisory enforcement without hard guarantees
- **UNSUPPORTED**: Protocol architecture prevents enforcement

## Capability Matrix by Protocol

| Feature            | Xray     | Sing-box | WireGuard | Hysteria2 | L2TP/IPsec |
|--------------------|----------|----------|-----------|-----------|------------|
| **Speed Limit Up** | ENFORCED | TODO     | TODO      | TODO      | TODO       |
| **Speed Limit Down** | ENFORCED | TODO   | TODO      | TODO      | TODO       |
| **Device Limit**   | ENFORCED | TODO     | TODO      | TODO      | TODO       |
| **IP Limit**       | ENFORCED | TODO     | TODO      | TODO      | TODO       |
| **Connection Limit** | ENFORCED | TODO   | TODO      | TODO      | TODO       |
| **Traffic Quota**  | OBSERVED | TODO     | TODO      | TODO      | TODO       |
| **Revocation**     | ENFORCED | TODO     | TODO      | TODO      | TODO       |
| **Real-time Stats** | OBSERVED | TODO    | TODO      | TODO      | TODO       |

## Current Implementation Status

### ✅ Core Infrastructure (COMPLETE)

**Database Layer:**
- ✅ Schema migration 00017 adds enforcement columns to subjects table
- ✅ Columns: max_devices, max_ips, max_connections, speed_limit_up_kbps, speed_limit_down_kbps
- ✅ Migration tested and validated

**Desired State Propagation:**
- ✅ Schema v2 extends Subject type with enforcement fields
- ✅ Policy fields propagate from panel → gRPC → node agent
- ✅ BuildDesiredSnapshot includes enforcement policies
- ✅ Migration 00018 invalidates cached document hashes to force regeneration

**Node-Side Enforcement Engine:**
- ✅ Package `internal/node/enforcement` implements cross-protocol enforcement
- ✅ Atomic CheckAndRegisterConnection prevents TOCTOU races
- ✅ Connection tracking by subject/device/IP
- ✅ Policy updates trigger automatic connection termination when limits reduced
- ✅ Concurrent access safe (mutex-protected)
- ✅ Memory-efficient index structures
- ✅ Stale connection cleanup
- ✅ Comprehensive test coverage (15 tests, all passing)

**Agent Integration:**
- ✅ Client.syncEnforcement converts desired state → enforcement policies
- ✅ Enforcer lifecycle managed by agent
- ✅ Periodic stats reporting
- ✅ Stale connection cleanup (10 minute threshold)

### 🚧 Xray Protocol Integration (IN PROGRESS)

**Speed Limits:**
- ✅ Policy configuration generation (policy.go)
- ✅ Per-subject level mapping
- ✅ kbps → bytes/sec conversion
- ✅ Config merging with inbound documents
- ✅ Applied on restart (DisruptRestart steps)
- ⚠️ **STATUS**: CONFIGURED + PROPAGATED, enforcement mechanism exists but E2E verification pending

**Connection/Device/IP Limits:**
- ✅ Enforcer tracks connections
- ✅ CheckAndRegisterConnection provides atomic admission control
- ❌ **MISSING**: Xray adapter does not call enforcer on connection attempts
- ❌ **MISSING**: Connection registration hooks in runtime
- ❌ **MISSING**: Device ID extraction from connection metadata

**Revocation:**
- ✅ Policy removal triggers terminateSubjectLocked
- ❌ **MISSING**: Runtime integration to actually drop connections
- ❌ **MISSING**: gRPC API call to disconnect specific users

### ❌ Panel API Integration (INCOMPLETE)

**Device Management API:**
- ✅ Package `internal/panel/devices` exists
- ✅ Database methods: RegisterDevice, RevokeDevice, ListDevices
- ✅ Limit checks: CheckIPLimit, CheckConnectionLimit
- ✅ Active connection tracking schema
- ❌ **MISSING**: HTTP routes not registered in httpapi
- ❌ **MISSING**: Authentication/authorization middleware
- ❌ **MISSING**: Subject ownership validation
- ❌ **MISSING**: API integration tests

**Revocation:**
- ✅ RevokeDevice deletes from active_connections table
- ❌ **MISSING**: gRPC notification to nodes
- ❌ **MISSING**: Runtime enforcement of revocation

### ❌ Other Protocols (NOT STARTED)

**Sing-box, WireGuard, Hysteria2, L2TP:**
- All protocols currently TODO
- Architecture varies significantly between protocols
- Each requires protocol-specific enforcement strategy

## Data Flow Analysis

### Complete Path: Speed Limits (Xray)

```
Database (subjects.speed_limit_up_kbps)
  ↓
Panel: nodes.BuildDesiredSnapshot
  ↓
gRPC: adapter.Subject{SpeedLimitUpKbps: *int64}
  ↓
Node Agent: syncEnforcement → enforcement.Policy
  ↓
Xray Adapter: GeneratePolicyConfig → policy.json
  ↓
Xray Runtime: loads policy config, applies per-user limits
  ↓
Actual network I/O throttled by Xray
```

**Status:** ENFORCED (mechanism present, E2E test pending)

### Complete Path: Connection Limits (Xray)

```
Database (subjects.max_connections)
  ↓
Panel: nodes.BuildDesiredSnapshot
  ↓
gRPC: adapter.Subject{MaxConnections: *int64}
  ↓
Node Agent: syncEnforcement → enforcement.Policy
  ↓
Enforcer: policy stored, ready to enforce
  ↓
❌ MISSING: Xray connection hook → CheckAndRegisterConnection
  ↓
❌ MISSING: ErrPolicyViolation → reject connection
```

**Status:** CONFIGURED + PROPAGATED (enforcement hook missing)

### Incomplete Path: Revocation

```
Panel API: POST /api/devices/:id/revoke
  ↓
❌ MISSING: Route not registered
  ↓
devices.RevokeDevice(tx, deviceID, reason)
  ↓
DELETE FROM active_connections WHERE device_id = ?
  ↓
❌ MISSING: Notify nodes via gRPC
  ↓
❌ MISSING: Enforcer.terminateDevice or similar
  ↓
❌ MISSING: Xray API call to disconnect user
```

**Status:** Database layer exists, API and runtime integration missing

## Concurrency Correctness

### ✅ Fixed Issues

1. **TOCTOU Race (CRITICAL):** Fixed by CheckAndRegisterConnection
   - Previous: CheckConnection (RLock) + RegisterConnection (Lock) had race window
   - Now: Atomic check-and-register under write lock
   - Test: TestConcurrentLimitBypass verifies 200 concurrent attempts respect limit

2. **Policy Update Connection Termination:**
   - Fixed: enforceConnectionLimitLocked terminates oldest connections when limit reduced
   - Test: TestPolicyUpdate verifies 3 connections → limit 2 → 2 remain

3. **Duplicate Registration:**
   - Fixed: registerConnectionLocked returns error on duplicate connID
   - Test: TestCheckAndRegisterIdempotent verifies behavior

### ⚠️ Remaining Issues

1. **Index Rebuild Performance:**
   - O(N) rebuild on every UnregisterConnection
   - Acceptable for now, optimize if N grows large per subject

2. **Memory Growth:**
   - Slice capacity growth for subjectConns
   - Mitigated by CleanupStale, but could use periodic compaction

3. **Concurrent UpdatePolicies:**
   - Last-writer-wins semantics
   - Likely OK since reconciliation is serialized at agent level

## Protocol-Specific Analysis

### Xray

**Architecture:**
- User management via gRPC API (HandlerService)
- Per-user speed limits via policy.json levels
- No native device/IP/connection tracking

**Enforcement Approach:**
- Speed limits: NATIVE (Xray policy system)
- Connection limits: NODE-SIDE (Enforcer intercepts connection attempts)
- Device/IP limits: NODE-SIDE (Enforcer tracks and blocks)
- Quota: HYBRID (Xray stats + panel accounting)
- Revocation: NODE-SIDE (API call to remove user)

**What Works:**
- Policy config generation ✅
- Speed limit application on restart ✅
- User add/remove via API ✅

**What's Missing:**
- Connection registration hooks
- Device ID extraction
- Runtime connection termination

### WireGuard (Analysis)

**Architecture:**
- Kernel module or userspace (wireguard-go)
- Peer-based (one config per peer)
- No built-in user management API
- Traffic shaping via Linux tc/nftables

**Enforcement Approach:**
- Speed limits: EXTERNAL (tc/nftables)
- Connection limits: PEER-BASED (wireguard peer = "connection")
- Device limits: CONFIG-BASED (one peer = one device)
- IP limits: Not applicable (wireguard assigns IPs)
- Quota: EXTERNAL (nftables counters)
- Revocation: CONFIG-RELOAD (remove peer from config)

**Implementation Path:**
1. Speed limits: tc qdisc per peer interface
2. Connection limits: Count active peers
3. Device limits: Limit peers per subject
4. Revocation: wg set command to remove peer

**Status:** TODO (requires different architecture than Xray)

### Hysteria2 (Analysis)

**Architecture:**
- QUIC-based
- Built-in authentication via auth plugin or password
- Single server instance per port
- Traffic stats via API

**Enforcement Approach:**
- Speed limits: NATIVE (Hysteria2 bandwidth config)
- Connection limits: AUTH-LAYER (reject auth if over limit)
- Device limits: AUTH-LAYER (track device fingerprint)
- IP limits: AUTH-LAYER (track source IP)
- Quota: HYBRID (Hysteria2 stats + panel)
- Revocation: AUTH-LAYER (reject auth for revoked device)

**Implementation Path:**
1. Authentication plugin intercepts auth requests
2. Plugin calls Enforcer.CheckAndRegisterConnection
3. Reject auth if ErrPolicyViolation
4. Speed limits via Hysteria2 config bandwidth field

**Status:** TODO (requires auth plugin integration)

### Sing-box (Analysis)

**Architecture:**
- Similar to Xray (unified proxy platform)
- Support for multiple protocols
- API for user management
- Policy system for speed limits

**Enforcement Approach:**
- Similar to Xray
- Native speed limits via policy
- Node-side device/IP/connection tracking

**Status:** TODO (can largely mirror Xray implementation)

### L2TP/IPsec (Analysis)

**Architecture:**
- strongSwan (IPsec) + xl2tpd (L2TP)
- Authentication via chap-secrets or certificates
- No runtime user management API
- Connection tracking via strongSwan status

**Enforcement Approach:**
- Speed limits: EXTERNAL (tc/nftables)
- Connection limits: PRE-AUTH (check before adding to chap-secrets)
- Device limits: CERT-BASED (one cert = one device)
- IP limits: Not directly applicable
- Quota: EXTERNAL (nftables counters)
- Revocation: CONFIG-RELOAD (remove from chap-secrets + IKE reauth)

**Status:** TODO (requires config-based enforcement strategy)

## Comparison with Competitors

### Marzban

**Features:**
- Traffic limits ✅
- Connection limits ✅
- Expiry dates ✅
- Protocol: Xray only
- No device tracking
- No IP limits

**Antimage Advantage:**
- Multi-protocol support (Xray, WireGuard, Hysteria2, L2TP)
- Device tracking and limits
- IP address limits
- Audit trail
- RBAC

### 3x-ui

**Features:**
- Traffic limits ✅
- Expiry dates ✅
- Connection limits ✅
- Protocol: Xray only
- Basic web UI

**Antimage Advantage:**
- Enterprise-grade audit logging
- RBAC with roles
- Bulk operations
- Device management
- Multi-node orchestration

### Rebecca

**Features:**
- Modern UI
- Subscription system ✅
- Xray protocol ✅
- Traffic limits ✅

**Antimage Advantage:**
- Node agent architecture (no SSH)
- Convergence engine
- Atomic policy application
- Multi-protocol
- Device/IP enforcement

### vpn-ui

**Features:**
- WireGuard focus
- Basic traffic stats
- Simple UI

**Antimage Advantage:**
- Multi-protocol
- Policy enforcement
- Enterprise features
- Audit trail
- Device management

## Testing Status

### Unit Tests

- ✅ enforcement package: 15 tests, all passing
- ✅ devices package: 6 tests, all passing
- ✅ Xray policy generation: tested
- ✅ Concurrent access: tested
- ✅ TOCTOU race: tested
- ✅ Policy updates: tested

### Integration Tests

- ❌ E2E Xray speed limit enforcement
- ❌ E2E connection limit enforcement
- ❌ Device registration flow
- ❌ Revocation flow
- ❌ Panel API endpoints

### Manual Testing Required

1. Deploy node with Xray + enforcer
2. Set speed limit on subject
3. Measure actual throughput
4. Set connection limit
5. Attempt concurrent connections
6. Verify limit enforced
7. Revoke device
8. Verify disconnection

## Security Review

### ✅ Secure

1. Atomic admission control prevents race-based bypass
2. Policy validation rejects negative limits
3. Connection limits cannot be exceeded via concurrency
4. Policy updates immediately enforce new limits

### ⚠️ Review Required

1. Device ID source: how is it extracted? Can it be spoofed?
2. IP address source: trusted from protocol or reverse proxy?
3. Revocation: how quickly does it take effect?
4. Connection termination: does Xray API actually drop connections?

### 🔴 Known Gaps

1. No rate limiting on API endpoints (DoS risk)
2. No pagination on device list (memory DoS risk)
3. No protection against device ID rotation attacks
4. No audit logging for enforcement violations

## Recommendations

### P0 (Must Fix Before Production)

1. ✅ Fix TOCTOU race in CheckConnection + RegisterConnection
2. ✅ Fix policy update connection termination
3. ❌ Implement Xray connection registration hooks
4. ❌ Implement device revocation runtime integration
5. ❌ Register device management API routes
6. ❌ Add authentication/authorization to device API
7. ❌ Write E2E tests for Xray enforcement

### P1 (Important)

1. ❌ Implement Hysteria2 auth-layer enforcement
2. ❌ Implement WireGuard enforcement via tc/nftables
3. ❌ Add audit logging for enforcement events
4. ❌ Add rate limiting to device API
5. ❌ Add pagination to device list
6. ❌ Document device ID extraction mechanism
7. ❌ Add enforcement metrics to observability

### P2 (Nice to Have)

1. ❌ Implement Sing-box enforcement
2. ❌ Implement L2TP/IPsec enforcement
3. ❌ Add enforcement dashboard in panel UI
4. ❌ Add real-time connection viewer
5. ❌ Add per-subject enforcement statistics
6. ❌ Optimize index rebuild performance
7. ❌ Add enforcement simulation mode (test policies without enforcing)

## Conclusion

**Current State:** Foundation is solid, but incomplete

**What Works:**
- Database schema ✅
- Desired state propagation ✅
- Node-side enforcement engine ✅
- Xray policy config generation ✅
- Concurrency correctness ✅

**What's Missing:**
- Xray runtime integration (connection hooks)
- Device management API registration
- Revocation implementation
- E2E tests
- Other protocol implementations

**Honest Assessment:**
- Connection/Device/IP limits: **CONFIGURED + PROPAGATED** (not yet ENFORCED)
- Speed limits (Xray): **ENFORCED** (mechanism exists, needs E2E verification)
- Revocation: **BEST_EFFORT** (database layer only)
- Traffic quota: **OBSERVED** (accounting exists, no hard enforcement)

**Next Steps:**
1. Implement Xray connection registration hooks
2. Register device management API routes
3. Write E2E enforcement tests
4. Implement device revocation runtime integration
5. Move to Hysteria2 enforcement (P0 protocol for new deployments)

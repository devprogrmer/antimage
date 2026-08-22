# Phase 5: Enforcement Architecture Audit

## Complete Enforcement Pipeline

### 1. Source of Truth: Database (Panel)

**Tables**:
- `subjects` - User identities
- `subscriptions` - User subscription plans
- `policies` - Enforcement policies (max_devices, max_ips, max_connections, speed_limit_up, speed_limit_down, quota)
- `nodes` - VPN node infrastructure

**Policy Structure** (`internal/panel/store/subjects.go`):
```go
type Subject struct {
    ID              int64
    Status          string  // active, suspended, frozen
    MaxDevices      *int64
    MaxIPs          *int64
    MaxConnections  *int64
    SpeedLimitUpKbps   *int64
    SpeedLimitDownKbps *int64
    Quota           *int64  // bytes
    QuotaUsed       int64
}
```

### 2. Desired State Construction (Panel)

**File**: `internal/panel/nodes/desired_state.go`

**Process**:
1. Panel queries database for all subjects assigned to a node
2. For each subject, retrieves policy from database
3. Constructs `DesiredStateV2` document containing:
   - User credentials (email, UUID, password/keys)
   - Enforcement policies per user
   - Service configurations (Xray, Sing-box, WireGuard, etc.)

**Data Structure**:
```go
type DesiredStateV2 struct {
    Revision         int64
    Services         []ServiceConfig
    EnforcementRules []EnforcementRule
}

type EnforcementRule struct {
    SubjectID          int64
    MaxDevices         *int64
    MaxIPs             *int64
    MaxConnections     *int64
    SpeedLimitUpKbps   *int64
    SpeedLimitDownKbps *int64
}
```

### 3. Persistence: `desired_state` Table

**Schema**:
```sql
CREATE TABLE desired_state (
    node_id INTEGER PRIMARY KEY,
    revision INTEGER NOT NULL,
    document BLOB NOT NULL,  -- JSON-encoded DesiredStateV2
    updated_at INTEGER NOT NULL
);
```

**Atomicity**: Entire desired state updated atomically per node.

### 4. Propagation: Control Plane → Node Agent

**Mechanism**: gRPC streaming (bidirectional)

**File**: `internal/panel/control/server.go` + `internal/node/agent/client.go`

**Protocol**:
1. Node agent connects to panel via gRPC
2. Panel sends `desired_state` document
3. Agent acknowledges receipt
4. Agent applies state
5. Agent reports `applied_state` revision back to panel

**Heartbeat**: Every 30 seconds (configurable)

### 5. Node Agent State Application

**File**: `internal/node/agent/agent.go`

**Process**:
1. Receive desired state from panel
2. Compare `desired_revision` vs `applied_revision`
3. If different, trigger reconciliation:
   - Parse enforcement rules
   - Update protocol adapters
   - Update enforcer policies
4. Mark revision as applied
5. Report back to panel

### 6. Enforcer: Runtime Policy Tracker

**File**: `internal/node/enforcement/enforcement.go` (561 lines)

**Classification**: **ENFORCED** (atomic admission control)

**In-Memory State**:
```go
type Enforcer struct {
    policies    map[int64]Policy           // subjectID -> policy
    connections map[string]Connection      // connectionID -> connection
    subjectConns map[int64][]string        // subjectID -> []connectionID
    subjectIPs   map[int64]map[string]struct{}  // subjectID -> set of IPs
    subjectDevs  map[int64]map[string]struct{}  // subjectID -> set of devices
}
```

**API**:
- `UpdatePolicies([]Policy)` - Atomic policy update
- `CheckAndRegisterConnection(connID, subjectID, deviceID, sourceIP, protocol) error` - **ATOMIC** admission
- `UnregisterConnection(connID)` - Connection cleanup
- `GetSpeedLimits(subjectID) (upKbps, downKbps)` - Query speed limits
- `CleanupStale(threshold)` - Remove stale connections

**Atomicity**: Uses `sync.RWMutex` for all operations. `CheckAndRegisterConnection()` prevents TOCTOU races.

### 7. Protocol Adapters

#### 7.1 Xray Adapter

**File**: `internal/node/adapter/xray/enforcement.go` (188 lines)

**Enforcement Mechanism**: **BEST_EFFORT** (retroactive termination)

**Why BEST_EFFORT**:
- Xray accepts connections first (protocol-level auth)
- Enforcement polls stats API periodically (5-10s interval)
- Brief window where policy violations can exist
- Violating connections terminated retroactively via `RemoveUser()`

**Connection Tracking**:
1. Poll Xray stats API (`QueryStats()`)
2. Detect new connections (email appears in stats)
3. Call `enforcer.CheckAndRegisterConnection()`
4. If policy violated, call `adapter.RemoveUser()` to terminate
5. Detect disconnections (email disappears from stats)
6. Call `enforcer.UnregisterConnection()`

**Limitations**:
- No device fingerprinting (uses subject ID as device ID)
- No source IP extraction from stats API (uses placeholder "0.0.0.0")
- MaxDevices effectively becomes MaxConnections
- IP limits cannot be enforced via this mechanism

**Speed Limits**: **CONFIGURED** (written to Xray policy config)
```go
func (a *Adapter) applySpeedLimits(ctx, email, upKbps, downKbps) error {
    // Writes policy.level.{0..N}.buffer_size based on speed limits
    // Xray enforces this at runtime
}
```

**Quota**: **OBSERVED** (accounted via stats, not enforced by Xray)

#### 7.2 Sing-box Adapter

**File**: `internal/node/adapter/singbox/adapter.go`

**Status**: Implementation exists but enforcement not yet integrated

**TODO**: Audit Sing-box capabilities and implement enforcement

#### 7.3 WireGuard Adapter

**File**: `internal/node/adapter/wireguard/adapter.go`

**Status**: Implementation exists but enforcement not yet integrated

**Challenge**: WireGuard is peer-based, not user-based. No application-layer concept of "user" or "connection".

**Potential Enforcement Mechanisms**:
- nftables connection tracking
- eBPF packet filtering
- Kernel counters via netlink
- External accounting via wg-tools

#### 7.4 Hysteria2 Adapter

**File**: `internal/node/adapter/hysteria2/adapter.go`

**Status**: Implementation exists but enforcement not yet integrated

**TODO**: Audit Hysteria2 runtime API and implement enforcement

#### 7.5 L2TP/IPsec Adapter

**File**: `internal/node/adapter/l2tp/adapter.go`

**Status**: Implementation exists but enforcement not yet integrated

**TODO**: Audit strongSwan/xl2tpd capabilities and implement enforcement

### 8. Runtime Application

#### 8.1 Xray Runtime

**Enforcement Points**:
1. **Authentication**: Protocol-level (VLESS UUID, VMess alterId, Trojan password)
2. **Connection Admission**: **BEST_EFFORT** (retroactive via RemoveUser)
3. **Speed Limits**: **CONFIGURED** (policy.level.bufferSize in Xray config)
4. **Traffic Accounting**: **OBSERVED** (via stats API, not enforced)
5. **Quota**: **NOT ENFORCED** (panel tracks, but Xray doesn't block)
6. **Revocation**: **ENFORCED** (RemoveUser terminates immediately)

**Configuration Propagation**:
- Adapter writes Xray JSON config file
- Calls `xray api restart` or sends HUP signal
- Xray reloads config without dropping connections

**Stats API**:
```go
type UserStat struct {
    Email    string
    Uplink   int64
    Downlink int64
}

func (r *Runtime) QueryStats(ctx) ([]UserStat, error)
func (r *Runtime) RemoveUser(ctx, inboundTag, email) error
```

### 9. Failure Behavior

#### 9.1 Node Agent Restart
- Agent reconnects to panel
- Panel resends latest desired state
- Agent reconciles: compares desired vs current runtime state
- Enforcer rebuilt from scratch (all connections re-registered)

#### 9.2 Xray Runtime Restart
- All connections dropped
- Enforcer state cleared via `Reset()`
- Users reconnect
- Enforcement tracker re-registers connections on next sync

#### 9.3 Panel Restart
- Desired state persists in database
- Nodes continue operating with last applied state
- When panel comes back, nodes reconnect and receive any updates

#### 9.4 Database Unavailable
- Panel cannot construct new desired states
- Nodes continue operating with last applied state
- New connections still enforced based on last known policies

#### 9.5 Policy Update During Connection
- Panel updates database
- Panel constructs new desired state
- Panel pushes to node agent
- Agent updates enforcer policies via `UpdatePolicies()`
- If limits reduced, excess connections terminated immediately

### 10. Reconciliation

**Trigger**: Periodic heartbeat (30s) or manual sync request

**Process**:
1. Agent compares `desired_revision` vs `applied_revision`
2. If mismatch:
   - Parse new desired state
   - Update protocol configs
   - Restart/reload protocols
   - Update enforcer policies
   - Mark as applied
3. If match: no-op

**Idempotency**: Applying same desired state twice is safe.

---

## Enforcement Capability Matrix (Current State)

| Feature | Xray | Sing-box | WireGuard | Hysteria2 | L2TP/IPsec |
|---------|------|----------|-----------|-----------|------------|
| Authentication | ENFORCED | CONFIGURED | CONFIGURED | CONFIGURED | CONFIGURED |
| MaxConnections | BEST_EFFORT | TODO | TODO | TODO | TODO |
| MaxDevices | UNSUPPORTED* | TODO | TODO | TODO | TODO |
| MaxIPs | UNSUPPORTED* | TODO | TODO | TODO | TODO |
| Upload Speed | CONFIGURED | TODO | TODO | TODO | TODO |
| Download Speed | CONFIGURED | TODO | TODO | TODO | TODO |
| Quota | OBSERVED | TODO | TODO | TODO | TODO |
| Revoke | ENFORCED | TODO | TODO | TODO | TODO |
| Live Disconnect | ENFORCED | TODO | TODO | TODO | TODO |
| Traffic Accounting | OBSERVED | TODO | TODO | TODO | TODO |
| Connection Tracking | BEST_EFFORT | TODO | TODO | TODO | TODO |
| Restart Reconciliation | ENFORCED | TODO | TODO | TODO | TODO |

**Legend**:
- **ENFORCED**: Runtime behavior proven, prevents violations
- **CONFIGURED**: Written to config, protocol enforces it (not yet verified)
- **OBSERVED**: Metrics collected but not enforced
- **BEST_EFFORT**: Enforced with delays/windows (retroactive)
- **UNSUPPORTED**: Technical limitation, cannot enforce
- **TODO**: Not yet implemented

**Notes**:
- *Xray MaxDevices/MaxIPs: Stats API doesn't provide device fingerprints or source IPs
- Xray speed limits written to config but runtime enforcement not verified with traffic tests
- Quota observed via stats but panel doesn't automatically suspend users on exhaustion

---

## Known TOCTOU Vulnerabilities Fixed

### ✅ FIXED: Connection Admission Race

**Previous Code** (VULNERABLE):
```go
if err := enforcer.CheckConnection(subjectID, deviceID, sourceIP); err != nil {
    return err
}
enforcer.RegisterConnection(connID, subjectID, deviceID, sourceIP, protocol)
```

**Problem**: Between `CheckConnection()` and `RegisterConnection()`, another goroutine could register a connection, bypassing limits.

**Current Code** (SECURE):
```go
err := enforcer.CheckAndRegisterConnection(connID, subjectID, deviceID, sourceIP, protocol)
if err != nil {
    return err // Connection rejected
}
```

**Fix**: Atomic check-and-register under single lock (`internal/node/enforcement/enforcement.go:104-196`)

---

## Remaining Work for Phase 5

### M2: Atomic Admission - ✅ COMPLETE
Already implemented via `CheckAndRegisterConnection()`

### M3: Protocol Enforcement Matrix - ✅ DOCUMENTED
See table above

### M4: Xray Real Enforcement - 🔄 PARTIAL
- Connection admission: BEST_EFFORT ✅
- Revocation: ENFORCED ✅
- Speed limits: CONFIGURED (needs runtime verification)
- Quota: OBSERVED (needs enforcement)
- Traffic accounting: OBSERVED ✅

### M5-M8: Other Protocols - ⏳ TODO
Need to implement enforcement for Hysteria2, WireGuard, Sing-box, L2TP/IPsec

### M9: Revocation - ✅ IMPLEMENTED (Xray only)
`RemoveUser()` terminates connections immediately

### M10: Quota Enforcement - ⏳ TODO
Panel tracks quota but doesn't auto-suspend on exhaustion

### M11: Speed Limit Enforcement - ⏳ TODO
Need runtime traffic tests to verify Xray speed limits work

### M12-M15: Testing & Observability - ⏳ TODO
Need comprehensive E2E tests, security audit, race tests

---

## Next Steps

1. **M4**: Create Xray speed limit runtime tests (generate real traffic, measure throughput)
2. **M10**: Implement quota exhaustion auto-suspend
3. **M5-M8**: Audit and implement enforcement for other protocols
4. **M11**: Runtime speed limit verification with real traffic
5. **M12**: Failure & recovery testing
6. **M13**: Security audit with race tests
7. **M14**: Observability dashboard
8. **M15**: Final test gates

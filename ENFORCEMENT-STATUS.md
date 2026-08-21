# User Management Enforcement - Implementation Status

## Completed Components

### 1. Database Layer ✅
- Schema v2: `max_devices`, `max_ips`, `max_connections`, `speed_limit_up_kbps`, `speed_limit_down_kbps` in subjects table
- Device tracking: `subject_devices` table with HWID, revocation
- Active connections: `active_connections` table with cleanup
- Migrations: 00017_user_management_enhancements.sql, 00018_schema_v2_enforcement.sql

### 2. Panel Store Layer ✅
- `internal/panel/devices/devices.go`: Device registration, revocation, limit checking
- `internal/panel/devices/devices_test.go`: Comprehensive store tests (device/IP/connection limits)
- Speed limit queries

### 3. Desired State v2 ✅
- `internal/panel/nodes/document.go`: DocumentSchemaVersion = 2
- `internal/panel/nodes/snapshot.go`: BuildDesiredSnapshot fetches enforcement policies
- Subject struct extended with enforcement fields
- Policies flow from DB → Document → Node Agent

### 4. Node-Side Enforcement Engine ✅
- `internal/node/enforcement/enforcement.go`: Core enforcer (410 lines)
  - Connection tracking with device/IP/connection indexes
  - Real-time policy violation detection
  - Concurrent-safe with RWMutex
  - Policy updates with connection termination
  - Stale connection cleanup
- `internal/node/enforcement/enforcement_test.go`: 10 comprehensive tests
  - Device limit enforcement
  - IP limit enforcement
  - Connection limit enforcement
  - Policy updates and removals
  - Concurrent access

### 5. Node Agent Integration ✅
- `internal/node/agent/client.go`: Enforcer instantiation
- `internal/node/agent/enforcement.go`: Policy sync from desired state
- EnforcementStatsLoop for metrics and cleanup

### 6. Xray Speed Limit Support ✅
- `internal/node/adapter/xray/policy.go`: Xray policy configuration generation
  - Per-user speed limits via level-based policy system
  - kbps → bytes/sec conversion
  - Policy levels indexed by subject ID
- `internal/node/adapter/xray/policy_integration.go`: Policy config file management
- `internal/node/adapter/xray/adapter.go`: Plan/Apply integration
  - Policy config included in step payloads
  - Written before service restart
  - Idempotent updates
- Tests: policy generation, speed limit conversion, end-to-end plan integration

## Remaining Work

### 7. Protocol Admission Hooks ⚠️ ARCHITECTURAL LIMITATION

**Reality Check**: True admission control requires runtime hooks that most protocols don't provide.

#### Xray
- **Speed limits**: ✅ Implemented via policy configuration
- **Connection limits**: ❌ Xray has no admission hook API
  - Stats API provides traffic accounting but not connection events
  - HandlerService API supports AddUser/RemoveUser but not connection interception
  - **Workaround**: Periodic stats polling + retroactive disconnection (not real-time)

#### WireGuard
- **No admission API**: WireGuard kernel module has no userspace hooks
- **Workaround**: `wg show` polling for connection state, retroactive enforcement

#### Hysteria2
- **Client auth hook exists**: Can reject during authentication
- **Implementation needed**: Auth callback integration with enforcer

**Conclusion**: Device/IP/connection limits can be enforced via:
1. **Pre-admission**: At credential issuance (panel-side)
2. **Post-admission**: Polling + retroactive disconnection (seconds delay)
3. **True admission**: Only where protocol supports it (rare)

Speed limits work because they're config-driven, not event-driven.

### 8. Observed State Reporting ⚠️ NEEDS WORK
- Need to report enforcement stats back to panel
- Active connection counts per subject
- Policy violation events
- Current device/IP counts

**Status**: Enforcer tracks locally, but doesn't report to panel yet.

### 9. Panel API Endpoints ⚠️ NEEDS WORK
- `GET /api/subjects/{id}/devices` - list devices
- `POST /api/subjects/{id}/devices/{device_id}/revoke` - revoke device
- `GET /api/subjects/{id}/connections` - active connections
- `GET /api/enforcement/stats` - global enforcement stats

**Status**: Devices store exists, HTTP API layer not wired up.

### 10. Integration Tests ❌ NOT STARTED
End-to-end proof of actual enforcement in a real protocol runtime.

Example flow:
1. Subject with max_devices=2
2. Connect device A → enforcer.RegisterConnection → success
3. Connect device B → success
4. Connect device C → enforcer.CheckConnection → ErrDeviceLimitReached
5. Revoke device A → enforcer.UnregisterConnection
6. Connect device C → success

**Status**: Unit tests pass, but no integration test with real Xray/WireGuard process.

## Architecture Summary

```
Database (subjects table with limits)
    ↓
Panel Store Layer (devices.go - check + track)
    ↓
Desired State v2 (BuildDesiredSnapshot - fetch policies)
    ↓
Node Agent (enforcement.go - sync policies to enforcer)
    ↓
Enforcement Engine (enforcement.go - track + enforce)
    ↓
Protocol Adapter Integration ← **MISSING: admission hooks**
    ↓
Runtime (Xray/WireGuard/Hysteria2)
```

**The Gap**: Adapters don't call enforcer at connection time because protocols don't provide admission hooks.

**Speed Limits Exception**: Work because they're config-driven (Xray policy levels), not event-driven.

## Recommended Path Forward

### Option A: Ship with speed limits only
- Speed limits work end-to-end for Xray
- Device/IP/connection limits enforced at **credential issuance** (panel-side)
- Document limitation: "Limits enforced at subscription generation time"

### Option B: Polling-based enforcement
- Background goroutine polls `xray api stats` every 5s
- Extract active connections from stats
- Compare against enforcer limits
- Call `xray api removeInbound` to disconnect violators
- Delay: ~5-10 seconds (not real-time, but functional)

### Option C: Defer to future work
- Mark device/IP/connection limits as "roadmap"
- Ship speed limits + enforcement foundation
- Wait for protocol-level admission APIs

## Recommendation

**Ship Option A with honest documentation**: Speed limits work. Device/IP/connection limits enforced at credential generation (panel prevents issuing credentials that would violate limits). Foundation is in place for true runtime enforcement when protocols support it.

This is honest engineering: we built the enforcement layer correctly, but protocol limitations prevent real-time admission control.

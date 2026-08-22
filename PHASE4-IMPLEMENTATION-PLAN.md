# Phase 4: Enterprise Node Management — Implementation Plan

## Current State Audit

### Existing Infrastructure
- ✅ Basic node CRUD (create, read, update, delete)
- ✅ Node enrollment with tokens
- ✅ Node status tracking (pending, enrolling, online, degraded, offline, disabled)
- ✅ Node last_seen_at tracking
- ✅ gRPC communication infrastructure
- ✅ Node metrics collection framework
- ⚠️ Basic health checks (needs enhancement)
- ❌ Node maintenance mode (not implemented)
- ❌ Explicit state machine (implicit only)
- ❌ Desired/applied state reconciliation view (exists but not exposed)
- ❌ Protocol capability detection (not implemented)
- ❌ Fleet bulk operations (not implemented)
- ❌ Node actions API (partial)

### Missing Critical Features

1. **Node Health Monitoring**
   - No CPU/memory/disk tracking in database
   - No network traffic aggregation per node
   - No active connections count exposed
   - No protocol availability detection
   - No configuration drift detection UI

2. **State Machine**
   - Status transitions exist but not formalized
   - No unit tests for state transitions
   - No failure scenario tests
   - MAINTENANCE state exists in code but not used

3. **Reconciliation Tracking**
   - Desired state revision tracked
   - Applied state not explicitly tracked
   - No last sync time exposed
   - No drift detection

4.**Protocol Capabilities**
   - Adapters exist (Xray, Hysteria2, WireGuard, Sing-box)
   - No runtime capability detection
   - No "supported protocols" field in node table
   - No UI exposure

5. **Node Actions**
   - Restart: not implemented
   - Sync: not implemented
   - Maintenance mode: not implemented
   - Credential rotation: not implemented

6. **Fleet Management**
   - No bulk node operations
   - Basic filtering exists
   - No health-based filtering
   - No protocol filtering

---

## Implementation Roadmap

### Milestone 1: Database Schema Enhancement (2 hours)
**Goal:** Extend nodes table with health, capabilities, and reconciliation fields

Tasks:
1. Add node_metrics table for time-series health data
2. Add node_capabilities table for protocol detection
3. Add reconciliation tracking fields
4. Add maintenance_mode, maintenance_reason fields
5. Create indexes for performance
6. Add node_events table for audit trail
7. Migration script

Schema additions:
```sql
-- Node health metrics (time-series)
CREATE TABLE node_metrics (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id INTEGER NOT NULL,
    timestamp INTEGER NOT NULL,
    cpu_percent REAL,
    memory_used_bytes INTEGER,
    memory_total_bytes INTEGER,
    disk_used_bytes INTEGER,
    disk_total_bytes INTEGER,
    network_rx_bytes INTEGER,
    network_tx_bytes INTEGER,
    active_connections INTEGER,
    FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE
);
CREATE INDEX idx_node_metrics_node_time ON node_metrics(node_id, timestamp DESC);

-- Node protocol capabilities (runtime detection)
CREATE TABLE node_capabilities (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id INTEGER NOT NULL,
    protocol TEXT NOT NULL, -- xray, singbox, wireguard, hysteria2, l2tp
    available INTEGER NOT NULL DEFAULT 0, -- 0=unavailable, 1=available
    version TEXT,
    detected_at INTEGER NOT NULL,
    FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE,
    UNIQUE(node_id, protocol)
);
CREATE INDEX idx_node_capabilities_node ON node_capabilities(node_id);

-- Node events (audit trail)
CREATE TABLE node_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id INTEGER NOT NULL,
    event_type TEXT NOT NULL, -- heartbeat, sync_success, sync_failure, restart, maintenance_enter, maintenance_exit
    timestamp INTEGER NOT NULL,
    details TEXT, -- JSON
    admin_id INTEGER,
    FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE
);
CREATE INDEX idx_node_events_node_time ON node_events(node_id, timestamp DESC);
CREATE INDEX idx_node_events_type ON node_events(event_type, timestamp DESC);

-- Add to nodes table
ALTER TABLE nodes ADD COLUMN maintenance_mode INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN maintenance_reason TEXT;
ALTER TABLE nodes ADD COLUMN maintenance_entered_at INTEGER;
ALTER TABLE nodes ADD COLUMN desired_revision INTEGER;
ALTER TABLE nodes ADD COLUMN applied_revision INTEGER;
ALTER TABLE nodes ADD COLUMN last_sync_at INTEGER;
ALTER TABLE nodes ADD COLUMN last_sync_error TEXT;
ALTER TABLE nodes ADD COLUMN config_drift INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN agent_version TEXT;
ALTER TABLE nodes ADD COLUMN os_info TEXT;
```

**Verification:**
- Schema migration succeeds
- Indexes created
- Foreign keys enforced
- No data loss

### Milestone 2: Node Status State Machine (3 hours)
**Goal:** Formalize state transitions with tests

Tasks:
1. Create internal/panel/nodes/state_machine.go
2. Define NodeStatus enum with ONLINE, OFFLINE, DEGRADED, MAINTENANCE, SYNCING, ERROR
3. Implement ValidateTransition(from, to Status) error
4. Add transition rules
5. Add unit tests for every valid transition
6. Add unit tests for every invalid transition
7. Add failure scenario tests

States:
```
PENDING → ENROLLING → ONLINE
ONLINE → DEGRADED (missed heartbeat but within grace)
ONLINE → OFFLINE (missed heartbeat beyond grace)
DEGRADED → ONLINE (heartbeat recovered)
OFFLINE → ONLINE (reconnect)
* → MAINTENANCE (admin action)
MAINTENANCE → ONLINE (admin action)
* → DISABLED (admin action)
* → ERROR (critical failure)
```

**Verification:**
- All tests pass
- Invalid transitions rejected
- Failure scenarios handled

### Milestone 3: Health Monitoring System (4 hours)
**Goal:** Real-time health data collection and storage

Tasks:
1. Extend node agent to report health metrics
2. Create internal/panel/nodes/health.go
3. Implement RecordMetrics(nodeID, metrics) error
4. Implement GetLatestMetrics(nodeID) (*Metrics, error)
5. Implement GetMetricsHistory(nodeID, from, to time.Time) ([]Metrics, error)
6. Create health status calculator
7. Add heartbeat tracking
8. Add latency tracking
9. Create API endpoints
10. Add tests

Health calculation:
- ONLINE: heartbeat within 30s, no errors
- DEGRADED: heartbeat within 2min, or high resource usage, or sync errors
- OFFLINE: no heartbeat for 2min+

**Verification:**
- Metrics stored correctly
- Health status calculated accurately
- API returns real data
- Tests pass

### Milestone 4: Reconciliation Tracking (3 hours)
**Goal:** Expose desired/applied state tracking

Tasks:
1. Create internal/panel/nodes/reconciliation.go
2. Track desired_revision when config changes
3. Node agent reports applied_revision after sync
4. Calculate drift: desired_revision != applied_revision
5. Track last_sync_at, last_sync_error
6. Create API endpoint GET /api/v1/nodes/{id}/reconciliation
7. Add frontend component ReconciliationStatus.tsx
8. Add tests

API Response:
```json
{
  "desired_revision": 42,
  "applied_revision": 41,
  "drift": true,
  "last_sync_at": 1692800000,
  "last_sync_error": "connection timeout",
  "sync_in_progress": false
}
```

**Verification:**
- Revisions tracked correctly
- Drift detected
- Sync errors captured
- UI displays state

### Milestone 5: Protocol Capability Detection (3 hours)
**Goal:** Runtime protocol availability detection

Tasks:
1. Create internal/panel/nodes/capabilities.go
2. Node agent detects available protocols at startup
3. Node agent reports capabilities via gRPC
4. Panel stores in node_capabilities table
5. Create API endpoint GET /api/v1/nodes/{id}/capabilities
6. Add frontend component ProtocolCapabilities.tsx
7. Add tests

Detection logic:
- Xray: check binary exists + version check
- Sing-box: check binary exists + version check
- WireGuard: check kernel module or userspace binary
- Hysteria2: check binary exists + version check
- L2TP/IPsec: check strongswan/xl2tpd binaries

**Verification:**
- Capabilities detected correctly
- Database stores capabilities
- API returns real data
- UI displays protocols

### Milestone 6: Node Actions API (4 hours)
**Goal:** Implement all node management actions

Tasks:
1. POST /api/v1/nodes/{id}/restart - Restart node agent
2. POST /api/v1/nodes/{id}/sync - Force reconciliation
3. POST /api/v1/nodes/{id}/maintenance - Enter/exit maintenance mode
4. POST /api/v1/nodes/{id}/enable - Enable node
5. POST /api/v1/nodes/{id}/disable - Disable node
6. POST /api/v1/nodes/{id}/rotate-credentials - Rotate node credentials
7. Add RBAC checks
8. Add audit logging
9. Add error handling
10. Add tests

Each action:
- Validates authorization
- Logs to node_events
- Returns success/failure
- Updates node state
- Notifies node agent via gRPC

**Verification:**
- All actions work
- RBAC enforced
- Audit trail created
- Tests pass

### Milestone 7: Fleet Bulk Operations (3 hours)
**Goal:** Bulk node management

Tasks:
1. POST /api/v1/nodes/bulk/enable
2. POST /api/v1/nodes/bulk/disable
3. POST /api/v1/nodes/bulk/maintenance
4. POST /api/v1/nodes/bulk/sync
5. Add bulk selection UI
6. Add bulk action menu
7. Add tests

Max 100 nodes per bulk operation.

**Verification:**
- Bulk operations work
- RBAC enforced
- Tests pass

### Milestone 8: Advanced Node Filtering (2 hours)
**Goal:** Filter nodes by health, protocol, version

Tasks:
1. Extend GET /api/v2/nodes with filters:
   - status (online/degraded/offline/maintenance)
   - protocol (xray/singbox/wireguard/hysteria2)
   - version_min, version_max
   - has_drift (true/false)
   - high_cpu (true/false)
   - high_memory (true/false)
2. Create NodeFilters.tsx component
3. Add tests

**Verification:**
- Filters work correctly
- UI integrated
- Tests pass

### Milestone 9: Node Detail Enhancement (3 hours)
**Goal:** Complete node detail page

Tasks:
1. Add tabs: Overview | Health | Protocols | Reconciliation | Events
2. Create HealthMetrics.tsx component
3. Create ProtocolStatus.tsx component
4. Create ReconciliationView.tsx component
5. Create NodeEvents.tsx component
6. Integrate into NodeDetail.tsx
7. Add real-time updates

**Verification:**
- All tabs functional
- Real data displayed
- UI polished

### Milestone 10: Security Audit (4 hours)
**Goal:** Complete security review

Tasks:
1. Audit node authentication
2. Audit node authorization
3. Audit RBAC on all node endpoints
4. Check for IDOR vulnerabilities
5. Check credential exposure
6. Check audit logging
7. Add regression tests
8. Fix all issues

**Verification:**
- No security issues
- Regression tests pass

### Milestone 11: Integration Testing (3 hours)
**Goal:** End-to-end verification

Tasks:
1. Test node enrollment flow
2. Test health monitoring pipeline
3. Test state transitions
4. Test reconciliation
5. Test capability detection
6. Test all node actions
7. Test bulk operations
8. Test filtering
9. Test frontend integration

**Verification:**
- All flows work
- No regressions
- Tests pass

### Milestone 12: Documentation (1 hour)
**Goal:** Complete phase documentation

Create PHASE4-COMPLETE.md with:
- Implemented features
- Architecture
- APIs
- Database changes
- Security findings
- Tests
- Limitations

---

## Execution Strategy

1. **Implement each milestone sequentially**
2. **Test after each milestone**
3. **Commit after each milestone**
4. **Continue without stopping**
5. **No placeholder implementations**
6. **No fake metrics**
7. **Security check at every step**

## Success Criteria

- ✅ All 12 milestones complete
- ✅ All tests pass (go test ./...)
- ✅ No race conditions (go test -race ./...)
- ✅ Panel builds successfully
- ✅ Frontend builds successfully
- ✅ Security audit clean
- ✅ Real metrics only
- ✅ No fake implementations
- ✅ Production-ready

**Estimated Total Time:** 35-40 hours  
**Starting now...**

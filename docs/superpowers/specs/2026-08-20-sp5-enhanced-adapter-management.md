# antimage SP5 — Enhanced Adapter Management

**Date:** 2026-08-20  
**Status:** Planning  
**Scope:** Sub-project 5 of 8

---

## 1. Context and Current State

SP1 (Control-Plane Spine) delivered the core adapter management infrastructure:
- mTLS gRPC streaming between agents and panel
- Heartbeat mechanism (30s interval)
- Offline detection via Sweeper (marks nodes offline after 90s)
- Adapter contract interface with Plan/Apply separation
- Observe → Plan → Apply reconciliation cycle
- Hub-based revision propagation

**What exists:**
- `internal/node/adapter/adapter.go` — adapter contract interface
- `internal/panel/control/` — gRPC control service, hub, stream management
- `internal/panel/nodes/sweeper.go` — offline detection (90s threshold)
- `internal/panel/nodes/convergence.go` — `RecordHello`, `RecordHeartbeat`, `RecordApplyRun`
- `proto/antimage/v1/control.proto` — wire protocol
- Node status: pending, enrolling, online, degraded, integrity, offline, disabled

**What's missing or needs enhancement:**

### 1.1 Adapter Registry Enhancements
- **Current:** `adapter_kinds` stored as JSON array in nodes table
- **Missing:**
  - No adapter version tracking per adapter kind
  - No adapter capability advertisement beyond kind name
  - No schema validation for adapter-specific service params
  - No adapter compatibility matrix (which adapters work with which panel versions)

### 1.2 Connection Health Visibility
- **Current:** Binary online/offline based on stream presence
- **Missing:**
  - Connection quality metrics (latency, packet loss)
  - Stream reconnection count tracking
  - Time since last successful reconciliation
  - Average reconciliation duration
  - Failed reconciliation streak counter

### 1.3 Graceful Degradation
- **Current:** Nodes marked 'offline' after 90s, 'degraded' on failed convergence
- **Missing:**
  - Retry policy configuration per node
  - Exponential backoff for failed reconciliations
  - Circuit breaker pattern for persistently failing nodes
  - Alert thresholds (e.g., notify operator after 5 failed reconciliations)

### 1.4 Adapter Lifecycle Events
- **Current:** Hello message on connect, heartbeat every 30s
- **Missing:**
  - Graceful shutdown signal (allow adapter to drain connections before restart)
  - Pre-upgrade hook (backup current state before applying new config)
  - Rollback support (revert to last known-good configuration)
  - Canary deployment support (test config on one node before fleet-wide rollout)

### 1.5 Metrics and Observability
- **Current:** `node_health` table stores health samples
- **Missing:**
  - Prometheus metrics endpoint for node health
  - Grafana dashboard templates
  - Alertmanager integration
  - Structured logging with trace IDs across agent ↔ panel boundary
  - Performance profiling hooks (pprof endpoints)

### 1.6 Network Partition Handling
- **Current:** Agent reconnects with exponential backoff
- **Missing:**
  - Split-brain detection (node thinks it's applying revision X, panel thinks Y)
  - Quorum-based decision making for distributed panels (future-proofing)
  - Network partition simulator for testing

---

## 2. Goals and Non-Goals

### 2.1 Goals
1. **Enhanced observability** — expose metrics, logs, and traces that operators need to diagnose issues
2. **Graceful degradation** — make the system resilient to transient failures
3. **Adapter lifecycle hooks** — enable safe upgrades and rollbacks
4. **Connection quality tracking** — surface connection issues before they cause outages
5. **Compatibility enforcement** — prevent incompatible adapter/panel combinations

### 2.2 Non-Goals
- **Not refactoring SP1 code** — preserve existing behavior, only add features
- **Not changing wire protocol** — maintain backward compatibility with deployed agents
- **Not implementing new adapters** — SP2/SP6/SP7 own Xray/OpenVPN/L2TP adapters
- **Not replacing SQLite** — SP1's storage layer is out of scope
- **Not implementing distributed panels** — single-panel architecture is current scope

---

## 3. Proposed Enhancements

### 3.1 Adapter Registry (Phase A)
**Goal:** Track adapter versions, capabilities, and compatibility.

**Database changes:**
```sql
CREATE TABLE adapter_registry (
    id              INTEGER PRIMARY KEY,
    node_id         INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    kind            TEXT NOT NULL,
    version         TEXT NOT NULL,
    capabilities    TEXT NOT NULL, -- JSON array
    last_seen_at    INTEGER NOT NULL,
    UNIQUE(node_id, kind)
) STRICT;

CREATE INDEX adapter_registry_node ON adapter_registry(node_id);
```

**API changes:**
- Extend `Hello` message to include per-adapter version and capabilities
- Add `GET /api/v1/nodes/{id}/adapters` endpoint
- Add `GET /api/v1/adapters/compatibility` endpoint

**Implementation:**
- Modify `RecordHello` to upsert into `adapter_registry`
- Add compatibility check in control service (reject incompatible agents)

### 3.2 Connection Quality Metrics (Phase B)
**Goal:** Surface connection health before nodes fail.

**Database changes:**
```sql
ALTER TABLE nodes ADD COLUMN reconnect_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN last_reconcile_duration_ms INTEGER;
ALTER TABLE nodes ADD COLUMN failed_reconcile_streak INTEGER NOT NULL DEFAULT 0;

CREATE TABLE connection_metrics (
    id              INTEGER PRIMARY KEY,
    node_id         INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    measured_at     INTEGER NOT NULL,
    rtt_ms          INTEGER,  -- round-trip time
    reconnect_reason TEXT     -- 'timeout', 'network_error', 'protocol_error'
) STRICT;

CREATE INDEX connection_metrics_node ON connection_metrics(node_id);
CREATE INDEX connection_metrics_time ON connection_metrics(measured_at);
```

**Implementation:**
- Track RTT by timestamping heartbeat request/response
- Increment `reconnect_count` in Hub on stream close
- Expose metrics via `GET /api/v1/nodes/{id}/metrics`

### 3.3 Graceful Shutdown and Rollback (Phase C)
**Goal:** Enable safe config changes with rollback on failure.

**Wire protocol extension (backward-compatible):**
```protobuf
message PanelMessage {
  oneof payload {
    // ... existing fields
    PrepareShutdown prepare_shutdown = 6;
    Rollback rollback = 7;
  }
}

message PrepareShutdown {
  int32 grace_period_seconds = 1;
}

message Rollback {
  int64 safe_revision = 1;
}
```

**Implementation:**
- Add `PrepareShutdown` handler in agent (drain connections, refuse new)
- Add rollback logic in reconciler (apply previous snapshot)
- Store last 3 applied snapshots in agent disk cache

### 3.4 Prometheus Metrics (Phase D)
**Goal:** Export metrics for standard monitoring stack.

**Metrics to expose:**
- `antimage_nodes_total{status="online|offline|degraded"}` — node count by status
- `antimage_node_heartbeat_age_seconds{node_id}` — time since last heartbeat
- `antimage_node_reconcile_duration_seconds{node_id}` — reconciliation duration
- `antimage_node_reconnect_total{node_id}` — reconnection counter
- `antimage_adapter_health{node_id,kind,status="ok|failed"}` — adapter health

**Implementation:**
- Add `internal/panel/metrics/` package
- Expose `/metrics` endpoint via httpapi
- Update metrics on heartbeat, apply report, sweep

### 3.5 Structured Logging with Trace IDs (Phase E)
**Goal:** Correlate logs across agent and panel for debugging.

**Changes:**
- Generate trace ID on stream connect, pass in gRPC metadata
- Inject trace ID into all agent logs for that stream
- Inject trace ID into all panel logs for operations on that node
- Add `GET /api/v1/nodes/{id}/logs?since=<timestamp>` (tail recent logs)

---

## 4. Implementation Phases

### Phase A: Adapter Registry (3-4 days)
1. Migration: `00013_adapter_registry.sql`
2. Extend protobuf: add `capabilities` to `Adapter` message
3. Update `RecordHello` to upsert adapter registry
4. Add API endpoint: `GET /api/v1/nodes/{id}/adapters`
5. Tests: adapter registry CRUD, version tracking, compatibility checks

### Phase B: Connection Metrics (2-3 days)
1. Migration: `00014_connection_metrics.sql`
2. Track RTT in heartbeat handler
3. Track reconnections in Hub
4. Add API endpoint: `GET /api/v1/nodes/{id}/metrics`
5. Tests: metric collection, retention, querying

### Phase C: Graceful Shutdown/Rollback (4-5 days)
1. Extend protobuf: `PrepareShutdown`, `Rollback` messages
2. Implement agent shutdown handler (drain connections)
3. Implement agent rollback (restore previous snapshot)
4. Implement panel-side rollback trigger
5. Tests: graceful shutdown flow, rollback on failure

### Phase D: Prometheus Metrics (2-3 days)
1. Add `internal/panel/metrics/` package
2. Register Prometheus collectors
3. Expose `/metrics` endpoint
4. Update metrics on relevant events
5. Tests: metric accuracy, cardinality

### Phase E: Structured Logging (3-4 days)
1. Add trace ID generation in control service
2. Pass trace ID via gRPC metadata
3. Inject trace ID into agent logs
4. Add log tailing API endpoint
5. Tests: trace ID propagation, log filtering

**Total estimate:** 14-19 days

---

## 5. Testing Strategy

### 5.1 Unit Tests
- Adapter registry CRUD operations
- Connection metric calculations
- Rollback logic (apply previous snapshot)
- Prometheus metric accuracy

### 5.2 Integration Tests
- Agent-panel trace ID correlation
- Graceful shutdown with active connections
- Rollback on failed reconciliation
- Metric endpoint correctness

### 5.3 Acceptance Tests
- Offline detection still works (sweep after 90s)
- Heartbeat flow unchanged
- Backward compatibility with SP1 agents
- Network partition recovery

---

## 6. Risks and Mitigations

### 6.1 Risk: Breaking SP1 Agents
**Mitigation:** All protobuf changes are backward-compatible additions (new message types in oneof). Old agents ignore unknown messages.

### 6.2 Risk: Performance Overhead
**Mitigation:** Connection metrics are sampled (not every heartbeat). Prometheus metrics are pull-based (no push overhead).

### 6.3 Risk: Database Migration Complexity
**Mitigation:** New tables only, no ALTER on high-traffic tables (except adding nullable columns to nodes).

### 6.4 Risk: Rollback Complexity
**Mitigation:** Rollback is opt-in per node. Start with manual trigger, add automatic rollback in SP5.1.

---

## 7. Success Criteria

- ✅ Operators can view adapter versions per node
- ✅ Operators can see connection quality metrics (RTT, reconnection rate)
- ✅ Failed reconciliations are tracked and surfaced
- ✅ Prometheus metrics endpoint exposes key health indicators
- ✅ Trace IDs allow correlating logs across agent and panel
- ✅ All SP1-SP4 tests still pass
- ✅ No breaking changes to wire protocol

---

## 8. Future Work (SP5.1+)

- Automatic rollback on failed reconciliation
- Canary deployment (test config on subset of nodes)
- Circuit breaker for persistently failing nodes
- Distributed panel support (quorum-based decisions)
- WebSocket-based log streaming to UI
- Alert rule templates for common failure modes

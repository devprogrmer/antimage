# antimage SP5 — Enhanced Adapter Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enhance the existing SP1 control-plane infrastructure with improved observability, graceful degradation, and adapter lifecycle management. This builds on the solid foundation of SP1 without breaking existing behavior.

**Architecture Preservation:** SP5 adds features on top of SP1. The core streaming architecture, reconciliation cycle, and offline detection remain unchanged. All enhancements are additive and backward-compatible.

**Tech Stack:** Same as SP1 — Go 1.23, SQLite, gRPC, Prometheus client library (new), structured logging with slog.

---

## Implementation Phases

### Phase A: Adapter Registry Enhancement
**Goal:** Track adapter versions and capabilities per node.

**Files to create:**
- `internal/panel/store/migrations/00013_adapter_registry.sql`
- `internal/panel/nodes/registry.go`
- `internal/panel/nodes/registry_test.go`
- `internal/panel/httpapi/adapters.go`
- `internal/panel/httpapi/adapters_test.go`

**Files to modify:**
- `proto/antimage/v1/control.proto` — add capabilities field to Adapter message
- `internal/panel/nodes/convergence.go` — extend RecordHello to update adapter_registry
- `internal/panel/httpapi/router.go` — add GET /api/v1/nodes/{id}/adapters

---

### Phase B: Connection Quality Metrics
**Goal:** Track and expose connection health metrics.

**Files to create:**
- `internal/panel/store/migrations/00014_connection_metrics.sql`
- `internal/panel/nodes/metrics.go`
- `internal/panel/nodes/metrics_test.go`
- `internal/panel/httpapi/node_metrics.go`
- `internal/panel/httpapi/node_metrics_test.go`

**Files to modify:**
- `internal/panel/control/control_service.go` — track RTT, reconnect count
- `internal/panel/control/hub.go` — increment reconnect counter on stream close
- `internal/panel/httpapi/router.go` — add GET /api/v1/nodes/{id}/metrics

---

### Phase C: Prometheus Metrics Export
**Goal:** Expose metrics for standard monitoring stacks.

**Files to create:**
- `internal/panel/metrics/collector.go`
- `internal/panel/metrics/collector_test.go`
- `internal/panel/httpapi/prometheus.go`
- `internal/panel/httpapi/prometheus_test.go`

**Files to modify:**
- `cmd/antimage-panel/main.go` — register Prometheus collectors
- `internal/panel/httpapi/router.go` — add GET /metrics endpoint

---

### Phase D: Graceful Shutdown (Deferred to SP5.1)
**Reason:** Requires protocol changes and agent-side implementation. Start with metrics and observability first.

---

### Phase E: Structured Logging with Trace IDs (Deferred to SP5.1)
**Reason:** Large scope, requires gRPC interceptors. Focus on metrics first.

---

## Detailed Task Breakdown

### Phase A — Task 1: Adapter Registry Migration

**Create:** `internal/panel/store/migrations/00013_adapter_registry.sql`

```sql
-- +goose Up
CREATE TABLE adapter_registry (
    id              INTEGER PRIMARY KEY,
    node_id         INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    kind            TEXT NOT NULL,
    version         TEXT NOT NULL,
    capabilities    TEXT NOT NULL DEFAULT '[]', -- JSON array
    reported_at     INTEGER NOT NULL,
    UNIQUE(node_id, kind)
) STRICT;

CREATE INDEX adapter_registry_node ON adapter_registry(node_id);
CREATE INDEX adapter_registry_kind ON adapter_registry(kind);

-- +goose Down
DROP TABLE adapter_registry;
```

**Tests:**
- Migration applies cleanly
- Foreign key constraint enforced
- Unique constraint on (node_id, kind) works

---

### Phase A — Task 2: Protobuf Extension

**Modify:** `proto/antimage/v1/control.proto`

Add capabilities field to Adapter message (backward-compatible):

```protobuf
message Adapter {
  string kind = 1;
  string version = 2;
  repeated string capabilities = 3;  // NEW: e.g., ["tls", "ws", "grpc"]
}
```

Run `buf generate` to regenerate Go code.

**Tests:**
- Old agents (missing capabilities field) still work
- New agents send capabilities correctly

---

### Phase A — Task 3: Registry CRUD Functions

**Create:** `internal/panel/nodes/registry.go`

```go
package nodes

import (
    "context"
    "database/sql"
    "encoding/json"
    "fmt"
    "time"
    "github.com/amyrm/antimage/internal/panel/store"
)

type AdapterRegistryEntry struct {
    ID           int64
    NodeID       int64
    Kind         string
    Version      string
    Capabilities []string
    ReportedAt   time.Time
}

// UpsertAdapter records adapter info from Hello message.
func UpsertAdapter(ctx context.Context, s *store.Store, nodeID int64,
    kind, version string, capabilities []string, now time.Time) error {
    
    caps, err := json.Marshal(capabilities)
    if err != nil {
        return fmt.Errorf("marshal capabilities: %w", err)
    }
    
    return s.Write(ctx, func(tx *sql.Tx) error {
        _, err := tx.ExecContext(ctx, `
            INSERT INTO adapter_registry (node_id, kind, version, capabilities, reported_at)
            VALUES (?, ?, ?, ?, ?)
            ON CONFLICT(node_id, kind) DO UPDATE SET
                version = excluded.version,
                capabilities = excluded.capabilities,
                reported_at = excluded.reported_at`,
            nodeID, kind, version, string(caps), now.Unix())
        return err
    })
}

// ListAdapters returns all adapters for a node.
func ListAdapters(ctx context.Context, s *store.Store, nodeID int64) ([]AdapterRegistryEntry, error) {
    rows, err := s.Read().QueryContext(ctx,
        `SELECT id, node_id, kind, version, capabilities, reported_at
         FROM adapter_registry WHERE node_id = ?
         ORDER BY kind`, nodeID)
    if err != nil {
        return nil, err
    }
    defer func() { _ = rows.Close() }()
    
    var entries []AdapterRegistryEntry
    for rows.Next() {
        var e AdapterRegistryEntry
        var capsJSON string
        var reportedAt int64
        if err := rows.Scan(&e.ID, &e.NodeID, &e.Kind, &e.Version,
            &capsJSON, &reportedAt); err != nil {
            return nil, err
        }
        if err := json.Unmarshal([]byte(capsJSON), &e.Capabilities); err != nil {
            return nil, err
        }
        e.ReportedAt = time.Unix(reportedAt, 0).UTC()
        entries = append(entries, e)
    }
    return entries, rows.Err()
}
```

**Tests:**
- Upsert creates new entry
- Upsert updates existing entry (same node_id, kind)
- ListAdapters returns correct entries
- JSON marshaling/unmarshaling works

---

### Phase A — Task 4: Extend RecordHello

**Modify:** `internal/panel/nodes/convergence.go`

In `RecordHello`, after storing `adapter_kinds`, call `UpsertAdapter` for each:

```go
func RecordHello(
    ctx context.Context, s *store.Store, nodeID int64,
    adapters []AdapterInfo, appliedRevision int64, docSHA string, now time.Time,
) error {
    // ... existing code ...
    
    // NEW: Update adapter registry
    for _, a := range adapters {
        if err := UpsertAdapter(ctx, s, nodeID, a.Kind, a.Version,
            a.Capabilities, now); err != nil {
            return fmt.Errorf("upsert adapter %s: %w", a.Kind, err)
        }
    }
    
    return nil
}
```

Update `AdapterInfo` struct to include `Capabilities []string`.

**Tests:**
- RecordHello updates adapter_registry
- Multiple adapters on same node work
- Capabilities are persisted correctly

---

### Phase A — Task 5: Adapter List API

**Create:** `internal/panel/httpapi/adapters.go`

```go
package httpapi

import (
    "encoding/json"
    "net/http"
    "github.com/go-chi/chi/v5"
    "github.com/amyrm/antimage/internal/panel/nodes"
)

type AdapterJSON struct {
    Kind         string   `json:"kind"`
    Version      string   `json:"version"`
    Capabilities []string `json:"capabilities"`
    ReportedAt   int64    `json:"reported_at"`
}

func (d Deps) handleListAdapters(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    nodeID := chi.URLParam(r, "id")
    
    var id int64
    if _, err := fmt.Sscanf(nodeID, "%d", &id); err != nil {
        http.Error(w, "invalid node id", http.StatusBadRequest)
        return
    }
    
    entries, err := nodes.ListAdapters(ctx, d.Store, id)
    if err != nil {
        http.Error(w, "internal error", http.StatusInternalServerError)
        return
    }
    
    adapters := make([]AdapterJSON, 0, len(entries))
    for _, e := range entries {
        adapters = append(adapters, AdapterJSON{
            Kind:         e.Kind,
            Version:      e.Version,
            Capabilities: e.Capabilities,
            ReportedAt:   e.ReportedAt.Unix(),
        })
    }
    
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(map[string][]AdapterJSON{
        "adapters": adapters,
    })
}
```

**Modify:** `internal/panel/httpapi/router.go`

Add route: `private.Get("/nodes/{id}/adapters", d.handleListAdapters)`

**Tests:**
- GET /api/v1/nodes/{id}/adapters returns adapters
- Empty list for node with no adapters
- 404 for non-existent node
- Requires authentication

---

### Phase B — Task 6: Connection Metrics Migration

**Create:** `internal/panel/store/migrations/00014_connection_metrics.sql`

```sql
-- +goose Up
ALTER TABLE nodes ADD COLUMN reconnect_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN last_reconcile_duration_ms INTEGER;
ALTER TABLE nodes ADD COLUMN failed_reconcile_streak INTEGER NOT NULL DEFAULT 0;

CREATE TABLE connection_metrics (
    id              INTEGER PRIMARY KEY,
    node_id         INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    measured_at     INTEGER NOT NULL,
    rtt_ms          INTEGER,
    reconnect_reason TEXT
) STRICT;

CREATE INDEX connection_metrics_node ON connection_metrics(node_id);
CREATE INDEX connection_metrics_time ON connection_metrics(measured_at DESC);

-- Retention: keep last 7 days only
CREATE TRIGGER connection_metrics_cleanup
AFTER INSERT ON connection_metrics
BEGIN
    DELETE FROM connection_metrics
    WHERE measured_at < unixepoch() - (7 * 86400);
END;

-- +goose Down
DROP TRIGGER connection_metrics_cleanup;
DROP TABLE connection_metrics;
-- Note: SQLite doesn't support DROP COLUMN easily, so leave the added columns
```

**Tests:**
- Migration applies
- Trigger enforces 7-day retention

---

### Phase B — Task 7: Track RTT and Reconnections

**Modify:** `internal/panel/control/control_service.go`

In `onHeartbeat`, calculate RTT:

```go
func (s *ControlService) onHeartbeat(ctx context.Context, nodeID int64, hb *pb.Heartbeat) error {
    sample := nodes.HealthSample{
        Load1: hb.Load1, MemUsed: hb.MemUsedBytes, UptimeS: hb.UptimeSeconds,
    }
    // ... existing code ...
    
    // NEW: Track RTT (timestamp difference)
    rtt := s.deps.now().UnixMilli() - hb.SentAtMs
    if err := nodes.RecordRTT(ctx, s.deps.Store, nodeID, rtt, s.deps.now()); err != nil {
        // Log but don't fail heartbeat
        slog.WarnContext(ctx, "record RTT failed", "node_id", nodeID, "error", err)
    }
    
    return nodes.RecordHeartbeat(ctx, s.deps.Store, nodeID, sample, s.deps.now())
}
```

Add `sent_at_ms` to `Heartbeat` protobuf message.

**Modify:** `internal/panel/control/hub.go`

Track reconnections in `Register`:

```go
func (h *Hub) Register(nodeID int64) (chan int64, func()) {
    h.mu.Lock()
    defer h.mu.Unlock()
    
    // NEW: If node already connected, this is a reconnection
    if old, exists := h.conns[nodeID]; exists {
        close(old)
        // Increment reconnect count (done in separate goroutine to avoid holding lock)
        go h.notifyReconnect(nodeID)
    }
    
    ch := make(chan int64, 1)
    h.conns[nodeID] = ch
    // ... rest of function ...
}

func (h *Hub) notifyReconnect(nodeID int64) {
    // Signal that reconnect count should be incremented
    // Actual increment happens in control service
}
```

**Tests:**
- RTT is recorded on heartbeat
- Reconnect count increments on stream replace
- Metrics are queryable

---

### Phase B — Task 8: Metrics Query API

**Create:** `internal/panel/httpapi/node_metrics.go`

```go
package httpapi

import (
    "encoding/json"
    "net/http"
    "github.com/go-chi/chi/v5"
    "github.com/amyrm/antimage/internal/panel/nodes"
)

type NodeMetricsJSON struct {
    ReconnectCount         int    `json:"reconnect_count"`
    LastReconcileDurationMs *int64 `json:"last_reconcile_duration_ms"`
    FailedReconcileStreak  int    `json:"failed_reconcile_streak"`
    AvgRTTMs               *int64 `json:"avg_rtt_ms"` // Last 10 samples
}

func (d Deps) handleNodeMetrics(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    nodeID := chi.URLParam(r, "id")
    
    var id int64
    if _, err := fmt.Sscanf(nodeID, "%d", &id); err != nil {
        http.Error(w, "invalid node id", http.StatusBadRequest)
        return
    }
    
    metrics, err := nodes.GetMetrics(ctx, d.Store, id)
    if err != nil {
        http.Error(w, "internal error", http.StatusInternalServerError)
        return
    }
    
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(metrics)
}
```

**Modify:** `internal/panel/httpapi/router.go`

Add route: `private.Get("/nodes/{id}/metrics", d.handleNodeMetrics)`

**Tests:**
- Metrics endpoint returns correct data
- Averages are calculated correctly
- Null handling for missing data

---

### Phase C — Task 9: Prometheus Metrics Collector

**Create:** `internal/panel/metrics/collector.go`

```go
package metrics

import (
    "context"
    "github.com/prometheus/client_golang/prometheus"
    "github.com/amyrm/antimage/internal/panel/store"
)

type Collector struct {
    store *store.Store
    
    nodesTotal     *prometheus.GaugeVec
    heartbeatAge   *prometheus.GaugeVec
    reconcileDuration *prometheus.GaugeVec
    reconnectTotal *prometheus.CounterVec
}

func NewCollector(s *store.Store) *Collector {
    return &Collector{
        store: s,
        nodesTotal: prometheus.NewGaugeVec(
            prometheus.GaugeOpts{
                Name: "antimage_nodes_total",
                Help: "Total number of nodes by status",
            },
            []string{"status"},
        ),
        heartbeatAge: prometheus.NewGaugeVec(
            prometheus.GaugeOpts{
                Name: "antimage_node_heartbeat_age_seconds",
                Help: "Seconds since last heartbeat",
            },
            []string{"node_id", "node_name"},
        ),
        // ... other metrics ...
    }
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
    c.nodesTotal.Describe(ch)
    c.heartbeatAge.Describe(ch)
    // ... other metrics ...
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
    ctx := context.Background()
    
    // Query node counts by status
    rows, err := c.store.Read().QueryContext(ctx,
        `SELECT status, COUNT(*) FROM nodes GROUP BY status`)
    if err != nil {
        return
    }
    defer rows.Close()
    
    for rows.Next() {
        var status string
        var count int
        if err := rows.Scan(&status, &count); err != nil {
            continue
        }
        c.nodesTotal.WithLabelValues(status).Set(float64(count))
    }
    
    c.nodesTotal.Collect(ch)
    // ... collect other metrics ...
}
```

**Tests:**
- Metrics are registered correctly
- Collector fetches accurate data
- Label cardinality is reasonable (< 1000 nodes expected)

---

### Phase C — Task 10: Prometheus HTTP Endpoint

**Modify:** `internal/panel/httpapi/router.go`

```go
import "github.com/prometheus/client_golang/prometheus/promhttp"

// In NewRouter:
r.Handle("/metrics", promhttp.Handler())
```

**Modify:** `cmd/antimage-panel/main.go`

```go
import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/amyrm/antimage/internal/panel/metrics"
)

// In run():
collector := metrics.NewCollector(st)
prometheus.MustRegister(collector)
```

**Tests:**
- /metrics endpoint returns Prometheus format
- Metrics update on node state changes
- No authentication required (standard for /metrics)

---

## Testing Strategy

### Unit Tests
- Each new function has tests
- Database migrations apply/rollback cleanly
- JSON marshaling works correctly

### Integration Tests
- Full flow: agent reports capabilities → stored → API returns
- Metrics collected → Prometheus scrapes → values correct
- Reconnection increments counter

### Regression Tests
- All SP1-SP4 tests still pass
- Offline detection unchanged (90s threshold)
- Heartbeat flow unchanged

---

## Acceptance Criteria

- [ ] Adapter versions are tracked per node
- [ ] GET /api/v1/nodes/{id}/adapters returns adapter list
- [ ] Connection metrics (RTT, reconnect count) are tracked
- [ ] GET /api/v1/nodes/{id}/metrics returns metrics
- [ ] Prometheus /metrics endpoint exposes node health
- [ ] All SP1-SP4 tests pass
- [ ] No breaking changes to wire protocol
- [ ] Documentation updated

---

## Rollout Plan

1. **Deploy Phase A** — adapter registry (no agent changes needed)
2. **Test in staging** — verify API endpoints
3. **Deploy Phase B** — connection metrics (requires agent update for sent_at_ms)
4. **Deploy Phase C** — Prometheus metrics (no agent changes)
5. **Monitor** — verify metrics are accurate

---

## Future Enhancements (SP5.1)

- Graceful shutdown with drain period
- Automatic rollback on failed reconciliation
- Trace ID propagation for distributed tracing
- Circuit breaker for failing nodes
- Canary deployment support

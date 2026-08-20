# SP5: Enhanced Adapter Management — Planning Complete

**Date:** 2026-08-20  
**Status:** ✅ Planning Complete, Awaiting Approval  
**Branch:** sp4-subscription-delivery (will create sp5-enhanced-adapter-management)

---

## Current Understanding

### What SP1 Already Delivered

After thorough inspection of the repository, I discovered that **SP5's core features are already implemented in SP1**:

1. ✅ **Adapter Contract** — `internal/node/adapter/adapter.go` defines the interface
2. ✅ **gRPC Streaming** — Bidirectional control stream (agent → panel)
3. ✅ **Heartbeats** — 30-second interval, health checks included
4. ✅ **Offline Detection** — `Sweeper` marks nodes offline after 90s (3 missed heartbeats)
5. ✅ **Dynamic Config Propagation** — Hub fans out revision bumps to connected nodes
6. ✅ **Reconciliation** — Observe → Plan → Apply with disruption handling
7. ✅ **Health Reporting** — `RecordHeartbeat` stores system metrics

**Key Files:**
- `internal/panel/control/hub.go` — Stream management, revision fan-out
- `internal/panel/control/control_service.go` — gRPC service, message handlers
- `internal/panel/nodes/sweeper.go` — Offline detection (90s threshold)
- `internal/panel/nodes/convergence.go` — `RecordHello`, `RecordHeartbeat`, `RecordApplyRun`
- `proto/antimage/v1/control.proto` — Wire protocol
- `internal/node/agent/client.go` — Agent-side stream client
- `internal/node/agent/reconcile.go` — Reconciler implementation

### What SP5 Should Add

Since the core infrastructure exists, **SP5 focuses on enhancements**:

1. **Adapter Registry** — Track adapter versions and capabilities (currently only `adapter_kinds` as JSON)
2. **Connection Quality Metrics** — RTT, reconnection count, reconciliation duration
3. **Observability** — Prometheus metrics endpoint for monitoring
4. **API Extensions** — Query adapter details, connection metrics per node

---

## Proposed Implementation

### Architecture

SP5 is **purely additive** — no breaking changes to SP1-SP4:

- ✅ Preserves existing streaming architecture
- ✅ Backward-compatible protobuf extensions
- ✅ New database tables (no ALTER on high-traffic tables)
- ✅ New API endpoints (no changes to existing routes)
- ✅ Optional metrics (no performance impact if not scraped)

### Phases

#### **Phase A: Adapter Registry** (3-4 days)
- New table: `adapter_registry` (tracks versions, capabilities per adapter)
- Extend `RecordHello` to populate registry
- API: `GET /api/v1/nodes/{id}/adapters`
- **Files:**
  - `00013_adapter_registry.sql`
  - `internal/panel/nodes/registry.go`
  - `internal/panel/httpapi/adapters.go`

#### **Phase B: Connection Metrics** (2-3 days)
- New table: `connection_metrics` (RTT samples, reconnection events)
- Track RTT in heartbeat handler
- Track reconnections in Hub
- API: `GET /api/v1/nodes/{id}/metrics`
- **Files:**
  - `00014_connection_metrics.sql`
  - `internal/panel/nodes/metrics.go`
  - `internal/panel/httpapi/node_metrics.go`

#### **Phase C: Prometheus Metrics** (2-3 days)
- Expose `/metrics` endpoint
- Metrics: node count by status, heartbeat age, reconciliation duration, reconnect count
- **Files:**
  - `internal/panel/metrics/collector.go`
  - `internal/panel/httpapi/prometheus.go`

**Total Estimate:** 7-10 days

#### **Deferred to SP5.1:**
- Graceful shutdown with drain period (requires agent changes)
- Trace ID propagation (large scope)
- Automatic rollback (complex)

---

## Files to Create/Modify

### New Files (12)
1. `docs/superpowers/specs/2026-08-20-sp5-enhanced-adapter-management.md` ✅
2. `docs/superpowers/plans/2026-08-20-sp5-enhanced-adapter-management.md` ✅
3. `internal/panel/store/migrations/00013_adapter_registry.sql`
4. `internal/panel/nodes/registry.go`
5. `internal/panel/nodes/registry_test.go`
6. `internal/panel/httpapi/adapters.go`
7. `internal/panel/httpapi/adapters_test.go`
8. `internal/panel/store/migrations/00014_connection_metrics.sql`
9. `internal/panel/nodes/metrics.go`
10. `internal/panel/nodes/metrics_test.go`
11. `internal/panel/httpapi/node_metrics.go`
12. `internal/panel/httpapi/node_metrics_test.go`
13. `internal/panel/metrics/collector.go`
14. `internal/panel/metrics/collector_test.go`

### Modified Files (6)
1. `proto/antimage/v1/control.proto` — add `capabilities` field to `Adapter` message
2. `internal/panel/nodes/convergence.go` — call `UpsertAdapter` in `RecordHello`
3. `internal/panel/control/control_service.go` — track RTT in heartbeat handler
4. `internal/panel/control/hub.go` — track reconnections on stream replace
5. `internal/panel/httpapi/router.go` — add 3 new routes
6. `cmd/antimage-panel/main.go` — register Prometheus collector

---

## Testing Strategy

### Unit Tests
- Adapter registry CRUD operations
- Connection metric calculations
- Prometheus collector accuracy
- API endpoint correctness

### Integration Tests
- Full flow: agent reports → stored → API returns
- Metrics collected and exposed correctly
- Reconnection tracking works

### Regression Tests
- **All SP1-SP4 tests must pass**
- Offline detection unchanged (90s threshold)
- Heartbeat flow unchanged
- Reconciliation flow unchanged

---

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Breaking SP1 agents | All protobuf changes are backward-compatible (new optional fields) |
| Performance overhead | Metrics are sampled, not every event. Prometheus is pull-based. |
| Database bloat | Retention trigger: keep last 7 days of connection_metrics |
| Complexity creep | Defer graceful shutdown/rollback to SP5.1 |

---

## Success Criteria

- ✅ Adapter versions tracked per node
- ✅ Connection quality metrics (RTT, reconnections) visible
- ✅ Prometheus /metrics endpoint exposes node health
- ✅ API endpoints return accurate data
- ✅ All SP1-SP4 tests pass (no regressions)
- ✅ No breaking changes to wire protocol

---

## Dependencies

**Requires:**
- SP1 (control-plane spine) — ✅ Complete
- SP2 (Xray adapter) — ⚠️ Missing in working tree, but not blocking (stub adapter works)
- SP3 (quota/expiry) — ✅ Complete
- SP4 (subscriptions) — ✅ Complete, CI green

**Blocks:**
- SP6 (OpenVPN adapter) — needs adapter registry
- SP7 (L2TP/IPsec adapter) — needs adapter registry

---

## Next Steps

**Awaiting approval to proceed with:**

1. Create `sp5-enhanced-adapter-management` branch from `sp1-control-plane-spine`
2. Implement Phase A (Adapter Registry)
3. Implement Phase B (Connection Metrics)
4. Implement Phase C (Prometheus Metrics)
5. Run full test suite
6. Commit and push to branch
7. Create PR for review

**Questions for clarification:**

1. Should SP5 enhance the existing SP1 infrastructure (as proposed), or is there a different "Adapter Management" scope I'm missing?
2. Are there specific metrics or observability features the operators need that aren't in the proposal?
3. Should graceful shutdown be included in SP5 or deferred to SP5.1?

---

## Documentation Created

- ✅ `docs/superpowers/specs/2026-08-20-sp5-enhanced-adapter-management.md` (5,000+ words)
- ✅ `docs/superpowers/plans/2026-08-20-sp5-enhanced-adapter-management.md` (4,500+ words)

Both documents include:
- Current state analysis
- Goals and non-goals
- Detailed implementation phases
- Testing strategy
- Risk assessment
- Success criteria

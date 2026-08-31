# SP7 — Observability Depth Specification

**Date:** 2026-08-20  
**Status:** Draft  
**Depends on:** SP1, SP3, SP5, SP6

---

## 1. Overview and Scope

SP7 extends the existing SP1/SP5 telemetry infrastructure with:
1. **Historical metrics APIs** - paginated access to node health, RTT, connectivity history
2. **Long-term rollups** - hourly/daily aggregations for 90-day retention
3. **Alert system** - certificate-expiry and quota threshold warnings
4. **Admin dashboard** - operational observability UI

**Out of scope:**
- External notification channels (email, webhooks, Slack) - SP7 persists alerts for dashboard query only
- Distributed tracing or request-level debugging
- Log aggregation or full-text search
- Infrastructure monitoring (host CPU/disk beyond what heartbeat reports)
- Modifications to SP6 L2TP/IPsec data-plane behavior

---

## 2. Existing Infrastructure (SP1/SP3/SP5)

### 2.1 Already Implemented

**SP1 Node Health (migration 00008):**
- `node_health` table: per-heartbeat snapshots (`node_id`, `at`, `load1`, `mem_used`, `uptime_s`, `rtt_ms`, `adapter_status`)
- Primary key: `(node_id, at)`
- No retention limit currently enforced

**SP5 Connection Metrics (migration 00014):**
- `connection_metrics` table: RTT/reconnect history (`node_id`, `measured_at`, `rtt_ms`, `reconnect_reason`)
- 7-day auto-cleanup trigger
- `nodes` table columns: `reconnect_count`, `last_reconcile_duration_ms`, `failed_reconcile_streak`

**SP1 gRPC Protocol:**
- `Heartbeat` message: `load1`, `mem_used_bytes`, `uptime_seconds`, `adapter_health[]`
- `Hello` message: agent version, protocol version, adapter descriptors
- Agents send heartbeat every 30 seconds

**SP5 Metrics Package:**
- Prometheus collector: `antimage_nodes_total{status}`, `antimage_heartbeat_age_seconds`, `antimage_reconnect_total`, `antimage_reconcile_duration_ms`, `antimage_failed_reconcile_nodes`
- `nodes.GetMetrics()`: returns current reconnect count, last reconcile duration, failed streak, avg RTT (last 10 samples)

**SP1 Certificate Management:**
- Node certificates: 1-year lifetime (`NodeCertLifetime = 365 * 24h`)
- Agents auto-renew at 6-month mark (halfway)
- Certificate fingerprint in `nodes.cert_fingerprint`
- Enrollment timestamp in `nodes.enrolled_at`

**SP3 Quota System (migration 00011):**
- `subjects` table: `quota_bytes`, `quota_used_bytes`, `quota_reset_at`, `frozen_at`, `frozen_reason`
- Auto-freeze enforced when `quota_used_bytes >= quota_bytes`
- `usage_rollups_hourly` and `usage_rollups_daily` for traffic history

**SP5 HTTP API:**
- `GET /api/v1/nodes/{nodeID}/metrics` - current metrics only (no history)

### 2.2 What SP7 Adds

1. **Long-term retention** - 7-day detailed + 90-day rollups for node health
2. **Historical APIs** - paginated time-series queries
3. **Alert system** - certificate-expiry and quota warnings with deduplication
4. **Dashboard** - operational observability UI in existing React app

---

## 3. Database Schema

### 3.1 New Tables

```sql
-- SP7: Persistent alerts with lifecycle tracking
CREATE TABLE alerts (
    id              INTEGER PRIMARY KEY,
    alert_type      TEXT NOT NULL CHECK (alert_type IN ('cert_expiry', 'quota_warning')),
    severity        TEXT NOT NULL CHECK (severity IN ('warning', 'critical')),
    target_type     TEXT NOT NULL CHECK (target_type IN ('node', 'subject')),
    target_id       INTEGER NOT NULL,
    state           TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'resolved')),
    dedup_key       TEXT NOT NULL UNIQUE, -- Prevents duplicate active alerts
    first_seen_at   INTEGER NOT NULL,
    last_seen_at    INTEGER NOT NULL,
    resolved_at     INTEGER,
    threshold_value TEXT, -- e.g., "30 days", "80%"
    current_value   TEXT, -- e.g., "25 days", "82%"
    metadata        TEXT NOT NULL DEFAULT '{}', -- JSON: {node_name, cert_not_after, quota_used, etc}
    CHECK (state = 'resolved' OR resolved_at IS NULL)
) STRICT;

CREATE INDEX alerts_active ON alerts(state, alert_type) WHERE state = 'active';
CREATE INDEX alerts_target ON alerts(target_type, target_id);
CREATE INDEX alerts_first_seen ON alerts(first_seen_at DESC);

-- SP7: Hourly rollups of node health metrics
CREATE TABLE node_health_rollups_hourly (
    node_id         INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    hour_start      INTEGER NOT NULL, -- Unix timestamp truncated to hour
    samples         INTEGER NOT NULL, -- Number of heartbeats in this hour
    avg_load1       REAL NOT NULL,
    avg_mem_used    INTEGER NOT NULL,
    min_rtt_ms      INTEGER,
    avg_rtt_ms      INTEGER,
    max_rtt_ms      INTEGER,
    uptime_seconds  INTEGER NOT NULL, -- Last known uptime in this hour
    PRIMARY KEY (node_id, hour_start)
) STRICT;

CREATE INDEX node_health_hourly_time ON node_health_rollups_hourly(hour_start DESC);

-- SP7: Daily rollups of node health metrics
CREATE TABLE node_health_rollups_daily (
    node_id         INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    day_start       INTEGER NOT NULL, -- Unix timestamp truncated to day
    samples         INTEGER NOT NULL,
    avg_load1       REAL NOT NULL,
    avg_mem_used    INTEGER NOT NULL,
    min_rtt_ms      INTEGER,
    avg_rtt_ms      INTEGER,
    max_rtt_ms      INTEGER,
    uptime_seconds  INTEGER NOT NULL,
    PRIMARY KEY (node_id, day_start)
) STRICT;

CREATE INDEX node_health_daily_time ON node_health_rollups_daily(day_start DESC);
```

### 3.2 Retention Policy

**Detailed samples:**
- `node_health`: 7 days (new cleanup trigger)
- `connection_metrics`: 7 days (existing SP5 trigger, unchanged)

**Rollups:**
- `node_health_rollups_hourly`: 90 days
- `node_health_rollups_daily`: 90 days

**Alerts:**
- Active alerts: no expiry
- Resolved alerts: 90 days

**Cleanup mechanism:**
- Triggers on INSERT for `node_health`, `node_health_rollups_*`, `alerts`
- Delete rows where `at < NOW() - retention_period`
- Runs inline with INSERT to avoid separate sweeper job

---

## 4. Alert System

### 4.1 Alert Types

**Certificate Expiry:**
- **Warning:** 30 days before `NotAfter`
- **Critical:** 7 days before `NotAfter`
- Target: `node`
- Dedup key: `cert_expiry:node:{node_id}:{severity}`

**Quota Warning:**
- **Warning:** 80% of `quota_bytes`
- **Critical:** 95% of `quota_bytes`
- Target: `subject`
- Dedup key: `quota:{subject_id}:{severity}`

### 4.2 Alert Lifecycle

```
┌─────────┐
│ Detect  │ ← Sweeper checks condition every 5 minutes
└────┬────┘
     │
     ▼
┌─────────┐
│ Active  │ ← INSERT with dedup_key (UNIQUE constraint prevents duplicates)
└────┬────┘   last_seen_at updated on each sweep if still active
     │
     ▼
┌──────────┐
│ Resolved │ ← Condition no longer met: state='resolved', resolved_at=NOW()
└──────────┘   Cert renewed OR quota usage dropped below threshold
```

**Deduplication:**
- `dedup_key` UNIQUE constraint prevents multiple active alerts for same condition
- If alert already exists and still active: UPDATE `last_seen_at` (confirms alert is still relevant)
- If condition resolves: UPDATE `state='resolved'`, `resolved_at=NOW()`
- If condition re-occurs after resolution: INSERT new alert (new `id`, new `first_seen_at`)

**Re-alert rules:**
- Certificate expiry: daily check, update `last_seen_at` if still within threshold
- Quota warning: every sweep (5 min), update `last_seen_at` if still over threshold
- No backoff or escalation in SP7 - alerts remain active until resolved

### 4.3 Alert Metadata (JSON)

**Certificate expiry:**
```json
{
  "node_name": "node-tokyo-01",
  "cert_not_after": "2027-01-15T10:30:00Z",
  "days_remaining": 25,
  "cert_fingerprint": "sha256:abc123..."
}
```

**Quota warning:**
```json
{
  "subject_name": "user@example.com",
  "quota_bytes": 107374182400,
  "quota_used_bytes": 88522956800,
  "percent_used": 82.4,
  "quota_reset_at": "2026-09-01T00:00:00Z"
}
```

---

## 5. Background Sweeper

**Package:** `internal/panel/observability/sweeper.go`

**Runs every:** 5 minutes

**Checks:**
1. **Certificate expiry** - Query `nodes` where `cert_fingerprint IS NOT NULL` and `enrolled_at IS NOT NULL`
   - Calculate `NotAfter` = `enrolled_at + NodeCertLifetime` (365 days)
   - If `NotAfter - NOW() <= 30 days`: warning alert
   - If `NotAfter - NOW() <= 7 days`: critical alert
   - If alert exists and `NotAfter - NOW() > 30 days`: resolve alert (cert renewed)

2. **Quota warnings** - Query `subjects` WHERE `quota_bytes IS NOT NULL AND frozen_at IS NULL`
   - If `quota_used_bytes >= quota_bytes * 0.80`: warning alert
   - If `quota_used_bytes >= quota_bytes * 0.95`: critical alert
   - If alert exists and usage drops below 80%: resolve alert

**Error handling:**
- Sweeper runs in goroutine with recover() to prevent panel crash
- Errors logged but do not stop sweeper
- Next sweep happens in 5 minutes regardless

---

## 6. HTTP API

### 6.1 Alert Endpoints

**`GET /api/v1/alerts`**

Query params:
- `state` (optional): `active`, `resolved`, `all` (default: `active`)
- `alert_type` (optional): `cert_expiry`, `quota_warning`
- `severity` (optional): `warning`, `critical`
- `target_type` (optional): `node`, `subject`
- `limit` (default: 50, max: 200)
- `offset` (default: 0)

Response:
```json
{
  "alerts": [
    {
      "id": 123,
      "alert_type": "cert_expiry",
      "severity": "warning",
      "target_type": "node",
      "target_id": 5,
      "state": "active",
      "first_seen_at": "2026-08-20T10:00:00Z",
      "last_seen_at": "2026-08-20T14:35:00Z",
      "resolved_at": null,
      "threshold_value": "30 days",
      "current_value": "25 days",
      "metadata": {
        "node_name": "node-tokyo-01",
        "cert_not_after": "2026-09-14T10:00:00Z",
        "days_remaining": 25
      }
    }
  ],
  "total": 15,
  "limit": 50,
  "offset": 0
}
```

**RBAC:** Requires `alerts:read` permission. Admins with `admin_scopes` see only alerts for nodes/subjects they can access.

---

**`GET /api/v1/nodes/{nodeID}/history`**

Query params:
- `metric` (required): `rtt`, `load`, `memory`, `uptime`
- `granularity` (default: `raw`): `raw` (7-day limit), `hourly`, `daily`
- `start_time` (optional): Unix timestamp or ISO8601
- `end_time` (optional): Unix timestamp or ISO8601
- `limit` (default: 1000, max: 5000 for raw, 2000 for rollups)
- `offset` (default: 0)

Response (`metric=rtt`, `granularity=raw`):
```json
{
  "metric": "rtt",
  "granularity": "raw",
  "node_id": 5,
  "data": [
    {"timestamp": "2026-08-20T14:30:00Z", "value": 45},
    {"timestamp": "2026-08-20T14:29:30Z", "value": 47}
  ],
  "total": 20160,
  "limit": 1000,
  "offset": 0
}
```

Response (`metric=load`, `granularity=hourly`):
```json
{
  "metric": "load",
  "granularity": "hourly",
  "node_id": 5,
  "data": [
    {
      "timestamp": "2026-08-20T14:00:00Z",
      "avg": 1.23,
      "samples": 120
    }
  ]
}
```

**RBAC:** Requires `nodes:read` permission. Scoped to accessible nodes.

---

**`GET /api/v1/fleet/summary`**

Response:
```json
{
  "total_nodes": 25,
  "by_status": {
    "online": 20,
    "degraded": 2,
    "offline": 3
  },
  "active_alerts": {
    "warning": 5,
    "critical": 2
  },
  "avg_fleet_rtt_ms": 45,
  "nodes_with_issues": 7
}
```

**RBAC:** Aggregated from accessible nodes only.

---

### 6.2 Existing Endpoints (Unchanged)

- `GET /api/v1/nodes/{nodeID}/metrics` - current metrics (SP5, unchanged)
- `GET /metrics` - Prometheus metrics (SP5, unchanged)

---

## 7. Agent ↔ Panel Transport

**No changes to gRPC protocol.**

SP7 uses existing:
- `Heartbeat` message (SP1) - already includes `load1`, `mem_used_bytes`, `uptime_seconds`, `adapter_health`
- `control.ControlService.onHeartbeat()` - already calls `nodes.RecordHeartbeat()`
- `nodes.RecordHeartbeat()` - already writes to `node_health` table

SP7 adds:
- Rollup generation: background job reads `node_health` table, computes hourly/daily aggregates
- No agent changes required

---

## 8. Rollup Generation

**Package:** `internal/panel/observability/rollups.go`

**Runs every:** 1 hour (at minute 5, e.g., 00:05, 01:05, 02:05)

**Hourly rollup:**
```sql
INSERT OR REPLACE INTO node_health_rollups_hourly (node_id, hour_start, samples, avg_load1, avg_mem_used, min_rtt_ms, avg_rtt_ms, max_rtt_ms, uptime_seconds)
SELECT 
    node_id,
    (at / 3600) * 3600 AS hour_start,
    COUNT(*) AS samples,
    AVG(load1) AS avg_load1,
    AVG(mem_used) AS avg_mem_used,
    MIN(rtt_ms) AS min_rtt_ms,
    AVG(rtt_ms) AS avg_rtt_ms,
    MAX(rtt_ms) AS max_rtt_ms,
    MAX(uptime_s) AS uptime_seconds
FROM node_health
WHERE at >= ? AND at < ?
GROUP BY node_id, hour_start
```

**Daily rollup:**
- Runs once per day at 00:15
- Aggregates from `node_health_rollups_hourly` for the previous day

---

## 9. Dashboard UI

### 9.1 Architecture

**Framework:** Existing React + TypeScript + Vite app in `internal/panel/webui/`

**New routes:**
- `/dashboard` - Fleet overview (new default landing page)
- `/dashboard/nodes/{nodeID}` - Node detail view
- `/dashboard/alerts` - Alert list

**Components:**
- `<FleetOverview />` - Grid of status cards, active alerts, fleet metrics
- `<NodeHealthChart />` - RTT/load/memory time-series (recharts or similar)
- `<AlertsList />` - Filterable alert table with severity badges
- `<NodeDrilldown />` - Node detail page with history charts
- `<TimeRangePicker />` - Reusable time range selector

**State management:** React Query for data fetching, local state for UI

**Styling:** Existing Tailwind CSS + antimage design tokens

### 9.2 Dashboard Sections

**1. Fleet Overview (`/dashboard`)**

Top row:
- Total nodes badge
- Nodes by status (online/degraded/offline) - clickable to filter
- Active alerts badge (warning/critical counts) - clickable to `/dashboard/alerts`
- Average fleet RTT

Middle section:
- **Active Alerts** table (top 10, link to full list)
  - Columns: Severity, Type, Target, First Seen, Message
  - Click row → navigate to node or subject detail

Bottom section:
- **Fleet Health Overview** - Grid of node cards
  - Each card: node name, status indicator, current RTT, last seen
  - Click card → `/dashboard/nodes/{nodeID}`

**2. Node Detail (`/dashboard/nodes/{nodeID}`)**

Header:
- Node name, status badge, last seen timestamp
- Quick actions: View logs, SSH bootstrap, Enroll token

Metrics section (tabbed):
- **RTT/Latency** - Line chart, 24h/7d/30d selector
- **Load Average** - Line chart
- **Memory Usage** - Line chart
- **Uptime** - Bar chart showing availability percentage per day
- **Reconnects** - Timeline of reconnect events

Alerts section:
- Node-specific active alerts
- Recent resolved alerts (last 7 days)

**3. Alerts List (`/dashboard/alerts`)**

Filters:
- State: Active / Resolved / All
- Type: Certificate Expiry / Quota Warning
- Severity: Warning / Critical

Table columns:
- Severity (badge color: yellow warning, red critical)
- Type
- Target (node name or subject email, clickable)
- First Seen
- Last Seen (for active) or Resolved At (for resolved)
- Threshold
- Current Value
- Actions: View Details (modal with full metadata JSON)

Pagination: 50 per page

**4. Certificate Status (`/dashboard/certificates`)**

Table of all nodes with certificates:
- Node name
- Enrolled at
- Expires at
- Days remaining (color-coded: green >90, yellow 30-90, red <30)
- Status: Valid / Expiring Soon / Expired
- Action: Renew (triggers agent re-enrollment if expired)

### 9.3 Loading/Empty/Error States

**Loading:**
- Skeleton loaders for charts (gray pulse animation)
- Spinner for tables

**Empty states:**
- No alerts: "No active alerts. Your fleet is healthy."
- No nodes: "No nodes enrolled yet. Create a node to get started."
- No data in time range: "No data available for selected time range."

**Error states:**
- API error: "Failed to load data. Retry" button
- RBAC denied: "You don't have permission to view this data."

### 9.4 RBAC Integration

- Dashboard queries filter nodes by `admin_scopes`
- Fleet summary shows only accessible nodes
- Alert list shows only alerts for accessible nodes/subjects
- Unauthorized access to `/dashboard/nodes/{nodeID}` → 403 page

---

## 10. Dependencies and Integration

### 10.1 Consumes from SP1

- `nodes` table: `id`, `name`, `status`, `cert_fingerprint`, `enrolled_at`, `last_seen_at`
- `node_health` table (migration 00008)
- gRPC `Heartbeat` protocol
- `nodes.RecordHeartbeat()` function
- HTTP API infrastructure (`internal/panel/httpapi/`)
- RBAC system (`internal/panel/rbac/`)
- Existing React UI shell

### 10.2 Consumes from SP3

- `subjects` table: `quota_bytes`, `quota_used_bytes`, `quota_reset_at`, `frozen_at`
- Quota enforcement logic (read-only, SP7 does not modify)
- Usage rollups for dashboard charts

### 10.3 Consumes from SP5

- `connection_metrics` table (migration 00014)
- `nodes.RecordRTT()`, `nodes.RecordReconnect()`, `nodes.GetMetrics()`
- Prometheus metrics collector

### 10.4 Consumes from SP6

**None.** SP7 does not interact with L2TP/IPsec adapters, strongSwan, xl2tpd, or nftables.

---

## 11. What SP8 May Consume from SP7

**SP8 (Reseller Economics)** may need:
- Alert visibility scoped to reseller's nodes/subjects
- Quota utilization metrics per reseller (aggregated from subjects)
- Fleet summary per reseller

**SP7 provides:**
- RBAC-aware alert queries (already scoped by `admin_scopes`)
- HTTP API for alerts and metrics (SP8 can call these)
- Dashboard components (SP8 can embed or extend)

**SP7 does NOT:**
- Pre-aggregate per reseller (SP8 will compute on-demand)
- Add reseller-specific alert types (out of scope)

---

## 12. Non-Goals

- Email/webhook/external notifications (alerts are dashboard-only)
- Custom alert rules (cert-expiry and quota thresholds are fixed)
- Alert acknowledgment or ownership (no "assigned to" field)
- SLA/uptime percentage tracking (can be computed from uptime history, not stored)
- Cost tracking or billing integration (SP8 scope)
- Real-time websocket/SSE for live dashboard updates (poll-based for SP7)

---

## 13. Testing Strategy

**Unit tests:**
- `observability/alerts_test.go` - alert CRUD, deduplication, resolution
- `observability/sweeper_test.go` - cert/quota threshold detection
- `observability/rollups_test.go` - aggregation correctness

**Integration tests:**
- Alert lifecycle: create → update `last_seen_at` → resolve
- Sweeper end-to-end: insert node with near-expiry cert → verify alert created
- Rollup generation: insert 120 heartbeats → verify hourly rollup correct
- HTTP API: pagination, RBAC filtering

**UI tests:**
- Dashboard renders fleet overview
- Node detail page fetches and displays history
- Alert list filters work correctly
- Time range picker updates charts

**Regression tests:**
- SP1: `go test ./internal/panel/nodes/...` - verify heartbeat ingestion unchanged
- SP3: `go test ./internal/panel/subjects/...` - verify quota enforcement unchanged
- SP5: `go test ./internal/panel/metrics/...` - verify Prometheus collector unchanged
- SP6: `go test ./internal/node/adapter/l2tp/...` - verify no L2TP changes

---

## 14. Rollout Plan

**Phase 1:** Database migrations, alert schema, basic CRUD
**Phase 2:** Background sweeper (cert-expiry + quota alerts)
**Phase 3:** Rollup generation (hourly/daily)
**Phase 4:** HTTP API (alerts + history endpoints)
**Phase 5:** Dashboard UI (fleet overview, node detail, alerts list)
**Phase 6:** Testing and refinement

Each phase is independently testable and deployable.

---

## 15. Open Questions

None. Design is complete pending approval.

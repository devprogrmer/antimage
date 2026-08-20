# SP7 — Observability Depth Implementation Plan

**Date:** 2026-08-20  
**Status:** Draft  
**Spec:** `2026-08-20-sp7-observability-depth.md`

---

## Implementation Phases

SP7 divides into 6 independently verifiable phases. Each phase:
1. Implements a complete vertical slice
2. Adds tests
3. Passes `go test`, `go vet`, `golangci-lint`
4. Preserves SP1-SP6 behavior (regression tests)
5. Commits with clear message
6. Reports results before next phase

---

## Phase 1: Database Schema and Migrations

**Goal:** Add alert tables, rollup tables, and retention triggers

**Tasks:**

### 1.1 Create Migration File
- `internal/panel/store/migrations/00015_observability.sql`
- Define `alerts`, `node_health_rollups_hourly`, `node_health_rollups_daily` tables
- Add 7-day retention trigger for `node_health`
- Add 90-day retention triggers for rollups and alerts

### 1.2 Schema Details

```sql
-- +goose Up
-- SP7: Observability Depth

-- Persistent alerts with lifecycle tracking
CREATE TABLE alerts (
    id              INTEGER PRIMARY KEY,
    alert_type      TEXT NOT NULL CHECK (alert_type IN ('cert_expiry', 'quota_warning')),
    severity        TEXT NOT NULL CHECK (severity IN ('warning', 'critical')),
    target_type     TEXT NOT NULL CHECK (target_type IN ('node', 'subject')),
    target_id       INTEGER NOT NULL,
    state           TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'resolved')),
    dedup_key       TEXT NOT NULL UNIQUE,
    first_seen_at   INTEGER NOT NULL,
    last_seen_at    INTEGER NOT NULL,
    resolved_at     INTEGER,
    threshold_value TEXT,
    current_value   TEXT,
    metadata        TEXT NOT NULL DEFAULT '{}',
    CHECK (state = 'resolved' OR resolved_at IS NULL)
) STRICT;

CREATE INDEX alerts_active ON alerts(state, alert_type) WHERE state = 'active';
CREATE INDEX alerts_target ON alerts(target_type, target_id);
CREATE INDEX alerts_first_seen ON alerts(first_seen_at DESC);

-- Hourly rollups of node health metrics
CREATE TABLE node_health_rollups_hourly (
    node_id         INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    hour_start      INTEGER NOT NULL,
    samples         INTEGER NOT NULL,
    avg_load1       REAL NOT NULL,
    avg_mem_used    INTEGER NOT NULL,
    min_rtt_ms      INTEGER,
    avg_rtt_ms      INTEGER,
    max_rtt_ms      INTEGER,
    uptime_seconds  INTEGER NOT NULL,
    PRIMARY KEY (node_id, hour_start)
) STRICT;

CREATE INDEX node_health_hourly_time ON node_health_rollups_hourly(hour_start DESC);

-- Daily rollups of node health metrics
CREATE TABLE node_health_rollups_daily (
    node_id         INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    day_start       INTEGER NOT NULL,
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

-- Retention: 7 days for detailed node_health
-- +goose StatementBegin
CREATE TRIGGER node_health_cleanup
AFTER INSERT ON node_health
BEGIN
    DELETE FROM node_health
    WHERE at < (SELECT MAX(at) FROM node_health) - (7 * 86400);
END;
-- +goose StatementEnd

-- Retention: 90 days for hourly rollups
-- +goose StatementBegin
CREATE TRIGGER node_health_hourly_cleanup
AFTER INSERT ON node_health_rollups_hourly
BEGIN
    DELETE FROM node_health_rollups_hourly
    WHERE hour_start < (SELECT MAX(hour_start) FROM node_health_rollups_hourly) - (90 * 86400);
END;
-- +goose StatementEnd

-- Retention: 90 days for daily rollups
-- +goose StatementBegin
CREATE TRIGGER node_health_daily_cleanup
AFTER INSERT ON node_health_rollups_daily
BEGIN
    DELETE FROM node_health_rollups_daily
    WHERE day_start < (SELECT MAX(day_start) FROM node_health_rollups_daily) - (90 * 86400);
END;
-- +goose StatementEnd

-- Retention: 90 days for resolved alerts
-- +goose StatementBegin
CREATE TRIGGER alerts_cleanup
AFTER INSERT ON alerts
BEGIN
    DELETE FROM alerts
    WHERE state = 'resolved' AND resolved_at < unixepoch() - (90 * 86400);
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER alerts_cleanup;
DROP TRIGGER node_health_daily_cleanup;
DROP TRIGGER node_health_hourly_cleanup;
DROP TRIGGER node_health_cleanup;
DROP TABLE node_health_rollups_daily;
DROP TABLE node_health_rollups_hourly;
DROP TABLE alerts;
```

### 1.3 Tests
- `internal/panel/store/migrations_test.go` - verify migration applies cleanly
- Insert sample data, verify triggers fire correctly
- Test retention: insert old data, insert new data, verify old data deleted

### 1.4 Verification
```bash
go test ./internal/panel/store/...
go vet ./...
golangci-lint run
git diff
```

**Expected result:** Migration applies, triggers work, all tests pass

**Commit:** `feat(sp7): Phase 1 - database schema for alerts and rollups`

---

## Phase 2: Observability Package - Alert CRUD

**Goal:** Implement alert creation, update, resolution logic

**Tasks:**

### 2.1 Create Package Structure
```
internal/panel/observability/
  alerts.go
  alerts_test.go
```

### 2.2 Core Alert Functions

**`alerts.go`:**
```go
package observability

import (
    "context"
    "database/sql"
    "encoding/json"
    "fmt"
    "time"

    "github.com/amyrm/antimage/internal/panel/store"
)

type AlertType string
const (
    AlertTypeCertExpiry   AlertType = "cert_expiry"
    AlertTypeQuotaWarning AlertType = "quota_warning"
)

type Severity string
const (
    SeverityWarning  Severity = "warning"
    SeverityCritical Severity = "critical"
)

type AlertState string
const (
    StateActive   AlertState = "active"
    StateResolved AlertState = "resolved"
)

type TargetType string
const (
    TargetNode    TargetType = "node"
    TargetSubject TargetType = "subject"
)

type Alert struct {
    ID             int64
    AlertType      AlertType
    Severity       Severity
    TargetType     TargetType
    TargetID       int64
    State          AlertState
    DedupKey       string
    FirstSeenAt    time.Time
    LastSeenAt     time.Time
    ResolvedAt     *time.Time
    ThresholdValue string
    CurrentValue   string
    Metadata       map[string]interface{}
}

// CreateOrUpdateAlert creates a new alert or updates last_seen_at if already active.
// Returns (alert_id, created=true/false, error).
func CreateOrUpdateAlert(ctx context.Context, s *store.Store, a Alert, now time.Time) (int64, bool, error) {
    // Implementation
}

// ResolveAlert marks an active alert as resolved.
func ResolveAlert(ctx context.Context, s *store.Store, dedupKey string, now time.Time) error {
    // Implementation
}

// ListAlerts queries alerts with filters and pagination.
func ListAlerts(ctx context.Context, s *store.Store, filters AlertFilters) ([]Alert, int, error) {
    // Implementation
}

type AlertFilters struct {
    State      AlertState
    AlertType  AlertType
    Severity   Severity
    TargetType TargetType
    TargetID   *int64
    Limit      int
    Offset     int
}
```

### 2.3 Tests (`alerts_test.go`)
- `TestCreateOrUpdateAlert_New` - insert new alert, verify ID returned, `created=true`
- `TestCreateOrUpdateAlert_Existing` - insert duplicate dedup_key, verify `last_seen_at` updated, `created=false`
- `TestCreateOrUpdateAlert_DedupKeyUnique` - verify UNIQUE constraint enforced
- `TestResolveAlert` - mark alert resolved, verify `state='resolved'`, `resolved_at` set
- `TestResolveAlert_NotFound` - resolve non-existent alert, verify no error (idempotent)
- `TestListAlerts_ActiveOnly` - filter by `state=active`, verify only active returned
- `TestListAlerts_Pagination` - insert 100 alerts, query with limit/offset, verify correct slice

### 2.4 Verification
```bash
go test ./internal/panel/observability/...
go vet ./...
golangci-lint run
git diff
```

**Expected result:** All alert CRUD tests pass

**Commit:** `feat(sp7): Phase 2 - alert CRUD and deduplication logic`

---

## Phase 3: Background Sweeper

**Goal:** Implement cert-expiry and quota threshold detection

**Tasks:**

### 3.1 Sweeper Implementation

**`sweeper.go`:**
```go
package observability

import (
    "context"
    "fmt"
    "log"
    "time"

    "github.com/amyrm/antimage/internal/panel/nodes"
    "github.com/amyrm/antimage/internal/panel/store"
)

const (
    CertWarningThreshold  = 30 * 24 * time.Hour // 30 days
    CertCriticalThreshold = 7 * 24 * time.Hour  // 7 days
    QuotaWarningPercent   = 0.80                // 80%
    QuotaCriticalPercent  = 0.95                // 95%
)

type Sweeper struct {
    store *store.Store
}

func NewSweeper(s *store.Store) *Sweeper {
    return &Sweeper{store: s}
}

// Run starts the background sweeper, runs every 5 minutes.
func (sw *Sweeper) Run(ctx context.Context) {
    ticker := time.NewTicker(5 * time.Minute)
    defer ticker.Stop()

    // Run immediately on start
    sw.sweep(ctx)

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            sw.sweep(ctx)
        }
    }
}

func (sw *Sweeper) sweep(ctx context.Context) {
    // Wrap in recover to prevent panic from crashing panel
    defer func() {
        if r := recover(); r != nil {
            log.Printf("[observability] sweeper panic: %v", r)
        }
    }()

    if err := sw.checkCertificates(ctx); err != nil {
        log.Printf("[observability] cert check failed: %v", err)
    }

    if err := sw.checkQuotas(ctx); err != nil {
        log.Printf("[observability] quota check failed: %v", err)
    }
}

func (sw *Sweeper) checkCertificates(ctx context.Context) error {
    // Query nodes with certificates
    // Calculate NotAfter = enrolled_at + NodeCertLifetime
    // Check thresholds, create/update/resolve alerts
}

func (sw *Sweeper) checkQuotas(ctx context.Context) error {
    // Query subjects with quotas (quota_bytes IS NOT NULL, frozen_at IS NULL)
    // Calculate usage percent
    // Check thresholds, create/update/resolve alerts
}
```

### 3.2 Tests (`sweeper_test.go`)
- `TestCheckCertificates_Warning` - node enrolled 335 days ago → warning alert
- `TestCheckCertificates_Critical` - node enrolled 358 days ago → critical alert
- `TestCheckCertificates_Resolved` - node renewed (enrolled_at updated) → alert resolved
- `TestCheckCertificates_NoAlert` - node enrolled 200 days ago → no alert
- `TestCheckQuotas_Warning` - subject at 85% usage → warning alert
- `TestCheckQuotas_Critical` - subject at 96% usage → critical alert
- `TestCheckQuotas_Resolved` - subject usage drops to 75% → alert resolved
- `TestSweeper_Panic` - sweeper catches panic, does not crash

### 3.3 Integration Test
- Start sweeper in test goroutine
- Insert node with near-expiry cert
- Wait 6 seconds (sweeper runs every 5 min in prod, use shorter interval in test)
- Verify alert created
- Update enrolled_at (simulate renewal)
- Wait 6 seconds
- Verify alert resolved

### 3.4 Verification
```bash
go test ./internal/panel/observability/...
go vet ./...
golangci-lint run
git diff
```

**Expected result:** All sweeper tests pass, cert/quota alerts detected correctly

**Commit:** `feat(sp7): Phase 3 - background sweeper for cert and quota alerts`

---

## Phase 4: Rollup Generation

**Goal:** Implement hourly/daily aggregation of node health metrics

**Tasks:**

### 4.1 Rollup Implementation

**`rollups.go`:**
```go
package observability

import (
    "context"
    "database/sql"
    "fmt"
    "log"
    "time"

    "github.com/amyrm/antimage/internal/panel/store"
)

type RollupGenerator struct {
    store *store.Store
}

func NewRollupGenerator(s *store.Store) *RollupGenerator {
    return &RollupGenerator{store: s}
}

// RunHourly starts hourly rollup generation, runs at minute 5 of each hour.
func (rg *RollupGenerator) RunHourly(ctx context.Context) {
    // Calculate next hour + 5 minutes
    // Sleep until then
    // Generate rollup for previous hour
    // Repeat
}

// RunDaily starts daily rollup generation, runs at 00:15 each day.
func (rg *RollupGenerator) RunDaily(ctx context.Context) {
    // Similar to hourly, but for daily aggregation
}

func (rg *RollupGenerator) generateHourlyRollup(ctx context.Context, hourStart time.Time) error {
    // INSERT OR REPLACE INTO node_health_rollups_hourly
    // SELECT node_id, hour_start, COUNT(*), AVG(load1), AVG(mem_used), ...
    // FROM node_health
    // WHERE at >= ? AND at < ?
    // GROUP BY node_id
}

func (rg *RollupGenerator) generateDailyRollup(ctx context.Context, dayStart time.Time) error {
    // Aggregate from node_health_rollups_hourly for previous day
}
```

### 4.2 Tests (`rollups_test.go`)
- `TestGenerateHourlyRollup` - insert 120 heartbeats over 1 hour, generate rollup, verify averages correct
- `TestGenerateHourlyRollup_MultipleNodes` - 2 nodes, verify separate rollups
- `TestGenerateHourlyRollup_NoData` - generate rollup for hour with no data, verify no error
- `TestGenerateDailyRollup` - insert 24 hourly rollups, generate daily rollup, verify aggregation
- `TestRollupTrigger_Retention` - insert rollup 91 days old, insert new rollup, verify old one deleted

### 4.3 Verification
```bash
go test ./internal/panel/observability/...
go vet ./...
golangci-lint run
git diff
```

**Expected result:** All rollup tests pass, aggregations correct, retention works

**Commit:** `feat(sp7): Phase 4 - hourly and daily rollup generation`

---

## Phase 5: HTTP API Endpoints

**Goal:** Expose alerts and history through REST API

**Tasks:**

### 5.1 API Handlers

**`internal/panel/httpapi/alerts.go`:**
```go
package httpapi

import (
    "encoding/json"
    "net/http"
    "strconv"

    "github.com/amyrm/antimage/internal/panel/observability"
    "github.com/amyrm/antimage/internal/panel/rbac"
)

type AlertJSON struct {
    ID             int64                  `json:"id"`
    AlertType      string                 `json:"alert_type"`
    Severity       string                 `json:"severity"`
    TargetType     string                 `json:"target_type"`
    TargetID       int64                  `json:"target_id"`
    State          string                 `json:"state"`
    FirstSeenAt    string                 `json:"first_seen_at"`
    LastSeenAt     string                 `json:"last_seen_at"`
    ResolvedAt     *string                `json:"resolved_at"`
    ThresholdValue string                 `json:"threshold_value"`
    CurrentValue   string                 `json:"current_value"`
    Metadata       map[string]interface{} `json:"metadata"`
}

func (d Deps) handleListAlerts(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    admin := rbac.AdminFromContext(ctx)
    if admin == nil {
        http.Error(w, "unauthorized", http.StatusUnauthorized)
        return
    }

    // Check permission
    if !admin.HasPermission("alerts:read") {
        http.Error(w, "forbidden", http.StatusForbidden)
        return
    }

    // Parse query params
    state := r.URL.Query().Get("state")
    if state == "" {
        state = "active"
    }

    limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
    if limit == 0 {
        limit = 50
    }
    if limit > 200 {
        limit = 200
    }

    offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

    // Query alerts
    filters := observability.AlertFilters{
        State:  observability.AlertState(state),
        Limit:  limit,
        Offset: offset,
    }

    alerts, total, err := observability.ListAlerts(ctx, d.Store, filters)
    if err != nil {
        http.Error(w, "internal error", http.StatusInternalServerError)
        return
    }

    // TODO: Filter by admin_scopes (only show alerts for accessible nodes/subjects)

    // Convert to JSON
    alertsJSON := make([]AlertJSON, len(alerts))
    for i, a := range alerts {
        alertsJSON[i] = AlertJSON{
            ID:             a.ID,
            AlertType:      string(a.AlertType),
            Severity:       string(a.Severity),
            TargetType:     string(a.TargetType),
            TargetID:       a.TargetID,
            State:          string(a.State),
            FirstSeenAt:    a.FirstSeenAt.Format(time.RFC3339),
            LastSeenAt:     a.LastSeenAt.Format(time.RFC3339),
            ThresholdValue: a.ThresholdValue,
            CurrentValue:   a.CurrentValue,
            Metadata:       a.Metadata,
        }
        if a.ResolvedAt != nil {
            resolved := a.ResolvedAt.Format(time.RFC3339)
            alertsJSON[i].ResolvedAt = &resolved
        }
    }

    response := map[string]interface{}{
        "alerts": alertsJSON,
        "total":  total,
        "limit":  limit,
        "offset": offset,
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(response)
}
```

**`internal/panel/httpapi/node_history.go`:**
```go
package httpapi

import (
    "encoding/json"
    "net/http"
    "strconv"
    "time"

    "github.com/go-chi/chi/v5"
)

type HistoryDataPoint struct {
    Timestamp string  `json:"timestamp"`
    Value     float64 `json:"value,omitempty"`
    Avg       float64 `json:"avg,omitempty"`
    Min       int64   `json:"min,omitempty"`
    Max       int64   `json:"max,omitempty"`
    Samples   int     `json:"samples,omitempty"`
}

func (d Deps) handleNodeHistory(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    nodeID := chi.URLParam(r, "nodeID")

    var id int64
    if _, err := fmt.Sscanf(nodeID, "%d", &id); err != nil {
        http.Error(w, "invalid node id", http.StatusBadRequest)
        return
    }

    // Check RBAC
    // TODO: verify admin has access to this node

    metric := r.URL.Query().Get("metric")
    if metric == "" {
        http.Error(w, "metric required", http.StatusBadRequest)
        return
    }

    granularity := r.URL.Query().Get("granularity")
    if granularity == "" {
        granularity = "raw"
    }

    limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
    if limit == 0 {
        limit = 1000
    }

    // Query data based on metric and granularity
    var data []HistoryDataPoint
    var err error

    switch granularity {
    case "raw":
        // Query node_health or connection_metrics
        data, err = queryRawMetric(ctx, d.Store, id, metric, limit)
    case "hourly":
        // Query node_health_rollups_hourly
        data, err = queryHourlyMetric(ctx, d.Store, id, metric, limit)
    case "daily":
        // Query node_health_rollups_daily
        data, err = queryDailyMetric(ctx, d.Store, id, metric, limit)
    default:
        http.Error(w, "invalid granularity", http.StatusBadRequest)
        return
    }

    if err != nil {
        http.Error(w, "internal error", http.StatusInternalServerError)
        return
    }

    response := map[string]interface{}{
        "metric":      metric,
        "granularity": granularity,
        "node_id":     id,
        "data":        data,
        "total":       len(data),
        "limit":       limit,
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(response)
}

func queryRawMetric(ctx context.Context, s *store.Store, nodeID int64, metric string, limit int) ([]HistoryDataPoint, error) {
    // Implementation: query node_health or connection_metrics
}

func queryHourlyMetric(ctx context.Context, s *store.Store, nodeID int64, metric string, limit int) ([]HistoryDataPoint, error) {
    // Implementation: query node_health_rollups_hourly
}

func queryDailyMetric(ctx context.Context, s *store.Store, nodeID int64, metric string, limit int) ([]HistoryDataPoint, error) {
    // Implementation: query node_health_rollups_daily
}
```

**`internal/panel/httpapi/fleet_summary.go`:**
```go
package httpapi

import (
    "encoding/json"
    "net/http"
)

type FleetSummaryJSON struct {
    TotalNodes       int            `json:"total_nodes"`
    ByStatus         map[string]int `json:"by_status"`
    ActiveAlerts     map[string]int `json:"active_alerts"`
    AvgFleetRTTMs    *int64         `json:"avg_fleet_rtt_ms"`
    NodesWithIssues  int            `json:"nodes_with_issues"`
}

func (d Deps) handleFleetSummary(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    // Check RBAC
    // TODO: filter by admin_scopes

    // Query aggregated fleet metrics
    summary := FleetSummaryJSON{}

    // Count nodes by status
    summary.ByStatus = make(map[string]int)
    // SELECT status, COUNT(*) FROM nodes GROUP BY status

    // Count active alerts by severity
    summary.ActiveAlerts = make(map[string]int)
    // SELECT severity, COUNT(*) FROM alerts WHERE state='active' GROUP BY severity

    // Calculate average fleet RTT
    // SELECT AVG(avg_rtt_ms) FROM (SELECT AVG(rtt_ms) as avg_rtt_ms FROM connection_metrics GROUP BY node_id)

    // Count nodes with issues (degraded + offline + active alerts)
    // SELECT COUNT(DISTINCT node_id) FROM ...

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(summary)
}
```

### 5.2 Router Updates

**`internal/panel/httpapi/router.go`:**
```go
// Add to private routes
private.Get("/alerts", d.handleListAlerts)
private.Get("/nodes/{nodeID}/history", d.handleNodeHistory)
private.Get("/fleet/summary", d.handleFleetSummary)
```

### 5.3 Tests
- `alerts_test.go` - test list alerts endpoint, verify filtering, pagination, RBAC
- `node_history_test.go` - test history endpoint, verify metrics, granularity, limits
- `fleet_summary_test.go` - test fleet summary, verify aggregations

### 5.4 Verification
```bash
go test ./internal/panel/httpapi/...
go vet ./...
golangci-lint run
git diff
```

**Expected result:** All API tests pass, endpoints return correct data

**Commit:** `feat(sp7): Phase 5 - HTTP API for alerts and metrics history`

---

## Phase 6: Dashboard UI

**Goal:** Build operational observability dashboard in React

**Tasks:**

### 6.1 API Client Functions

**`internal/panel/webui/src/api/observability.ts`:**
```typescript
export interface Alert {
  id: number;
  alert_type: 'cert_expiry' | 'quota_warning';
  severity: 'warning' | 'critical';
  target_type: 'node' | 'subject';
  target_id: number;
  state: 'active' | 'resolved';
  first_seen_at: string;
  last_seen_at: string;
  resolved_at?: string;
  threshold_value: string;
  current_value: string;
  metadata: Record<string, any>;
}

export interface AlertsResponse {
  alerts: Alert[];
  total: number;
  limit: number;
  offset: number;
}

export async function fetchAlerts(params: {
  state?: 'active' | 'resolved' | 'all';
  alert_type?: string;
  limit?: number;
  offset?: number;
}): Promise<AlertsResponse> {
  const query = new URLSearchParams(params as any).toString();
  const res = await fetch(`/api/v1/alerts?${query}`);
  if (!res.ok) throw new Error('Failed to fetch alerts');
  return res.json();
}

export interface HistoryDataPoint {
  timestamp: string;
  value?: number;
  avg?: number;
  min?: number;
  max?: number;
  samples?: number;
}

export interface NodeHistoryResponse {
  metric: string;
  granularity: string;
  node_id: number;
  data: HistoryDataPoint[];
  total: number;
  limit: number;
}

export async function fetchNodeHistory(
  nodeId: number,
  metric: 'rtt' | 'load' | 'memory' | 'uptime',
  granularity: 'raw' | 'hourly' | 'daily',
  limit?: number
): Promise<NodeHistoryResponse> {
  const params = new URLSearchParams({ metric, granularity, limit: String(limit || 1000) });
  const res = await fetch(`/api/v1/nodes/${nodeId}/history?${params}`);
  if (!res.ok) throw new Error('Failed to fetch node history');
  return res.json();
}

export interface FleetSummary {
  total_nodes: number;
  by_status: Record<string, number>;
  active_alerts: Record<string, number>;
  avg_fleet_rtt_ms?: number;
  nodes_with_issues: number;
}

export async function fetchFleetSummary(): Promise<FleetSummary> {
  const res = await fetch('/api/v1/fleet/summary');
  if (!res.ok) throw new Error('Failed to fetch fleet summary');
  return res.json();
}
```

### 6.2 Dashboard Components

**`internal/panel/webui/src/pages/Dashboard.tsx`:**
```tsx
import React from 'react';
import { useQuery } from '@tanstack/react-query';
import { fetchFleetSummary, fetchAlerts } from '../api/observability';
import { FleetOverview } from '../components/FleetOverview';
import { ActiveAlertsList } from '../components/ActiveAlertsList';

export function Dashboard() {
  const { data: summary, isLoading: summaryLoading } = useQuery({
    queryKey: ['fleet-summary'],
    queryFn: fetchFleetSummary,
    refetchInterval: 30000, // Refresh every 30s
  });

  const { data: alerts, isLoading: alertsLoading } = useQuery({
    queryKey: ['alerts', 'active'],
    queryFn: () => fetchAlerts({ state: 'active', limit: 10 }),
    refetchInterval: 30000,
  });

  if (summaryLoading || alertsLoading) {
    return <div className="p-8">Loading...</div>;
  }

  return (
    <div className="p-8 space-y-6">
      <h1 className="text-2xl font-bold">Fleet Dashboard</h1>

      <FleetOverview summary={summary!} />
      
      <ActiveAlertsList alerts={alerts!.alerts} />

      {/* Node grid will be added here */}
    </div>
  );
}
```

**`internal/panel/webui/src/components/FleetOverview.tsx`:**
```tsx
import React from 'react';
import { FleetSummary } from '../api/observability';

interface Props {
  summary: FleetSummary;
}

export function FleetOverview({ summary }: Props) {
  return (
    <div className="grid grid-cols-1 md:grid-cols-4 gap-4">
      <div className="bg-white p-6 rounded-lg shadow">
        <div className="text-sm text-gray-500">Total Nodes</div>
        <div className="text-3xl font-bold">{summary.total_nodes}</div>
      </div>

      <div className="bg-white p-6 rounded-lg shadow">
        <div className="text-sm text-gray-500">Online</div>
        <div className="text-3xl font-bold text-green-600">
          {summary.by_status['online'] || 0}
        </div>
      </div>

      <div className="bg-white p-6 rounded-lg shadow">
        <div className="text-sm text-gray-500">Active Alerts</div>
        <div className="text-3xl font-bold text-red-600">
          {(summary.active_alerts['warning'] || 0) + (summary.active_alerts['critical'] || 0)}
        </div>
      </div>

      <div className="bg-white p-6 rounded-lg shadow">
        <div className="text-sm text-gray-500">Avg Fleet RTT</div>
        <div className="text-3xl font-bold">
          {summary.avg_fleet_rtt_ms ? `${summary.avg_fleet_rtt_ms}ms` : '—'}
        </div>
      </div>
    </div>
  );
}
```

**`internal/panel/webui/src/components/ActiveAlertsList.tsx`:**
```tsx
import React from 'react';
import { Alert } from '../api/observability';
import { Link } from 'react-router-dom';

interface Props {
  alerts: Alert[];
}

export function ActiveAlertsList({ alerts }: Props) {
  if (alerts.length === 0) {
    return (
      <div className="bg-white p-6 rounded-lg shadow">
        <h2 className="text-lg font-semibold mb-4">Active Alerts</h2>
        <p className="text-gray-500">No active alerts. Your fleet is healthy.</p>
      </div>
    );
  }

  return (
    <div className="bg-white p-6 rounded-lg shadow">
      <div className="flex justify-between items-center mb-4">
        <h2 className="text-lg font-semibold">Active Alerts</h2>
        <Link to="/dashboard/alerts" className="text-blue-600 hover:underline">
          View all
        </Link>
      </div>

      <table className="w-full">
        <thead>
          <tr className="text-left text-sm text-gray-500 border-b">
            <th className="pb-2">Severity</th>
            <th className="pb-2">Type</th>
            <th className="pb-2">Target</th>
            <th className="pb-2">First Seen</th>
          </tr>
        </thead>
        <tbody>
          {alerts.map((alert) => (
            <tr key={alert.id} className="border-b last:border-0">
              <td className="py-3">
                <span className={`px-2 py-1 rounded text-xs font-medium ${
                  alert.severity === 'critical' ? 'bg-red-100 text-red-800' : 'bg-yellow-100 text-yellow-800'
                }`}>
                  {alert.severity}
                </span>
              </td>
              <td className="py-3">{alert.alert_type}</td>
              <td className="py-3">
                {alert.target_type === 'node' ? (
                  <Link to={`/dashboard/nodes/${alert.target_id}`} className="text-blue-600 hover:underline">
                    {alert.metadata.node_name || `Node ${alert.target_id}`}
                  </Link>
                ) : (
                  alert.metadata.subject_name || `Subject ${alert.target_id}`
                )}
              </td>
              <td className="py-3">{new Date(alert.first_seen_at).toLocaleString()}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
```

**`internal/panel/webui/src/pages/NodeDetail.tsx`:**
```tsx
import React, { useState } from 'react';
import { useParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { fetchNodeHistory } from '../api/observability';
import { LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer } from 'recharts';

type TimeRange = '24h' | '7d' | '30d';

export function NodeDetail() {
  const { nodeId } = useParams<{ nodeId: string }>();
  const [timeRange, setTimeRange] = useState<TimeRange>('24h');
  const [metric, setMetric] = useState<'rtt' | 'load' | 'memory'>('rtt');

  const { data, isLoading } = useQuery({
    queryKey: ['node-history', nodeId, metric, timeRange],
    queryFn: () => fetchNodeHistory(Number(nodeId), metric, 'raw', 1000),
    refetchInterval: 60000,
  });

  if (isLoading) {
    return <div className="p-8">Loading...</div>;
  }

  const chartData = data!.data.map(d => ({
    time: new Date(d.timestamp).toLocaleTimeString(),
    value: d.value || d.avg,
  }));

  return (
    <div className="p-8 space-y-6">
      <h1 className="text-2xl font-bold">Node {nodeId}</h1>

      <div className="bg-white p-6 rounded-lg shadow">
        <div className="flex justify-between items-center mb-4">
          <div className="space-x-2">
            <button
              onClick={() => setMetric('rtt')}
              className={metric === 'rtt' ? 'font-bold' : ''}
            >
              RTT
            </button>
            <button
              onClick={() => setMetric('load')}
              className={metric === 'load' ? 'font-bold' : ''}
            >
              Load
            </button>
            <button
              onClick={() => setMetric('memory')}
              className={metric === 'memory' ? 'font-bold' : ''}
            >
              Memory
            </button>
          </div>

          <div className="space-x-2">
            <button onClick={() => setTimeRange('24h')}>24h</button>
            <button onClick={() => setTimeRange('7d')}>7d</button>
            <button onClick={() => setTimeRange('30d')}>30d</button>
          </div>
        </div>

        <ResponsiveContainer width="100%" height={300}>
          <LineChart data={chartData}>
            <CartesianGrid strokeDasharray="3 3" />
            <XAxis dataKey="time" />
            <YAxis />
            <Tooltip />
            <Line type="monotone" dataKey="value" stroke="#3b82f6" />
          </LineChart>
        </ResponsiveContainer>
      </div>
    </div>
  );
}
```

### 6.3 Router Updates

**`internal/panel/webui/src/App.tsx`:**
```tsx
<Route path="/dashboard" element={<Dashboard />} />
<Route path="/dashboard/nodes/:nodeId" element={<NodeDetail />} />
<Route path="/dashboard/alerts" element={<AlertsPage />} />
```

### 6.4 Tests
- Unit tests for components (vitest + React Testing Library)
- Integration tests for API calls (MSW mock handlers)
- E2E tests for dashboard flows (Playwright)

### 6.5 Verification
```bash
cd internal/panel/webui
npm test
npm run build
```

**Expected result:** UI builds successfully, components render correctly

**Commit:** `feat(sp7): Phase 6 - dashboard UI for observability`

---

## Phase 7: Integration and Final Testing

**Goal:** Wire sweeper and rollup generators to panel startup, verify end-to-end

**Tasks:**

### 7.1 Panel Integration

**`cmd/antimage-panel/main.go`:**
```go
// Start observability sweeper
sweeper := observability.NewSweeper(st)
go sweeper.Run(ctx)

// Start rollup generators
rollups := observability.NewRollupGenerator(st)
go rollups.RunHourly(ctx)
go rollups.RunDaily(ctx)
```

### 7.2 End-to-End Tests
- Start panel with test database
- Enroll node with cert expiring in 25 days
- Wait for sweeper cycle
- Verify cert-expiry warning alert created via HTTP API
- Query node history via HTTP API
- Verify rollup generated correctly

### 7.3 Regression Tests
```bash
# SP1: Node health ingestion
go test ./internal/panel/nodes/...

# SP3: Quota enforcement
go test ./internal/panel/subjects/...

# SP5: Prometheus metrics
go test ./internal/panel/metrics/...

# SP6: L2TP adapter
go test ./internal/node/adapter/l2tp/...
```

### 7.4 Full Test Suite
```bash
go test ./...
go vet ./...
golangci-lint run
```

**Expected result:** All tests pass, no regressions

**Commit:** `feat(sp7): Phase 7 - integrate sweeper and rollup generators`

---

## Definition of Done

- [ ] All 7 phases complete
- [ ] Database migrations applied cleanly
- [ ] Alert CRUD works with deduplication
- [ ] Sweeper detects cert-expiry and quota alerts correctly
- [ ] Rollup generation produces correct hourly/daily aggregates
- [ ] HTTP API endpoints return correct data with pagination
- [ ] Dashboard UI renders fleet overview, node detail, alerts list
- [ ] RBAC enforced on all endpoints
- [ ] go test ./... passes
- [ ] go vet ./... passes
- [ ] golangci-lint run passes
- [ ] No SP1-SP6 regressions
- [ ] No SP6 L2TP/IPsec data-plane modifications
- [ ] Feature branch created: sp7-observability-depth
- [ ] All commits include Co-Authored-By line
- [ ] PR created targeting sp1-control-plane-spine
- [ ] NOT merged automatically
- [ ] SP8 NOT started

---

## Estimated Timeline

- Phase 1: 1-2 hours (schema, migrations)
- Phase 2: 2-3 hours (alert CRUD)
- Phase 3: 3-4 hours (sweeper logic + tests)
- Phase 4: 2-3 hours (rollup generation)
- Phase 5: 4-5 hours (HTTP API + tests)
- Phase 6: 6-8 hours (dashboard UI)
- Phase 7: 2-3 hours (integration + regression)

**Total: ~20-28 hours** (2.5-3.5 days of focused work)

---

## Dependencies Summary

**SP1 provides:**
- `nodes` table, `node_health` table
- gRPC `Heartbeat` protocol
- `nodes.RecordHeartbeat()` function
- HTTP API infrastructure
- RBAC system
- React UI shell

**SP3 provides:**
- `subjects` table with quota fields
- Quota enforcement (read-only for SP7)

**SP5 provides:**
- `connection_metrics` table
- `nodes.RecordRTT()`, `nodes.GetMetrics()`
- Prometheus metrics collector

**SP6:**
- No interaction (L2TP/IPsec boundary preserved)

**SP8 will consume:**
- RBAC-aware alert queries
- Metrics APIs (already scoped by admin_scopes)
- Dashboard components (can be embedded or extended)

---

## Risk Mitigation

**Risk:** Sweeper causes DB lock contention
**Mitigation:** Run sweeper every 5 minutes, queries use indexes, write transactions are short

**Risk:** Rollup generation fails mid-aggregation
**Mitigation:** Use INSERT OR REPLACE (idempotent), wrap in transaction, log errors

**Risk:** Dashboard polling overloads API
**Mitigation:** 30-second refresh interval, pagination limits enforced, indexes on time columns

**Risk:** Alert deduplication fails (duplicate alerts)
**Mitigation:** UNIQUE constraint on dedup_key, tests verify constraint enforcement

**Risk:** SP1-SP6 regression
**Mitigation:** Run full test suite after each phase, explicit regression test list in Phase 7

---

## Open Issues

None. Plan is complete pending approval.

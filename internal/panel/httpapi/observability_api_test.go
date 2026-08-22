package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/observability"
)

func TestListAlertsAPI(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "admin", "pw", "super_admin")
	token := env.login(t, "admin", "pw")

	// Create test node
	ctx := context.Background()
	var nodeID int64
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, status, created_at)
			 VALUES ('test-node', '10.0.0.1:8443', 'online', ?)`,
			time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, err = result.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}

	// Create test alert
	alert := observability.Alert{
		AlertType:      observability.AlertTypeCertExpiry,
		Severity:       observability.SeverityWarning,
		TargetType:     observability.TargetNode,
		TargetID:       nodeID,
		State:          observability.StateActive,
		DedupKey:       "cert_expiry:node:1:warning",
		ThresholdValue: "30 days",
		CurrentValue:   "25 days",
		Metadata: map[string]interface{}{
			"node_name":      "test-node",
			"days_remaining": 25,
		},
	}

	_, _, err = observability.CreateOrUpdateAlert(ctx, env.store, alert, time.Now())
	if err != nil {
		t.Fatalf("create alert: %v", err)
	}

	res := env.get(t, "/api/v1/alerts?state=active", token)
	if res.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", res.Code, res.Body)
	}

	var body AlertsResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(body.Alerts) == 0 {
		t.Error("expected at least one alert")
	}

	if body.Alerts[0].AlertType != "cert_expiry" {
		t.Errorf("expected alert_type=cert_expiry, got %s", body.Alerts[0].AlertType)
	}

	if body.Alerts[0].TargetID != nodeID {
		t.Errorf("expected target_id=%d, got %d", nodeID, body.Alerts[0].TargetID)
	}
}

func TestListAlertsUnauthorized(t *testing.T) {
	env := newTestEnv(t)

	res := env.get(t, "/api/v1/alerts", "")
	if res.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", res.Code)
	}
}

func TestListAlertsFiltering(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "admin", "pw", "super_admin")
	token := env.login(t, "admin", "pw")

	ctx := context.Background()

	// Create test node
	var nodeID int64
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, status, created_at)
			 VALUES ('test-node', '10.0.0.1:8443', 'online', ?)`,
			time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, err = result.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}

	// Create warning alert
	warning := observability.Alert{
		AlertType:      observability.AlertTypeCertExpiry,
		Severity:       observability.SeverityWarning,
		TargetType:     observability.TargetNode,
		TargetID:       nodeID,
		State:          observability.StateActive,
		DedupKey:       "cert_expiry:node:1:warning",
		ThresholdValue: "30 days",
		CurrentValue:   "25 days",
		Metadata:       map[string]interface{}{"node_name": "test-node"},
	}
	_, _, err = observability.CreateOrUpdateAlert(ctx, env.store, warning, time.Now())
	if err != nil {
		t.Fatalf("create warning alert: %v", err)
	}

	// Create critical alert
	critical := observability.Alert{
		AlertType:      observability.AlertTypeCertExpiry,
		Severity:       observability.SeverityCritical,
		TargetType:     observability.TargetNode,
		TargetID:       nodeID,
		State:          observability.StateActive,
		DedupKey:       "cert_expiry:node:1:critical",
		ThresholdValue: "7 days",
		CurrentValue:   "5 days",
		Metadata:       map[string]interface{}{"node_name": "test-node"},
	}
	_, _, err = observability.CreateOrUpdateAlert(ctx, env.store, critical, time.Now())
	if err != nil {
		t.Fatalf("create critical alert: %v", err)
	}

	// Filter by severity=warning
	res := env.get(t, "/api/v1/alerts?state=active&severity=warning", token)
	if res.Code != http.StatusOK {
		t.Fatalf("filter status = %d", res.Code)
	}

	var body AlertsResponse
	json.NewDecoder(res.Body).Decode(&body)

	if len(body.Alerts) != 1 {
		t.Errorf("expected 1 warning alert, got %d", len(body.Alerts))
	}
	if len(body.Alerts) > 0 && body.Alerts[0].Severity != "warning" {
		t.Errorf("expected severity=warning, got %s", body.Alerts[0].Severity)
	}

	// Filter by severity=critical
	res = env.get(t, "/api/v1/alerts?state=active&severity=critical", token)
	json.NewDecoder(res.Body).Decode(&body)

	if len(body.Alerts) != 1 {
		t.Errorf("expected 1 critical alert, got %d", len(body.Alerts))
	}
	if len(body.Alerts) > 0 && body.Alerts[0].Severity != "critical" {
		t.Errorf("expected severity=critical, got %s", body.Alerts[0].Severity)
	}
}

func TestNodeHistoryAPI(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "admin", "pw", "super_admin")
	token := env.login(t, "admin", "pw")

	ctx := context.Background()

	// Create test node
	var nodeID int64
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, status, created_at)
			 VALUES ('test-node', '10.0.0.1:8443', 'online', ?)`,
			time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, err = result.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}

	// Insert test health data
	now := time.Now().Unix()
	err = env.store.Write(ctx, func(tx *sql.Tx) error {
		for i := 0; i < 5; i++ {
			_, err := tx.ExecContext(ctx,
				`INSERT INTO node_health (node_id, at, load1, mem_used, uptime_s, rtt_ms)
				 VALUES (?, ?, 1.5, 1000000, 3600, 45)`,
				nodeID, now-int64(i*60))
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("insert health data: %v", err)
	}

	// Query RTT history
	res := env.get(t, "/api/v1/nodes/"+itoa64(nodeID)+"/history?metric=rtt&granularity=raw", token)
	if res.Code != http.StatusOK {
		t.Fatalf("history status = %d, body = %s", res.Code, res.Body)
	}

	var body NodeHistoryResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if body.Metric != "rtt" {
		t.Errorf("expected metric=rtt, got %s", body.Metric)
	}
	if body.NodeID != nodeID {
		t.Errorf("expected node_id=%d, got %d", nodeID, body.NodeID)
	}
	if len(body.Data) != 5 {
		t.Errorf("expected 5 data points, got %d", len(body.Data))
	}
	if len(body.Data) > 0 && body.Data[0].Value == nil {
		t.Error("expected value field in raw data")
	}
}

func TestNodeHistoryUnauthorized(t *testing.T) {
	env := newTestEnv(t)

	res := env.get(t, "/api/v1/nodes/1/history?metric=rtt", "")
	if res.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", res.Code)
	}
}

func TestNodeHistoryNotFound(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "admin", "pw", "super_admin")
	token := env.login(t, "admin", "pw")

	res := env.get(t, "/api/v1/nodes/99999/history?metric=rtt", token)
	if res.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", res.Code)
	}
}

func TestNodeHistoryInvalidMetric(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "admin", "pw", "super_admin")
	token := env.login(t, "admin", "pw")

	ctx := context.Background()
	var nodeID int64
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, status, created_at)
			 VALUES ('test-node', '10.0.0.1:8443', 'online', ?)`,
			time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, err = result.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}

	res := env.get(t, "/api/v1/nodes/"+itoa64(nodeID)+"/history?metric=invalid", token)
	if res.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500 for invalid metric, got %d", res.Code)
	}
}

func TestNodeHistoryHourlyRollup(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "admin", "pw", "super_admin")
	token := env.login(t, "admin", "pw")

	ctx := context.Background()

	// Create test node
	var nodeID int64
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, status, created_at)
			 VALUES ('test-node', '10.0.0.1:8443', 'online', ?)`,
			time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, err = result.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}

	// Insert hourly rollup data
	hourStart := time.Now().Truncate(time.Hour).Unix()
	err = env.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO node_health_rollups_hourly
			 (node_id, hour_start, samples, avg_load1, avg_mem_used, min_rtt_ms, avg_rtt_ms, max_rtt_ms, uptime_seconds)
			 VALUES (?, ?, 120, 1.5, 1000000, 40, 45, 50, 3600)`,
			nodeID, hourStart)
		return err
	})
	if err != nil {
		t.Fatalf("insert hourly data: %v", err)
	}

	// Query hourly RTT history
	res := env.get(t, "/api/v1/nodes/"+itoa64(nodeID)+"/history?metric=rtt&granularity=hourly", token)
	if res.Code != http.StatusOK {
		t.Fatalf("history status = %d, body = %s", res.Code, res.Body)
	}

	var body NodeHistoryResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if body.Granularity != "hourly" {
		t.Errorf("expected granularity=hourly, got %s", body.Granularity)
	}
	if len(body.Data) != 1 {
		t.Fatalf("expected 1 data point, got %d", len(body.Data))
	}

	dp := body.Data[0]
	if dp.Min == nil || *dp.Min != 40 {
		t.Errorf("expected min_rtt=40, got %v", dp.Min)
	}
	if dp.Avg == nil || *dp.Avg != 45 {
		t.Errorf("expected avg_rtt=45, got %v", dp.Avg)
	}
	if dp.Max == nil || *dp.Max != 50 {
		t.Errorf("expected max_rtt=50, got %v", dp.Max)
	}
	if dp.Samples == nil || *dp.Samples != 120 {
		t.Errorf("expected samples=120, got %v", dp.Samples)
	}
}

func TestFleetSummaryAPI(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "admin", "pw", "super_admin")
	token := env.login(t, "admin", "pw")

	ctx := context.Background()

	// Create test nodes
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, status, created_at)
			 VALUES
			 ('node-1', '10.0.0.1:8443', 'online', ?),
			 ('node-2', '10.0.0.2:8443', 'online', ?),
			 ('node-3', '10.0.0.3:8443', 'degraded', ?)`,
			time.Now().Unix(), time.Now().Unix(), time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("create nodes: %v", err)
	}

	res := env.get(t, "/api/v1/fleet/summary", token)
	if res.Code != http.StatusOK {
		t.Fatalf("summary status = %d, body = %s", res.Code, res.Body)
	}

	var body FleetSummaryJSON
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if body.TotalNodes != 3 {
		t.Errorf("expected total_nodes=3, got %d", body.TotalNodes)
	}
	if body.ByStatus["online"] != 2 {
		t.Errorf("expected 2 online nodes, got %d", body.ByStatus["online"])
	}
	if body.ByStatus["degraded"] != 1 {
		t.Errorf("expected 1 degraded node, got %d", body.ByStatus["degraded"])
	}
}

func TestFleetSummaryUnauthorized(t *testing.T) {
	env := newTestEnv(t)

	res := env.get(t, "/api/v1/fleet/summary", "")
	if res.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", res.Code)
	}
}

func TestFleetSummaryWithAlerts(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "admin", "pw", "super_admin")
	token := env.login(t, "admin", "pw")

	ctx := context.Background()

	// Create test node
	var nodeID int64
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, status, created_at)
			 VALUES ('test-node', '10.0.0.1:8443', 'online', ?)`,
			time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, err = result.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}

	// Create alerts
	warning := observability.Alert{
		AlertType:      observability.AlertTypeCertExpiry,
		Severity:       observability.SeverityWarning,
		TargetType:     observability.TargetNode,
		TargetID:       nodeID,
		State:          observability.StateActive,
		DedupKey:       "cert_expiry:node:" + itoa64(nodeID) + ":warning",
		ThresholdValue: "30 days",
		CurrentValue:   "25 days",
		Metadata:       map[string]interface{}{"node_name": "test-node"},
	}
	_, _, err = observability.CreateOrUpdateAlert(ctx, env.store, warning, time.Now())
	if err != nil {
		t.Fatalf("create warning: %v", err)
	}

	critical := observability.Alert{
		AlertType:      observability.AlertTypeCertExpiry,
		Severity:       observability.SeverityCritical,
		TargetType:     observability.TargetNode,
		TargetID:       nodeID,
		State:          observability.StateActive,
		DedupKey:       "cert_expiry:node:" + itoa64(nodeID) + ":critical",
		ThresholdValue: "7 days",
		CurrentValue:   "5 days",
		Metadata:       map[string]interface{}{"node_name": "test-node"},
	}
	_, _, err = observability.CreateOrUpdateAlert(ctx, env.store, critical, time.Now())
	if err != nil {
		t.Fatalf("create critical: %v", err)
	}

	res := env.get(t, "/api/v1/fleet/summary", token)
	if res.Code != http.StatusOK {
		t.Fatalf("summary status = %d", res.Code)
	}

	var body FleetSummaryJSON
	json.NewDecoder(res.Body).Decode(&body)

	if body.ActiveAlerts["warning"] != 1 {
		t.Errorf("expected 1 warning alert, got %d", body.ActiveAlerts["warning"])
	}
	if body.ActiveAlerts["critical"] != 1 {
		t.Errorf("expected 1 critical alert, got %d", body.ActiveAlerts["critical"])
	}
	if body.NodesWithIssues != 1 {
		t.Errorf("expected 1 node with issues, got %d", body.NodesWithIssues)
	}
}

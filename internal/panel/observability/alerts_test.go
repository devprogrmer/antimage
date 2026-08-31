package observability

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/testutil/storetest"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := storetest.OpenCopy(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCreateOrUpdateAlert_NewAlert(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	alert := Alert{
		AlertType:      AlertTypeCertExpiry,
		Severity:       SeverityWarning,
		TargetType:     TargetNode,
		TargetID:       1,
		DedupKey:       "cert_expiry:node:1:warning",
		ThresholdValue: "30 days",
		CurrentValue:   "25 days",
		Metadata: map[string]interface{}{
			"node_name":      "node-tokyo-01",
			"days_remaining": 25,
			"cert_not_after": "2027-01-15T10:30:00Z",
		},
	}

	id, created, err := CreateOrUpdateAlert(ctx, s, alert, now)
	if err != nil {
		t.Fatalf("CreateOrUpdateAlert: %v", err)
	}

	if !created {
		t.Error("expected created=true for new alert")
	}
	if id == 0 {
		t.Error("expected non-zero alert ID")
	}

	// Verify alert exists in database
	alerts, total, err := ListAlerts(ctx, s, AlertFilters{
		Scope: rbac.Scope{AdminID: 1, IsSuper: true},
		State: StateActive,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if total != 1 {
		t.Errorf("expected 1 alert, got %d", total)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert in list, got %d", len(alerts))
	}

	a := alerts[0]
	if a.ID != id {
		t.Errorf("alert ID = %d, want %d", a.ID, id)
	}
	if a.AlertType != AlertTypeCertExpiry {
		t.Errorf("alert_type = %s, want cert_expiry", a.AlertType)
	}
	if a.State != StateActive {
		t.Errorf("state = %s, want active", a.State)
	}
	if a.DedupKey != alert.DedupKey {
		t.Errorf("dedup_key = %s, want %s", a.DedupKey, alert.DedupKey)
	}
	if a.ThresholdValue != "30 days" {
		t.Errorf("threshold_value = %s, want '30 days'", a.ThresholdValue)
	}
	if a.CurrentValue != "25 days" {
		t.Errorf("current_value = %s, want '25 days'", a.CurrentValue)
	}
	if a.Metadata["node_name"] != "node-tokyo-01" {
		t.Errorf("metadata[node_name] = %v, want 'node-tokyo-01'", a.Metadata["node_name"])
	}
}

func TestCreateOrUpdateAlert_UpdateExisting(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	alert := Alert{
		AlertType:      AlertTypeQuotaWarning,
		Severity:       SeverityWarning,
		TargetType:     TargetSubject,
		TargetID:       5,
		DedupKey:       "quota:subject:5:warning",
		ThresholdValue: "80%",
		CurrentValue:   "82%",
		Metadata:       map[string]interface{}{"percent_used": 82.0},
	}

	// Create initial alert
	id1, created1, err := CreateOrUpdateAlert(ctx, s, alert, now)
	if err != nil {
		t.Fatalf("CreateOrUpdateAlert (first): %v", err)
	}
	if !created1 {
		t.Error("expected created=true for first alert")
	}

	// Wait a moment
	time.Sleep(10 * time.Millisecond)

	// Update with new current value (condition still active)
	later := now.Add(5 * time.Minute)
	alert.CurrentValue = "85%"
	alert.Metadata["percent_used"] = 85.0

	id2, created2, err := CreateOrUpdateAlert(ctx, s, alert, later)
	if err != nil {
		t.Fatalf("CreateOrUpdateAlert (second): %v", err)
	}

	if created2 {
		t.Error("expected created=false for update")
	}
	if id1 != id2 {
		t.Errorf("alert ID changed: first=%d, second=%d", id1, id2)
	}

	// Verify updated values
	alerts, total, err := ListAlerts(ctx, s, AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}, State: StateActive, Limit: 10})
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if total != 1 {
		t.Errorf("expected 1 alert, got %d", total)
	}

	a := alerts[0]
	if a.CurrentValue != "85%" {
		t.Errorf("current_value = %s, want '85%%'", a.CurrentValue)
	}
	if a.Metadata["percent_used"].(float64) != 85.0 {
		t.Errorf("metadata[percent_used] = %v, want 85.0", a.Metadata["percent_used"])
	}
	if !a.LastSeenAt.After(a.FirstSeenAt) {
		t.Error("last_seen_at should be after first_seen_at")
	}
}

func TestResolveAlert(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	alert := Alert{
		AlertType:      AlertTypeCertExpiry,
		Severity:       SeverityCritical,
		TargetType:     TargetNode,
		TargetID:       10,
		DedupKey:       "cert_expiry:node:10:critical",
		ThresholdValue: "7 days",
		CurrentValue:   "5 days",
		Metadata:       map[string]interface{}{"days_remaining": 5},
	}

	// Create alert
	id, _, err := CreateOrUpdateAlert(ctx, s, alert, now)
	if err != nil {
		t.Fatalf("CreateOrUpdateAlert: %v", err)
	}

	// Resolve it
	resolvedAt := now.Add(1 * time.Hour)
	if err := ResolveAlert(ctx, s, alert.DedupKey, resolvedAt); err != nil {
		t.Fatalf("ResolveAlert: %v", err)
	}

	// Verify resolved
	alerts, total, err := ListAlerts(ctx, s, AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}, State: StateResolved, Limit: 10})
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if total != 1 {
		t.Errorf("expected 1 resolved alert, got %d", total)
	}

	a := alerts[0]
	if a.ID != id {
		t.Errorf("alert ID = %d, want %d", a.ID, id)
	}
	if a.State != StateResolved {
		t.Errorf("state = %s, want resolved", a.State)
	}
	if a.ResolvedAt == nil {
		t.Fatal("resolved_at is nil")
	}
	if a.ResolvedAt.Before(a.FirstSeenAt) {
		t.Error("resolved_at should be after first_seen_at")
	}

	// Verify no longer in active list
	activeAlerts, activeTotal, err := ListAlerts(ctx, s, AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}, State: StateActive, Limit: 10})
	if err != nil {
		t.Fatalf("ListAlerts (active): %v", err)
	}
	if activeTotal != 0 {
		t.Errorf("expected 0 active alerts, got %d", activeTotal)
	}
	if len(activeAlerts) != 0 {
		t.Errorf("expected empty active alerts list, got %d", len(activeAlerts))
	}
}

func TestResolveAlert_Idempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	dedupKey := "cert_expiry:node:99:warning"

	// Resolve non-existent alert - should not error
	if err := ResolveAlert(ctx, s, dedupKey, now); err != nil {
		t.Errorf("ResolveAlert (non-existent): unexpected error: %v", err)
	}

	// Create and resolve alert
	alert := Alert{
		AlertType:      AlertTypeCertExpiry,
		Severity:       SeverityWarning,
		TargetType:     TargetNode,
		TargetID:       99,
		DedupKey:       dedupKey,
		ThresholdValue: "30 days",
		CurrentValue:   "28 days",
		Metadata:       map[string]interface{}{},
	}

	_, _, err := CreateOrUpdateAlert(ctx, s, alert, now)
	if err != nil {
		t.Fatalf("CreateOrUpdateAlert: %v", err)
	}

	if err := ResolveAlert(ctx, s, dedupKey, now); err != nil {
		t.Fatalf("ResolveAlert (first): %v", err)
	}

	// Resolve again - should not error
	if err := ResolveAlert(ctx, s, dedupKey, now); err != nil {
		t.Errorf("ResolveAlert (second): unexpected error: %v", err)
	}
}

func TestAlertLifecycle_ReAlert(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	dedupKey := "cert_expiry:node:20:warning"

	alert := Alert{
		AlertType:      AlertTypeCertExpiry,
		Severity:       SeverityWarning,
		TargetType:     TargetNode,
		TargetID:       20,
		DedupKey:       dedupKey,
		ThresholdValue: "30 days",
		CurrentValue:   "28 days",
		Metadata:       map[string]interface{}{"days_remaining": 28},
	}

	// Create first alert
	id1, created1, err := CreateOrUpdateAlert(ctx, s, alert, now)
	if err != nil {
		t.Fatalf("CreateOrUpdateAlert (first): %v", err)
	}
	if !created1 {
		t.Error("expected created=true for first alert")
	}

	// Resolve it (certificate renewed)
	resolvedAt := now.Add(1 * time.Hour)
	if err := ResolveAlert(ctx, s, dedupKey, resolvedAt); err != nil {
		t.Fatalf("ResolveAlert: %v", err)
	}

	// Condition re-occurs (certificate expired again after 6 months)
	reoccurAt := now.Add(180 * 24 * time.Hour)
	alert.CurrentValue = "25 days"
	alert.Metadata["days_remaining"] = 25

	id2, created2, err := CreateOrUpdateAlert(ctx, s, alert, reoccurAt)
	if err != nil {
		t.Fatalf("CreateOrUpdateAlert (re-alert): %v", err)
	}

	// Verify new alert created (not update)
	if !created2 {
		t.Error("expected created=true for re-alert")
	}
	if id1 == id2 {
		t.Error("expected different alert ID for re-alert")
	}

	// Verify both alerts exist
	_, total, err := ListAlerts(ctx, s, AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}, Limit: 10})
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if total != 2 {
		t.Errorf("expected 2 alerts total (1 resolved, 1 active), got %d", total)
	}

	// Verify one active, one resolved
	activeAlerts, activeTotal, err := ListAlerts(ctx, s, AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}, State: StateActive, Limit: 10})
	if err != nil {
		t.Fatalf("ListAlerts (active): %v", err)
	}
	if activeTotal != 1 {
		t.Errorf("expected 1 active alert, got %d", activeTotal)
	}
	if len(activeAlerts) != 1 {
		t.Fatalf("expected 1 alert in active list, got %d", len(activeAlerts))
	}
	if activeAlerts[0].ID != id2 {
		t.Errorf("active alert ID = %d, want %d (second alert)", activeAlerts[0].ID, id2)
	}

	resolvedAlerts, resolvedTotal, err := ListAlerts(ctx, s, AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}, State: StateResolved, Limit: 10})
	if err != nil {
		t.Fatalf("ListAlerts (resolved): %v", err)
	}
	if resolvedTotal != 1 {
		t.Errorf("expected 1 resolved alert, got %d", resolvedTotal)
	}
	if len(resolvedAlerts) != 1 {
		t.Fatalf("expected 1 alert in resolved list, got %d", len(resolvedAlerts))
	}
	if resolvedAlerts[0].ID != id1 {
		t.Errorf("resolved alert ID = %d, want %d (first alert)", resolvedAlerts[0].ID, id1)
	}
}

func TestListAlerts_Filtering(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Create multiple alerts
	alerts := []Alert{
		{
			AlertType:      AlertTypeCertExpiry,
			Severity:       SeverityWarning,
			TargetType:     TargetNode,
			TargetID:       1,
			DedupKey:       "cert_expiry:node:1:warning",
			ThresholdValue: "30 days",
			CurrentValue:   "25 days",
			Metadata:       map[string]interface{}{},
		},
		{
			AlertType:      AlertTypeCertExpiry,
			Severity:       SeverityCritical,
			TargetType:     TargetNode,
			TargetID:       2,
			DedupKey:       "cert_expiry:node:2:critical",
			ThresholdValue: "7 days",
			CurrentValue:   "5 days",
			Metadata:       map[string]interface{}{},
		},
		{
			AlertType:      AlertTypeQuotaWarning,
			Severity:       SeverityWarning,
			TargetType:     TargetSubject,
			TargetID:       10,
			DedupKey:       "quota:subject:10:warning",
			ThresholdValue: "80%",
			CurrentValue:   "85%",
			Metadata:       map[string]interface{}{},
		},
		{
			AlertType:      AlertTypeQuotaWarning,
			Severity:       SeverityCritical,
			TargetType:     TargetSubject,
			TargetID:       11,
			DedupKey:       "quota:subject:11:critical",
			ThresholdValue: "95%",
			CurrentValue:   "96%",
			Metadata:       map[string]interface{}{},
		},
	}

	for _, a := range alerts {
		if _, _, err := CreateOrUpdateAlert(ctx, s, a, now); err != nil {
			t.Fatalf("CreateOrUpdateAlert: %v", err)
		}
	}

	// Resolve one cert-expiry alert
	if err := ResolveAlert(ctx, s, "cert_expiry:node:2:critical", now); err != nil {
		t.Fatalf("ResolveAlert: %v", err)
	}

	tests := []struct {
		name         string
		filters      AlertFilters
		wantCount    int
		wantAlertIDs []string // dedup_keys for verification
	}{
		{
			name:      "all active",
			filters:   AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}, State: StateActive, Limit: 10},
			wantCount: 3,
		},
		{
			name:      "all resolved",
			filters:   AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}, State: StateResolved, Limit: 10},
			wantCount: 1,
		},
		{
			name:      "cert expiry only",
			filters:   AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}, AlertType: AlertTypeCertExpiry, Limit: 10},
			wantCount: 2,
		},
		{
			name:      "quota warning only",
			filters:   AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}, AlertType: AlertTypeQuotaWarning, Limit: 10},
			wantCount: 2,
		},
		{
			name:      "critical severity",
			filters:   AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}, Severity: SeverityCritical, Limit: 10},
			wantCount: 2,
		},
		{
			name:      "warning severity",
			filters:   AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}, Severity: SeverityWarning, Limit: 10},
			wantCount: 2,
		},
		{
			name:      "node targets",
			filters:   AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}, TargetType: TargetNode, Limit: 10},
			wantCount: 2,
		},
		{
			name:      "subject targets",
			filters:   AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}, TargetType: TargetSubject, Limit: 10},
			wantCount: 2,
		},
		{
			name:      "specific target",
			filters:   AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}, TargetType: TargetSubject, TargetID: intPtr(10), Limit: 10},
			wantCount: 1,
		},
		{
			name:      "active cert expiry",
			filters:   AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}, State: StateActive, AlertType: AlertTypeCertExpiry, Limit: 10},
			wantCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, total, err := ListAlerts(ctx, s, tt.filters)
			if err != nil {
				t.Fatalf("ListAlerts: %v", err)
			}
			if total != tt.wantCount {
				t.Errorf("total = %d, want %d", total, tt.wantCount)
			}
			if len(results) != tt.wantCount {
				t.Errorf("len(results) = %d, want %d", len(results), tt.wantCount)
			}
		})
	}
}

func TestListAlerts_Pagination(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Create 25 alerts
	for i := 1; i <= 25; i++ {
		alert := Alert{
			AlertType:      AlertTypeCertExpiry,
			Severity:       SeverityWarning,
			TargetType:     TargetNode,
			TargetID:       int64(i),
			DedupKey:       fmt.Sprintf("cert_expiry:node:%d:warning", i),
			ThresholdValue: "30 days",
			CurrentValue:   "25 days",
			Metadata:       map[string]interface{}{},
		}
		if _, _, err := CreateOrUpdateAlert(ctx, s, alert, now); err != nil {
			t.Fatalf("CreateOrUpdateAlert: %v", err)
		}
	}

	// Query first page (10 results)
	page1, total1, err := ListAlerts(ctx, s, AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}, Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("ListAlerts (page 1): %v", err)
	}
	if total1 != 25 {
		t.Errorf("total = %d, want 25", total1)
	}
	if len(page1) != 10 {
		t.Errorf("len(page1) = %d, want 10", len(page1))
	}

	// Query second page
	page2, total2, err := ListAlerts(ctx, s, AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}, Limit: 10, Offset: 10})
	if err != nil {
		t.Fatalf("ListAlerts (page 2): %v", err)
	}
	if total2 != 25 {
		t.Errorf("total = %d, want 25", total2)
	}
	if len(page2) != 10 {
		t.Errorf("len(page2) = %d, want 10", len(page2))
	}

	// Query third page
	page3, total3, err := ListAlerts(ctx, s, AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}, Limit: 10, Offset: 20})
	if err != nil {
		t.Fatalf("ListAlerts (page 3): %v", err)
	}
	if total3 != 25 {
		t.Errorf("total = %d, want 25", total3)
	}
	if len(page3) != 5 {
		t.Errorf("len(page3) = %d, want 5", len(page3))
	}

	// Verify no overlap between pages
	page1IDs := make(map[int64]bool)
	for _, a := range page1 {
		page1IDs[a.ID] = true
	}
	for _, a := range page2 {
		if page1IDs[a.ID] {
			t.Errorf("alert %d appears in both page 1 and page 2", a.ID)
		}
	}
}

func TestListAlerts_DefaultLimit(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Create 60 alerts
	for i := 1; i <= 60; i++ {
		alert := Alert{
			AlertType:      AlertTypeCertExpiry,
			Severity:       SeverityWarning,
			TargetType:     TargetNode,
			TargetID:       int64(i),
			DedupKey:       fmt.Sprintf("cert_expiry:node:%d:warning", i),
			ThresholdValue: "30 days",
			CurrentValue:   "25 days",
			Metadata:       map[string]interface{}{},
		}
		if _, _, err := CreateOrUpdateAlert(ctx, s, alert, now); err != nil {
			t.Fatalf("CreateOrUpdateAlert: %v", err)
		}
	}

	// Query with no limit (should default to 50)
	results, total, err := ListAlerts(ctx, s, AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}})
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if total != 60 {
		t.Errorf("total = %d, want 60", total)
	}
	if len(results) != 50 {
		t.Errorf("len(results) = %d, want 50 (default limit)", len(results))
	}

	// Query with limit > 200 (should cap at 200)
	results2, total2, err := ListAlerts(ctx, s, AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}, Limit: 500})
	if err != nil {
		t.Fatalf("ListAlerts (limit 500): %v", err)
	}
	if total2 != 60 {
		t.Errorf("total = %d, want 60", total2)
	}
	if len(results2) != 60 {
		t.Errorf("len(results2) = %d, want 60 (capped at actual count)", len(results2))
	}
}

func intPtr(i int64) *int64 {
	return &i
}

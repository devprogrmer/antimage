package observability

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/rbac"
)

// superScope returns a super admin scope for testing
func superScope() rbac.Scope {
	return rbac.Scope{AdminID: 1, IsSuper: true}
}

func TestSweeperCheckCertificates_Warning(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	sw := NewSweeper(s)

	// Create node enrolled 335 days ago (30 days until expiry = warning threshold)
	now := time.Now().UTC()
	enrolledAt := now.Add(-335 * 24 * time.Hour)

	var nodeID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO nodes (name, address, enrolled_at, created_at) VALUES (?, ?, ?, ?)`,
			"test-node-cert-warning", "10.0.0.1", enrolledAt.Unix(), now.Unix())
		if err != nil {
			return err
		}
		nodeID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}

	// Run sweeper
	if err := sw.checkCertificates(ctx, now); err != nil {
		t.Fatalf("checkCertificates: %v", err)
	}

	// Verify warning alert created
	alerts, total, err := ListAlerts(ctx, s, AlertFilters{
		Scope:      superScope(),
		State:      StateActive,
		AlertType:  AlertTypeCertExpiry,
		Severity:   SeverityWarning,
		TargetType: TargetNode,
		TargetID:   &nodeID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if total != 1 {
		t.Errorf("expected 1 warning alert, got %d", total)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert in list, got %d", len(alerts))
	}

	a := alerts[0]
	if a.ThresholdValue != "30 days" {
		t.Errorf("threshold_value = %s, want '30 days'", a.ThresholdValue)
	}
	if a.Metadata["node_name"] != "test-node-cert-warning" {
		t.Errorf("metadata[node_name] = %v, want 'test-node-cert-warning'", a.Metadata["node_name"])
	}
}

func TestSweeperCheckCertificates_Critical(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	sw := NewSweeper(s)

	// Create node enrolled 358 days ago (7 days until expiry = critical threshold)
	now := time.Now().UTC()
	enrolledAt := now.Add(-358 * 24 * time.Hour)

	var nodeID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO nodes (name, address, enrolled_at, created_at) VALUES (?, ?, ?, ?)`,
			"test-node-cert-critical", "10.0.0.2", enrolledAt.Unix(), now.Unix())
		if err != nil {
			return err
		}
		nodeID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}

	// Run sweeper
	if err := sw.checkCertificates(ctx, now); err != nil {
		t.Fatalf("checkCertificates: %v", err)
	}

	// Verify critical alert created
	alerts, total, err := ListAlerts(ctx, s, AlertFilters{
		Scope:      superScope(),
		State:      StateActive,
		AlertType:  AlertTypeCertExpiry,
		Severity:   SeverityCritical,
		TargetType: TargetNode,
		TargetID:   &nodeID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if total != 1 {
		t.Errorf("expected 1 critical alert, got %d", total)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert in list, got %d", len(alerts))
	}

	a := alerts[0]
	if a.ThresholdValue != "7 days" {
		t.Errorf("threshold_value = %s, want '7 days'", a.ThresholdValue)
	}
}

func TestSweeperCheckCertificates_Expired(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	sw := NewSweeper(s)

	// Create node enrolled 366 days ago (cert expired 1 day ago)
	now := time.Now().UTC()
	enrolledAt := now.Add(-366 * 24 * time.Hour)

	var nodeID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO nodes (name, address, enrolled_at, created_at) VALUES (?, ?, ?, ?)`,
			"test-node-cert-expired", "10.0.0.3", enrolledAt.Unix(), now.Unix())
		if err != nil {
			return err
		}
		nodeID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}

	// Run sweeper
	if err := sw.checkCertificates(ctx, now); err != nil {
		t.Fatalf("checkCertificates: %v", err)
	}

	// Verify critical alert created (expired certs are critical)
	alerts, total, err := ListAlerts(ctx, s, AlertFilters{
		Scope:      superScope(),
		State:      StateActive,
		AlertType:  AlertTypeCertExpiry,
		TargetType: TargetNode,
		TargetID:   &nodeID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if total != 1 {
		t.Errorf("expected 1 alert for expired cert, got %d", total)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert in list, got %d", len(alerts))
	}

	a := alerts[0]
	if a.Severity != SeverityCritical {
		t.Errorf("severity = %s, want critical", a.Severity)
	}
	if a.CurrentValue != "0 days" {
		t.Errorf("current_value = %s, want '0 days'", a.CurrentValue)
	}
}

func TestSweeperCheckCertificates_Renewed(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	sw := NewSweeper(s)

	now := time.Now().UTC()

	// Create node with expiring cert
	enrolledAt := now.Add(-358 * 24 * time.Hour)
	var nodeID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO nodes (name, address, enrolled_at, created_at) VALUES (?, ?, ?, ?)`,
			"test-node-cert-renewed", "10.0.0.4", enrolledAt.Unix(), now.Unix())
		if err != nil {
			return err
		}
		nodeID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}

	// Run sweeper - should create alert
	if err := sw.checkCertificates(ctx, now); err != nil {
		t.Fatalf("checkCertificates (first): %v", err)
	}

	// Verify alert exists
	alerts1, total1, err := ListAlerts(ctx, s, AlertFilters{
		Scope:      superScope(),
		State:      StateActive,
		AlertType:  AlertTypeCertExpiry,
		TargetType: TargetNode,
		TargetID:   &nodeID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("ListAlerts (before renewal): %v", err)
	}
	if total1 != 1 {
		t.Errorf("expected 1 alert before renewal, got %d", total1)
	}

	// Simulate certificate renewal (update enrolled_at to now)
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE nodes SET enrolled_at = ? WHERE id = ?`, now.Unix(), nodeID)
		return err
	})
	if err != nil {
		t.Fatalf("update enrolled_at: %v", err)
	}

	// Run sweeper again - should resolve alert
	if err := sw.checkCertificates(ctx, now); err != nil {
		t.Fatalf("checkCertificates (after renewal): %v", err)
	}

	// Verify alert resolved
	_, total2, err := ListAlerts(ctx, s, AlertFilters{
		Scope:      superScope(),
		State:      StateActive,
		AlertType:  AlertTypeCertExpiry,
		TargetType: TargetNode,
		TargetID:   &nodeID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("ListAlerts (after renewal): %v", err)
	}
	if total2 != 0 {
		t.Errorf("expected 0 active alerts after renewal, got %d", total2)
	}

	// Verify alert was resolved, not deleted
	resolvedAlerts, totalResolved, err := ListAlerts(ctx, s, AlertFilters{
		Scope:      superScope(),
		State:      StateResolved,
		AlertType:  AlertTypeCertExpiry,
		TargetType: TargetNode,
		TargetID:   &nodeID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("ListAlerts (resolved): %v", err)
	}
	if totalResolved != 1 {
		t.Errorf("expected 1 resolved alert, got %d", totalResolved)
	}
	if len(resolvedAlerts) != 1 {
		t.Fatalf("expected 1 resolved alert in list, got %d", len(resolvedAlerts))
	}
	if resolvedAlerts[0].ID != alerts1[0].ID {
		t.Errorf("resolved alert ID = %d, want %d (same as original)", resolvedAlerts[0].ID, alerts1[0].ID)
	}
}

func TestSweeperCheckCertificates_RepeatedSweeps(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	sw := NewSweeper(s)

	now := time.Now().UTC()
	enrolledAt := now.Add(-335 * 24 * time.Hour)

	var nodeID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO nodes (name, address, enrolled_at, created_at) VALUES (?, ?, ?, ?)`,
			"test-node-repeated", "10.0.0.5", enrolledAt.Unix(), now.Unix())
		if err != nil {
			return err
		}
		nodeID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}

	// Run sweeper multiple times
	for i := 0; i < 5; i++ {
		if err := sw.checkCertificates(ctx, now.Add(time.Duration(i)*5*time.Minute)); err != nil {
			t.Fatalf("checkCertificates (iteration %d): %v", i, err)
		}
	}

	// Verify only one alert exists (deduplication works)
	alerts, total, err := ListAlerts(ctx, s, AlertFilters{
		Scope:      superScope(),
		State:      StateActive,
		AlertType:  AlertTypeCertExpiry,
		TargetType: TargetNode,
		TargetID:   &nodeID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if total != 1 {
		t.Errorf("expected 1 alert after repeated sweeps, got %d", total)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert in list, got %d", len(alerts))
	}

	// Verify last_seen_at was updated
	a := alerts[0]
	if !a.LastSeenAt.After(a.FirstSeenAt) {
		t.Error("last_seen_at should be after first_seen_at after repeated sweeps")
	}
}

func TestSweeperCheckQuotas_Warning(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	sw := NewSweeper(s)

	now := time.Now().UTC()

	// Create subject at 85% quota usage (above 80% warning threshold)
	var subjectID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO subjects (name, quota_bytes, quota_used_bytes, created_at) VALUES (?, ?, ?, ?)`,
			"user-quota-warning", 1000000, 850000, now.Unix())
		if err != nil {
			return err
		}
		subjectID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("create subject: %v", err)
	}

	// Run sweeper
	if err := sw.checkQuotas(ctx, now); err != nil {
		t.Fatalf("checkQuotas: %v", err)
	}

	// Verify warning alert created
	alerts, total, err := ListAlerts(ctx, s, AlertFilters{
		Scope:      superScope(),
		State:      StateActive,
		AlertType:  AlertTypeQuotaWarning,
		Severity:   SeverityWarning,
		TargetType: TargetSubject,
		TargetID:   &subjectID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if total != 1 {
		t.Errorf("expected 1 warning alert, got %d", total)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert in list, got %d", len(alerts))
	}

	a := alerts[0]
	if a.ThresholdValue != "80%" {
		t.Errorf("threshold_value = %s, want '80%%'", a.ThresholdValue)
	}
	if a.Metadata["subject_name"] != "user-quota-warning" {
		t.Errorf("metadata[subject_name] = %v", a.Metadata["subject_name"])
	}
}

func TestSweeperCheckQuotas_Critical(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	sw := NewSweeper(s)

	now := time.Now().UTC()

	// Create subject at 96% quota usage (above 95% critical threshold)
	var subjectID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO subjects (name, quota_bytes, quota_used_bytes, created_at) VALUES (?, ?, ?, ?)`,
			"user-quota-critical", 1000000, 960000, now.Unix())
		if err != nil {
			return err
		}
		subjectID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("create subject: %v", err)
	}

	// Run sweeper
	if err := sw.checkQuotas(ctx, now); err != nil {
		t.Fatalf("checkQuotas: %v", err)
	}

	// Verify critical alert created
	alerts, total, err := ListAlerts(ctx, s, AlertFilters{
		Scope:      superScope(),
		State:      StateActive,
		AlertType:  AlertTypeQuotaWarning,
		Severity:   SeverityCritical,
		TargetType: TargetSubject,
		TargetID:   &subjectID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if total != 1 {
		t.Errorf("expected 1 critical alert, got %d", total)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert in list, got %d", len(alerts))
	}

	a := alerts[0]
	if a.ThresholdValue != "95%" {
		t.Errorf("threshold_value = %s, want '95%%'", a.ThresholdValue)
	}
}

func TestSweeperCheckQuotas_QuotaReset(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	sw := NewSweeper(s)

	now := time.Now().UTC()

	// Create subject at 96% quota usage
	var subjectID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO subjects (name, quota_bytes, quota_used_bytes, created_at) VALUES (?, ?, ?, ?)`,
			"user-quota-reset", 1000000, 960000, now.Unix())
		if err != nil {
			return err
		}
		subjectID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("create subject: %v", err)
	}

	// Run sweeper - should create alert
	if err := sw.checkQuotas(ctx, now); err != nil {
		t.Fatalf("checkQuotas (before reset): %v", err)
	}

	// Verify alert exists
	_, total1, err := ListAlerts(ctx, s, AlertFilters{
		Scope:      superScope(),
		State:      StateActive,
		AlertType:  AlertTypeQuotaWarning,
		TargetType: TargetSubject,
		TargetID:   &subjectID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("ListAlerts (before reset): %v", err)
	}
	if total1 != 1 {
		t.Errorf("expected 1 alert before reset, got %d", total1)
	}

	// Simulate quota reset (SP3 resets quota_used_bytes to 0)
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE subjects SET quota_used_bytes = 0 WHERE id = ?`, subjectID)
		return err
	})
	if err != nil {
		t.Fatalf("reset quota: %v", err)
	}

	// Run sweeper again - should resolve alert
	if err := sw.checkQuotas(ctx, now); err != nil {
		t.Fatalf("checkQuotas (after reset): %v", err)
	}

	// Verify alert resolved
	_, total2, err := ListAlerts(ctx, s, AlertFilters{
		Scope:      superScope(),
		State:      StateActive,
		AlertType:  AlertTypeQuotaWarning,
		TargetType: TargetSubject,
		TargetID:   &subjectID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("ListAlerts (after reset): %v", err)
	}
	if total2 != 0 {
		t.Errorf("expected 0 active alerts after reset, got %d", total2)
	}
}

func TestSweeperCheckQuotas_EscalationAndDeescalation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	sw := NewSweeper(s)

	now := time.Now().UTC()

	// Create subject at 85% quota usage (warning level)
	var subjectID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO subjects (name, quota_bytes, quota_used_bytes, created_at) VALUES (?, ?, ?, ?)`,
			"user-escalation", 1000000, 850000, now.Unix())
		if err != nil {
			return err
		}
		subjectID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("create subject: %v", err)
	}

	// Run sweeper - should create warning alert
	if err := sw.checkQuotas(ctx, now); err != nil {
		t.Fatalf("checkQuotas (warning): %v", err)
	}

	// Verify warning alert
	_, warningTotal, err := ListAlerts(ctx, s, AlertFilters{
		Scope:      superScope(),
		State:      StateActive,
		AlertType:  AlertTypeQuotaWarning,
		Severity:   SeverityWarning,
		TargetType: TargetSubject,
		TargetID:   &subjectID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("ListAlerts (warning): %v", err)
	}
	if warningTotal != 1 {
		t.Errorf("expected 1 warning alert, got %d", warningTotal)
	}

	// Escalate to 96% (critical)
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE subjects SET quota_used_bytes = 960000 WHERE id = ?`, subjectID)
		return err
	})
	if err != nil {
		t.Fatalf("escalate quota: %v", err)
	}

	// Run sweeper - should create critical alert
	if err := sw.checkQuotas(ctx, now); err != nil {
		t.Fatalf("checkQuotas (critical): %v", err)
	}

	// Verify critical alert exists
	_, criticalTotal, err := ListAlerts(ctx, s, AlertFilters{
		Scope:      superScope(),
		State:      StateActive,
		AlertType:  AlertTypeQuotaWarning,
		Severity:   SeverityCritical,
		TargetType: TargetSubject,
		TargetID:   &subjectID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("ListAlerts (critical): %v", err)
	}
	if criticalTotal != 1 {
		t.Errorf("expected 1 critical alert, got %d", criticalTotal)
	}

	// Warning alert should still exist (separate dedup_key)
	_, warningTotal2, err := ListAlerts(ctx, s, AlertFilters{
		Scope:      superScope(),
		State:      StateActive,
		AlertType:  AlertTypeQuotaWarning,
		Severity:   SeverityWarning,
		TargetType: TargetSubject,
		TargetID:   &subjectID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("ListAlerts (warning after escalation): %v", err)
	}
	if warningTotal2 != 1 {
		t.Errorf("expected warning alert to persist, got %d", warningTotal2)
	}

	// De-escalate to 85% (below critical but still warning)
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE subjects SET quota_used_bytes = 850000 WHERE id = ?`, subjectID)
		return err
	})
	if err != nil {
		t.Fatalf("de-escalate quota: %v", err)
	}

	// Run sweeper - should resolve critical alert
	if err := sw.checkQuotas(ctx, now); err != nil {
		t.Fatalf("checkQuotas (de-escalate): %v", err)
	}

	// Verify critical alert resolved
	_, criticalTotal2, err := ListAlerts(ctx, s, AlertFilters{
		Scope:      superScope(),
		State:      StateActive,
		AlertType:  AlertTypeQuotaWarning,
		Severity:   SeverityCritical,
		TargetType: TargetSubject,
		TargetID:   &subjectID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("ListAlerts (critical after de-escalation): %v", err)
	}
	if criticalTotal2 != 0 {
		t.Errorf("expected critical alert resolved, got %d active", criticalTotal2)
	}

	// Warning alert should still exist
	_, warningTotal3, err := ListAlerts(ctx, s, AlertFilters{
		Scope:      superScope(),
		State:      StateActive,
		AlertType:  AlertTypeQuotaWarning,
		Severity:   SeverityWarning,
		TargetType: TargetSubject,
		TargetID:   &subjectID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("ListAlerts (warning after de-escalation): %v", err)
	}
	if warningTotal3 != 1 {
		t.Errorf("expected warning alert to persist after de-escalation, got %d", warningTotal3)
	}
}

func TestSweeperCheckQuotas_SkipsFrozenSubjects(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	sw := NewSweeper(s)

	now := time.Now().UTC()

	// Create frozen subject at 96% quota usage
	var subjectID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO subjects (name, quota_bytes, quota_used_bytes, frozen_at, created_at) VALUES (?, ?, ?, ?, ?)`,
			"user-frozen", 1000000, 960000, now.Unix(), now.Unix())
		if err != nil {
			return err
		}
		subjectID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("create frozen subject: %v", err)
	}

	// Run sweeper
	if err := sw.checkQuotas(ctx, now); err != nil {
		t.Fatalf("checkQuotas: %v", err)
	}

	// Verify no alerts created (frozen subjects skipped)
	alerts, total, err := ListAlerts(ctx, s, AlertFilters{
		Scope:      superScope(),
		State:      StateActive,
		AlertType:  AlertTypeQuotaWarning,
		TargetType: TargetSubject,
		TargetID:   &subjectID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if total != 0 {
		t.Errorf("expected 0 alerts for frozen subject, got %d", total)
	}
	if len(alerts) != 0 {
		t.Errorf("expected empty alerts list, got %d", len(alerts))
	}
}

func TestSweeperRun_ContextCancellation(t *testing.T) {
	s := openTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	sw := NewSweeper(s)

	// Run sweeper in background
	done := make(chan bool)
	go func() {
		sw.Run(ctx)
		done <- true
	}()

	// Cancel context after short delay
	time.Sleep(100 * time.Millisecond)
	cancel()

	// Wait for sweeper to exit
	select {
	case <-done:
		// Success - sweeper exited
	case <-time.After(2 * time.Second):
		t.Fatal("sweeper did not exit after context cancellation")
	}
}

func TestSweeperPanicRecovery(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Create sweeper with closed store to trigger panic
	_ = s.Close()

	sw := NewSweeper(s)

	// This should not crash despite closed store
	// (panic recovery in sweep() function)
	sw.sweep(ctx)

	// If we reach here, panic was recovered successfully
}

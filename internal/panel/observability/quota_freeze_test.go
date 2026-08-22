package observability

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

func TestQuotaAutoFreeze(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()

	// Create subjects with different quota states
	var overQuotaID, nearQuotaID, underQuotaID, alreadyFrozenID int64

	err = s.Write(ctx, func(tx *sql.Tx) error {
		// Subject 1: Over quota, should be frozen
		res, err := tx.Exec(`
			INSERT INTO subjects (name, enabled, created_at, quota_bytes, quota_used_bytes)
			VALUES (?, 1, ?, ?, ?)`,
			"over_quota", now.Unix(), 1000000, 1500000)
		if err != nil {
			return err
		}
		overQuotaID, _ = res.LastInsertId()

		// Subject 2: Near quota (90%), should not be frozen
		res, err = tx.Exec(`
			INSERT INTO subjects (name, enabled, created_at, quota_bytes, quota_used_bytes)
			VALUES (?, 1, ?, ?, ?)`,
			"near_quota", now.Unix(), 1000000, 900000)
		if err != nil {
			return err
		}
		nearQuotaID, _ = res.LastInsertId()

		// Subject 3: Under quota, should not be frozen
		res, err = tx.Exec(`
			INSERT INTO subjects (name, enabled, created_at, quota_bytes, quota_used_bytes)
			VALUES (?, 1, ?, ?, ?)`,
			"under_quota", now.Unix(), 1000000, 500000)
		if err != nil {
			return err
		}
		underQuotaID, _ = res.LastInsertId()

		// Subject 4: Over quota but already frozen
		res, err = tx.Exec(`
			INSERT INTO subjects (name, enabled, created_at, quota_bytes, quota_used_bytes, frozen_at, frozen_reason)
			VALUES (?, 1, ?, ?, ?, ?, ?)`,
			"already_frozen", now.Unix(), 1000000, 2000000, now.Unix(), "already frozen")
		if err != nil {
			return err
		}
		alreadyFrozenID, _ = res.LastInsertId()

		return nil
	})
	if err != nil {
		t.Fatalf("seed subjects: %v", err)
	}

	// Run quota enforcement
	sweeper := NewSweeper(s)
	if err := sweeper.enforceQuotaFreeze(ctx, now); err != nil {
		t.Fatalf("enforceQuotaFreeze: %v", err)
	}

	// Verify over_quota subject was frozen
	t.Run("over quota subject frozen", func(t *testing.T) {
		var frozenAt sql.NullInt64
		var frozenReason sql.NullString
		err := s.Read().QueryRow(`
			SELECT frozen_at, frozen_reason FROM subjects WHERE id = ?`,
			overQuotaID).Scan(&frozenAt, &frozenReason)
		if err != nil {
			t.Fatalf("query subject: %v", err)
		}

		if !frozenAt.Valid {
			t.Error("expected subject to be frozen")
		}
		if frozenReason.String != "quota exceeded: 1500000/1000000 bytes used" {
			t.Errorf("unexpected frozen_reason: %s", frozenReason.String)
		}
	})

	// Verify alert was created
	t.Run("quota exceeded alert created", func(t *testing.T) {
		var alertType, severity string
		err := s.Read().QueryRow(`
			SELECT alert_type, severity FROM alerts
			WHERE target_type = 'subject' AND target_id = ? AND state = 'active'`,
			overQuotaID).Scan(&alertType, &severity)
		if err != nil {
			t.Fatalf("query alert: %v", err)
		}

		if alertType != "quota_exceeded" {
			t.Errorf("expected alert_type quota_exceeded, got %s", alertType)
		}
		if severity != "critical" {
			t.Errorf("expected severity critical, got %s", severity)
		}
	})

	// Verify near_quota subject was NOT frozen
	t.Run("near quota subject not frozen", func(t *testing.T) {
		var frozenAt sql.NullInt64
		err := s.Read().QueryRow(`
			SELECT frozen_at FROM subjects WHERE id = ?`,
			nearQuotaID).Scan(&frozenAt)
		if err != nil {
			t.Fatalf("query subject: %v", err)
		}

		if frozenAt.Valid {
			t.Error("expected subject to NOT be frozen")
		}
	})

	// Verify under_quota subject was NOT frozen
	t.Run("under quota subject not frozen", func(t *testing.T) {
		var frozenAt sql.NullInt64
		err := s.Read().QueryRow(`
			SELECT frozen_at FROM subjects WHERE id = ?`,
			underQuotaID).Scan(&frozenAt)
		if err != nil {
			t.Fatalf("query subject: %v", err)
		}

		if frozenAt.Valid {
			t.Error("expected subject to NOT be frozen")
		}
	})

	// Verify already_frozen subject's frozen_at timestamp wasn't changed
	t.Run("already frozen subject unchanged", func(t *testing.T) {
		var frozenAt sql.NullInt64
		var frozenReason sql.NullString
		err := s.Read().QueryRow(`
			SELECT frozen_at, frozen_reason FROM subjects WHERE id = ?`,
			alreadyFrozenID).Scan(&frozenAt, &frozenReason)
		if err != nil {
			t.Fatalf("query subject: %v", err)
		}

		if frozenAt.Int64 != now.Unix() {
			t.Error("frozen_at timestamp should not have changed")
		}
		if frozenReason.String != "already frozen" {
			t.Error("frozen_reason should not have changed")
		}
	})
}

func TestQuotaAutoFreezeIdempotent(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()

	// Create subject over quota
	var subjectID int64
	err = s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			INSERT INTO subjects (name, enabled, created_at, quota_bytes, quota_used_bytes)
			VALUES (?, 1, ?, ?, ?)`,
			"test_user", now.Unix(), 1000000, 1500000)
		if err != nil {
			return err
		}
		subjectID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed subject: %v", err)
	}

	sweeper := NewSweeper(s)

	// Run enforcement first time
	if err := sweeper.enforceQuotaFreeze(ctx, now); err != nil {
		t.Fatalf("first enforceQuotaFreeze: %v", err)
	}

	// Get frozen_at timestamp
	var firstFrozenAt sql.NullInt64
	err = s.Read().QueryRow(`SELECT frozen_at FROM subjects WHERE id = ?`,
		subjectID).Scan(&firstFrozenAt)
	if err != nil {
		t.Fatalf("query frozen_at: %v", err)
	}

	// Run enforcement second time
	if err := sweeper.enforceQuotaFreeze(ctx, now.Add(time.Minute)); err != nil {
		t.Fatalf("second enforceQuotaFreeze: %v", err)
	}

	// Verify frozen_at timestamp wasn't changed
	var secondFrozenAt sql.NullInt64
	err = s.Read().QueryRow(`SELECT frozen_at FROM subjects WHERE id = ?`,
		subjectID).Scan(&secondFrozenAt)
	if err != nil {
		t.Fatalf("query frozen_at: %v", err)
	}

	if firstFrozenAt.Int64 != secondFrozenAt.Int64 {
		t.Errorf("frozen_at changed on second run: %d -> %d",
			firstFrozenAt.Int64, secondFrozenAt.Int64)
	}
}

func TestQuotaAutoFreezeDisabledSubjects(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()

	// Create disabled subject over quota
	var subjectID int64
	err = s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			INSERT INTO subjects (name, enabled, created_at, quota_bytes, quota_used_bytes)
			VALUES (?, 0, ?, ?, ?)`,
			"disabled_user", now.Unix(), 1000000, 1500000)
		if err != nil {
			return err
		}
		subjectID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed subject: %v", err)
	}

	sweeper := NewSweeper(s)

	// Run enforcement
	if err := sweeper.enforceQuotaFreeze(ctx, now); err != nil {
		t.Fatalf("enforceQuotaFreeze: %v", err)
	}

	// Verify disabled subject was NOT frozen (already disabled)
	var frozenAt sql.NullInt64
	err = s.Read().QueryRow(`SELECT frozen_at FROM subjects WHERE id = ?`,
		subjectID).Scan(&frozenAt)
	if err != nil {
		t.Fatalf("query frozen_at: %v", err)
	}

	if frozenAt.Valid {
		t.Error("disabled subject should not be frozen (already disabled)")
	}
}

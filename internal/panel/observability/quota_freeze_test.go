package observability

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/testutil/storetest"
)

// These covered enforceQuotaFreeze, which froze subjects from this package.
// That function is gone: it stamped frozen_at without setting enabled = 0 or
// committing a node change, so the subject stayed in service, and the stamp
// then excluded it from findSubjectsOverQuota permanently. Freezing belongs to
// nodes.QuotaEnforcementSweeper alone.
//
// The alerting half survived as alertQuotaExceeded and is what is covered here.
// The freezing half is covered by nodes.QuotaEnforcementSweeper's own tests and
// by TestTheQuotaEnforcerStillCutsServiceAfterASweep.

func quotaAlertFor(t *testing.T, s *sql.DB, subjectID int64) (alertType, severity string, found bool) {
	t.Helper()
	err := s.QueryRow(`
		SELECT alert_type, severity FROM alerts
		 WHERE target_type = 'subject' AND target_id = ? AND alert_type = 'quota_exceeded'`,
		subjectID).Scan(&alertType, &severity)
	if err == sql.ErrNoRows {
		return "", "", false
	}
	if err != nil {
		t.Fatalf("query alert: %v", err)
	}
	return alertType, severity, true
}

func TestQuotaExceededAlerting(t *testing.T) {
	s, err := storetest.OpenCopy(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()

	var overQuotaID, nearQuotaID, underQuotaID, alreadyFrozenID int64

	err = s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			INSERT INTO subjects (name, enabled, created_at, quota_bytes, quota_used_bytes)
			VALUES (?, 1, ?, ?, ?)`,
			"over_quota", now.Unix(), 1000000, 1500000)
		if err != nil {
			return err
		}
		overQuotaID, _ = res.LastInsertId()

		res, err = tx.Exec(`
			INSERT INTO subjects (name, enabled, created_at, quota_bytes, quota_used_bytes)
			VALUES (?, 1, ?, ?, ?)`,
			"near_quota", now.Unix(), 1000000, 900000)
		if err != nil {
			return err
		}
		nearQuotaID, _ = res.LastInsertId()

		res, err = tx.Exec(`
			INSERT INTO subjects (name, enabled, created_at, quota_bytes, quota_used_bytes)
			VALUES (?, 1, ?, ?, ?)`,
			"under_quota", now.Unix(), 1000000, 500000)
		if err != nil {
			return err
		}
		underQuotaID, _ = res.LastInsertId()

		// Already frozen AND over quota: the enforcer has already acted.
		res, err = tx.Exec(`
			INSERT INTO subjects (name, enabled, created_at, quota_bytes, quota_used_bytes, frozen_at, frozen_reason)
			VALUES (?, 0, ?, ?, ?, ?, ?)`,
			"already_frozen", now.Unix(), 1000000, 2000000, now.Unix(), "quota_exceeded")
		if err != nil {
			return err
		}
		alreadyFrozenID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed subjects: %v", err)
	}

	sweeper := NewSweeper(s)
	if err := sweeper.alertQuotaExceeded(ctx, now); err != nil {
		t.Fatalf("alertQuotaExceeded: %v", err)
	}

	t.Run("over quota subject is alerted", func(t *testing.T) {
		typ, sev, found := quotaAlertFor(t, s.Read(), overQuotaID)
		if !found {
			t.Fatal("expected a quota_exceeded alert")
		}
		if typ != "quota_exceeded" {
			t.Errorf("alert_type = %s, want quota_exceeded", typ)
		}
		if sev != "critical" {
			t.Errorf("severity = %s, want critical", sev)
		}
	})

	t.Run("near quota subject is not alerted", func(t *testing.T) {
		if _, _, found := quotaAlertFor(t, s.Read(), nearQuotaID); found {
			t.Error("90% of quota is not over quota")
		}
	})

	t.Run("under quota subject is not alerted", func(t *testing.T) {
		if _, _, found := quotaAlertFor(t, s.Read(), underQuotaID); found {
			t.Error("50% of quota is not over quota")
		}
	})

	// The condition is still true after the enforcer acts, and the operator
	// needs to see why service stopped.
	t.Run("an already frozen subject keeps its alert", func(t *testing.T) {
		if _, _, found := quotaAlertFor(t, s.Read(), alreadyFrozenID); !found {
			t.Error("a subject cut off for quota must still show why")
		}
	})

	// And the alerting pass must not have touched any subject.
	t.Run("alerting mutates no subject", func(t *testing.T) {
		for _, id := range []int64{overQuotaID, nearQuotaID, underQuotaID} {
			var enabled int
			var frozen sql.NullInt64
			if err := s.Read().QueryRow(
				`SELECT enabled, frozen_at FROM subjects WHERE id = ?`, id).
				Scan(&enabled, &frozen); err != nil {
				t.Fatalf("read subject %d: %v", id, err)
			}
			if enabled != 1 || frozen.Valid {
				t.Errorf("subject %d was mutated by the alerting pass "+
					"(enabled=%d frozen=%v); freezing belongs to "+
					"nodes.QuotaEnforcementSweeper", id, enabled, frozen.Valid)
			}
		}
	})
}

func TestQuotaExceededAlertingIsIdempotent(t *testing.T) {
	s, err := storetest.OpenCopy(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()

	var id int64
	err = s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			INSERT INTO subjects (name, enabled, created_at, quota_bytes, quota_used_bytes)
			VALUES (?, 1, ?, ?, ?)`, "over", now.Unix(), 1000, 5000)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed subject: %v", err)
	}

	sweeper := NewSweeper(s)
	for i := range 3 {
		if err := sweeper.alertQuotaExceeded(ctx, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("alertQuotaExceeded run %d: %v", i, err)
		}
	}

	var count int
	if err := s.Read().QueryRow(
		`SELECT COUNT(*) FROM alerts
		  WHERE target_type = 'subject' AND target_id = ? AND alert_type = 'quota_exceeded'`,
		id).Scan(&count); err != nil {
		t.Fatalf("count alerts: %v", err)
	}
	if count != 1 {
		t.Errorf("three sweeps produced %d alerts, want 1: the dedup key must "+
			"collapse a persistent condition into one row", count)
	}
}

package devices

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

func testDB(t *testing.T) (*store.Store, func()) {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Create store
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	// Run migrations
	if err := st.Migrate(); err != nil {
		st.Close()
		t.Fatalf("migrate: %v", err)
	}

	cleanup := func() {
		st.Close()
	}

	return st, cleanup
}

func setupTestSubject(t *testing.T, st *store.Store, maxDevices, maxIPs, maxConns *int64) int64 {
	t.Helper()

	ctx := context.Background()
	tx, err := st.Write().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	// Insert test subject
	res, err := tx.ExecContext(ctx,
		`INSERT INTO subjects (name, enabled, max_devices, max_ips, max_connections, created_at)
		 VALUES (?, 1, ?, ?, ?, ?)`,
		"test-subject", maxDevices, maxIPs, maxConns, time.Now().Unix())
	if err != nil {
		t.Fatalf("insert subject: %v", err)
	}

	subjectID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("get subject id: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	return subjectID
}

func TestRegisterDevice(t *testing.T) {
	st, cleanup := testDB(t)
	defer cleanup()

	deviceStore := NewStore(st, nil)
	ctx := context.Background()

	t.Run("register new device", func(t *testing.T) {
		maxDevices := int64(3)
		subjectID := setupTestSubject(t, st, &maxDevices, nil, nil)

		tx, _ := st.Write().BeginTx(ctx, nil)
		defer tx.Rollback()

		deviceID, err := deviceStore.RegisterDevice(ctx, tx, subjectID, "hwid-001", "Test Device", "192.168.1.100", "TestAgent/1.0")
		if err != nil {
			t.Fatalf("RegisterDevice failed: %v", err)
		}

		if deviceID == 0 {
			t.Error("expected non-zero device ID")
		}

		tx.Commit()

		// Verify device was stored
		devices, err := deviceStore.ListDevices(ctx, subjectID)
		if err != nil {
			t.Fatalf("ListDevices failed: %v", err)
		}

		if len(devices) != 1 {
			t.Fatalf("expected 1 device, got %d", len(devices))
		}

		if devices[0].HWID != "hwid-001" {
			t.Errorf("expected HWID hwid-001, got %s", devices[0].HWID)
		}
	})

	t.Run("device limit enforcement", func(t *testing.T) {
		maxDevices := int64(2)
		subjectID := setupTestSubject(t, st, &maxDevices, nil, nil)

		// Register 2 devices (at limit)
		for i := 1; i <= 2; i++ {
			tx, _ := st.Write().BeginTx(ctx, nil)
			_, err := deviceStore.RegisterDevice(ctx, tx, subjectID, fmt.Sprintf("hwid-%03d", i), "Device", "192.168.1.1", "Agent")
			tx.Commit()
			if err != nil {
				t.Fatalf("RegisterDevice %d failed: %v", i, err)
			}
		}

		// Try to register 3rd device - should fail
		tx, _ := st.Write().BeginTx(ctx, nil)
		defer tx.Rollback()

		_, err := deviceStore.RegisterDevice(ctx, tx, subjectID, "hwid-003", "Device 3", "192.168.1.1", "Agent")
		if err != ErrDeviceLimitReached {
			t.Errorf("expected ErrDeviceLimitReached, got %v", err)
		}
	})

	t.Run("revoked device rejection", func(t *testing.T) {
		subjectID := setupTestSubject(t, st, nil, nil, nil)

		// Register device
		tx, _ := st.Write().BeginTx(ctx, nil)
		deviceID, _ := deviceStore.RegisterDevice(ctx, tx, subjectID, "hwid-revoke", "Device", "192.168.1.1", "Agent")
		tx.Commit()

		// Revoke device
		tx, _ = st.Write().BeginTx(ctx, nil)
		err := deviceStore.RevokeDevice(ctx, tx, deviceID, "Testing revocation")
		tx.Commit()
		if err != nil {
			t.Fatalf("RevokeDevice failed: %v", err)
		}

		// Try to register same device again - should fail
		tx, _ = st.Write().BeginTx(ctx, nil)
		defer tx.Rollback()

		_, err = deviceStore.RegisterDevice(ctx, tx, subjectID, "hwid-revoke", "Device", "192.168.1.1", "Agent")
		if err != ErrDeviceRevoked {
			t.Errorf("expected ErrDeviceRevoked, got %v", err)
		}
	})

	t.Run("update existing device", func(t *testing.T) {
		subjectID := setupTestSubject(t, st, nil, nil, nil)

		// Register device
		tx, _ := st.Write().BeginTx(ctx, nil)
		deviceID1, _ := deviceStore.RegisterDevice(ctx, tx, subjectID, "hwid-update", "Device", "192.168.1.1", "Agent/1.0")
		tx.Commit()

		// Register same HWID again (simulates reconnection with new IP)
		tx, _ = st.Write().BeginTx(ctx, nil)
		deviceID2, err := deviceStore.RegisterDevice(ctx, tx, subjectID, "hwid-update", "Device", "192.168.1.2", "Agent/2.0")
		tx.Commit()

		if err != nil {
			t.Fatalf("RegisterDevice update failed: %v", err)
		}

		if deviceID1 != deviceID2 {
			t.Errorf("expected same device ID, got %d and %d", deviceID1, deviceID2)
		}

		// Verify IP was updated
		devices, _ := deviceStore.ListDevices(ctx, subjectID)
		if devices[0].LastIP != "192.168.1.2" {
			t.Errorf("expected IP 192.168.1.2, got %s", devices[0].LastIP)
		}
	})
}

func TestCheckIPLimit(t *testing.T) {
	st, cleanup := testDB(t)
	defer cleanup()

	deviceStore := NewStore(st, nil)
	ctx := context.Background()

	// Setup test node
	tx, _ := st.Write().BeginTx(ctx, nil)
	res, _ := tx.ExecContext(ctx,
		`INSERT INTO nodes (name, address, created_at) VALUES (?, ?, ?)`,
		"test-node", "10.0.0.1", time.Now().Unix())
	nodeID, _ := res.LastInsertId()
	tx.Commit()

	t.Run("unlimited IPs", func(t *testing.T) {
		subjectID := setupTestSubject(t, st, nil, nil, nil)

		err := deviceStore.CheckIPLimit(ctx, subjectID, "192.168.1.1")
		if err != nil {
			t.Errorf("expected no error for unlimited IPs, got %v", err)
		}
	})

	t.Run("enforce IP limit", func(t *testing.T) {
		maxIPs := int64(2)
		subjectID := setupTestSubject(t, st, nil, &maxIPs, nil)

		// Create 2 active connections from different IPs
		for i := 1; i <= 2; i++ {
			tx, _ := st.Write().BeginTx(ctx, nil)
			deviceStore.RecordConnection(ctx, tx, subjectID, nil, nodeID, fmt.Sprintf("conn-%d", i),
				fmt.Sprintf("192.168.1.%d", i), "test")
			tx.Commit()
		}

		// Try to connect from 3rd IP - should fail
		err := deviceStore.CheckIPLimit(ctx, subjectID, "192.168.1.3")
		if err != ErrIPLimitReached {
			t.Errorf("expected ErrIPLimitReached, got %v", err)
		}
	})

	t.Run("same IP reconnection allowed", func(t *testing.T) {
		maxIPs := int64(2)
		subjectID := setupTestSubject(t, st, nil, &maxIPs, nil)

		// Create active connection
		tx, _ := st.Write().BeginTx(ctx, nil)
		deviceStore.RecordConnection(ctx, tx, subjectID, nil, nodeID, "conn-1", "192.168.1.1", "test")
		tx.Commit()

		// Same IP should be allowed
		err := deviceStore.CheckIPLimit(ctx, subjectID, "192.168.1.1")
		if err != nil {
			t.Errorf("expected no error for same IP, got %v", err)
		}
	})
}

func TestCheckConnectionLimit(t *testing.T) {
	st, cleanup := testDB(t)
	defer cleanup()

	deviceStore := NewStore(st, nil)
	ctx := context.Background()

	// Setup test node
	tx, _ := st.Write().BeginTx(ctx, nil)
	res, _ := tx.ExecContext(ctx,
		`INSERT INTO nodes (name, address, created_at) VALUES (?, ?, ?)`,
		"test-node", "10.0.0.1", time.Now().Unix())
	nodeID, _ := res.LastInsertId()
	tx.Commit()

	t.Run("unlimited connections", func(t *testing.T) {
		subjectID := setupTestSubject(t, st, nil, nil, nil)

		err := deviceStore.CheckConnectionLimit(ctx, subjectID)
		if err != nil {
			t.Errorf("expected no error for unlimited connections, got %v", err)
		}
	})

	t.Run("enforce connection limit", func(t *testing.T) {
		maxConns := int64(3)
		subjectID := setupTestSubject(t, st, nil, nil, &maxConns)

		// Create 3 active connections (at limit)
		for i := 1; i <= 3; i++ {
			tx, _ := st.Write().BeginTx(ctx, nil)
			deviceStore.RecordConnection(ctx, tx, subjectID, nil, nodeID, fmt.Sprintf("conn-%d", i), "192.168.1.1", "test")
			tx.Commit()
		}

		// Try to open 4th connection - should fail
		err := deviceStore.CheckConnectionLimit(ctx, subjectID)
		if err != ErrConnectionLimit {
			t.Errorf("expected ErrConnectionLimit, got %v", err)
		}
	})
}

func TestCleanupStaleConnections(t *testing.T) {
	st, cleanup := testDB(t)
	defer cleanup()

	now := time.Now().UTC()
	deviceStore := NewStore(st, func() time.Time { return now })
	ctx := context.Background()

	// Setup test node and subject
	tx, _ := st.Write().BeginTx(ctx, nil)
	res, _ := tx.ExecContext(ctx,
		`INSERT INTO nodes (name, address, created_at) VALUES (?, ?, ?)`,
		"test-node", "10.0.0.1", now.Unix())
	nodeID, _ := res.LastInsertId()
	tx.Commit()

	subjectID := setupTestSubject(t, st, nil, nil, nil)

	// Create some connections
	for i := 1; i <= 5; i++ {
		lastSeen := now.Add(-time.Duration(i) * time.Minute).Unix()
		tx, _ := st.Write().BeginTx(ctx, nil)
		tx.ExecContext(ctx,
			`INSERT INTO active_connections (subject_id, node_id, connection_id, source_ip, connected_at, last_seen_at, protocol_info)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			subjectID, nodeID, fmt.Sprintf("conn-%d", i), "192.168.1.1", now.Unix(), lastSeen, "{}")
		tx.Commit()
	}

	// Cleanup connections older than 3 minutes
	tx, _ = st.Write().BeginTx(ctx, nil)
	count, err := deviceStore.CleanupStaleConnections(ctx, tx, 3*time.Minute)
	tx.Commit()

	if err != nil {
		t.Fatalf("CleanupStaleConnections failed: %v", err)
	}

	// Should remove 2 connections (4 and 5 minutes old)
	if count != 2 {
		t.Errorf("expected 2 cleaned connections, got %d", count)
	}
}

func TestGetSpeedLimits(t *testing.T) {
	st, cleanup := testDB(t)
	defer cleanup()

	deviceStore := NewStore(st, nil)
	ctx := context.Background()

	t.Run("no speed limits", func(t *testing.T) {
		subjectID := setupTestSubject(t, st, nil, nil, nil)

		up, down, err := deviceStore.GetSpeedLimits(ctx, subjectID)
		if err != nil {
			t.Fatalf("GetSpeedLimits failed: %v", err)
		}

		if up != nil || down != nil {
			t.Error("expected nil speed limits")
		}
	})

	t.Run("with speed limits", func(t *testing.T) {
		subjectID := setupTestSubject(t, st, nil, nil, nil)

		// Set speed limits
		tx, _ := st.Write().BeginTx(ctx, nil)
		tx.ExecContext(ctx,
			`UPDATE subjects SET speed_limit_up_kbps = 1000, speed_limit_down_kbps = 5000 WHERE id = ?`,
			subjectID)
		tx.Commit()

		up, down, err := deviceStore.GetSpeedLimits(ctx, subjectID)
		if err != nil {
			t.Fatalf("GetSpeedLimits failed: %v", err)
		}

		if up == nil || *up != 1000 {
			t.Errorf("expected up=1000, got %v", up)
		}
		if down == nil || *down != 5000 {
			t.Errorf("expected down=5000, got %v", down)
		}
	})
}

import "fmt"

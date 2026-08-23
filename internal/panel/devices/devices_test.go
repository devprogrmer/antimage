package devices

import (
	"context"
	"database/sql"
	"errors"
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

	// Create store - migrations run automatically in Open()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	cleanup := func() {
		st.Close()
	}

	return st, cleanup
}

func setupTestSubject(t *testing.T, st *store.Store, maxDevices, maxIPs, maxConns *int64) int64 {
	t.Helper()

	ctx := context.Background()
	var subjectID int64

	// Generate unique subject name to avoid conflicts
	subjectName := fmt.Sprintf("test-subject-%d", time.Now().UnixNano())

	err := st.Write(ctx, func(tx *sql.Tx) error {
		// Insert test subject
		res, err := tx.ExecContext(ctx,
			`INSERT INTO subjects (name, enabled, max_devices, max_ips, max_connections, created_at)
			 VALUES (?, 1, ?, ?, ?, ?)`,
			subjectName, maxDevices, maxIPs, maxConns, time.Now().Unix())
		if err != nil {
			return fmt.Errorf("insert subject: %w", err)
		}

		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("get subject id: %w", err)
		}
		subjectID = id
		return nil
	})

	if err != nil {
		t.Fatalf("setup subject: %v", err)
	}

	return subjectID
}

func setupTestNode(t *testing.T, st *store.Store) int64 {
	t.Helper()

	ctx := context.Background()
	var nodeID int64

	err := st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, created_at) VALUES (?, ?, ?)`,
			"test-node", "10.0.0.1", time.Now().Unix())
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		nodeID = id
		return err
	})

	if err != nil {
		t.Fatalf("setup node: %v", err)
	}

	return nodeID
}

func TestRegisterDevice(t *testing.T) {
	st, cleanup := testDB(t)
	defer cleanup()

	deviceStore := NewStore(st, nil)
	ctx := context.Background()

	t.Run("register new device", func(t *testing.T) {
		maxDevices := int64(3)
		subjectID := setupTestSubject(t, st, &maxDevices, nil, nil)

		var deviceID int64
		err := st.Write(ctx, func(tx *sql.Tx) error {
			id, err := deviceStore.RegisterDevice(ctx, tx, subjectID, "hwid-001", "Test Device", "192.168.1.100", "TestAgent/1.0")
			deviceID = id
			return err
		})
		if err != nil {
			t.Fatalf("RegisterDevice failed: %v", err)
		}

		if deviceID == 0 {
			t.Error("expected non-zero device ID")
		}

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
			err := st.Write(ctx, func(tx *sql.Tx) error {
				_, err := deviceStore.RegisterDevice(ctx, tx, subjectID, fmt.Sprintf("hwid-%03d", i), "Device", "192.168.1.1", "Agent")
				return err
			})
			if err != nil {
				t.Fatalf("RegisterDevice %d failed: %v", i, err)
			}
		}

		// Try to register 3rd device - should fail
		err := st.Write(ctx, func(tx *sql.Tx) error {
			_, err := deviceStore.RegisterDevice(ctx, tx, subjectID, "hwid-003", "Device 3", "192.168.1.1", "Agent")
			return err
		})
		if !errors.Is(err, ErrDeviceLimitReached) {
			t.Errorf("expected ErrDeviceLimitReached, got %v", err)
		}
	})

	t.Run("revoked device rejection", func(t *testing.T) {
		subjectID := setupTestSubject(t, st, nil, nil, nil)

		// Register device
		var deviceID int64
		st.Write(ctx, func(tx *sql.Tx) error {
			id, err := deviceStore.RegisterDevice(ctx, tx, subjectID, "hwid-revoke", "Device", "192.168.1.1", "Agent")
			deviceID = id
			return err
		})

		// Revoke device
		err := st.Write(ctx, func(tx *sql.Tx) error {
			return deviceStore.RevokeDevice(ctx, tx, deviceID, "Testing revocation")
		})
		if err != nil {
			t.Fatalf("RevokeDevice failed: %v", err)
		}

		// Try to register same device again - should fail
		err = st.Write(ctx, func(tx *sql.Tx) error {
			_, err := deviceStore.RegisterDevice(ctx, tx, subjectID, "hwid-revoke", "Device", "192.168.1.1", "Agent")
			return err
		})
		if !errors.Is(err, ErrDeviceRevoked) {
			t.Errorf("expected ErrDeviceRevoked, got %v", err)
		}
	})

	t.Run("update existing device", func(t *testing.T) {
		subjectID := setupTestSubject(t, st, nil, nil, nil)

		// Register device
		var deviceID1 int64
		st.Write(ctx, func(tx *sql.Tx) error {
			id, err := deviceStore.RegisterDevice(ctx, tx, subjectID, "hwid-update", "Device", "192.168.1.1", "Agent/1.0")
			deviceID1 = id
			return err
		})

		// Register same HWID again (simulates reconnection with new IP)
		var deviceID2 int64
		err := st.Write(ctx, func(tx *sql.Tx) error {
			id, err := deviceStore.RegisterDevice(ctx, tx, subjectID, "hwid-update", "Device", "192.168.1.2", "Agent/2.0")
			deviceID2 = id
			return err
		})

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
	nodeID := setupTestNode(t, st)

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
			st.Write(ctx, func(tx *sql.Tx) error {
				return deviceStore.RecordConnection(ctx, tx, subjectID, nil, nodeID, fmt.Sprintf("conn-%d", i),
					fmt.Sprintf("192.168.1.%d", i), "test")
			})
		}

		// Try to connect from 3rd IP - should fail
		err := deviceStore.CheckIPLimit(ctx, subjectID, "192.168.1.3")
		if !errors.Is(err, ErrIPLimitReached) {
			t.Errorf("expected ErrIPLimitReached, got %v", err)
		}
	})

	t.Run("same IP reconnection allowed", func(t *testing.T) {
		maxIPs := int64(2)
		subjectID := setupTestSubject(t, st, nil, &maxIPs, nil)

		// Create active connection
		st.Write(ctx, func(tx *sql.Tx) error {
			return deviceStore.RecordConnection(ctx, tx, subjectID, nil, nodeID, "conn-1", "192.168.1.1", "test")
		})

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
	nodeID := setupTestNode(t, st)

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
			st.Write(ctx, func(tx *sql.Tx) error {
				return deviceStore.RecordConnection(ctx, tx, subjectID, nil, nodeID, fmt.Sprintf("conn-%d", i), "192.168.1.1", "test")
			})
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

	nodeID := setupTestNode(t, st)
	subjectID := setupTestSubject(t, st, nil, nil, nil)

	// Create some connections with different staleness
	// Connections at 1, 2, 3, 4, 5 minutes old
	for i := 1; i <= 5; i++ {
		lastSeen := now.Add(-time.Duration(i) * time.Minute).Unix()
		st.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx,
				`INSERT INTO active_connections (subject_id, node_id, connection_id, source_ip, connected_at, last_seen_at, protocol_info)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				subjectID, nodeID, fmt.Sprintf("conn-%d", i), "192.168.1.1", now.Unix(), lastSeen, "test")
			return err
		})
	}

	// Cleanup connections older than 2.5 minutes
	// This should remove connections 3, 4, 5 (3+ minutes old)
	var deleted int64
	err := st.Write(ctx, func(tx *sql.Tx) error {
		d, err := deviceStore.CleanupStaleConnections(ctx, tx, 150*time.Second)
		deleted = d
		return err
	})
	if err != nil {
		t.Fatalf("CleanupStaleConnections failed: %v", err)
	}

	// Should delete connections 3, 4, 5
	if deleted != 3 {
		t.Errorf("expected 3 deletions, got %d", deleted)
	}

	// Verify remaining connections
	var count int
	st.Read().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM active_connections WHERE subject_id = ?`, subjectID).Scan(&count)
	if count != 2 {
		t.Errorf("expected 2 remaining connections, got %d", count)
	}
}

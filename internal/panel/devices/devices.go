// Package devices manages subject device tracking and limits
package devices

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

var (
	ErrDeviceLimitReached = errors.New("device limit reached")
	ErrDeviceRevoked      = errors.New("device has been revoked")
	ErrIPLimitReached     = errors.New("IP address limit reached")
	ErrConnectionLimit    = errors.New("connection limit reached")
)

// Device represents a registered client device
type Device struct {
	ID            int64
	SubjectID     int64
	HWID          string
	Name          string
	FirstSeenAt   time.Time
	LastSeenAt    time.Time
	LastIP        string
	UserAgent     string
	IsActive      bool
	RevokedAt     *time.Time
	RevokedReason string
}

// Store manages device tracking and enforcement
type Store struct {
	db  *store.Store
	now func() time.Time
}

func NewStore(db *store.Store, now func() time.Time) *Store {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Store{db: db, now: now}
}

// RegisterDevice registers or updates a device for a subject
// Returns device ID and whether registration was allowed
func (s *Store) RegisterDevice(ctx context.Context, tx *sql.Tx, subjectID int64, hwid, name, ip, userAgent string) (int64, error) {
	if hwid == "" {
		return 0, errors.New("hwid is required")
	}

	now := s.now()

	// Check if device already exists
	var deviceID int64
	var revokedAt sql.NullInt64
	err := tx.QueryRowContext(ctx,
		`SELECT id, revoked_at FROM subject_devices WHERE subject_id = ? AND hwid = ?`,
		subjectID, hwid).Scan(&deviceID, &revokedAt)

	if err == nil {
		// Device exists
		if revokedAt.Valid {
			return 0, ErrDeviceRevoked
		}

		// Update last seen
		_, err := tx.ExecContext(ctx,
			`UPDATE subject_devices
			 SET last_seen_at = ?, last_ip = ?, user_agent = ?, is_active = 1
			 WHERE id = ?`,
			now.Unix(), ip, userAgent, deviceID)
		return deviceID, err
	}

	if err != sql.ErrNoRows {
		return 0, fmt.Errorf("check device: %w", err)
	}

	// New device - check limit
	var maxDevices sql.NullInt64
	var currentCount int
	err = tx.QueryRowContext(ctx,
		`SELECT
			s.max_devices,
			(SELECT COUNT(*) FROM subject_devices WHERE subject_id = s.id AND revoked_at IS NULL) as device_count
		 FROM subjects s WHERE s.id = ?`,
		subjectID).Scan(&maxDevices, &currentCount)
	if err != nil {
		return 0, fmt.Errorf("check device limit: %w", err)
	}

	if maxDevices.Valid && currentCount >= int(maxDevices.Int64) {
		return 0, ErrDeviceLimitReached
	}

	// Insert new device
	if name == "" {
		name = fmt.Sprintf("Device %s", hwid[:min(8, len(hwid))])
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO subject_devices
		 (subject_id, hwid, name, first_seen_at, last_seen_at, last_ip, user_agent, is_active)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 1)`,
		subjectID, hwid, name, now.Unix(), now.Unix(), ip, userAgent)
	if err != nil {
		return 0, fmt.Errorf("insert device: %w", err)
	}

	deviceID, err = res.LastInsertId()
	return deviceID, err
}

// RevokeDevice revokes a device, preventing future connections
func (s *Store) RevokeDevice(ctx context.Context, tx *sql.Tx, deviceID int64, reason string) error {
	now := s.now()
	res, err := tx.ExecContext(ctx,
		`UPDATE subject_devices
		 SET revoked_at = ?, revoked_reason = ?, is_active = 0
		 WHERE id = ? AND revoked_at IS NULL`,
		now.Unix(), reason, deviceID)
	if err != nil {
		return err
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return errors.New("device not found or already revoked")
	}

	// Disconnect active connections from this device
	_, err = tx.ExecContext(ctx,
		`DELETE FROM active_connections WHERE device_id = ?`,
		deviceID)
	return err
}

// ListDevices returns all devices for a subject
func (s *Store) ListDevices(ctx context.Context, subjectID int64) ([]Device, error) {
	rows, err := s.db.Read().QueryContext(ctx,
		`SELECT id, subject_id, hwid, name, first_seen_at, last_seen_at, last_ip, user_agent, is_active, revoked_at, COALESCE(revoked_reason, '')
		 FROM subject_devices
		 WHERE subject_id = ?
		 ORDER BY last_seen_at DESC`,
		subjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devices []Device
	for rows.Next() {
		var d Device
		var firstSeen, lastSeen int64
		var revokedAt sql.NullInt64
		err := rows.Scan(&d.ID, &d.SubjectID, &d.HWID, &d.Name, &firstSeen, &lastSeen,
			&d.LastIP, &d.UserAgent, &d.IsActive, &revokedAt, &d.RevokedReason)
		if err != nil {
			return nil, err
		}
		d.FirstSeenAt = time.Unix(firstSeen, 0).UTC()
		d.LastSeenAt = time.Unix(lastSeen, 0).UTC()
		if revokedAt.Valid {
			t := time.Unix(revokedAt.Int64, 0).UTC()
			d.RevokedAt = &t
		}
		devices = append(devices, d)
	}
	return devices, rows.Err()
}

// ListDevicesPaginated returns devices for a subject with pagination
func (s *Store) ListDevicesPaginated(ctx context.Context, subjectID int64, limit, offset int) ([]Device, error) {
	rows, err := s.db.Read().QueryContext(ctx,
		`SELECT id, subject_id, hwid, name, first_seen_at, last_seen_at, last_ip, user_agent, is_active, revoked_at, COALESCE(revoked_reason, '')
		 FROM subject_devices
		 WHERE subject_id = ?
		 ORDER BY last_seen_at DESC
		 LIMIT ? OFFSET ?`,
		subjectID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devices []Device
	for rows.Next() {
		var d Device
		var firstSeen, lastSeen int64
		var revokedAt sql.NullInt64
		err := rows.Scan(&d.ID, &d.SubjectID, &d.HWID, &d.Name, &firstSeen, &lastSeen,
			&d.LastIP, &d.UserAgent, &d.IsActive, &revokedAt, &d.RevokedReason)
		if err != nil {
			return nil, err
		}
		d.FirstSeenAt = time.Unix(firstSeen, 0).UTC()
		d.LastSeenAt = time.Unix(lastSeen, 0).UTC()
		if revokedAt.Valid {
			t := time.Unix(revokedAt.Int64, 0).UTC()
			d.RevokedAt = &t
		}
		devices = append(devices, d)
	}
	return devices, rows.Err()
}

// CheckIPLimit verifies if subject can connect from a new IP address
func (s *Store) CheckIPLimit(ctx context.Context, subjectID int64, sourceIP string) error {
	var maxIPs sql.NullInt64
	var uniqueIPs int

	err := s.db.Read().QueryRowContext(ctx,
		`SELECT
			s.max_ips,
			(SELECT COUNT(DISTINCT source_ip) FROM active_connections WHERE subject_id = s.id) as ip_count
		 FROM subjects s WHERE s.id = ?`,
		subjectID).Scan(&maxIPs, &uniqueIPs)
	if err != nil {
		return fmt.Errorf("check IP limit: %w", err)
	}

	// If no limit, allow
	if !maxIPs.Valid {
		return nil
	}

	// Check if this IP is already connected
	var exists bool
	err = s.db.Read().QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM active_connections WHERE subject_id = ? AND source_ip = ?)`,
		subjectID, sourceIP).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check existing IP: %w", err)
	}

	// If IP already connected, allow
	if exists {
		return nil
	}

	// Check if adding this IP would exceed limit
	if uniqueIPs >= int(maxIPs.Int64) {
		return ErrIPLimitReached
	}

	return nil
}

// CheckConnectionLimit verifies if subject can open a new connection
func (s *Store) CheckConnectionLimit(ctx context.Context, subjectID int64) error {
	var maxConnections sql.NullInt64
	var currentConnections int

	err := s.db.Read().QueryRowContext(ctx,
		`SELECT
			s.max_connections,
			(SELECT COUNT(*) FROM active_connections WHERE subject_id = s.id) as conn_count
		 FROM subjects s WHERE s.id = ?`,
		subjectID).Scan(&maxConnections, &currentConnections)
	if err != nil {
		return fmt.Errorf("check connection limit: %w", err)
	}

	if maxConnections.Valid && currentConnections >= int(maxConnections.Int64) {
		return ErrConnectionLimit
	}

	return nil
}

// RecordConnection records an active connection
func (s *Store) RecordConnection(ctx context.Context, tx *sql.Tx, subjectID int64, deviceID *int64, nodeID int64, connectionID, sourceIP, protocolInfo string) error {
	now := s.now()

	var devID any
	if deviceID != nil {
		devID = *deviceID
	}

	_, err := tx.ExecContext(ctx,
		`INSERT INTO active_connections
		 (subject_id, device_id, node_id, connection_id, source_ip, connected_at, last_seen_at, protocol_info)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(node_id, connection_id) DO UPDATE SET
		   last_seen_at = excluded.last_seen_at`,
		subjectID, devID, nodeID, connectionID, sourceIP, now.Unix(), now.Unix(), protocolInfo)
	return err
}

// RemoveConnection removes an active connection
func (s *Store) RemoveConnection(ctx context.Context, tx *sql.Tx, nodeID int64, connectionID string) error {
	_, err := tx.ExecContext(ctx,
		`DELETE FROM active_connections WHERE node_id = ? AND connection_id = ?`,
		nodeID, connectionID)
	return err
}

// CleanupStaleConnections removes connections not seen recently
func (s *Store) CleanupStaleConnections(ctx context.Context, tx *sql.Tx, staleThreshold time.Duration) (int64, error) {
	cutoff := s.now().Add(-staleThreshold).Unix()
	res, err := tx.ExecContext(ctx,
		`DELETE FROM active_connections WHERE last_seen_at < ?`,
		cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// GetSpeedLimits returns speed limits for a subject (up/down in kbps)
func (s *Store) GetSpeedLimits(ctx context.Context, subjectID int64) (upKbps, downKbps *int64, err error) {
	var up, down sql.NullInt64
	err = s.db.Read().QueryRowContext(ctx,
		`SELECT speed_limit_up_kbps, speed_limit_down_kbps FROM subjects WHERE id = ?`,
		subjectID).Scan(&up, &down)
	if err != nil {
		return nil, nil, err
	}

	if up.Valid {
		upKbps = &up.Int64
	}
	if down.Valid {
		downKbps = &down.Int64
	}
	return upKbps, downKbps, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

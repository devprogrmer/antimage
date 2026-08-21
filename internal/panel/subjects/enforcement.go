// Package subjects provides credential-time enforcement for user management policies.
package subjects

import (
	"context"
	"database/sql"
	"fmt"
)

// CheckPolicyViolation verifies if issuing a credential would violate enforcement policies.
// This is panel-side enforcement at credential generation time.
func (s *Store) CheckPolicyViolation(ctx context.Context, subjectID int64, deviceID string, sourceIP string) error {
	var maxDevices, maxIPs, maxConns sql.NullInt64
	var currentDevices, currentIPs, currentConns int

	err := s.db.Read().QueryRowContext(ctx,
		`SELECT
			s.max_devices,
			s.max_ips,
			s.max_connections,
			(SELECT COUNT(*) FROM subject_devices WHERE subject_id = s.id AND revoked_at IS NULL) as device_count,
			(SELECT COUNT(DISTINCT source_ip) FROM active_connections WHERE subject_id = s.id) as ip_count,
			(SELECT COUNT(*) FROM active_connections WHERE subject_id = s.id) as conn_count
		 FROM subjects s
		 WHERE s.id = ?`,
		subjectID).Scan(&maxDevices, &maxIPs, &maxConns, &currentDevices, &currentIPs, &currentConns)

	if err != nil {
		return fmt.Errorf("check policy: %w", err)
	}

	// Check device limit
	if maxDevices.Valid {
		// Check if this device is already registered
		var exists bool
		err := s.db.Read().QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM subject_devices
			 WHERE subject_id = ? AND hwid = ? AND revoked_at IS NULL)`,
			subjectID, deviceID).Scan(&exists)
		if err != nil {
			return fmt.Errorf("check existing device: %w", err)
		}

		if !exists && currentDevices >= int(maxDevices.Int64) {
			return fmt.Errorf("device limit reached: %d/%d devices registered", currentDevices, maxDevices.Int64)
		}
	}

	// Check IP limit
	if maxIPs.Valid {
		// Check if this IP is already connected
		var exists bool
		err := s.db.Read().QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM active_connections
			 WHERE subject_id = ? AND source_ip = ?)`,
			subjectID, sourceIP).Scan(&exists)
		if err != nil {
			return fmt.Errorf("check existing IP: %w", err)
		}

		if !exists && currentIPs >= int(maxIPs.Int64) {
			return fmt.Errorf("IP limit reached: %d/%d unique IPs connected", currentIPs, maxIPs.Int64)
		}
	}

	// Check connection limit
	if maxConns.Valid && currentConns >= int(maxConns.Int64) {
		return fmt.Errorf("connection limit reached: %d/%d active connections", currentConns, maxConns.Int64)
	}

	return nil
}

// EnforcementStatus returns current enforcement status for a subject.
type EnforcementStatus struct {
	SubjectID          int64
	MaxDevices         *int64
	MaxIPs             *int64
	MaxConnections     *int64
	SpeedLimitUpKbps   *int64
	SpeedLimitDownKbps *int64
	CurrentDevices     int
	CurrentIPs         int
	CurrentConnections int
}

// GetEnforcementStatus returns current enforcement status for a subject.
func (s *Store) GetEnforcementStatus(ctx context.Context, subjectID int64) (*EnforcementStatus, error) {
	var status EnforcementStatus
	status.SubjectID = subjectID

	err := s.db.Read().QueryRowContext(ctx,
		`SELECT
			s.max_devices,
			s.max_ips,
			s.max_connections,
			s.speed_limit_up_kbps,
			s.speed_limit_down_kbps,
			(SELECT COUNT(*) FROM subject_devices WHERE subject_id = s.id AND revoked_at IS NULL) as device_count,
			(SELECT COUNT(DISTINCT source_ip) FROM active_connections WHERE subject_id = s.id) as ip_count,
			(SELECT COUNT(*) FROM active_connections WHERE subject_id = s.id) as conn_count
		 FROM subjects s
		 WHERE s.id = ?`,
		subjectID).Scan(
		&status.MaxDevices,
		&status.MaxIPs,
		&status.MaxConnections,
		&status.SpeedLimitUpKbps,
		&status.SpeedLimitDownKbps,
		&status.CurrentDevices,
		&status.CurrentIPs,
		&status.CurrentConnections,
	)

	if err != nil {
		return nil, fmt.Errorf("get enforcement status: %w", err)
	}

	return &status, nil
}

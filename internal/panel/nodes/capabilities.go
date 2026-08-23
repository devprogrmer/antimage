package nodes

import (
	"context"
	"database/sql"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

// Protocol represents a supported VPN protocol.
type Protocol string

const (
	ProtocolXray      Protocol = "xray"
	ProtocolSingbox   Protocol = "sing-box"
	ProtocolWireGuard Protocol = "wireguard"
	ProtocolHysteria2 Protocol = "hysteria2"
	ProtocolL2TPIPsec Protocol = "l2tp-ipsec"
)

// NodeCapability represents a protocol capability for a node.
type NodeCapability struct {
	NodeID     int64
	Protocol   Protocol
	Available  bool
	Version    *string
	DetectedAt time.Time
	LastCheckAt time.Time
}

// RecordCapability stores or updates a protocol capability for a node.
func RecordCapability(ctx context.Context, s *store.Store, cap NodeCapability) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO node_capabilities (node_id, protocol, available, version, detected_at, last_check_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(node_id, protocol) DO UPDATE SET
				available = excluded.available,
				version = excluded.version,
				last_check_at = excluded.last_check_at
		`, cap.NodeID, cap.Protocol, boolToInt(cap.Available), cap.Version,
		   cap.DetectedAt.Unix(), cap.LastCheckAt.Unix())
		return err
	})
}

// GetNodeCapabilities retrieves all protocol capabilities for a node.
func GetNodeCapabilities(ctx context.Context, s *store.Store, nodeID int64) ([]NodeCapability, error) {
	rows, err := s.Read().QueryContext(ctx, `
		SELECT node_id, protocol, available, version, detected_at, last_check_at
		FROM node_capabilities
		WHERE node_id = ?
		ORDER BY protocol
	`, nodeID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var capabilities []NodeCapability
	for rows.Next() {
		var (
			cap          NodeCapability
			availableInt int
			detectedUnix int64
			lastCheckUnix int64
		)
		err := rows.Scan(
			&cap.NodeID,
			&cap.Protocol,
			&availableInt,
			&cap.Version,
			&detectedUnix,
			&lastCheckUnix,
		)
		if err != nil {
			return nil, err
		}
		cap.Available = availableInt != 0
		cap.DetectedAt = time.Unix(detectedUnix, 0)
		cap.LastCheckAt = time.Unix(lastCheckUnix, 0)
		capabilities = append(capabilities, cap)
	}

	return capabilities, rows.Err()
}

// GetAvailableProtocols returns only the available protocols for a node.
func GetAvailableProtocols(ctx context.Context, s *store.Store, nodeID int64) ([]Protocol, error) {
	rows, err := s.Read().QueryContext(ctx, `
		SELECT protocol
		FROM node_capabilities
		WHERE node_id = ? AND available = 1
		ORDER BY protocol
	`, nodeID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var protocols []Protocol
	for rows.Next() {
		var p Protocol
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		protocols = append(protocols, p)
	}

	return protocols, rows.Err()
}

// boolToInt converts bool to SQLite integer (0 or 1).
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

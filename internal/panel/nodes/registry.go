package nodes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

// AdapterRegistryEntry represents an adapter registered by a node.
type AdapterRegistryEntry struct {
	ID           int64
	NodeID       int64
	Kind         string
	Version      string
	Capabilities []string
	ReportedAt   time.Time
}

// UpsertAdapter records adapter info from Hello message.
// It inserts a new entry or updates the existing one for the same node_id + kind.
func UpsertAdapter(ctx context.Context, s *store.Store, nodeID int64,
	kind, version string, capabilities []string, now time.Time) error {

	caps, err := json.Marshal(capabilities)
	if err != nil {
		return fmt.Errorf("marshal capabilities: %w", err)
	}

	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO adapter_registry (node_id, kind, version, capabilities, reported_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(node_id, kind) DO UPDATE SET
				version = excluded.version,
				capabilities = excluded.capabilities,
				reported_at = excluded.reported_at`,
			nodeID, kind, version, string(caps), now.Unix())
		return err
	})
}

// ListAdapters returns all adapters registered by a node.
func ListAdapters(ctx context.Context, s *store.Store, nodeID int64) ([]AdapterRegistryEntry, error) {
	rows, err := s.Read().QueryContext(ctx,
		`SELECT id, node_id, kind, version, capabilities, reported_at
		 FROM adapter_registry WHERE node_id = ?
		 ORDER BY kind`, nodeID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var entries []AdapterRegistryEntry
	for rows.Next() {
		var e AdapterRegistryEntry
		var capsJSON string
		var reportedAt int64
		if err := rows.Scan(&e.ID, &e.NodeID, &e.Kind, &e.Version,
			&capsJSON, &reportedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(capsJSON), &e.Capabilities); err != nil {
			return nil, fmt.Errorf("unmarshal capabilities: %w", err)
		}
		e.ReportedAt = time.Unix(reportedAt, 0).UTC()
		entries = append(entries, e)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return entries, nil
}

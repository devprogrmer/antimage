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
	// ServiceSchema is what this node published for this adapter. Empty means
	// the node has not reported one, which is what makes a protocol unofferable
	// rather than merely unconfigured.
	ServiceSchema  []byte
	HotUserAdd     bool
	SelfAccounting bool
	RequiresPKI    bool
}

// UpsertAdapter records what one adapter on one node reports about itself.
//
// Everything here is OBSERVED: it is the node describing its own installation,
// never configuration the panel pushes. That is why the whole row is replaced
// on conflict -- an agent that restarts with a different binary, a newly
// configured management API, or a protocol removed from node.yaml is telling
// the truth about now, and a merge would leave the panel believing a mixture of
// two installations that never existed.
//
// The one exception is service_schema, which is preserved when the agent sends
// none. An older agent predates the field entirely, and blanking a schema the
// panel already holds would make a working protocol unofferable on upgrade.
func UpsertAdapter(ctx context.Context, s *store.Store, nodeID int64,
	info AdapterInfo, now time.Time) error {

	caps, err := json.Marshal(info.Capabilities)
	if err != nil {
		return fmt.Errorf("marshal capabilities: %w", err)
	}

	var schema any
	if len(info.ServiceSchema) > 0 {
		schema = string(info.ServiceSchema)
	}

	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO adapter_registry
				(node_id, kind, version, capabilities, reported_at,
				 service_schema, hot_user_add, self_accounting, requires_pki)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(node_id, kind) DO UPDATE SET
				version = excluded.version,
				capabilities = excluded.capabilities,
				reported_at = excluded.reported_at,
				-- COALESCE, not excluded: an agent too old to send a schema must
				-- not erase one already recorded.
				service_schema = COALESCE(excluded.service_schema, adapter_registry.service_schema),
				hot_user_add = excluded.hot_user_add,
				self_accounting = excluded.self_accounting,
				requires_pki = excluded.requires_pki`,
			nodeID, info.Kind, info.Version, string(caps), now.Unix(),
			schema, boolToInt(info.HotUserAdd), boolToInt(info.SelfAccounting),
			boolToInt(info.RequiresPKI))
		return err
	})
}

// ListAdapters returns all adapters registered by a node.
func ListAdapters(ctx context.Context, s *store.Store, nodeID int64) ([]AdapterRegistryEntry, error) {
	rows, err := s.Read().QueryContext(ctx,
		`SELECT id, node_id, kind, version, capabilities, reported_at,
		        service_schema, hot_user_add, self_accounting, requires_pki
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
		var schema sql.NullString
		var hotAdd, selfAcct, pki int
		if err := rows.Scan(&e.ID, &e.NodeID, &e.Kind, &e.Version,
			&capsJSON, &reportedAt, &schema, &hotAdd, &selfAcct, &pki); err != nil {
			return nil, err
		}
		if schema.Valid {
			e.ServiceSchema = []byte(schema.String)
		}
		e.HotUserAdd = hotAdd == 1
		e.SelfAccounting = selfAcct == 1
		e.RequiresPKI = pki == 1
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

package nodes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/amyrm/antimage/internal/shared/canonical"
)

// sortServices orders services by ID so the canonical document is byte-identical
// across builds. This does not rely on SQL row order: services.id aliases
// SQLite's rowid today, so a bare SELECT happens to return sorted rows, but
// that is incidental and SQLite does not guarantee scan order without ORDER BY.
func sortServices(services []Service) {
	sort.Slice(services, func(i, j int) bool { return services[i].ID < services[j].ID })
}

// BuildDesiredSnapshot is the one authoritative reader of desired state
// (invariant 5).
//
// It takes a transaction rather than opening its own, which is what closes
// the read race in spec section 5: the revision counter and the rows that
// make up the document are read from a single consistent snapshot, so a
// document can never be labelled with a revision that does not describe it.
func BuildDesiredSnapshot(ctx context.Context, tx *sql.Tx, nodeID int64) (*Snapshot, error) {
	var revision int64
	err := tx.QueryRowContext(ctx,
		`SELECT desired_revision FROM nodes WHERE id = ?`, nodeID).Scan(&revision)
	if err != nil {
		return nil, fmt.Errorf("read revision for node %d: %w", nodeID, err)
	}

	// ORDER BY id gives the stable array ordering invariant 3 requires.
	rows, err := tx.QueryContext(ctx,
		`SELECT id, adapter_kind, enabled, params
		   FROM services WHERE node_id = ? ORDER BY id`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("read services for node %d: %w", nodeID, err)
	}
	defer func() { _ = rows.Close() }()

	var services []Service
	for rows.Next() {
		var (
			svc     Service
			enabled int
			params  string
		)
		if err := rows.Scan(&svc.ID, &svc.Kind, &enabled, &params); err != nil {
			return nil, fmt.Errorf("scan service: %w", err)
		}
		svc.Enabled = enabled == 1
		svc.Params = json.RawMessage(params)
		services = append(services, svc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate services: %w", err)
	}
	sortServices(services)

	doc := Document{
		SchemaVersion: DocumentSchemaVersion,
		Revision:      revision,
		NodeID:        nodeID,
		Services:      services,
		Subjects:      nil, // SP2 fills this; null is explicit, not omitted
	}

	bytes, sum, err := canonical.Hash(doc)
	if err != nil {
		return nil, fmt.Errorf("canonicalize document for node %d: %w", nodeID, err)
	}
	return &Snapshot{Revision: revision, Document: doc, Bytes: bytes, SHA256: sum}, nil
}

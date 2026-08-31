package nodes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// buildDNS assembles the node's DNS resolution behavior from its stored
// config blob.
//
// Returns nil when the node has none, which is what keeps the document at
// whatever version its other content already implies -- see
// effectiveSchemaVersion. A node given no DNS state must not have its hash
// move (and therefore reconcile) the moment schema v4 exists.
func buildDNS(ctx context.Context, tx *sql.Tx, nodeID int64) (*DNSConfig, error) {
	var raw string
	if err := tx.QueryRowContext(ctx,
		`SELECT dns_config FROM nodes WHERE id = ?`, nodeID).Scan(&raw); err != nil {
		return nil, fmt.Errorf("read dns config for node %d: %w", nodeID, err)
	}

	var cfg DNSConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("decode dns config for node %d: %w", nodeID, err)
	}

	if len(cfg.Servers) == 0 && len(cfg.Hosts) == 0 && len(cfg.FakeDNS) == 0 &&
		cfg.QueryStrategy == "" && !cfg.DisableCache {
		return nil, nil
	}
	return &cfg, nil
}

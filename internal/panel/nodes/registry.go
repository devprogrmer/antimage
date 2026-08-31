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

	// GeoUpdatedAt, GeoIPSHA256, GeoSiteSHA256 are set by the panel's own
	// geo-update handler when a command against this adapter succeeds --
	// never by Hello, which reports what the agent's process knows about
	// itself and has no way to know what happened to a file on disk since.
	// Nil/empty means never updated, not "unknown".
	GeoUpdatedAt  *time.Time
	GeoIPSHA256   string
	GeoSiteSHA256 string

	// CoreUpgradedAt is set by the panel's own core-upgrade handler after a
	// successful UpgradeCore command. The live Version field above already
	// reflects reality on the adapter's next Hello regardless; this answers
	// "when did the panel last push one", not "what is running now".
	CoreUpgradedAt *time.Time
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
// RecordCoreUpgrade is the one deliberate exception to "only Hello writes
// version": see its own comment for why a successful core upgrade needs to
// update this column before the agent's next Hello would.
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
		        service_schema, hot_user_add, self_accounting, requires_pki,
		        geo_updated_at, COALESCE(geo_geoip_sha256, ''), COALESCE(geo_geosite_sha256, ''),
		        core_upgraded_at
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
		var geoUpdatedAt, coreUpgradedAt sql.NullInt64
		if err := rows.Scan(&e.ID, &e.NodeID, &e.Kind, &e.Version,
			&capsJSON, &reportedAt, &schema, &hotAdd, &selfAcct, &pki,
			&geoUpdatedAt, &e.GeoIPSHA256, &e.GeoSiteSHA256, &coreUpgradedAt); err != nil {
			return nil, err
		}
		if geoUpdatedAt.Valid {
			t := time.Unix(geoUpdatedAt.Int64, 0).UTC()
			e.GeoUpdatedAt = &t
		}
		if coreUpgradedAt.Valid {
			t := time.Unix(coreUpgradedAt.Int64, 0).UTC()
			e.CoreUpgradedAt = &t
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

// RecordGeoUpdate stamps a successful geo-data update against an existing
// adapter_registry row.
//
// Only UPDATEs, never INSERTs: the row this is stamping was created by
// Hello when the adapter first connected, and a geo update for a kind the
// node has never reported would be updating a fact about an adapter that,
// as far as the panel knows, does not exist here. Reports whether a row
// existed to update, so the caller can distinguish "recorded" from
// "nothing to record it against".
func RecordGeoUpdate(ctx context.Context, s *store.Store, nodeID int64, kind, geoipSHA256, geositeSHA256 string, at time.Time) (bool, error) {
	var affected int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE adapter_registry
			    SET geo_updated_at = ?, geo_geoip_sha256 = ?, geo_geosite_sha256 = ?
			  WHERE node_id = ? AND kind = ?`,
			at.Unix(), geoipSHA256, geositeSHA256, nodeID, kind)
		if err != nil {
			return err
		}
		affected, err = res.RowsAffected()
		return err
	})
	return affected > 0, err
}

// RecordCoreUpgrade stamps a successful core-version upgrade against an
// existing adapter_registry row. Same reasoning as RecordGeoUpdate: only
// UPDATEs, never INSERTs, because the row this stamps was created by Hello,
// and stamping a kind the node never reported would be recording a fact
// about an adapter that, as far as the panel knows, does not exist here.
//
// This ALSO overwrites the version column directly, which every other
// write to it treats as Hello-exclusive territory (see UpsertAdapter's own
// comment on that). The exception is deliberate: a core upgrade restarts
// the ADAPTER's process (systemctl restart xray), not the antimage-node
// AGENT process, and the agent only sends Hello again on its own reconnect
// -- which can be days away. Without this, the browser would keep showing
// the pre-upgrade version as "current" long after the new one was actually
// installed and verified healthy, which is a worse lie than widening who
// may write this column.
func RecordCoreUpgrade(ctx context.Context, s *store.Store, nodeID int64, kind, installedVersion string, at time.Time) (bool, error) {
	var affected int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE adapter_registry SET core_upgraded_at = ?, version = ? WHERE node_id = ? AND kind = ?`,
			at.Unix(), installedVersion, nodeID, kind)
		if err != nil {
			return err
		}
		affected, err = res.RowsAffected()
		return err
	})
	return affected > 0, err
}

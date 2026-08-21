-- +goose Up
-- Premium Management Layer: Templates, Presets, Bulk Operations, Dashboard Stats

-- Service Templates: Reusable protocol configurations
CREATE TABLE service_templates (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE COLLATE NOCASE,
    adapter_kind TEXT NOT NULL CHECK (adapter_kind IN ('xray','singbox','openvpn','l2tp')),
    params_json TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    tags_json TEXT NOT NULL DEFAULT '[]',
    is_public INTEGER NOT NULL DEFAULT 0 CHECK (is_public IN (0,1)),
    created_by INTEGER REFERENCES admins(id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE INDEX service_templates_adapter ON service_templates(adapter_kind);
CREATE INDEX service_templates_creator ON service_templates(created_by);

-- User Presets: Common quota/expiry patterns
CREATE TABLE user_presets (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE COLLATE NOCASE,
    description TEXT NOT NULL DEFAULT '',
    quota_bytes INTEGER,
    validity_days INTEGER,
    auto_assign_services_json TEXT NOT NULL DEFAULT '[]',
    auto_assign_node_tags_json TEXT NOT NULL DEFAULT '[]',
    is_public INTEGER NOT NULL DEFAULT 0 CHECK (is_public IN (0,1)),
    created_by INTEGER REFERENCES admins(id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE INDEX user_presets_creator ON user_presets(created_by);

-- Bulk Operations: Track batch job progress
CREATE TABLE bulk_operations (
    id INTEGER PRIMARY KEY,
    operation_type TEXT NOT NULL CHECK (operation_type IN (
        'subjects_create',
        'subjects_update',
        'subjects_delete',
        'subjects_freeze',
        'subjects_unfreeze',
        'subjects_grant_service',
        'subjects_revoke_service',
        'nodes_configure',
        'nodes_update_settings'
    )),
    actor_admin_id INTEGER REFERENCES admins(id) ON DELETE SET NULL,
    total_items INTEGER NOT NULL CHECK (total_items > 0),
    completed_items INTEGER NOT NULL DEFAULT 0 CHECK (completed_items >= 0),
    failed_items INTEGER NOT NULL DEFAULT 0 CHECK (failed_items >= 0),
    status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN (
        'queued','running','completed','failed','cancelled'
    )),
    results_json TEXT NOT NULL DEFAULT '[]',
    created_at INTEGER NOT NULL,
    started_at INTEGER,
    completed_at INTEGER,
    CHECK (completed_items + failed_items <= total_items)
) STRICT;

CREATE INDEX bulk_operations_actor ON bulk_operations(actor_admin_id, created_at DESC);
CREATE INDEX bulk_operations_status ON bulk_operations(status, created_at);

-- Dashboard Stats: Materialized aggregates
CREATE TABLE dashboard_stats (
    admin_id INTEGER REFERENCES admins(id) ON DELETE CASCADE,
    computed_at INTEGER NOT NULL,
    nodes_total INTEGER NOT NULL DEFAULT 0,
    nodes_online INTEGER NOT NULL DEFAULT 0,
    nodes_degraded INTEGER NOT NULL DEFAULT 0,
    nodes_offline INTEGER NOT NULL DEFAULT 0,
    subjects_total INTEGER NOT NULL DEFAULT 0,
    subjects_active INTEGER NOT NULL DEFAULT 0,
    subjects_expired INTEGER NOT NULL DEFAULT 0,
    subjects_frozen INTEGER NOT NULL DEFAULT 0,
    traffic_24h_uplink INTEGER NOT NULL DEFAULT 0,
    traffic_24h_downlink INTEGER NOT NULL DEFAULT 0,
    quota_total_bytes INTEGER,
    quota_used_bytes INTEGER NOT NULL DEFAULT 0,
    quota_utilization_pct REAL,
    PRIMARY KEY (admin_id)
) STRICT;

CREATE INDEX dashboard_stats_computed ON dashboard_stats(computed_at);

-- Extend nodes with tags
ALTER TABLE nodes ADD COLUMN tags_json TEXT NOT NULL DEFAULT '[]';
CREATE INDEX nodes_tags ON nodes(tags_json) WHERE tags_json != '[]';

-- +goose Down
DROP INDEX IF EXISTS nodes_tags;
ALTER TABLE nodes DROP COLUMN tags_json;
DROP TABLE dashboard_stats;
DROP TABLE bulk_operations;
DROP TABLE user_presets;
DROP TABLE service_templates;

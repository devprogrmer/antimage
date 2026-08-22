-- +goose Up
-- SQLite doesn't support modifying CHECK constraints directly.
-- The workaround is to disable foreign key constraints and recreate the table.

PRAGMA foreign_keys=OFF;

-- Get the complete current schema
CREATE TABLE nodes_new (
    id                INTEGER PRIMARY KEY,
    name              TEXT NOT NULL UNIQUE,
    address           TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending','enrolling','online','degraded',
                                        'integrity','offline','disabled','maintenance')),
    adapter_kinds     TEXT NOT NULL DEFAULT '[]',
    cert_fingerprint  TEXT UNIQUE,
    desired_revision  INTEGER NOT NULL DEFAULT 0,
    applied_revision  INTEGER NOT NULL DEFAULT 0,
    last_seen_at      INTEGER,
    last_error        TEXT,
    maintenance_window TEXT,
    enrolled_at       INTEGER,
    created_at        INTEGER NOT NULL,
    reconnect_count INTEGER NOT NULL DEFAULT 0,
    last_reconcile_duration_ms INTEGER,
    failed_reconcile_streak INTEGER NOT NULL DEFAULT 0,
    tags_json TEXT NOT NULL DEFAULT '[]',
    maintenance_mode INTEGER NOT NULL DEFAULT 0,
    maintenance_reason TEXT,
    maintenance_entered_at INTEGER,
    last_sync_at INTEGER,
    last_sync_error TEXT,
    config_drift INTEGER NOT NULL DEFAULT 0,
    agent_version TEXT,
    os_info TEXT,
    CHECK (applied_revision <= desired_revision)
) STRICT;

-- Copy all data
INSERT INTO nodes_new SELECT * FROM nodes;

-- Drop old table
DROP TABLE nodes;

-- Rename new table
ALTER TABLE nodes_new RENAME TO nodes;

-- Recreate indexes (if any exist)
CREATE INDEX IF NOT EXISTS idx_nodes_status ON nodes(status);
CREATE INDEX IF NOT EXISTS idx_nodes_last_seen ON nodes(last_seen_at);

PRAGMA foreign_keys=ON;

-- +goose Down
PRAGMA foreign_keys=OFF;

CREATE TABLE nodes_new (
    id                INTEGER PRIMARY KEY,
    name              TEXT NOT NULL UNIQUE,
    address           TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending','enrolling','online','degraded',
                                        'integrity','offline','disabled')),
    adapter_kinds     TEXT NOT NULL DEFAULT '[]',
    cert_fingerprint  TEXT UNIQUE,
    desired_revision  INTEGER NOT NULL DEFAULT 0,
    applied_revision  INTEGER NOT NULL DEFAULT 0,
    last_seen_at      INTEGER,
    last_error        TEXT,
    maintenance_window TEXT,
    enrolled_at       INTEGER,
    created_at        INTEGER NOT NULL,
    reconnect_count INTEGER NOT NULL DEFAULT 0,
    last_reconcile_duration_ms INTEGER,
    failed_reconcile_streak INTEGER NOT NULL DEFAULT 0,
    tags_json TEXT NOT NULL DEFAULT '[]',
    maintenance_mode INTEGER NOT NULL DEFAULT 0,
    maintenance_reason TEXT,
    maintenance_entered_at INTEGER,
    last_sync_at INTEGER,
    last_sync_error TEXT,
    config_drift INTEGER NOT NULL DEFAULT 0,
    agent_version TEXT,
    os_info TEXT,
    CHECK (applied_revision <= desired_revision)
) STRICT;

-- Copy data back, converting maintenance status to online
INSERT INTO nodes_new
SELECT id, name, address,
       CASE WHEN status = 'maintenance' THEN 'online' ELSE status END,
       adapter_kinds, cert_fingerprint, desired_revision, applied_revision,
       last_seen_at, last_error, maintenance_window, enrolled_at, created_at,
       reconnect_count, last_reconcile_duration_ms, failed_reconcile_streak,
       tags_json, maintenance_mode, maintenance_reason, maintenance_entered_at,
       last_sync_at, last_sync_error, config_drift, agent_version, os_info
FROM nodes;

DROP TABLE nodes;
ALTER TABLE nodes_new RENAME TO nodes;

CREATE INDEX IF NOT EXISTS idx_nodes_status ON nodes(status);
CREATE INDEX IF NOT EXISTS idx_nodes_last_seen ON nodes(last_seen_at);

PRAGMA foreign_keys=ON;

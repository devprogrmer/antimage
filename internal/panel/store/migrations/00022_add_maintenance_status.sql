-- +goose Up
-- SQLite doesn't support modifying CHECK constraints directly.
-- We need to recreate the table, but we must preserve foreign key relationships.
-- Since PRAGMA foreign_keys cannot be changed inside a transaction (which goose uses),
-- we use a different approach: recreate dependent tables too.

-- Step 1: Create new nodes table with updated CHECK constraint
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

-- Step 2: Copy all node data
INSERT INTO nodes_new SELECT * FROM nodes;

-- Step 3: Recreate services table with FK pointing to nodes_new
CREATE TABLE services_new (
    id           INTEGER PRIMARY KEY,
    node_id      INTEGER NOT NULL REFERENCES nodes_new(id) ON DELETE CASCADE,
    adapter_kind TEXT NOT NULL,
    params       TEXT NOT NULL,
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   INTEGER NOT NULL
) STRICT;

-- Step 4: Copy all service data
INSERT INTO services_new SELECT * FROM services;

-- Step 5: Drop old tables
DROP TABLE services;
DROP TABLE nodes;

-- Step 6: Rename new tables to original names
ALTER TABLE nodes_new RENAME TO nodes;
ALTER TABLE services_new RENAME TO services;

-- Step 7: Recreate indexes
CREATE INDEX IF NOT EXISTS idx_nodes_status ON nodes(status);
CREATE INDEX IF NOT EXISTS idx_nodes_last_seen ON nodes(last_seen_at);
CREATE INDEX IF NOT EXISTS nodes_tags ON nodes(tags_json) WHERE tags_json != '[]';
CREATE INDEX services_node ON services (node_id);

-- +goose Down
-- Reverse the migration: remove maintenance status from CHECK constraint

-- Step 1: Create nodes table with old CHECK constraint (no 'maintenance')
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

-- Step 2: Copy data back, converting maintenance status to online
INSERT INTO nodes_new
SELECT id, name, address,
       CASE WHEN status = 'maintenance' THEN 'online' ELSE status END,
       adapter_kinds, cert_fingerprint, desired_revision, applied_revision,
       last_seen_at, last_error, maintenance_window, enrolled_at, created_at,
       reconnect_count, last_reconcile_duration_ms, failed_reconcile_streak,
       tags_json, maintenance_mode, maintenance_reason, maintenance_entered_at,
       last_sync_at, last_sync_error, config_drift, agent_version, os_info
FROM nodes;

-- Step 3: Recreate services table pointing to nodes_new
CREATE TABLE services_new (
    id           INTEGER PRIMARY KEY,
    node_id      INTEGER NOT NULL REFERENCES nodes_new(id) ON DELETE CASCADE,
    adapter_kind TEXT NOT NULL,
    params       TEXT NOT NULL,
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   INTEGER NOT NULL
) STRICT;

-- Step 4: Copy all service data
INSERT INTO services_new SELECT * FROM services;

-- Step 5: Drop old tables
DROP TABLE services;
DROP TABLE nodes;

-- Step 6: Rename new tables
ALTER TABLE nodes_new RENAME TO nodes;
ALTER TABLE services_new RENAME TO services;

-- Step 7: Recreate indexes
CREATE INDEX IF NOT EXISTS idx_nodes_status ON nodes(status);
CREATE INDEX IF NOT EXISTS idx_nodes_last_seen ON nodes(last_seen_at);
CREATE INDEX IF NOT EXISTS nodes_tags ON nodes(tags_json) WHERE tags_json != '[]';
CREATE INDEX services_node ON services (node_id);


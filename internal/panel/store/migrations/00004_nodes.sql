-- +goose Up
CREATE TABLE nodes (
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
    CHECK (applied_revision <= desired_revision)
) STRICT;

CREATE TABLE services (
    id           INTEGER PRIMARY KEY,
    node_id      INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    adapter_kind TEXT NOT NULL,
    params       TEXT NOT NULL,
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   INTEGER NOT NULL
) STRICT;

CREATE INDEX services_node ON services (node_id);

-- +goose Down
DROP TABLE services;
DROP TABLE nodes;

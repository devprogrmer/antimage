-- Stop the database deciding which protocols exist.
--
-- service_templates.adapter_kind has carried this CHECK since 00016:
--
--     CHECK (adapter_kind IN ('xray','singbox','openvpn','l2tp'))
--
-- and it is wrong in both directions. It permits 'openvpn', for which no
-- adapter has ever existed, so a template could be saved for a protocol no
-- node can run. It omits 'wireguard' and 'hysteria2', which are real, shipped
-- adapters -- so saving a template for either has always been rejected by the
-- database with a constraint error. Adding ocserv would have made that three.
--
-- The list is not fixed by adding the missing names, because a static list in
-- SQL is precisely the panel-side protocol knowledge the architecture exists
-- to prevent (§68: if adapter.Caps does not declare it, the panel does not
-- offer it). Every other place that needs to know which kinds are real asks
-- the adapters: services.adapter_kind is plain TEXT for the same reason, and
-- params are validated against the adapter's published ServiceSchema. A
-- constraint here duplicates that knowledge somewhere it cannot be kept
-- current, and it drifts silently -- nobody notices a missing name until an
-- operator cannot save a template.
--
-- Dropping the CHECK does not drop validation. It moves it to where the
-- protocol knowledge actually lives, which is the only place it can be right.
--
-- SQLite cannot drop a CHECK, so the table is rebuilt. Both indexes from 00016
-- are recreated; service_templates.id is a plain INTEGER PRIMARY KEY with no
-- AUTOINCREMENT, so there is no sqlite_sequence row to carry over.

-- +goose Up

CREATE TABLE service_templates_new (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE COLLATE NOCASE,
    adapter_kind TEXT NOT NULL,
    params_json TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    tags_json TEXT NOT NULL DEFAULT '[]',
    is_public INTEGER NOT NULL DEFAULT 0 CHECK (is_public IN (0,1)),
    created_by INTEGER REFERENCES admins(id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

INSERT INTO service_templates_new
    (id, name, adapter_kind, params_json, description, tags_json,
     is_public, created_by, created_at, updated_at)
SELECT
     id, name, adapter_kind, params_json, description, tags_json,
     is_public, created_by, created_at, updated_at
  FROM service_templates;

DROP TABLE service_templates;
ALTER TABLE service_templates_new RENAME TO service_templates;

CREATE INDEX service_templates_adapter ON service_templates(adapter_kind);
CREATE INDEX service_templates_creator ON service_templates(created_by);

-- +goose Down

-- Rows naming a kind the old constraint did not permit cannot come back, and
-- are dropped rather than rewritten to a protocol they are not.
CREATE TABLE service_templates_old (
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

INSERT INTO service_templates_old
    (id, name, adapter_kind, params_json, description, tags_json,
     is_public, created_by, created_at, updated_at)
SELECT
     id, name, adapter_kind, params_json, description, tags_json,
     is_public, created_by, created_at, updated_at
  FROM service_templates
 WHERE adapter_kind IN ('xray','singbox','openvpn','l2tp');

DROP TABLE service_templates;
ALTER TABLE service_templates_old RENAME TO service_templates;

CREATE INDEX service_templates_adapter ON service_templates(adapter_kind);
CREATE INDEX service_templates_creator ON service_templates(created_by);

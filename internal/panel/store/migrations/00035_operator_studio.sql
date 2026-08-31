-- +goose Up
-- Operator studio: Marzban/Rebecca-style subscription hosts and panel settings.
-- Hosts are how an inbound is *presented* to clients (CDN address, SNI, Reality
-- public key, remark). They never change what the node listens on.

CREATE TABLE subscription_hosts (
    id            INTEGER PRIMARY KEY,
    service_id    INTEGER NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    remark        TEXT NOT NULL DEFAULT '',
    address       TEXT NOT NULL DEFAULT '',
    port          INTEGER,
    sni           TEXT NOT NULL DEFAULT '',
    host          TEXT NOT NULL DEFAULT '',
    path          TEXT NOT NULL DEFAULT '',
    security      TEXT NOT NULL DEFAULT '',
    fingerprint   TEXT NOT NULL DEFAULT '',
    alpn          TEXT NOT NULL DEFAULT '',
    allow_insecure INTEGER NOT NULL DEFAULT 0 CHECK (allow_insecure IN (0,1)),
    public_key    TEXT NOT NULL DEFAULT '',
    short_id      TEXT NOT NULL DEFAULT '',
    spider_x      TEXT NOT NULL DEFAULT '',
    flow          TEXT NOT NULL DEFAULT '',
    enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    priority      INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL
) STRICT;

CREATE INDEX subscription_hosts_service ON subscription_hosts (service_id, priority, id);

CREATE TABLE panel_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE subscription_hosts;
DROP TABLE panel_settings;

-- +goose Up
-- SP7: Observability Depth
-- Node metrics history, long-term rollups, and persistent alerts.

-- Persistent alerts with lifecycle tracking
CREATE TABLE alerts (
    id              INTEGER PRIMARY KEY,
    alert_type      TEXT NOT NULL CHECK (alert_type IN ('cert_expiry', 'quota_warning', 'quota_exceeded')),
    severity        TEXT NOT NULL CHECK (severity IN ('warning', 'critical')),
    target_type     TEXT NOT NULL CHECK (target_type IN ('node', 'subject')),
    target_id       INTEGER NOT NULL,
    state           TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'resolved')),
    dedup_key       TEXT NOT NULL,
    first_seen_at   INTEGER NOT NULL,
    last_seen_at    INTEGER NOT NULL,
    resolved_at     INTEGER,
    threshold_value TEXT,
    current_value   TEXT,
    metadata        TEXT NOT NULL DEFAULT '{}',
    CHECK (state = 'resolved' OR resolved_at IS NULL)
) STRICT;

-- Partial unique index: only one active alert per dedup_key (allows re-alerts after resolution)
CREATE UNIQUE INDEX alerts_dedup_active ON alerts(dedup_key) WHERE state = 'active';

CREATE INDEX alerts_active ON alerts(state, alert_type) WHERE state = 'active';
CREATE INDEX alerts_target ON alerts(target_type, target_id);
CREATE INDEX alerts_first_seen ON alerts(first_seen_at DESC);

-- Hourly rollups of node health metrics (90-day retention)
CREATE TABLE node_health_rollups_hourly (
    node_id         INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    hour_start      INTEGER NOT NULL,
    samples         INTEGER NOT NULL,
    avg_load1       REAL NOT NULL,
    avg_mem_used    INTEGER NOT NULL,
    min_rtt_ms      INTEGER,
    avg_rtt_ms      INTEGER,
    max_rtt_ms      INTEGER,
    uptime_seconds  INTEGER NOT NULL,
    PRIMARY KEY (node_id, hour_start)
) STRICT;

CREATE INDEX node_health_hourly_time ON node_health_rollups_hourly(hour_start DESC);

-- Daily rollups of node health metrics (90-day retention)
CREATE TABLE node_health_rollups_daily (
    node_id         INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    day_start       INTEGER NOT NULL,
    samples         INTEGER NOT NULL,
    avg_load1       REAL NOT NULL,
    avg_mem_used    INTEGER NOT NULL,
    min_rtt_ms      INTEGER,
    avg_rtt_ms      INTEGER,
    max_rtt_ms      INTEGER,
    uptime_seconds  INTEGER NOT NULL,
    PRIMARY KEY (node_id, day_start)
) STRICT;

CREATE INDEX node_health_daily_time ON node_health_rollups_daily(day_start DESC);

-- Retention: 7 days for detailed node_health samples
-- +goose StatementBegin
CREATE TRIGGER node_health_cleanup
AFTER INSERT ON node_health
BEGIN
    DELETE FROM node_health
    WHERE at < (SELECT MAX(at) FROM node_health) - (7 * 86400);
END;
-- +goose StatementEnd

-- Retention: 90 days for hourly rollups
-- +goose StatementBegin
CREATE TRIGGER node_health_hourly_cleanup
AFTER INSERT ON node_health_rollups_hourly
BEGIN
    DELETE FROM node_health_rollups_hourly
    WHERE hour_start < (SELECT MAX(hour_start) FROM node_health_rollups_hourly) - (90 * 86400);
END;
-- +goose StatementEnd

-- Retention: 90 days for daily rollups
-- +goose StatementBegin
CREATE TRIGGER node_health_daily_cleanup
AFTER INSERT ON node_health_rollups_daily
BEGIN
    DELETE FROM node_health_rollups_daily
    WHERE day_start < (SELECT MAX(day_start) FROM node_health_rollups_daily) - (90 * 86400);
END;
-- +goose StatementEnd

-- Retention: 90 days for resolved alerts (active alerts never expire)
-- +goose StatementBegin
CREATE TRIGGER alerts_cleanup
AFTER INSERT ON alerts
BEGIN
    DELETE FROM alerts
    WHERE state = 'resolved' AND resolved_at < unixepoch() - (90 * 86400);
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER alerts_cleanup;
DROP TRIGGER node_health_daily_cleanup;
DROP TRIGGER node_health_hourly_cleanup;
DROP TRIGGER node_health_cleanup;
DROP TABLE node_health_rollups_daily;
DROP TABLE node_health_rollups_hourly;
DROP TABLE alerts;

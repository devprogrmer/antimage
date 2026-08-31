-- Let the alerts table hold the alert types the code already raises.
--
-- alerts.alert_type has carried this CHECK since 00015:
--
--     CHECK (alert_type IN ('cert_expiry', 'quota_warning', 'quota_exceeded'))
--
-- and the panel defines two more:
--
--     AlertTypeNodeOffline        = "node_offline"        (node_health.go)
--     AlertTypeEnforcementFailure = "enforcement_failure" (enforcement_failures.go)
--
-- Every attempt to raise either one was rejected by the constraint, so the
-- node-offline and enforcement-failure alerts had never fired. The failure was
-- invisible: sweep() logs the error and continues, so a panel with an offline
-- node looked exactly like a panel with none.
--
-- Both paths were ALSO selecting last_reconcile_at (and last_reconcile_error),
-- columns that exist in no migration, so they failed before reaching the insert.
-- Fixing only that would have moved the failure one step later rather than
-- fixing it, which is why the constraint is widened here in the same change.
--
-- SQLite cannot alter a CHECK, so the table is rebuilt. Every index from 00015
-- is recreated: the partial unique on dedup_key is what makes an alert
-- idempotent across sweeps, and losing it would let one condition accumulate a
-- row every five minutes forever.
--
-- alerts.id is a plain INTEGER PRIMARY KEY with no AUTOINCREMENT, so there is
-- no sqlite_sequence row to preserve here.

-- +goose Up

CREATE TABLE alerts_new (
    id              INTEGER PRIMARY KEY,
    alert_type      TEXT NOT NULL CHECK (alert_type IN (
                        'cert_expiry', 'quota_warning', 'quota_exceeded',
                        'node_offline', 'enforcement_failure')),
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

INSERT INTO alerts_new
    (id, alert_type, severity, target_type, target_id, state, dedup_key,
     first_seen_at, last_seen_at, resolved_at, threshold_value, current_value, metadata)
SELECT
     id, alert_type, severity, target_type, target_id, state, dedup_key,
     first_seen_at, last_seen_at, resolved_at, threshold_value, current_value, metadata
  FROM alerts;

DROP TABLE alerts;
ALTER TABLE alerts_new RENAME TO alerts;

CREATE UNIQUE INDEX alerts_dedup_active ON alerts(dedup_key) WHERE state = 'active';
CREATE INDEX alerts_active ON alerts(state, alert_type) WHERE state = 'active';
CREATE INDEX alerts_target ON alerts(target_type, target_id);
CREATE INDEX alerts_first_seen ON alerts(first_seen_at DESC);

-- DROP TABLE takes the table's triggers with it, so the 90-day retention
-- sweep from 00015 has to be recreated here or resolved alerts accumulate
-- forever. TestSP7RetentionTriggers is what catches its absence.
-- +goose StatementBegin
CREATE TRIGGER alerts_cleanup
AFTER INSERT ON alerts
BEGIN
    DELETE FROM alerts
    WHERE state = 'resolved' AND resolved_at < unixepoch() - (90 * 86400);
END;
-- +goose StatementEnd

-- +goose Down

-- Rows carrying the two new types cannot satisfy the old constraint, so they
-- are dropped rather than silently rewritten to a type they are not.
CREATE TABLE alerts_old (
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

INSERT INTO alerts_old
    (id, alert_type, severity, target_type, target_id, state, dedup_key,
     first_seen_at, last_seen_at, resolved_at, threshold_value, current_value, metadata)
SELECT
     id, alert_type, severity, target_type, target_id, state, dedup_key,
     first_seen_at, last_seen_at, resolved_at, threshold_value, current_value, metadata
  FROM alerts
 WHERE alert_type IN ('cert_expiry', 'quota_warning', 'quota_exceeded');

DROP TABLE alerts;
ALTER TABLE alerts_old RENAME TO alerts;

CREATE UNIQUE INDEX alerts_dedup_active ON alerts(dedup_key) WHERE state = 'active';
CREATE INDEX alerts_active ON alerts(state, alert_type) WHERE state = 'active';
CREATE INDEX alerts_target ON alerts(target_type, target_id);
CREATE INDEX alerts_first_seen ON alerts(first_seen_at DESC);

-- +goose StatementBegin
CREATE TRIGGER alerts_cleanup
AFTER INSERT ON alerts
BEGIN
    DELETE FROM alerts
    WHERE state = 'resolved' AND resolved_at < unixepoch() - (90 * 86400);
END;
-- +goose StatementEnd

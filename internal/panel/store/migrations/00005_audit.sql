-- +goose Up
CREATE TABLE audit_log (
    id             INTEGER PRIMARY KEY,
    at             INTEGER NOT NULL,
    actor_type     TEXT NOT NULL CHECK (actor_type IN ('admin','system','ctl')),
    actor_admin_id INTEGER REFERENCES admins(id),
    actor_label    TEXT NOT NULL DEFAULT '',
    actor_ip       TEXT NOT NULL DEFAULT '',
    request_id     TEXT NOT NULL DEFAULT '',
    action         TEXT NOT NULL,
    target_type    TEXT NOT NULL DEFAULT '',
    target_id      INTEGER,
    before_json    TEXT,
    after_json     TEXT,
    result         TEXT NOT NULL CHECK (result IN ('ok','denied','failed')),
    -- Invariant 8: system and ctl actors are first class; no synthetic admin rows.
    CHECK (actor_type <> 'admin' OR actor_admin_id IS NOT NULL)
) STRICT;

CREATE INDEX audit_log_at ON audit_log (at DESC);
CREATE INDEX audit_log_actor ON audit_log (actor_admin_id, at DESC);
CREATE INDEX audit_log_target ON audit_log (target_type, target_id, at DESC);

-- +goose Down
DROP TABLE audit_log;

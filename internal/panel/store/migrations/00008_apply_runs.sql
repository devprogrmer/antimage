-- +goose Up
CREATE TABLE node_apply_runs (
    id              INTEGER PRIMARY KEY,
    node_id         INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    target_revision INTEGER NOT NULL,
    started_at      INTEGER NOT NULL,
    finished_at     INTEGER,
    outcome         TEXT NOT NULL
                    CHECK (outcome IN ('converged','partial','deferred','failed','integrity'))
) STRICT;

CREATE INDEX node_apply_runs_node ON node_apply_runs (node_id, id DESC);

CREATE TABLE node_apply_steps (
    run_id      INTEGER NOT NULL REFERENCES node_apply_runs(id) ON DELETE CASCADE,
    seq         INTEGER NOT NULL,
    step_kind   TEXT NOT NULL,
    disruption  TEXT NOT NULL CHECK (disruption IN ('none','reload','restart','unknown')),
    outcome     TEXT NOT NULL CHECK (outcome IN ('ok','failed','skipped')),
    error       TEXT NOT NULL DEFAULT '',
    duration_ms INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (run_id, seq)
) STRICT;

CREATE TABLE node_health (
    node_id    INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    at         INTEGER NOT NULL,
    load1      REAL NOT NULL DEFAULT 0,
    mem_used   INTEGER NOT NULL DEFAULT 0,
    uptime_s   INTEGER NOT NULL DEFAULT 0,
    rtt_ms     INTEGER NOT NULL DEFAULT 0,
    adapter_status TEXT NOT NULL DEFAULT '[]',
    PRIMARY KEY (node_id, at)
) STRICT;

-- +goose Down
DROP TABLE node_health;
DROP TABLE node_apply_steps;
DROP TABLE node_apply_runs;

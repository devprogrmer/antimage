-- +goose Up
-- SP5 Phase B: Connection Quality Metrics
-- Track connection health, RTT, and reconciliation performance.

-- Add connection metrics columns to nodes table
ALTER TABLE nodes ADD COLUMN reconnect_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN last_reconcile_duration_ms INTEGER;
ALTER TABLE nodes ADD COLUMN failed_reconcile_streak INTEGER NOT NULL DEFAULT 0;

-- Track connection quality samples over time
CREATE TABLE connection_metrics (
    id              INTEGER PRIMARY KEY,
    node_id         INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    measured_at     INTEGER NOT NULL,
    rtt_ms          INTEGER,
    reconnect_reason TEXT
) STRICT;

CREATE INDEX connection_metrics_node ON connection_metrics(node_id);
CREATE INDEX connection_metrics_time ON connection_metrics(measured_at DESC);

-- Retention: keep last 7 days only (auto-cleanup on insert)
-- +goose StatementBegin
CREATE TRIGGER connection_metrics_cleanup
AFTER INSERT ON connection_metrics
BEGIN
    DELETE FROM connection_metrics
    WHERE measured_at < (SELECT MAX(measured_at) FROM connection_metrics) - (7 * 86400);
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER connection_metrics_cleanup;
DROP TABLE connection_metrics;
-- Note: SQLite doesn't support DROP COLUMN easily, added columns remain

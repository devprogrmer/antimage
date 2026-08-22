-- +goose Up
CREATE TABLE IF NOT EXISTS node_metrics (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id INTEGER NOT NULL,
    timestamp INTEGER NOT NULL,
    cpu_percent REAL,
    memory_used_bytes INTEGER,
    memory_total_bytes INTEGER,
    disk_used_bytes INTEGER,
    disk_total_bytes INTEGER,
    network_rx_bytes INTEGER,
    network_tx_bytes INTEGER,
    active_connections INTEGER,
    latency_ms INTEGER,
    FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_node_metrics_node_time ON node_metrics(node_id, timestamp DESC);

CREATE TABLE IF NOT EXISTS node_capabilities (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id INTEGER NOT NULL,
    protocol TEXT NOT NULL,
    available INTEGER NOT NULL DEFAULT 0,
    version TEXT,
    detected_at INTEGER NOT NULL,
    last_check_at INTEGER NOT NULL,
    UNIQUE(node_id, protocol),
    FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS node_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id INTEGER NOT NULL,
    event_type TEXT NOT NULL,
    timestamp INTEGER NOT NULL,
    details TEXT,
    admin_id INTEGER,
    severity TEXT NOT NULL DEFAULT 'info',
    FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE,
    FOREIGN KEY (admin_id) REFERENCES admins(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_node_events_node_time ON node_events(node_id, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_node_events_type ON node_events(event_type);

-- +goose Down
DROP TABLE IF EXISTS node_events;
DROP TABLE IF EXISTS node_capabilities;
DROP TABLE IF EXISTS node_metrics;

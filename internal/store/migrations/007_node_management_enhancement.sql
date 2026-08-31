-- Milestone 1: Node Management Database Schema Enhancement
-- Add health monitoring, protocol capabilities, and reconciliation tracking

-- Node health metrics (time-series data)
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
CREATE INDEX IF NOT EXISTS idx_node_metrics_timestamp ON node_metrics(timestamp DESC);

-- Node protocol capabilities (runtime detection)
CREATE TABLE IF NOT EXISTS node_capabilities (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id INTEGER NOT NULL,
    protocol TEXT NOT NULL, -- xray, singbox, wireguard, hysteria2, l2tp
    available INTEGER NOT NULL DEFAULT 0, -- 0=unavailable, 1=available
    version TEXT,
    detected_at INTEGER NOT NULL,
    last_check_at INTEGER NOT NULL,
    FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE,
    UNIQUE(node_id, protocol)
);

CREATE INDEX IF NOT EXISTS idx_node_capabilities_node ON node_capabilities(node_id);
CREATE INDEX IF NOT EXISTS idx_node_capabilities_protocol ON node_capabilities(protocol, available);

-- Node events (audit trail for node lifecycle)
CREATE TABLE IF NOT EXISTS node_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id INTEGER NOT NULL,
    event_type TEXT NOT NULL, -- heartbeat, sync_success, sync_failure, restart, maintenance_enter, maintenance_exit, state_change
    timestamp INTEGER NOT NULL,
    details TEXT, -- JSON with event-specific data
    admin_id INTEGER,
    severity TEXT NOT NULL DEFAULT 'info', -- info, warning, error, critical
    FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_node_events_node_time ON node_events(node_id, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_node_events_type ON node_events(event_type, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_node_events_severity ON node_events(severity, timestamp DESC);

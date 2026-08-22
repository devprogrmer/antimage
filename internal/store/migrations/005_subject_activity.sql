-- Add subject activity tracking for user history
CREATE TABLE IF NOT EXISTS subject_activity (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    subject_id INTEGER NOT NULL,
    event_type TEXT NOT NULL, -- connection_start/connection_end/traffic_update/quota_exceeded/disabled/enabled/created/deleted
    timestamp INTEGER NOT NULL,
    details TEXT, -- JSON with event-specific data
    ip_address TEXT,
    device_id TEXT,
    node_id INTEGER,
    bytes_up INTEGER DEFAULT 0,
    bytes_down INTEGER DEFAULT 0,
    FOREIGN KEY (subject_id) REFERENCES subjects(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_subject_activity_subject ON subject_activity(subject_id, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_subject_activity_timestamp ON subject_activity(timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_subject_activity_type ON subject_activity(event_type, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_subject_activity_node ON subject_activity(node_id, timestamp DESC);

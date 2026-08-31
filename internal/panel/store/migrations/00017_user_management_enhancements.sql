-- +goose Up
-- User management enhancements: device tracking, IP limits, connection limits, speed limiting
-- Part of P0 competitive parity work

-- Device tracking (HWID-based)
CREATE TABLE subject_devices (
    id              INTEGER PRIMARY KEY,
    subject_id      INTEGER NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
    -- Hardware identifier from client (could be UUID, MAC-based, or custom)
    hwid            TEXT NOT NULL,
    -- Human-readable name (e.g., "iPhone 13", "Work Laptop")
    name            TEXT NOT NULL DEFAULT '',
    -- First seen timestamp
    first_seen_at   INTEGER NOT NULL,
    -- Last connection timestamp
    last_seen_at    INTEGER NOT NULL,
    -- Last known IP address
    last_ip         TEXT NOT NULL DEFAULT '',
    -- User agent if available
    user_agent      TEXT NOT NULL DEFAULT '',
    -- Active if currently connected
    is_active       INTEGER NOT NULL DEFAULT 0 CHECK (is_active IN (0,1)),
    -- Revoked devices cannot reconnect
    revoked_at      INTEGER,
    revoked_reason  TEXT,
    UNIQUE (subject_id, hwid)
) STRICT;

CREATE INDEX subject_devices_subject ON subject_devices (subject_id);
CREATE INDEX subject_devices_active ON subject_devices (subject_id, is_active) WHERE is_active = 1;
CREATE INDEX subject_devices_hwid ON subject_devices (hwid);

-- Active connections tracking (for real-time enforcement)
CREATE TABLE active_connections (
    id              INTEGER PRIMARY KEY,
    subject_id      INTEGER NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
    device_id       INTEGER REFERENCES subject_devices(id) ON DELETE SET NULL,
    node_id         INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    -- Connection identifier (email/UUID from Xray, username from other protocols)
    connection_id   TEXT NOT NULL,
    -- Source IP address
    source_ip       TEXT NOT NULL,
    -- Connected at timestamp
    connected_at    INTEGER NOT NULL,
    -- Last heartbeat/activity timestamp
    last_seen_at    INTEGER NOT NULL,
    -- Protocol-specific info (JSON)
    protocol_info   TEXT NOT NULL DEFAULT '{}',
    UNIQUE (node_id, connection_id)
) STRICT;

CREATE INDEX active_connections_subject ON active_connections (subject_id);
CREATE INDEX active_connections_ip ON active_connections (subject_id, source_ip);
CREATE INDEX active_connections_device ON active_connections (device_id);
CREATE INDEX active_connections_cleanup ON active_connections (last_seen_at);

-- Extend subjects table with new limit fields
ALTER TABLE subjects ADD COLUMN max_devices INTEGER; -- NULL = unlimited
ALTER TABLE subjects ADD COLUMN max_ips INTEGER; -- NULL = unlimited
ALTER TABLE subjects ADD COLUMN max_connections INTEGER; -- NULL = unlimited
ALTER TABLE subjects ADD COLUMN speed_limit_up_kbps INTEGER; -- NULL = unlimited (kilobits per second)
ALTER TABLE subjects ADD COLUMN speed_limit_down_kbps INTEGER; -- NULL = unlimited

-- Index for device limit enforcement
CREATE INDEX subjects_device_limits ON subjects (max_devices) WHERE max_devices IS NOT NULL;

-- Connection audit log (for forensics and compliance)
CREATE TABLE connection_audit_log (
    id              INTEGER PRIMARY KEY,
    subject_id      INTEGER NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
    device_id       INTEGER REFERENCES subject_devices(id) ON DELETE SET NULL,
    node_id         INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    event_type      TEXT NOT NULL CHECK (event_type IN ('connect', 'disconnect', 'rejected', 'kicked')),
    source_ip       TEXT NOT NULL,
    rejection_reason TEXT, -- For rejected connections
    timestamp       INTEGER NOT NULL,
    -- Additional context (JSON)
    metadata        TEXT NOT NULL DEFAULT '{}'
) STRICT;

CREATE INDEX connection_audit_subject ON connection_audit_log (subject_id, timestamp);
CREATE INDEX connection_audit_event ON connection_audit_log (event_type, timestamp);
CREATE INDEX connection_audit_timestamp ON connection_audit_log (timestamp);

-- +goose Down
DROP TABLE connection_audit_log;
DROP TABLE active_connections;
DROP TABLE subject_devices;
-- Column drops require table rebuild on SQLite, omitted from Down for brevity

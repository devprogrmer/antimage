-- +goose Up
ALTER TABLE nodes ADD COLUMN maintenance_mode INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN maintenance_reason TEXT;
ALTER TABLE nodes ADD COLUMN maintenance_entered_at INTEGER;
ALTER TABLE nodes ADD COLUMN last_sync_at INTEGER;
ALTER TABLE nodes ADD COLUMN last_sync_error TEXT;
ALTER TABLE nodes ADD COLUMN config_drift INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN agent_version TEXT;
ALTER TABLE nodes ADD COLUMN os_info TEXT;

-- +goose Down
ALTER TABLE nodes DROP COLUMN os_info;
ALTER TABLE nodes DROP COLUMN agent_version;
ALTER TABLE nodes DROP COLUMN config_drift;
ALTER TABLE nodes DROP COLUMN last_sync_error;
ALTER TABLE nodes ADD COLUMN last_sync_at;
ALTER TABLE nodes DROP COLUMN maintenance_entered_at;
ALTER TABLE nodes DROP COLUMN maintenance_reason;
ALTER TABLE nodes DROP COLUMN maintenance_mode;

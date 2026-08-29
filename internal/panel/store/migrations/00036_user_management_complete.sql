-- +goose Up
-- User Management completeness: fields needed for Rebecca parity and production lifecycle.

-- Auto-delete after N days (Rebecca: auto_delete_in_days)
-- NULL = never auto-delete
ALTER TABLE subjects ADD COLUMN auto_delete_in_days INTEGER CHECK (auto_delete_in_days IS NULL OR auto_delete_in_days >= 0);

-- Data limit reset strategy: no_reset, daily, weekly, monthly, etc.
-- Default no_reset means quota is absolute, not periodic.
ALTER TABLE subjects ADD COLUMN data_limit_reset_strategy TEXT NOT NULL DEFAULT 'no_reset'
  CHECK (data_limit_reset_strategy IN ('no_reset','daily','weekly','monthly','yearly','on_hold'));

-- On-hold expire duration (seconds) — how long on_hold lasts
ALTER TABLE subjects ADD COLUMN on_hold_expire_duration INTEGER CHECK (on_hold_expire_duration IS NULL OR on_hold_expire_duration >= 0);

-- On-hold expires at — when on_hold status should expire
ALTER TABLE subjects ADD COLUMN on_hold_expires_at INTEGER;

-- Explicit status for on_hold and other lifecycle states
-- NULL means status is derived from enabled/expired/frozen as before.
-- 'on_hold' is the new state; others remain derived but stored for query speed.
ALTER TABLE subjects ADD COLUMN status TEXT CHECK (status IS NULL OR status IN ('active','limited','expired','disabled','on_hold'));

-- Lifetime used traffic — total ever used, not just current period
ALTER TABLE subjects ADD COLUMN lifetime_used_bytes INTEGER NOT NULL DEFAULT 0 CHECK (lifetime_used_bytes >= 0);

-- Telegram ID and contact number (Rebecca parity)
ALTER TABLE subjects ADD COLUMN telegram_id TEXT;
ALTER TABLE subjects ADD COLUMN contact_number TEXT;

-- Last online timestamp (for online/offline status)
ALTER TABLE subjects ADD COLUMN last_online_at INTEGER;

-- Online status cache (optional, for quick queries)
ALTER TABLE subjects ADD COLUMN is_online INTEGER NOT NULL DEFAULT 0 CHECK (is_online IN (0,1));

-- Owner admin ID cache for faster filtering (denormalized from reseller_subjects + admin)
-- NULL = platform-owned
ALTER TABLE subjects ADD COLUMN owner_admin_id INTEGER REFERENCES admins(id) ON DELETE SET NULL;

-- Service ID cache for primary service (for filtering, even though multi-service is supported)
ALTER TABLE subjects ADD COLUMN primary_service_id INTEGER REFERENCES services(id) ON DELETE SET NULL;

-- Indexes for new filters
CREATE INDEX subjects_auto_delete ON subjects (auto_delete_in_days) WHERE auto_delete_in_days IS NOT NULL;
CREATE INDEX subjects_reset_strategy ON subjects (data_limit_reset_strategy) WHERE data_limit_reset_strategy != 'no_reset';
CREATE INDEX subjects_status ON subjects (status) WHERE status IS NOT NULL;
CREATE INDEX subjects_owner ON subjects (owner_admin_id) WHERE owner_admin_id IS NOT NULL;
CREATE INDEX subjects_primary_service ON subjects (primary_service_id) WHERE primary_service_id IS NOT NULL;
CREATE INDEX subjects_last_online ON subjects (last_online_at) WHERE last_online_at IS NOT NULL;
CREATE INDEX subjects_online ON subjects (is_online) WHERE is_online = 1;

-- Backfill owner_admin_id from reseller_subjects -> resellers.admin_id
UPDATE subjects
SET owner_admin_id = (
  SELECT r.admin_id
  FROM reseller_subjects rs
  JOIN resellers r ON r.id = rs.reseller_id
  WHERE rs.subject_id = subjects.id
)
WHERE EXISTS (SELECT 1 FROM reseller_subjects WHERE subject_id = subjects.id);

-- Backfill primary_service_id from first service grant
UPDATE subjects
SET primary_service_id = (
  SELECT service_id FROM subject_services WHERE subject_id = subjects.id ORDER BY service_id LIMIT 1
)
WHERE EXISTS (SELECT 1 FROM subject_services WHERE subject_id = subjects.id);

-- +goose Down
DROP INDEX IF EXISTS subjects_online;
DROP INDEX IF EXISTS subjects_last_online;
DROP INDEX IF EXISTS subjects_primary_service;
DROP INDEX IF EXISTS subjects_owner;
DROP INDEX IF EXISTS subjects_status;
DROP INDEX IF EXISTS subjects_reset_strategy;
DROP INDEX IF EXISTS subjects_auto_delete;
ALTER TABLE subjects DROP COLUMN primary_service_id;
ALTER TABLE subjects DROP COLUMN owner_admin_id;
ALTER TABLE subjects DROP COLUMN is_online;
ALTER TABLE subjects DROP COLUMN last_online_at;
ALTER TABLE subjects DROP COLUMN contact_number;
ALTER TABLE subjects DROP COLUMN telegram_id;
ALTER TABLE subjects DROP COLUMN lifetime_used_bytes;
ALTER TABLE subjects DROP COLUMN status;
ALTER TABLE subjects DROP COLUMN on_hold_expires_at;
ALTER TABLE subjects DROP COLUMN on_hold_expire_duration;
ALTER TABLE subjects DROP COLUMN data_limit_reset_strategy;
ALTER TABLE subjects DROP COLUMN auto_delete_in_days;

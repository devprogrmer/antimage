-- +goose Up
-- SP3 accounting and quotas.
--
-- Design decisions are in docs/superpowers/specs/2026-08-19-sp3-design-decisions.md.
-- Every choice below is derived from observed Xray behavior, not from docs.

-- Raw per-poll accounting deltas. Kept briefly for forensics; hourly rollups
-- answer most operational questions without this volume.
CREATE TABLE usage_deltas (
    id          INTEGER PRIMARY KEY,
    node_id     INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    subject_id  INTEGER NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
    -- Monotonic per node. Idempotency key: (node_id, sequence).
    sequence    INTEGER NOT NULL,
    uplink_bytes   INTEGER NOT NULL CHECK (uplink_bytes >= 0),
    downlink_bytes INTEGER NOT NULL CHECK (downlink_bytes >= 0),
    created_at  INTEGER NOT NULL,
    UNIQUE (node_id, sequence)
) STRICT;

CREATE INDEX usage_deltas_subject ON usage_deltas (subject_id, created_at);
CREATE INDEX usage_deltas_created ON usage_deltas (created_at);

-- Hourly rollups. Kept indefinitely for billing history and charts.
CREATE TABLE usage_rollups_hourly (
    subject_id     INTEGER NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
    hour_start     INTEGER NOT NULL,
    uplink_bytes   INTEGER NOT NULL CHECK (uplink_bytes >= 0),
    downlink_bytes INTEGER NOT NULL CHECK (downlink_bytes >= 0),
    PRIMARY KEY (subject_id, hour_start)
) STRICT;

-- Daily rollups. Kept indefinitely for billing statements.
CREATE TABLE usage_rollups_daily (
    subject_id     INTEGER NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
    day_start      INTEGER NOT NULL,
    uplink_bytes   INTEGER NOT NULL CHECK (uplink_bytes >= 0),
    downlink_bytes INTEGER NOT NULL CHECK (downlink_bytes >= 0),
    PRIMARY KEY (subject_id, day_start)
) STRICT;

-- Extend subjects with quota fields (SP3 design decision 5).
-- Quota is one shared value per subject across all protocols and nodes.
ALTER TABLE subjects ADD COLUMN quota_bytes INTEGER;
ALTER TABLE subjects ADD COLUMN quota_used_bytes INTEGER NOT NULL DEFAULT 0 CHECK (quota_used_bytes >= 0);
-- Explicit timestamp, not a computed calendar rule. Idempotent, auditable.
ALTER TABLE subjects ADD COLUMN quota_reset_at INTEGER;
-- When frozen, who did it and why. Cleared on unfreeze.
ALTER TABLE subjects ADD COLUMN frozen_at INTEGER;
ALTER TABLE subjects ADD COLUMN frozen_reason TEXT;

-- Index for quota sweeper: find subjects at or over quota, not yet frozen.
CREATE INDEX subjects_quota_enforcement ON subjects (quota_used_bytes, frozen_at)
  WHERE quota_bytes IS NOT NULL;

-- Index for reset sweeper: find subjects past their reset time.
CREATE INDEX subjects_quota_reset ON subjects (quota_reset_at)
  WHERE quota_reset_at IS NOT NULL;

-- +goose Down
DROP TABLE usage_rollups_daily;
DROP TABLE usage_rollups_hourly;
DROP TABLE usage_deltas;
-- Column drops require table rebuild on SQLite, omitted from Down for brevity.

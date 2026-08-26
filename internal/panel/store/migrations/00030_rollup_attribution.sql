-- C3: give the rollups the dimensions the billable formula needs.
--
-- AD-2 defines
--
--     billable = raw * node_coef * service_coef * subject_coef * reseller_coef
--
-- and the plan has it computed at read time from the rollups. The rollups were
-- keyed by (subject_id, hour_start) alone, so two of those four factors had
-- nothing to apply to: a rollup row could not say which node or which service
-- earned its bytes. usage_deltas carries both, but PruneUsageDeltas clears it
-- after seven days, so beyond that window node_coef and service_coef were not
-- merely unapplied -- they were unrecoverable.
--
-- That made the formula impossible to implement as specified rather than merely
-- inconvenient, so the rollups gain the grain. C5 says no migration touches
-- them; C5 was written assuming raw aggregation sufficed, and it does not.
--
-- NULL means "nobody recorded which", exactly as in usage_deltas. Every row
-- that existed before this migration gets NULL for both, which is the true
-- statement about them -- the dimension was never captured. A reader treats a
-- missing dimension as x1.0, so historical usage bills exactly as it did
-- before and this migration changes no number.

-- +goose Up

CREATE TABLE usage_rollups_hourly_new (
    subject_id     INTEGER NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
    -- Nullable even though usage_deltas.node_id is NOT NULL: rows folded before
    -- this migration have no node, and inventing one would be a false
    -- attribution rather than a missing one.
    node_id        INTEGER REFERENCES nodes(id) ON DELETE SET NULL,
    service_id     INTEGER REFERENCES services(id) ON DELETE SET NULL,
    hour_start     INTEGER NOT NULL,
    uplink_bytes   INTEGER NOT NULL CHECK (uplink_bytes >= 0),
    downlink_bytes INTEGER NOT NULL CHECK (downlink_bytes >= 0)
) STRICT;

CREATE TABLE usage_rollups_daily_new (
    subject_id     INTEGER NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
    node_id        INTEGER REFERENCES nodes(id) ON DELETE SET NULL,
    service_id     INTEGER REFERENCES services(id) ON DELETE SET NULL,
    day_start      INTEGER NOT NULL,
    uplink_bytes   INTEGER NOT NULL CHECK (uplink_bytes >= 0),
    downlink_bytes INTEGER NOT NULL CHECK (downlink_bytes >= 0)
) STRICT;

-- History carries forward with both dimensions unknown.
INSERT INTO usage_rollups_hourly_new
    (subject_id, node_id, service_id, hour_start, uplink_bytes, downlink_bytes)
SELECT subject_id, NULL, NULL, hour_start, uplink_bytes, downlink_bytes
FROM usage_rollups_hourly;

INSERT INTO usage_rollups_daily_new
    (subject_id, node_id, service_id, day_start, uplink_bytes, downlink_bytes)
SELECT subject_id, NULL, NULL, day_start, uplink_bytes, downlink_bytes
FROM usage_rollups_daily;

DROP TABLE usage_rollups_hourly;
DROP TABLE usage_rollups_daily;
ALTER TABLE usage_rollups_hourly_new RENAME TO usage_rollups_hourly;
ALTER TABLE usage_rollups_daily_new RENAME TO usage_rollups_daily;

-- The identity of a rollup row, as an EXPRESSION index over COALESCE.
--
-- This is load-bearing and the reason neither table has a PRIMARY KEY. SQLite
-- treats NULLs as distinct in a unique index, so a plain
-- PRIMARY KEY (subject_id, node_id, service_id, hour_start) would never fire
-- ON CONFLICT for an unattributed row. The hourly fold would then INSERT a new
-- row on every run instead of merging into the existing one, and the daily
-- fold -- which overwrites with the group total rather than adding -- would
-- read those duplicates and multiply the day's traffic by however many rows had
-- accumulated.
--
-- That is the 168x inflation defect 00026 fixed, returning through a different
-- door and only for traffic nobody could attribute. COALESCE collapses NULL to
-- a value that compares equal, so the upsert merges.
CREATE UNIQUE INDEX usage_rollups_hourly_key ON usage_rollups_hourly (
    subject_id, hour_start, COALESCE(node_id, 0), COALESCE(service_id, 0)
);
CREATE UNIQUE INDEX usage_rollups_daily_key ON usage_rollups_daily (
    subject_id, day_start, COALESCE(node_id, 0), COALESCE(service_id, 0)
);

-- Reading one subject's bill over a period is the whole point of the table.
CREATE INDEX usage_rollups_hourly_subject ON usage_rollups_hourly (subject_id, hour_start);
CREATE INDEX usage_rollups_daily_subject ON usage_rollups_daily (subject_id, day_start);

-- +goose Down

-- Down collapses the dimensions away and keeps every byte. SUM is the only
-- merge that preserves the total; the attribution is what is given up, which is
-- precisely the state the table was in before this migration.
CREATE TABLE usage_rollups_hourly_old (
    subject_id     INTEGER NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
    hour_start     INTEGER NOT NULL,
    uplink_bytes   INTEGER NOT NULL CHECK (uplink_bytes >= 0),
    downlink_bytes INTEGER NOT NULL CHECK (downlink_bytes >= 0),
    PRIMARY KEY (subject_id, hour_start)
) STRICT;

INSERT INTO usage_rollups_hourly_old (subject_id, hour_start, uplink_bytes, downlink_bytes)
SELECT subject_id, hour_start, SUM(uplink_bytes), SUM(downlink_bytes)
FROM usage_rollups_hourly
GROUP BY subject_id, hour_start;

CREATE TABLE usage_rollups_daily_old (
    subject_id     INTEGER NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
    day_start      INTEGER NOT NULL,
    uplink_bytes   INTEGER NOT NULL CHECK (uplink_bytes >= 0),
    downlink_bytes INTEGER NOT NULL CHECK (downlink_bytes >= 0),
    PRIMARY KEY (subject_id, day_start)
) STRICT;

INSERT INTO usage_rollups_daily_old (subject_id, day_start, uplink_bytes, downlink_bytes)
SELECT subject_id, day_start, SUM(uplink_bytes), SUM(downlink_bytes)
FROM usage_rollups_daily
GROUP BY subject_id, day_start;

DROP INDEX IF EXISTS usage_rollups_hourly_subject;
DROP INDEX IF EXISTS usage_rollups_daily_subject;
DROP INDEX IF EXISTS usage_rollups_hourly_key;
DROP INDEX IF EXISTS usage_rollups_daily_key;
DROP TABLE usage_rollups_hourly;
DROP TABLE usage_rollups_daily;
ALTER TABLE usage_rollups_hourly_old RENAME TO usage_rollups_hourly;
ALTER TABLE usage_rollups_daily_old RENAME TO usage_rollups_daily;

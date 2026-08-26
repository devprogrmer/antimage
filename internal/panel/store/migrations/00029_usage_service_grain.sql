-- C2: widen the usage uniqueness grain to (node, sequence, subject, service).
--
-- 00027 already added usage_deltas.service_id and its index, ahead of the code
-- that would fill them. C2 is that code: the Xray adapter now carries the
-- service id in each user's tag, so a report can say which inbound earned the
-- traffic. This migration is what makes writing it safe.
--
-- Without it, C2 is data loss. A subject entitled to two inbounds on one node
-- produces TWO samples in a single report -- same node, same sequence, same
-- subject, different service -- and the key 00026 installed,
-- UNIQUE (node_id, sequence, subject_id), admits only one of them. The second
-- INSERT fails, and a failed INSERT fails the whole report.
--
-- That is the same defect 00026 fixed one level up, where UNIQUE
-- (node_id, sequence) admitted one row per report while a report carried many
-- subjects. Making the rows finer without making the key finer reintroduces it
-- exactly.
--
-- A rebuild rather than an ALTER because 00026 declared the grain as a
-- table-level UNIQUE constraint, which SQLite implements as an auto-named index
-- that cannot be dropped.

-- +goose Up

-- Stash the AUTOINCREMENT high-water mark before the old table goes.
--
-- This is load-bearing and easy to miss. A table rebuild does NOT carry
-- sqlite_sequence across: the copy sets it to MAX(id) of the rows copied, and
-- when the source table is EMPTY it is not set at all, so ids restart at 1.
--
-- An empty usage_deltas is the normal state, not an edge case -- PruneUsageDeltas
-- empties it for any node quiet longer than the seven-day retention window. The
-- rollup watermark is an id, so restarting beneath it would strand every future
-- delta from the rollups, permanently and silently. That is precisely what
-- 00026 added AUTOINCREMENT to prevent, and a naive rebuild reintroduces it at
-- the moment of migration.
CREATE TEMP TABLE usage_deltas_seq_stash AS
SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'usage_deltas'), 0) AS seq;

CREATE TABLE usage_deltas_new (
    -- AUTOINCREMENT preserved from 00026: the rollup watermark is an id, so
    -- ids must never go backwards.
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id     INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    subject_id  INTEGER NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
    -- Carried forward from 00027, definition unchanged.
    service_id  INTEGER REFERENCES services(id) ON DELETE SET NULL,
    -- Monotonic per node.
    sequence    INTEGER NOT NULL,
    uplink_bytes   INTEGER NOT NULL CHECK (uplink_bytes >= 0),
    downlink_bytes INTEGER NOT NULL CHECK (downlink_bytes >= 0),
    created_at  INTEGER NOT NULL,
    -- The grain, widened.
    --
    -- SQLite treats NULLs as distinct in a UNIQUE index, so unattributed rows
    -- never collide with each other. That is the safe direction and it is
    -- chosen, not inherited: making them collide (by indexing
    -- COALESCE(service_id,-1)) would turn a duplicate unattributed sample into
    -- a failed INSERT, and a failed INSERT fails the whole report. Losing a
    -- node's entire accounting to guard against one duplicate row is the worse
    -- trade -- the same reasoning that makes an unresolvable service id write
    -- NULL rather than fail.
    UNIQUE (node_id, sequence, subject_id, service_id)
) STRICT;

-- id and service_id both carried across explicitly: id because it is the rollup
-- watermark and renumbering would make the watermark meaningless, service_id
-- because 00027 may already hold attribution written by a newer node.
INSERT INTO usage_deltas_new (id, node_id, subject_id, service_id, sequence,
                              uplink_bytes, downlink_bytes, created_at)
SELECT id, node_id, subject_id, service_id, sequence,
       uplink_bytes, downlink_bytes, created_at
FROM usage_deltas;

DROP TABLE usage_deltas;
ALTER TABLE usage_deltas_new RENAME TO usage_deltas;

-- Restore the high-water mark. MAX of the stash and whatever the copy set, so
-- the counter never moves backwards regardless of which was higher.
INSERT INTO sqlite_sequence (name, seq)
SELECT 'usage_deltas', (SELECT seq FROM usage_deltas_seq_stash)
WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'usage_deltas');

UPDATE sqlite_sequence
   SET seq = MAX(seq, (SELECT seq FROM usage_deltas_seq_stash))
 WHERE name = 'usage_deltas';

DROP TABLE usage_deltas_seq_stash;

-- Every index the dropped table carried, including usage_deltas_service from
-- 00027. That one must exist by the end of this migration or 00027's own Down,
-- which drops it before dropping the column, would fail.
CREATE INDEX usage_deltas_subject ON usage_deltas (subject_id, created_at);
CREATE INDEX usage_deltas_created ON usage_deltas (created_at);
CREATE INDEX usage_deltas_service ON usage_deltas (service_id, created_at);

-- +goose Down

-- Back to 00026's grain, keeping the column and index 00027 owns so that
-- migration's Down still finds them.
--
-- Lossy in one place and it cannot be otherwise: two rows differing only by
-- service_id become duplicates under the narrower key. GROUP BY sums them,
-- which is the only merge that preserves the total, and attribution is what is
-- given up -- MIN(service_id) would be a lie about half the bytes, so the
-- collapsed row records NULL. The smallest id is kept so the watermark still
-- sees the row.
CREATE TEMP TABLE usage_deltas_seq_stash AS
SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'usage_deltas'), 0) AS seq;

CREATE TABLE usage_deltas_old (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id     INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    subject_id  INTEGER NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
    service_id  INTEGER REFERENCES services(id) ON DELETE SET NULL,
    sequence    INTEGER NOT NULL,
    uplink_bytes   INTEGER NOT NULL CHECK (uplink_bytes >= 0),
    downlink_bytes INTEGER NOT NULL CHECK (downlink_bytes >= 0),
    created_at  INTEGER NOT NULL,
    UNIQUE (node_id, sequence, subject_id)
) STRICT;

INSERT INTO usage_deltas_old (id, node_id, subject_id, service_id, sequence,
                              uplink_bytes, downlink_bytes, created_at)
SELECT MIN(id), node_id, subject_id,
       -- One surviving service id is only honest when there was only one.
       CASE WHEN COUNT(DISTINCT service_id) = 1 THEN MIN(service_id) ELSE NULL END,
       sequence,
       SUM(uplink_bytes), SUM(downlink_bytes), MIN(created_at)
FROM usage_deltas
GROUP BY node_id, sequence, subject_id;

DROP TABLE usage_deltas;
ALTER TABLE usage_deltas_old RENAME TO usage_deltas;

INSERT INTO sqlite_sequence (name, seq)
SELECT 'usage_deltas', (SELECT seq FROM usage_deltas_seq_stash)
WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'usage_deltas');

UPDATE sqlite_sequence
   SET seq = MAX(seq, (SELECT seq FROM usage_deltas_seq_stash))
 WHERE name = 'usage_deltas';

DROP TABLE usage_deltas_seq_stash;

CREATE INDEX usage_deltas_subject ON usage_deltas (subject_id, created_at);
CREATE INDEX usage_deltas_created ON usage_deltas (created_at);
CREATE INDEX usage_deltas_service ON usage_deltas (service_id, created_at);

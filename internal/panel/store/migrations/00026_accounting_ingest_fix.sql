-- Fixes the two ingest defects that made accounting wrong.
--
-- Both were confirmed by executing the code, not by reading it. Neither had
-- ever surfaced because every accounting test used a report carrying exactly
-- one subject and called each rollup exactly once, and production does neither.

-- +goose Up

-- DEFECT 1: UNIQUE (node_id, sequence) was enforced on the wrong grain.
--
-- IngestUsageReport inserts one row per SAMPLE under a single sequence, so a
-- report carrying two subjects collided on the second insert. The insert runs
-- inside one transaction, so the whole report was lost -- silently, because the
-- node had already advanced its counters. A node reports every user it serves
-- in one message, so this was the normal path on any node with two users.
--
-- The key is right in intent: (node_id, sequence) is what makes at-least-once
-- delivery exact. It just has to admit the several rows one report contains.
-- The report-level guard in IngestUsageReport is what actually rejects a replay;
-- this constraint is the backstop under it.
--
-- SQLite cannot alter a constraint, so this is the documented 12-step table
-- rebuild. Foreign keys are already off inside a goose migration transaction;
-- the rename below is what keeps referencing indexes consistent.

CREATE TABLE usage_deltas_new (
    id          INTEGER PRIMARY KEY,
    node_id     INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    subject_id  INTEGER NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
    -- Monotonic per node. One report carries many subjects, so the idempotency
    -- key admits one row per subject within a sequence and no more.
    sequence    INTEGER NOT NULL,
    uplink_bytes   INTEGER NOT NULL CHECK (uplink_bytes >= 0),
    downlink_bytes INTEGER NOT NULL CHECK (downlink_bytes >= 0),
    created_at  INTEGER NOT NULL,
    UNIQUE (node_id, sequence, subject_id)
) STRICT;

-- id is carried across explicitly. It is the rollup watermark below, so
-- renumbering rows here would make the watermark meaningless.
INSERT INTO usage_deltas_new (id, node_id, subject_id, sequence,
                              uplink_bytes, downlink_bytes, created_at)
SELECT id, node_id, subject_id, sequence, uplink_bytes, downlink_bytes, created_at
FROM usage_deltas;

DROP TABLE usage_deltas;
ALTER TABLE usage_deltas_new RENAME TO usage_deltas;

CREATE INDEX usage_deltas_subject ON usage_deltas (subject_id, created_at);
CREATE INDEX usage_deltas_created ON usage_deltas (created_at);

-- DEFECT 2: RollupHourly re-folded every unpruned delta on every run.
--
-- It selected WHERE created_at < ? with no lower bound and merged with
-- x = x + excluded.x. The sweeper runs it every hour; PruneUsageDeltas keeps
-- deltas for seven days. Each delta was therefore folded on the order of 168
-- times, and the rollup grew every hour for traffic that happened once.
--
-- The watermark is the missing lower bound: the highest delta id already folded.
-- id rather than created_at because id is assigned by the database and is
-- strictly monotonic, while created_at is supplied by the caller and can repeat
-- or arrive out of order -- a cutoff on it can step over a row forever.
CREATE TABLE usage_rollup_state (
    name          TEXT PRIMARY KEY,
    last_delta_id INTEGER NOT NULL DEFAULT 0
) STRICT;

-- Seed the watermark at the current maximum rather than at zero.
--
-- Zero would re-fold every surviving delta on the first run after this
-- migration, adding one more inflation on top of whatever is already there.
-- Starting at the maximum stops the bleeding without pretending to repair the
-- past: rollups written before this migration are already inflated by an
-- unknown factor, and the deltas that would be needed to recompute them have
-- been pruned. Recomputing what IS still recoverable is a separate, opt-in
-- operation -- see the repair command -- because deleting rollup rows is not
-- something a migration should do without being asked.
INSERT INTO usage_rollup_state (name, last_delta_id)
VALUES ('hourly', COALESCE((SELECT MAX(id) FROM usage_deltas), 0));

-- +goose Down

DROP TABLE IF EXISTS usage_rollup_state;

CREATE TABLE usage_deltas_old (
    id          INTEGER PRIMARY KEY,
    node_id     INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    subject_id  INTEGER NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
    sequence    INTEGER NOT NULL,
    uplink_bytes   INTEGER NOT NULL CHECK (uplink_bytes >= 0),
    downlink_bytes INTEGER NOT NULL CHECK (downlink_bytes >= 0),
    created_at  INTEGER NOT NULL,
    UNIQUE (node_id, sequence)
) STRICT;

-- Down is LOSSY and cannot be otherwise.
--
-- The old constraint admits one row per (node_id, sequence). Any report ingested
-- while the fix was live carries several, and they cannot all be kept. GROUP BY
-- collapses them into one row per sequence, summing the bytes so the totals
-- survive even though the per-subject attribution does not; subject_id keeps an
-- arbitrary member of the group, which is the honest outcome -- there is no
-- correct single subject for a row that represents several.
--
-- Reverting past this point therefore destroys attribution. It is a deployment
-- rollback, not an operational undo. Restore from backup instead if the data
-- matters; see docs/BACKUP-RESTORE.md.
INSERT INTO usage_deltas_old (id, node_id, subject_id, sequence,
                              uplink_bytes, downlink_bytes, created_at)
SELECT MIN(id), node_id, MIN(subject_id), sequence,
       SUM(uplink_bytes), SUM(downlink_bytes), MIN(created_at)
FROM usage_deltas
GROUP BY node_id, sequence;

DROP TABLE usage_deltas;
ALTER TABLE usage_deltas_old RENAME TO usage_deltas;

CREATE INDEX usage_deltas_subject ON usage_deltas (subject_id, created_at);
CREATE INDEX usage_deltas_created ON usage_deltas (created_at);

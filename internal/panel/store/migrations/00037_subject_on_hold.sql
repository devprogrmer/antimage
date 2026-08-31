-- on_hold: a subject whose validity period has not started yet.
--
-- The gap this closes is commercial, not technical. A reseller sells a 30-day
-- credential today and the customer sets it up next week; with expires_at fixed
-- at creation, the customer has already lost a week of what they paid for. Every
-- comparable panel offers this, and Antimage had no way to express it: expiry
-- was absolute from the moment of creation.
--
-- WHY A DURATION RATHER THAN A FLAG. "On hold" is not a boolean the panel can
-- act on -- the thing that has to survive until first use is HOW LONG the plan
-- runs once it starts. Storing the duration is what lets activation be a single
-- write (expires_at = now + on_hold_seconds), with no second table to consult
-- and no default to guess at. NULL means "not on hold", so an ordinary subject
-- is byte-identical to one created before this migration.
--
-- WHY NO status COLUMN. The obvious companion to on_hold is a stored status
-- enum, and it is deliberately absent. Entitlement is already decided by
-- enabled, frozen_at and expires_at -- columns that buildSubjects,
-- findSubjectsOverQuota and three sweepers all read directly in SQL. A stored
-- status would be a second source of truth for the same question, and this
-- codebase has already been bitten badly by exactly that: two quota-freeze
-- paths disagreed, and the one that ran first silently disabled the other.
-- Status is therefore DERIVED in one function (subjects.Subject.Status) from
-- the columns that already govern service, so it cannot drift from them.
--
-- An on-hold subject IS entitled to service. That is the whole mechanism: they
-- must be able to connect, because connecting is what starts the clock. With
-- expires_at NULL until activation, Subject.Active already returns true for
-- them and buildSubjects already includes them -- so no query changes here.

-- +goose Up

-- How long the plan runs once it starts, in seconds. NULL = not on hold.
-- Cleared at activation, which is what makes the state one-way: a subject that
-- has started cannot silently return to not-yet-started.
ALTER TABLE subjects ADD COLUMN on_hold_seconds INTEGER;

-- When the subject's effective status last changed.
--
-- The panel could say a subject was frozen and not when, so "it stopped working
-- on Tuesday" had nothing to check against. Nullable rather than defaulted to
-- created_at: for the rows that exist now nobody recorded a transition, and
-- inventing one would be a fact nobody established.
ALTER TABLE subjects ADD COLUMN status_changed_at INTEGER;

-- Activation is a write per subject on the usage-ingest path, which runs for
-- every node report. Without this the sweeperless activation check would scan
-- subjects on each ingest; with it, the common case (no subject on hold) is an
-- empty index probe.
CREATE INDEX subjects_on_hold ON subjects (on_hold_seconds)
    WHERE on_hold_seconds IS NOT NULL;

-- +goose Down

DROP INDEX subjects_on_hold;
ALTER TABLE subjects DROP COLUMN status_changed_at;
ALTER TABLE subjects DROP COLUMN on_hold_seconds;

-- C4: give the quota period a start and a length.
--
-- Quota is enforced per period, and the period had neither. subjects carried
-- only quota_reset_at -- when the period ENDS -- and the sweeper reconstructed
-- the rest from a constant it admitted was a placeholder:
--
--     // Determine the period: assume monthly (30 days) if quota is set.
--     // This is a simple default; a production system might store the period.
--     const defaultPeriodSeconds = 30 * 24 * 60 * 60
--
-- That was survivable while enforcement read a counter that the reset zeroed,
-- because the counter implicitly WAS the period. C4 enforces on billable, which
-- is computed from usage history over a window, and a window needs a start.
-- Without one there is nothing to compute "this period" from, and the whole
-- comparison is between a per-period counter and an all-time figure.
--
-- Both columns are nullable: a subject with no quota has no period, and
-- inventing one would be a fact nobody established.

-- +goose Up

-- When the current period began. Enforcement sums billable from here.
ALTER TABLE subjects ADD COLUMN quota_period_start INTEGER;

-- How long a period lasts, replacing the hardcoded 30 days.
--
-- Stored per subject rather than as a global setting because it is part of what
-- was sold: a weekly plan and a monthly plan are different products, and the
-- reset sweeper cannot tell them apart from a constant.
ALTER TABLE subjects ADD COLUMN quota_period_seconds INTEGER;

-- Backfill to exactly what the old code assumed, so this migration changes no
-- behaviour. Every subject with a reset time gets the 30 days the constant
-- meant, and a start derived from it.
--
-- The derived start is a RECONSTRUCTION, not a record -- nobody wrote down when
-- these periods began. It is the same assumption enforcement was already making
-- implicitly, so it is no less true than what it replaces, and from here on the
-- value is recorded rather than guessed.
UPDATE subjects
   SET quota_period_seconds = 2592000,
       quota_period_start   = quota_reset_at - 2592000
 WHERE quota_reset_at IS NOT NULL;

-- Enforcement scans subjects that have a quota; the period start is read for
-- each one it finds.
CREATE INDEX subjects_quota_period ON subjects (quota_period_start)
  WHERE quota_bytes IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS subjects_quota_period;
ALTER TABLE subjects DROP COLUMN quota_period_seconds;
ALTER TABLE subjects DROP COLUMN quota_period_start;

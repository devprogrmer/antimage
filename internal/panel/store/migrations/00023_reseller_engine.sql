-- +goose Up
-- Reseller engine: multi-tenant commercial layer.
--
-- This layer sits entirely above subjects. It touches no node-side code and no
-- gRPC: a reseller provisioning a user is an ordinary subject creation that
-- happens to be paid for and owned. That containment is deliberate -- the
-- commercial layer must not be able to destabilise the control plane.
--
-- The central decision is that CREDIT IS A LEDGER, NOT A BALANCE COLUMN.
-- A mutable balance cannot answer "why is this reseller at 40 credits", cannot
-- be audited after the fact, and corrupts permanently under a lost update. The
-- same reasoning produced usage_deltas in SP3.

CREATE TABLE resellers (
    id            INTEGER PRIMARY KEY,
    -- One reseller per admin account. The admin row carries authentication;
    -- this row carries commerce. Splitting them keeps auth logic unaware of
    -- billing and lets a reseller be disabled without deleting their login.
    admin_id      INTEGER NOT NULL UNIQUE REFERENCES admins(id) ON DELETE CASCADE,
    display_name  TEXT NOT NULL COLLATE NOCASE,
    enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),

    -- Hard ceilings, enforced independently of credit. Credit limits how much
    -- a reseller can sell; these limit how much they can sell AT ONCE, which
    -- is what stops one tenant exhausting a node.
    max_subjects    INTEGER CHECK (max_subjects IS NULL OR max_subjects >= 0),
    max_quota_bytes INTEGER CHECK (max_quota_bytes IS NULL OR max_quota_bytes >= 0),

    -- Whether this reseller may run a negative balance, and by how much.
    -- Post-paid resellers are a real business model; the default is pre-paid.
    credit_floor  INTEGER NOT NULL DEFAULT 0,

    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
) STRICT;

CREATE UNIQUE INDEX resellers_display_name_unique
    ON resellers (display_name COLLATE NOCASE);

-- Append-only credit ledger. Balance is SUM(delta) and is never stored.
--
-- Nothing in the codebase may UPDATE or DELETE a row here. Corrections are new
-- rows with reason='adjustment', which is what makes the history defensible
-- when a reseller disputes a charge.
CREATE TABLE reseller_credit_ledger (
    id           INTEGER PRIMARY KEY,
    reseller_id  INTEGER NOT NULL REFERENCES resellers(id) ON DELETE CASCADE,

    -- Signed. Positive credits the reseller, negative debits them. Integer
    -- micro-units, never a float: binary floating point cannot represent
    -- money, and a rounding error in a ledger is unrecoverable.
    delta        INTEGER NOT NULL CHECK (delta <> 0),

    reason       TEXT NOT NULL CHECK (reason IN
                    ('topup','provision','renew','refund','adjustment')),

    -- The subject this movement paid for, when there is one. SET NULL rather
    -- than CASCADE: deleting a customer must not erase the record that they
    -- were charged for.
    subject_id   INTEGER REFERENCES subjects(id) ON DELETE SET NULL,

    -- Who caused it. A top-up is performed by a super-admin; a provision is
    -- performed by the reseller themselves.
    actor_admin_id INTEGER REFERENCES admins(id) ON DELETE SET NULL,

    note         TEXT NOT NULL DEFAULT '',

    -- Idempotency. A retried top-up after an ambiguous network failure must
    -- not double-credit. Callers pass a client-generated key; the UNIQUE
    -- constraint makes the second attempt a no-op rather than free money.
    idempotency_key TEXT NOT NULL,

    at           INTEGER NOT NULL,
    UNIQUE (reseller_id, idempotency_key)
) STRICT;

-- Balance is computed by summing a reseller's rows, so that is the index.
CREATE INDEX reseller_ledger_balance ON reseller_credit_ledger (reseller_id, id);
CREATE INDEX reseller_ledger_subject ON reseller_credit_ledger (subject_id)
    WHERE subject_id IS NOT NULL;

-- Ownership. A subject belongs to at most one reseller; a subject with no row
-- here is owned directly by the platform.
CREATE TABLE reseller_subjects (
    subject_id  INTEGER PRIMARY KEY REFERENCES subjects(id) ON DELETE CASCADE,

    -- RESTRICT, not CASCADE: deleting a reseller who still has live customers
    -- must fail loudly. Cascading would silently orphan paying users, and the
    -- subjects would keep being served with nobody accountable for them.
    reseller_id INTEGER NOT NULL REFERENCES resellers(id) ON DELETE RESTRICT,

    -- What the reseller was charged at provisioning time. Recorded here as
    -- well as in the ledger so a refund knows what to return without
    -- re-deriving it from pricing that may since have changed.
    cost        INTEGER NOT NULL CHECK (cost >= 0),

    created_at  INTEGER NOT NULL
) STRICT;

CREATE INDEX reseller_subjects_owner ON reseller_subjects (reseller_id);

-- +goose Down
DROP INDEX IF EXISTS reseller_subjects_owner;
DROP TABLE IF EXISTS reseller_subjects;
DROP INDEX IF EXISTS reseller_ledger_subject;
DROP INDEX IF EXISTS reseller_ledger_balance;
DROP TABLE IF EXISTS reseller_credit_ledger;
DROP INDEX IF EXISTS resellers_display_name_unique;
DROP TABLE IF EXISTS resellers;

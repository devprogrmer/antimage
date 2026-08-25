-- Phase C part 1: attribution and coefficients. Implements AD-2.
--
-- This migration adds columns and reads nothing. Every value it writes is a
-- default chosen so that the system computes exactly what it computed before,
-- which is what makes it safe to ship ahead of the code that uses it and cheap
-- to revert. The coefficient READER lands separately; see the rollout note in
-- docs/premium/PHASE-C-ACCOUNTING-PLAN.md.

-- +goose Up

-- Part 1 of AD-2: attribute usage to a service.
--
-- Nullable, because historical rows cannot be back-attributed and inventing an
-- attribution would corrupt the ledger. NULL is a true statement -- "recorded
-- before attribution existed" -- and any default would be a false one.
--
-- ON DELETE SET NULL rather than CASCADE, for the reason the credit ledger uses
-- it: deleting an inbound must not erase the record that traffic was billed
-- through it. CASCADE here would let removing a service silently reduce a
-- customer's recorded usage.
ALTER TABLE usage_deltas
    ADD COLUMN service_id INTEGER REFERENCES services(id) ON DELETE SET NULL;

CREATE INDEX usage_deltas_service ON usage_deltas (service_id, created_at);

-- Part 2 of AD-2: coefficients, one per level the spec names.
--
-- Integer basis points: 10000 = x1.0. Never floats. The credit ledger already
-- refuses floats for money and says why; billable traffic is money. Basis points
-- keep x0.0001 resolution in exact integers, and the product of four of them is
-- exact rather than accumulating representation error across a billing period.
--
-- DEFAULT 10000 is what makes this migration behaviour-preserving: every
-- existing row bills at x1.0, so nothing changes until an operator sets one.
ALTER TABLE nodes     ADD COLUMN usage_coefficient INTEGER NOT NULL DEFAULT 10000;
ALTER TABLE services  ADD COLUMN usage_coefficient INTEGER NOT NULL DEFAULT 10000;
ALTER TABLE subjects  ADD COLUMN usage_coefficient INTEGER NOT NULL DEFAULT 10000;
ALTER TABLE resellers ADD COLUMN usage_coefficient INTEGER NOT NULL DEFAULT 10000;

-- +goose Down
--
-- Structurally clean, semantically lossy, and the distinction matters.
--
-- Down restores the previous schema exactly. It does not restore data: dropping
-- service_id discards attribution recorded while the fix was live, and dropping
-- the coefficient columns discards operator configuration with no record that
-- anything other than x1.0 was ever in force.
--
-- So this is safe as a DEPLOYMENT rollback and is not an operational undo. It is
-- lossless only while every coefficient is still 10000 and no attribution has
-- been written -- which is exactly the window this migration is designed to sit
-- in, before the reader ships. Past that point the recovery path is a restore
-- from backup; see docs/BACKUP-RESTORE.md.
--
-- The preflight in `antimage-panel accounting verify` reports whether that line
-- has been crossed.
ALTER TABLE resellers DROP COLUMN usage_coefficient;
ALTER TABLE subjects  DROP COLUMN usage_coefficient;
ALTER TABLE services  DROP COLUMN usage_coefficient;
ALTER TABLE nodes     DROP COLUMN usage_coefficient;
DROP INDEX IF EXISTS usage_deltas_service;
ALTER TABLE usage_deltas DROP COLUMN service_id;

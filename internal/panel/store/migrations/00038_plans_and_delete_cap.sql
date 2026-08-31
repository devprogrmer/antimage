-- Two controls that make selling through resellers safe: plans, and a cap on
-- what a reseller may delete.

-- +goose Up

-- PLANS.
--
-- user_presets already carried everything a plan needs -- name, quota_bytes,
-- validity_days, auto_assign_services_json -- with full CRUD, routes and a
-- management screen. Nothing had ever APPLIED one to a subject, so the table
-- was a catalogue of products that could not be sold. Applying them is the
-- work; the only column missing is whether the plan starts on first use.
--
-- Boolean rather than a duration: the duration is already validity_days. A
-- plan is "30 days" either way, and what differs is when the count begins.
ALTER TABLE user_presets ADD COLUMN on_hold INTEGER NOT NULL DEFAULT 0
    CHECK (on_hold IN (0,1));

-- DELETE CAP.
--
-- A reseller is billed on the traffic their customers carry. Without this, the
-- cheapest way to avoid a bill is to delete the customer before settlement --
-- the usage rows cascade with the subject, and the evidence goes with them.
--
-- Per admin rather than per role: the cap is a commercial trust level that
-- differs between two resellers holding identical permissions, and encoding it
-- in the role would force a new role per credit limit.
--
-- NULL means no cap, which is what every existing admin gets: this must not
-- silently start refusing deletions on upgrade. A cap of 0 is a real setting
-- meaning "may not delete a customer who has used anything at all".
ALTER TABLE admins ADD COLUMN delete_cap_bytes INTEGER;

-- +goose Down

ALTER TABLE admins DROP COLUMN delete_cap_bytes;
ALTER TABLE user_presets DROP COLUMN on_hold;

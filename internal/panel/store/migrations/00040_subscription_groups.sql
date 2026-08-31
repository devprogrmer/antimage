-- Subscription groups: a named, reusable answer to "which of this customer's
-- inbounds should their subscription actually contain".
--
-- Today a subscription carries everything the subject is granted. That is the
-- right default and the wrong only option, for two reasons that come up
-- constantly in this product:
--
--   1. Clients differ. A customer on a v2rayNG build with no Hysteria2 support
--      gets entries it cannot use, and picks one at random when the list
--      rotates. Handing them a link limited to what their client speaks is the
--      difference between "it works" and "it works sometimes".
--   2. Tiers differ. The same node set is often sold twice -- a cheap plan on
--      one protocol, a premium plan on several -- and the only thing separating
--      them is what the subscription hands out.
--
-- WHY A TABLE RATHER THAN A COLUMN ON subjects. The selection is reused across
-- many customers and changes as a unit: adding a protocol to a tier should not
-- mean editing every subject sold on it. A per-subject list would also have no
-- name, and an operator cannot reason about "the protocols on subject 412".
--
-- WHY PROTOCOLS AND NOT SERVICE IDS. A group naming service ids would break
-- every time an inbound is recreated -- which the studio's clone flow does
-- routinely -- and would have to be edited per node. Protocols are stable
-- across nodes and are what the customer's client actually cares about.

-- +goose Up

CREATE TABLE subscription_groups (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE COLLATE NOCASE,
    description TEXT NOT NULL DEFAULT '',

    -- A JSON array of protocol names: ["vless","trojan"]. An EMPTY array means
    -- "every protocol", not "no protocols".
    --
    -- That choice is deliberate and is the safer of the two. A group created
    -- with nothing selected yet, or one whose protocols were all removed,
    -- should hand the customer their whole entitlement rather than silently
    -- cut them off -- an over-broad subscription is a support question, an
    -- empty one is an outage. The UI states which it is rather than leaving
    -- the operator to infer it from an empty box.
    protocols_json TEXT NOT NULL DEFAULT '[]',

    -- Ownership follows the same rule as user_presets and service_templates:
    -- public, or the admin's own. Reusing the rule means a reseller cannot
    -- read a competitor's tier definition by guessing an id.
    is_public INTEGER NOT NULL DEFAULT 0 CHECK (is_public IN (0,1)),
    created_by INTEGER REFERENCES admins(id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE INDEX subscription_groups_creator ON subscription_groups(created_by);

-- ON DELETE SET NULL, not CASCADE. Deleting a group must not delete the
-- customers sold on it; they fall back to receiving everything they are
-- granted, which is the pre-group behaviour and is recoverable. Cascading
-- would turn "remove a tier" into "delete its customers".
ALTER TABLE subjects ADD COLUMN subscription_group_id INTEGER
    REFERENCES subscription_groups(id) ON DELETE SET NULL;

CREATE INDEX subjects_subscription_group ON subjects(subscription_group_id)
    WHERE subscription_group_id IS NOT NULL;

-- +goose Down

DROP INDEX subjects_subscription_group;
ALTER TABLE subjects DROP COLUMN subscription_group_id;
DROP INDEX subscription_groups_creator;
DROP TABLE subscription_groups;

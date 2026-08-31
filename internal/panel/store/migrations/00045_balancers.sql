-- Balancers: named pools of outbounds a routing rule can select instead of
-- one fixed outbound, which document schema v5 carries.
--
-- Node-scoped and independently addressable like outbounds and routing
-- rules, not a single JSON blob like dns_config: an operator adds, edits,
-- and deletes individual balancers the same way they manage outbounds.

-- +goose Up

CREATE TABLE balancers (
    id      INTEGER PRIMARY KEY,
    node_id INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,

    -- Tag is what a routing rule references. UNIQUE per node for the same
    -- reason outbounds.tag is: a duplicate is not an override, and the
    -- adapter refuses it at render time rather than silently picking one.
    tag TEXT NOT NULL,

    -- Outbound tag PREFIXES this balancer picks among, stored as a JSON
    -- array. Xray's own matching is prefix-based, not exact.
    selector TEXT NOT NULL DEFAULT '[]',

    -- random needs no live data; least_ping does, which is what causes the
    -- adapter to emit an observatory block probing this balancer's own
    -- selector -- see xray/balancer.go.
    strategy TEXT NOT NULL DEFAULT 'random' CHECK (strategy IN ('random', 'least_ping')),

    -- A disabled balancer is omitted from the document rather than deleted,
    -- the same reasoning outbounds.enabled and routing_rules.enabled use.
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),

    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,

    UNIQUE (node_id, tag)
) STRICT;

CREATE INDEX balancers_node ON balancers (node_id);

-- The routing rule's second possible target, alongside the existing
-- outbound_tag. Exactly one of the two is non-empty on any given row --
-- enforced at the application layer (validateRoutingRule), the same place
-- "at least one matcher" already is, rather than as a CHECK spanning two
-- columns that ALTER TABLE ADD COLUMN cannot express here.
ALTER TABLE routing_rules ADD COLUMN balancer_tag TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE routing_rules DROP COLUMN balancer_tag;
DROP INDEX IF EXISTS balancers_node;
DROP TABLE IF EXISTS balancers;

-- Egress: the outbounds and routing rules that document schema v3 carries.
--
-- Both are NODE-SCOPED. An outbound is a path off one specific host, and a
-- routing rule selects between the outbounds that host has, so neither is
-- meaningful without a node. This mirrors services, which are node-scoped for
-- the same reason.

-- +goose Up

CREATE TABLE outbounds (
    id       INTEGER PRIMARY KEY,
    node_id  INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,

    -- Tag is what a routing rule references, and what the adapter writes into
    -- the proxy's own configuration.
    --
    -- UNIQUE per node because a duplicate tag is not an override: Xray resolves
    -- duplicates by first match, so a second outbound sharing a tag would
    -- silently never be used. The adapters refuse this at render time too; the
    -- constraint means an operator is told at the API instead of discovering it
    -- when a rule quietly does nothing.
    tag      TEXT NOT NULL,

    -- Kind is the document's vocabulary -- direct, block, socks, http,
    -- wireguard -- not any one proxy's. The adapter maps it: Xray calls direct
    -- "freedom", sing-box calls it "direct".
    kind     TEXT NOT NULL,

    -- Adapter-specific, validated against the adapter's published
    -- OutboundSchema before it is ever stored. The panel holds no protocol
    -- knowledge of its own.
    params   TEXT NOT NULL DEFAULT '{}',

    -- A disabled outbound is omitted from the document rather than deleted, so
    -- an operator can take an egress path out of service without losing its
    -- configuration or the rules that name it.
    enabled  INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),

    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,

    UNIQUE (node_id, tag)
) STRICT;

CREATE INDEX outbounds_node ON outbounds (node_id);

CREATE TABLE routing_rules (
    id      INTEGER PRIMARY KEY,
    node_id INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,

    -- Evaluation order, lowest first. Ties break on id, which the document
    -- builder applies, so two rules sharing a priority still serialise
    -- deterministically and the node's hash settles.
    priority INTEGER NOT NULL DEFAULT 0,

    -- Matchers, each a JSON array. Stored as JSON rather than as child tables
    -- because they are read and written only as a whole rule: no query filters
    -- on "rules matching this one domain", so normalising them would buy
    -- nothing and cost a join per rule on every document build.
    domains      TEXT NOT NULL DEFAULT '[]',
    ip_cidrs     TEXT NOT NULL DEFAULT '[]',
    geoip        TEXT NOT NULL DEFAULT '[]',
    geosite      TEXT NOT NULL DEFAULT '[]',
    ports        TEXT NOT NULL DEFAULT '[]',
    inbound_tags TEXT NOT NULL DEFAULT '[]',
    subject_ids  TEXT NOT NULL DEFAULT '[]',

    -- Scalar matcher: tcp, udp, or empty for both.
    network TEXT NOT NULL DEFAULT '' CHECK (network IN ('', 'tcp', 'udp')),

    -- The outbound this rule selects, by tag.
    --
    -- Deliberately NOT a foreign key to outbounds(tag): a rule may legally
    -- reference a tag the adapter provides rather than the panel, and Xray's
    -- accounting configuration supplies "direct" without any row here. The
    -- adapters refuse a rule naming a tag the node does not have, which is the
    -- check that can see both sources.
    outbound_tag TEXT NOT NULL,

    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),

    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE INDEX routing_rules_node ON routing_rules (node_id, priority, id);

-- Where unmatched traffic goes. Empty means the proxy's own default applies.
--
-- A column on nodes rather than a table: there is exactly one per node, and a
-- single-row-per-node table would be a more expensive way to say the same
-- thing.
ALTER TABLE nodes ADD COLUMN default_outbound_tag TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE nodes DROP COLUMN default_outbound_tag;
DROP INDEX IF EXISTS routing_rules_node;
DROP TABLE IF EXISTS routing_rules;
DROP INDEX IF EXISTS outbounds_node;
DROP TABLE IF EXISTS outbounds;

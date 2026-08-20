-- +goose Up
-- SP5 Phase A: Adapter Registry
-- Track adapter versions and capabilities per node.
CREATE TABLE adapter_registry (
    id              INTEGER PRIMARY KEY,
    node_id         INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    kind            TEXT NOT NULL,
    version         TEXT NOT NULL,
    capabilities    TEXT NOT NULL DEFAULT '[]', -- JSON array
    reported_at     INTEGER NOT NULL,
    UNIQUE(node_id, kind)
) STRICT;

CREATE INDEX adapter_registry_node ON adapter_registry(node_id);
CREATE INDEX adapter_registry_kind ON adapter_registry(kind);

-- +goose Down
DROP TABLE adapter_registry;

-- +goose Up
CREATE TABLE deployments (
    id              INTEGER PRIMARY KEY,
    revision_id     INTEGER NOT NULL,
    strategy        TEXT NOT NULL CHECK (strategy IN ('all_at_once','canary','staged','rolling')),
    status          TEXT NOT NULL CHECK (status IN ('pending','validating','in_progress','completed','failed','rolled_back')),
    created_by      INTEGER NOT NULL REFERENCES admins(id),
    created_at      INTEGER NOT NULL,
    started_at      INTEGER,
    completed_at    INTEGER,
    error           TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX deployments_revision ON deployments (revision_id);
CREATE INDEX deployments_status ON deployments (status, created_at DESC);

CREATE TABLE deployment_node_status (
    deployment_id   INTEGER NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    node_id         INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    status          TEXT NOT NULL CHECK (status IN ('pending','applying','completed','failed','rolled_back','skipped')),
    started_at      INTEGER,
    completed_at    INTEGER,
    error           TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (deployment_id, node_id)
) STRICT;

CREATE INDEX deployment_node_status_deployment ON deployment_node_status (deployment_id, status);

CREATE TABLE deployment_validations (
    id              INTEGER PRIMARY KEY,
    revision_id     INTEGER NOT NULL,
    validated_by    INTEGER NOT NULL REFERENCES admins(id),
    validated_at    INTEGER NOT NULL,
    is_valid        INTEGER NOT NULL CHECK (is_valid IN (0, 1)),
    conflicts       TEXT NOT NULL DEFAULT '[]',
    warnings        TEXT NOT NULL DEFAULT '[]'
) STRICT;

CREATE INDEX deployment_validations_revision ON deployment_validations (revision_id, validated_at DESC);

-- +goose Down
DROP TABLE deployment_validations;
DROP TABLE deployment_node_status;
DROP TABLE deployments;

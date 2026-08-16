-- +goose Up
CREATE TABLE node_revisions (
    node_id        INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    revision       INTEGER NOT NULL CHECK (revision > 0),
    created_at     INTEGER NOT NULL,
    actor_type     TEXT NOT NULL CHECK (actor_type IN ('admin','system','ctl')),
    actor_admin_id INTEGER REFERENCES admins(id),
    actor_label    TEXT NOT NULL DEFAULT '',
    reason         TEXT NOT NULL DEFAULT '',
    doc_sha256     TEXT NOT NULL,
    PRIMARY KEY (node_id, revision),
    CHECK (actor_type <> 'admin' OR actor_admin_id IS NOT NULL)
) STRICT;

-- +goose StatementBegin
-- Invariant 10: revisions increase by exactly one, with no gaps or reuse.
CREATE TRIGGER node_revisions_monotonic
BEFORE INSERT ON node_revisions
FOR EACH ROW
WHEN NEW.revision <> 1 + COALESCE(
        (SELECT MAX(revision) FROM node_revisions WHERE node_id = NEW.node_id), 0)
BEGIN
    SELECT RAISE(ABORT, 'node_revisions: revision must be exactly max(revision)+1');
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER node_revisions_monotonic;
DROP TABLE node_revisions;

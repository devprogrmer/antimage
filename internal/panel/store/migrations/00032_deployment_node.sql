-- A deployment must record which node it deployed.
--
-- deployments.revision_id holds the node's REVISION NUMBER, and node_revisions
-- is keyed (node_id, revision) -- revisions start at 1 and count up per node.
-- So revision_id = 3 belongs to every node that has reached three, and a
-- deployment row did not identify a node at all.
--
-- That is not only a reporting gap. Nothing downstream could be scoped:
-- GET /deployments could not filter to the caller's nodes because there was no
-- node to filter on, and rollback could not check which node it was about to
-- change. The authorization fix in this change depends on this column existing.

-- +goose Up

-- Nullable, because for existing rows the answer is genuinely unknown and
-- inventing one would attribute a change to a node that never had it.
--
-- ON DELETE CASCADE matches node_revisions: the deployment history of a node
-- that no longer exists is not history anybody can act on, and the alternative
-- is a growing set of rows pointing at nothing.
ALTER TABLE deployments
    ADD COLUMN node_id INTEGER REFERENCES nodes(id) ON DELETE CASCADE;

-- One case IS recoverable. On a panel with exactly one node, every deployment
-- was that node's -- there was nowhere else for it to go. Anything else stays
-- NULL, and a NULL is excluded from a scoped list rather than shown to
-- everybody: an unattributable deployment is exactly the row a scoped caller
-- must not be handed.
UPDATE deployments
   SET node_id = (SELECT id FROM nodes)
 WHERE node_id IS NULL
   AND (SELECT COUNT(*) FROM nodes) = 1;

-- Listing a node's deployments is the query this exists to serve.
CREATE INDEX deployments_node ON deployments (node_id, created_at);

-- +goose Down
DROP INDEX IF EXISTS deployments_node;
ALTER TABLE deployments DROP COLUMN node_id;

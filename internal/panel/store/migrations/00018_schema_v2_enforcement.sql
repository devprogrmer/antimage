-- +goose Up
-- Schema version 2: Update document hashes for new Subject fields
-- User Management Enhancements added enforcement policies to Subject

-- Mark all node revisions as needing recomputation
-- The control plane will regenerate hashes on next desired state fetch
UPDATE node_revisions SET doc_sha256 = '' WHERE doc_sha256 != '';

-- +goose Down
-- Hashes will be recomputed on next fetch, no explicit rollback needed

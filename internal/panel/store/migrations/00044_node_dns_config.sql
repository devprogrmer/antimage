-- DNS: the servers, static host overrides, and fake-IP pools that document
-- schema v4 carries.
--
-- One JSON column, not a table: there is exactly one DNS config per node
-- (like default_outbound_tag), and unlike outbounds or routing rules it is
-- edited and read as a single whole-object form, never as independently
-- addressable rows. tags_json on this same table already established the
-- pattern of a single-node-scoped JSON blob for exactly this reason.

-- +goose Up

ALTER TABLE nodes ADD COLUMN dns_config TEXT NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE nodes DROP COLUMN dns_config;

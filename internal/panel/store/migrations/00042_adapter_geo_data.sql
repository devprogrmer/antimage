-- Tracks when an adapter's geo database (geoip.dat/geosite.dat) was last
-- refreshed through the panel's geo-update command, and what it verified to.
--
-- WHY HERE. adapter_registry is already the per-node-per-kind row Hello
-- populates (version, capabilities, reported_at); geo data is the same
-- shape of fact -- one adapter, one node, "what do we currently have" --
-- and a separate table would need its own node_id+kind uniqueness to mean
-- anything, duplicating what this table already enforces.
--
-- WHY NOT POPULATED FROM Hello. Hello reports what the agent's process
-- knows about ITSELF (version, capabilities); geo data is a file on disk
-- that Xray reads once at startup and never reports back on its own. The
-- panel is the one that knows an update happened, because it is the one
-- that sent the command and read the AgentCommandResult -- so these columns
-- are written from the HTTP handler that processes that result, not from
-- the Hello handler.
--
-- All three nullable: a row inserted by Hello (every adapter, on first
-- connect) has never had a geo update and should say so plainly rather than
-- fabricate a timestamp for something that never happened.

-- +goose Up

ALTER TABLE adapter_registry ADD COLUMN geo_updated_at INTEGER;
ALTER TABLE adapter_registry ADD COLUMN geo_geoip_sha256 TEXT;
ALTER TABLE adapter_registry ADD COLUMN geo_geosite_sha256 TEXT;

-- +goose Down

ALTER TABLE adapter_registry DROP COLUMN geo_geosite_sha256;
ALTER TABLE adapter_registry DROP COLUMN geo_geoip_sha256;
ALTER TABLE adapter_registry DROP COLUMN geo_updated_at;

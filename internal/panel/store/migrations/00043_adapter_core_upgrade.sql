-- Tracks when the panel last pushed a core-version upgrade through the
-- command channel, and what the agent reported installing.
--
-- The LIVE version is already visible via adapter_registry.version, updated
-- from every Hello -- an upgraded adapter reports its new version on its
-- very next reconnect regardless of this column. What that column cannot
-- say is WHEN a change happened or whether it went through the panel's own
-- upgrade command at all versus, say, an operator upgrading the OS package
-- by hand outside antimage entirely. This column answers "when did *this
-- panel* last push one", the same relationship RecordGeoUpdate already has
-- to geo_updated_at.

-- +goose Up

ALTER TABLE adapter_registry ADD COLUMN core_upgraded_at INTEGER;

-- +goose Down

ALTER TABLE adapter_registry DROP COLUMN core_upgraded_at;

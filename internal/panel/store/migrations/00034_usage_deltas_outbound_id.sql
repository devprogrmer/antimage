-- +goose Up
-- Add outbound_id to usage_deltas for full attribution (Phase F, §27)
--
-- Extends usage attribution with the outbound dimension, completing the
-- 5-factor billable computation:
--   billable = raw × node × service × subject × reseller × outbound
--
-- Also adds outbound_id to usage_rollups_hourly and _daily for consistency.
--
-- Nullable because:
--   1. Existing rows have no outbound_id (recorded before this migration)
--   2. Protocols without outbound support (WireGuard, Hysteria2, L2TP) send NULL
--   3. Single-outbound configurations have no ambiguity even with NULL

-- Add to deltas table
ALTER TABLE usage_deltas ADD COLUMN outbound_id INTEGER REFERENCES outbounds(id) ON DELETE SET NULL;

-- Add to rollup tables (for consistent grouping)
ALTER TABLE usage_rollups_hourly ADD COLUMN outbound_id INTEGER REFERENCES outbounds(id) ON DELETE SET NULL;
ALTER TABLE usage_rollups_daily ADD COLUMN outbound_id INTEGER REFERENCES outbounds(id) ON DELETE SET NULL;

-- Index for the billable query join
CREATE INDEX IF NOT EXISTS idx_usage_deltas_outbound_id ON usage_deltas(outbound_id) WHERE outbound_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_usage_rollups_hourly_outbound_id ON usage_rollups_hourly(outbound_id) WHERE outbound_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_usage_rollups_daily_outbound_id ON usage_rollups_daily(outbound_id) WHERE outbound_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_usage_rollups_daily_outbound_id;
DROP INDEX IF EXISTS idx_usage_rollups_hourly_outbound_id;
DROP INDEX IF EXISTS idx_usage_deltas_outbound_id;
ALTER TABLE usage_rollups_daily DROP COLUMN outbound_id;
ALTER TABLE usage_rollups_hourly DROP COLUMN outbound_id;
ALTER TABLE usage_deltas DROP COLUMN outbound_id;

-- +goose Up
-- Outbound coefficients (Phase F, §27)
--
-- Extends the Phase C accounting model with a fifth coefficient dimension:
--   billable = raw × node_coef × service_coef × subject_coef × reseller_coef × outbound_coef
--
-- Basis points (10000 = ×1.0), same as existing coefficient columns.
-- Defaults to 10000 so existing traffic is unaffected.

ALTER TABLE outbounds ADD COLUMN usage_coefficient INTEGER NOT NULL DEFAULT 10000 CHECK(usage_coefficient >= 0);

-- +goose Down
ALTER TABLE outbounds DROP COLUMN usage_coefficient;

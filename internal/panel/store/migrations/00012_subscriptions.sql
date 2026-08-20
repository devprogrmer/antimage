-- SP4: Subscription delivery
-- Add subscription_token column to subjects table for subscription URL authentication

-- +goose Up
ALTER TABLE subjects ADD COLUMN subscription_token TEXT NOT NULL DEFAULT '';

-- Index for fast token lookup (sparse index: only non-empty tokens)
-- Unique constraint prevents token collisions
CREATE UNIQUE INDEX idx_subjects_subscription_token
  ON subjects(subscription_token)
  WHERE subscription_token != '';

-- +goose Down
DROP INDEX IF EXISTS idx_subjects_subscription_token;
ALTER TABLE subjects DROP COLUMN subscription_token;

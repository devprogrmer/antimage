-- +goose Up
-- totp_pending_enc holds a secret that has been generated and shown to the
-- admin but not yet proven. Keeping it server-side means the confirm step
-- never accepts a secret from the client: anything holding a session could
-- otherwise pin a secret it already knows and silently own the second factor.
ALTER TABLE admins ADD COLUMN totp_pending_enc BLOB;

CREATE TABLE admin_recovery_codes (
    id          INTEGER PRIMARY KEY,
    admin_id    INTEGER NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    -- argon2id, same cost as a password. Never the plaintext: the codes are
    -- shown exactly once, at confirm time, and are unrecoverable afterwards.
    code_hash   TEXT NOT NULL,
    created_at  INTEGER NOT NULL,
    -- NULL while spendable. Set, never cleared, when a code is used.
    consumed_at INTEGER
) STRICT;

CREATE INDEX admin_recovery_codes_admin ON admin_recovery_codes (admin_id);

-- +goose Down
DROP TABLE admin_recovery_codes;
ALTER TABLE admins DROP COLUMN totp_pending_enc;

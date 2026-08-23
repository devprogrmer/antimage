-- +goose Up
-- Telegram account linking.
--
-- A linked Telegram account is a STANDING CREDENTIAL: it survives password
-- changes, and Telegram accounts are lost to SIM-swap attacks. The schema is
-- shaped around that fact -- links are revocable, auditable, and checked on
-- every command rather than cached.
--
-- Credentials are never accepted in chat. Telegram retains history on their
-- servers and syncs it across a user's devices, so a password typed to a bot
-- is a password in someone else's cloud. Linking instead uses a one-time code
-- issued from an already-authenticated panel session.

CREATE TABLE telegram_links (
    -- Telegram's own user id. Set by Telegram's servers, not by the client,
    -- so it cannot be forged inside a genuine update.
    telegram_id  INTEGER PRIMARY KEY,

    admin_id     INTEGER NOT NULL REFERENCES admins(id) ON DELETE CASCADE,

    -- Denormalised for display in the panel's linked-accounts list, so
    -- showing who is linked does not require a Telegram API call.
    username     TEXT NOT NULL DEFAULT '',

    linked_at    INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,

    -- Soft revocation. The row is kept so an incident review can see that an
    -- account WAS linked and when it was cut off; a deleted row proves
    -- nothing. Checked on every command, never cached.
    revoked_at   INTEGER,

    -- One Telegram account per admin, and (via the primary key) one admin per
    -- Telegram account. A shared bot identity would make every audit record
    -- ambiguous about who actually acted.
    UNIQUE (admin_id)
) STRICT;

CREATE INDEX telegram_links_admin ON telegram_links (admin_id);
CREATE INDEX telegram_links_active ON telegram_links (telegram_id)
    WHERE revoked_at IS NULL;

-- One-time linking codes.
--
-- Stored HASHED, following sessions.token_hash: read access to the database
-- must not yield working link codes. The plaintext exists only in the panel
-- response that issued it and in the user's Telegram message.
CREATE TABLE telegram_link_codes (
    code_hash   BLOB PRIMARY KEY,

    admin_id    INTEGER NOT NULL REFERENCES admins(id) ON DELETE CASCADE,

    -- Short TTL. A code is typed from a screen into a chat within a minute or
    -- two; anything longer is a window with no legitimate use.
    expires_at  INTEGER NOT NULL,

    -- Single use. Set on redemption rather than deleting the row, so a replay
    -- can be distinguished from a code that never existed.
    consumed_at INTEGER,
    consumed_by INTEGER,   -- the telegram_id that redeemed it

    created_at  INTEGER NOT NULL
) STRICT;

CREATE INDEX telegram_link_codes_admin ON telegram_link_codes (admin_id);
CREATE INDEX telegram_link_codes_expiry ON telegram_link_codes (expires_at)
    WHERE consumed_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS telegram_link_codes_expiry;
DROP INDEX IF EXISTS telegram_link_codes_admin;
DROP TABLE IF EXISTS telegram_link_codes;
DROP INDEX IF EXISTS telegram_links_active;
DROP INDEX IF EXISTS telegram_links_admin;
DROP TABLE IF EXISTS telegram_links;

-- +goose Up
CREATE TABLE roles (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    is_builtin  INTEGER NOT NULL DEFAULT 0,
    permissions TEXT NOT NULL             -- JSON array of permission keys
) STRICT;

CREATE TABLE admins (
    id              INTEGER PRIMARY KEY,
    username        TEXT NOT NULL COLLATE NOCASE,
    password_hash   TEXT NOT NULL,
    role_id         INTEGER NOT NULL REFERENCES roles(id),
    parent_admin_id INTEGER REFERENCES admins(id),
    status          TEXT NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active','suspended')),
    totp_secret_enc BLOB,
    created_at      INTEGER NOT NULL
) STRICT;

CREATE UNIQUE INDEX admins_username_unique ON admins (username COLLATE NOCASE);

CREATE TABLE admin_scopes (
    admin_id   INTEGER NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    scope_type TEXT NOT NULL CHECK (scope_type IN ('node','service')),
    scope_id   INTEGER NOT NULL,
    PRIMARY KEY (admin_id, scope_type, scope_id)
) STRICT;

CREATE TABLE sessions (
    id           INTEGER PRIMARY KEY,
    admin_id     INTEGER NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    token_hash   BLOB NOT NULL UNIQUE,
    ip           TEXT NOT NULL,
    user_agent   TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL,
    revoked_at   INTEGER
) STRICT;

CREATE INDEX sessions_admin ON sessions (admin_id);

-- +goose Down
DROP TABLE sessions;
DROP TABLE admin_scopes;
DROP TABLE admins;
DROP TABLE roles;

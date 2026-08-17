-- +goose Up
CREATE TABLE enroll_tokens (
    token_hash BLOB PRIMARY KEY,
    node_id    INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL,
    used_at    INTEGER,
    created_at INTEGER NOT NULL
) STRICT;

CREATE TABLE panel_ca (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    cert_der    BLOB NOT NULL,
    key_sealed  BLOB NOT NULL,   -- AES-256-GCM under the master key
    created_at  INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE panel_ca;
DROP TABLE enroll_tokens;

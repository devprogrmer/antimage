-- +goose Up
CREATE TABLE login_attempts (
    id        INTEGER PRIMARY KEY,
    kind      TEXT NOT NULL CHECK (kind IN ('account','ip')),
    subject   TEXT NOT NULL,
    failed_at INTEGER NOT NULL
) STRICT;

CREATE INDEX login_attempts_lookup ON login_attempts (kind, subject, failed_at);

-- +goose Down
DROP TABLE login_attempts;

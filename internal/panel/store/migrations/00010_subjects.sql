-- +goose Up
-- Subjects are the people (or devices) a node serves. SP2 introduces them; the
-- desired document has carried an empty `subjects` array since SP1.
CREATE TABLE subjects (
    id          INTEGER PRIMARY KEY,
    -- Stable handle an operator recognises and SP3 aggregates traffic by.
    -- COLLATE NOCASE on the column, not just the index: the column collation
    -- is what makes comparisons case-insensitive (learned in SP1 task 5).
    name        TEXT NOT NULL COLLATE NOCASE,
    enabled     INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    -- NULL means no expiry. Enforced panel-side by omission from the desired
    -- document; see the SP2 decision record.
    expires_at  INTEGER,
    created_at  INTEGER NOT NULL,
    -- Set when the expiry sweeper disables the subject, so a re-enable is
    -- distinguishable from one that never expired.
    expired_at  INTEGER,
    note        TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE UNIQUE INDEX subjects_name_unique ON subjects (name COLLATE NOCASE);
CREATE INDEX subjects_expiry ON subjects (expires_at) WHERE expires_at IS NOT NULL;

-- Which services a subject may use. A subject with no grants appears on no
-- node, which is what makes "created but not yet provisioned" representable.
CREATE TABLE subject_services (
    subject_id INTEGER NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
    service_id INTEGER NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    PRIMARY KEY (subject_id, service_id)
) STRICT;

CREATE INDEX subject_services_service ON subject_services (service_id);

-- Credential material, sealed with AES-256-GCM under the master key before it
-- reaches this table (SP2 decision 1). A leaked database without the key
-- yields nothing.
CREATE TABLE subject_credentials (
    id          INTEGER PRIMARY KEY,
    subject_id  INTEGER NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
    -- 'uuid' for VLESS/VMess, 'password' for Trojan/Shadowsocks.
    kind        TEXT NOT NULL CHECK (kind IN ('uuid','password')),
    value_enc   BLOB NOT NULL,
    -- Incremented on rotation so one credential can be replaced without
    -- touching any other, which pure derivation could not do.
    rotation    INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    UNIQUE (subject_id, kind)
) STRICT;

-- +goose Down
DROP TABLE subject_credentials;
DROP TABLE subject_services;
DROP TABLE subjects;

-- What each node's adapters can do, as the node itself reports it.
--
-- adapter_registry already recorded kind, version and capabilities from Hello.
-- The Hello message also carries each adapter's ServiceSchema and its
-- capability flags, and the panel discarded them -- so the only schema it could
-- validate against was the one it had been COMPILED with, in
-- nodes.KnownAdapters().
--
-- That difference decides whether an editor can be honest. Compiled-in
-- knowledge says what this build of the panel understands; a node's Hello says
-- what that host can actually execute. Offering an operator a protocol the
-- panel knows and the node does not produces a service that can never be
-- applied, which is the failure AD-3 names.

-- +goose Up

-- The JSON Schema this adapter publishes for its service params.
--
-- Nullable rather than defaulted: an adapter that has not reported one is a
-- different thing from one that reports an empty schema, and only the first
-- should make a protocol unofferable. Older agents predate this field and send
-- nothing, so their rows stay NULL and the panel falls back to what it knows.
ALTER TABLE adapter_registry ADD COLUMN service_schema TEXT;

-- Capability flags, per NODE rather than per adapter type.
--
-- hot_user_add especially: whether Xray can add a user without a restart
-- depends on that host having configured the management API, so the same
-- adapter kind legitimately differs between two nodes. Storing it per row is
-- the point, not an accident of the schema.
--
-- Defaulted to 0 because "not reported" and "not capable" lead to the same
-- safe behaviour here: plan the restart. Unlike the schema above, being
-- conservative costs a restart rather than hiding a protocol.
ALTER TABLE adapter_registry ADD COLUMN hot_user_add INTEGER NOT NULL DEFAULT 0;
ALTER TABLE adapter_registry ADD COLUMN self_accounting INTEGER NOT NULL DEFAULT 0;
ALTER TABLE adapter_registry ADD COLUMN requires_pki INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE adapter_registry DROP COLUMN requires_pki;
ALTER TABLE adapter_registry DROP COLUMN self_accounting;
ALTER TABLE adapter_registry DROP COLUMN hot_user_add;
ALTER TABLE adapter_registry DROP COLUMN service_schema;

-- Proxy Hub: credentials for third-party proxy/VPN providers (Cloudflare
-- WARP, NordVPN today) that an operator registers once and then uses to
-- provision real outbounds on any node, as many times as they like.
--
-- Not node-scoped, unlike outbounds themselves: a WARP device or a NordVPN
-- token is an account-level credential the operator owns, independent of
-- which node(s) end up routing traffic through it. Provisioning an outbound
-- FROM an account is a separate action (see the outbounds table) that
-- references this table only to read credentials at provisioning time.

-- +goose Up

CREATE TABLE proxy_provider_accounts (
    id       INTEGER PRIMARY KEY,

    -- Which provider this account belongs to. A fixed, closed set: adding a
    -- provider is a code change (a new client + rendering path), not
    -- something an operator can freeform.
    provider TEXT NOT NULL CHECK (provider IN ('warp', 'nordvpn')),

    -- Operator-chosen name, so a fleet with several WARP devices or NordVPN
    -- tokens can tell them apart in a picker.
    label TEXT NOT NULL,

    -- Sealed (AES-256-GCM, the same box that protects outbound params and
    -- the CA key) JSON blob: WireGuard private key, provider access
    -- token/device id, whatever that provider's registration returned.
    -- Never returned by any read endpoint -- see proxyhub.go.
    credentials TEXT NOT NULL,

    -- Non-secret, safe-to-display info about the account: for WARP, device
    -- type and whether a license is attached; for NordVPN, nothing beyond
    -- what's already in label today, but kept for symmetry and future use
    -- without another migration.
    metadata TEXT NOT NULL DEFAULT '{}',

    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE IF EXISTS proxy_provider_accounts;

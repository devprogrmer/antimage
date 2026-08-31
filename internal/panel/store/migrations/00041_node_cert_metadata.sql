-- Node certificate metadata, so the panel can answer "when does this expire?"
--
-- The panel is the CA for the whole fleet: it signs every node certificate and
-- it is the only verifier of them. What it did NOT do is remember anything
-- about what it signed. nodes.cert_fingerprint held the SHA-256 and nothing
-- else -- no expiry, no serial -- because the fingerprint is all the mTLS gate
-- in control/server.go needs to decide whether a caller is allowed in.
--
-- That is sufficient for admission and useless for operations. NodeCertLifetime
-- is 365 days, so every enrolled node has an expiry a year out that nobody can
-- see and nothing warns about. The failure it produces is quiet and total: the
-- certificate lapses, VerifyPeer stops matching, and the node drops off the
-- control plane looking like a network fault. An operator investigating that
-- has no way, from the panel, to discover the real cause.
--
-- WHY THESE ARE NULLABLE. Nodes enrolled before this migration were signed by a
-- certificate the panel did not keep, and it cannot be recovered -- the panel
-- retains the fingerprint, not the DER. Backfilling would mean computing an
-- expiry from an enrolment date and a lifetime constant, which produces a
-- confident-looking date that nobody actually issued. NULL says "not recorded",
-- and the certificates API reports those rows as unknown rather than inventing
-- a deadline an operator might plan around. They become known on re-enrolment.
--
-- WHY NOT STORE THE CERTIFICATE ITSELF. Nothing needs it. The agent holds the
-- only copy that matters, verification is a fingerprint comparison, and
-- revocation clears the fingerprint rather than publishing a CRL. Keeping the
-- DER would add a blob per node to serve a question nobody asks.

-- +goose Up

-- Unix seconds at which the node's current certificate stops being valid.
-- NULL = never recorded (pre-migration enrolment) or no certificate at all.
ALTER TABLE nodes ADD COLUMN cert_not_after INTEGER;

-- The certificate's serial number, hex. Not used for verification -- the
-- fingerprint does that -- but it is what an operator matches against when
-- reading the agent's own logs or an openssl dump on the host.
ALTER TABLE nodes ADD COLUMN cert_serial TEXT;

-- +goose Down

ALTER TABLE nodes DROP COLUMN cert_serial;
ALTER TABLE nodes DROP COLUMN cert_not_after;

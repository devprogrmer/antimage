# Security policy

## Reporting a vulnerability

Report privately to the maintainer through GitHub's **Report a vulnerability**
button on the repository's Security tab, which opens a private advisory. Do not
open a public issue for a security problem.

Please include the version or commit, what an attacker can do with it, and a
reproduction if you have one. Expect an acknowledgement within a few days.

## Supported versions

| Version | Supported |
|---|---|
| 0.1.x (SP1) | ✅ |
| anything earlier | ❌ pre-release, never published |

## Threat model

antimage assumes:

- The **panel host is trusted**. It holds the master key, the CA private key,
  and the database. Compromise of the panel host is total compromise.
- **Managed nodes are semi-trusted.** A node holds only its own certificate and
  its own desired state. A compromised node cannot read another node's
  configuration, cannot mint enrolment tokens, and cannot escalate to the panel.
- **The network between them is hostile.** All control traffic is mutually
  authenticated; agents pin the panel's CA rather than trusting a public trust
  store.
- **Operators are semi-trusted and scoped.** A `reseller` is expected to be
  restricted to their own nodes, and that restriction is enforced in SQL as well
  as in handlers.

Out of the model: a malicious panel administrator, physical access to the panel
host, and side channels in the host kernel or hypervisor.

## Design decisions with security consequences

**Node private keys never leave the node.** The agent generates its keypair
locally and sends only a CSR. The panel never sees a node private key.

**Certificate pinning, not web PKI.** Agents verify the panel against a pinned
CA fingerprint in `node.yaml`. A hijacked DNS record or a mis-issued public
certificate yields nothing. A config without `ca_fingerprint` is **refused at
startup** rather than falling back to the system trust store.

**Revocation is an allow-list, not a CRL.** The panel is the only verifier, so
it accepts a connection only when the presented fingerprint matches
`nodes.cert_fingerprint`. Deleting a node locks it out immediately, with no CRL
to distribute and no OCSP responder to run.

**Opaque sessions, not JWTs.** Session tokens are random and server-side; only
their SHA-256 is stored. Revocation therefore takes effect on the next request
rather than at token expiry. The SSE stream re-validates its session on every
tick using a path that deliberately does **not** refresh `last_used_at`, so an
open browser tab cannot hold a session alive past the idle timeout.

**Secrets are sealed outside the database.** TOTP secrets and the CA private key
are encrypted with AES-256-GCM under a master key held in a 0600 file or an
environment variable. A leaked database backup yields neither.

**SSH credentials are never persisted.** There is no table, no column, and no
serialisation tag for them; the type is wiped in place after use. Storing them
would make the panel database a root-key vault for the entire fleet, so one
panel compromise would become total fleet compromise.

**Fail closed on the second factor.** When an admin has TOTP enrolled and the
panel cannot verify a code — no master key, an unopenable secret, a wrong code —
it **denies**. A panel that cannot check the factor must not decide the factor
passed.

**Downloads use an allow-list and a rooted open.** The `/download/{name}`
endpoint matches names against a fixed map rather than sanitising them, and
opens files with `os.OpenInRoot`, which refuses to leave the directory even
through a symlink. Both layers are tested for what each uniquely contributes.

**Enrolment tokens are single-use, short-lived, and redacted.** Stored hashed,
30-minute expiry, burned on use, and stripped from SSH bootstrap output before
it reaches the audit log.

**The audit log cannot be erased by deleting its subject.** `audit_log` has no
foreign key to `nodes`, so removing a node leaves its trail intact.

## Hardening checklist for operators

- Put a TLS terminator in front of `:8080`. Expose `:8443` **directly** — the
  panel serves mTLS there itself and a terminator would break client
  certificate verification.
- Create the `antimage` system user; the packaged unit runs as it and will not
  start otherwise.
- Keep `/var/lib/antimage` at mode 0700 and `node.yaml` at 0600.
- **Back up `master.key` separately from the database.** Storing them together
  defeats the reason the key lives outside the database.
- Pass `--ca-fingerprint` to `install.sh` from an out-of-band channel. Omitting
  it falls back to trust on first use, which a hijacked DNS record can defeat.
- Enrol TOTP for every account that can write.
- Restart the panel at least every 90 days so its server certificate is
  reissued.
- Review `GET /api/v1/audit` for `authz.deny` entries; they record scope probing.

## Known gaps in this release

Stated plainly because an operator needs them when threat-modelling a
deployment:

- **Node certificate auto-renewal is not implemented.** Certificates last a
  year; renewal at the halfway mark is designed but not built. A node whose
  certificate expires must be re-enrolled.
- **TOTP codes are not single-use.** A code remains valid across the ±30-second
  skew window and can be replayed inside it.
- **The audit view is not scope-filtered.** Any holder of `audit:read` sees all
  rows, including other admins' login IP addresses. The `reseller` role does not
  hold that permission.
- **No global policy to require TOTP for super admins.** Deliberately deferred:
  such a policy can lock every super admin out with no route back except
  `antimage-ctl`, and it needs a designed escape hatch first.
- **`readOnlyMiddleware` keys off the role name**, not a permission. It is
  defence in depth only; real enforcement is the `rbac.Check` in each handler.
- **Down migrations are untested.** Roll back by restoring a backup.
- **The race detector does not run on every developer machine.** CI runs
  `go test ./... -race`; local runs without a C toolchain do not.

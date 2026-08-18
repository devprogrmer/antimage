# Changelog

All notable changes to this project are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
While the major version is `0`, the API and the on-disk schema may change in a
minor release.

## [0.1.0] — 2026-08-18

First release. **SP1 — the control-plane spine.**

This delivers the foundation an operator needs to run a fleet: authentication,
authorization, audit, the node registry, mTLS enrolment, bootstrap, the adapter
contract, health, and the UI shell. The only adapter that ships is a stub used
to prove convergence end to end. See [Known limitations](README.md#known-limitations)
for what is deliberately absent.

### Added

**Control plane**
- Desired-state reconciliation over an agent-dialled gRPC stream, so nodes need
  no inbound port and an offline node self-heals when it returns.
- Revision model with RFC 8785 canonical serialisation and SHA-256 hashing;
  `applied_revision` advances only on verified convergence.
- Integrity detection: a revision match with a hash mismatch is a fault, never
  convergence, and the status is sticky until an operator sees it.
- Per-step apply reports carrying step kind, disruption level, duration, and
  stderr.
- Offline sweeper marking nodes offline after three missed heartbeats.
- Adapter contract with `Observe` / `Plan` / `Apply` separation and step-level
  disruption levels.
- Stub adapter with checksummed managed files, so hand edits are detected.

**Security**
- Private CA per panel; mutually authenticated gRPC end to end.
- Node enrolment with single-use, 30-minute tokens; private keys never leave
  the node.
- Certificate pinning by CA fingerprint; a config without one is refused at
  startup rather than falling back to the system trust store.
- Revocation as an allow-list on `cert_fingerprint` — no CRL, no OCSP.
- argon2id password hashing (m=64 MB, t=3, p=4).
- Opaque server-side sessions with 4-hour idle and 7-day absolute lifetimes.
- TOTP two-factor with ten single-use recovery codes.
- Login rate limiting and account lockout, with constant-time behaviour for
  unknown usernames.
- Two-layer authorization: an `rbac.Check` permission gate plus an independent
  SQL scope predicate.
- Append-only audit log covering privileged actions, authorization denials, and
  validation rejections.
- AES-256-GCM sealing of TOTP secrets and the CA key under a master key held
  outside the database.
- SSH bootstrap with host-key pinning; credentials never persisted and wiped
  after use.
- Agent binary downloads guarded by an allow-list and `os.OpenInRoot`.

**Operations**
- `install.sh` bootstrap with OS and architecture guards and mandatory checksum
  verification.
- systemd units for panel and node.
- `antimage-ctl` for admin creation, password recovery, enrolment tokens, and
  consistent online backups.
- Server-Sent Events live status that re-validates its session every tick.

**Interface**
- React SPA embedded in the panel binary, with node list and node detail showing
  desired versus applied revision, drift, revision history, and apply runs.
- Localisation in English, Persian, Russian, Simplified Chinese, and Arabic,
  with right-to-left support enforced by a build gate.
- Documentation in the same five languages.

### Security fixes made during development

These were found and fixed before this release; they never shipped, but they
are recorded because each is a class of bug worth naming.

- **TOTP was never enforced at login.** The primitives existed and nothing
  called them, so an admin who enrolled a second factor was single-factor in
  reality. Login now requires a valid code whenever a secret is enrolled and
  denies on every branch it cannot verify.
- **The gRPC server had no TLS.** The panel constructed a plaintext server while
  both agent paths dialled with TLS, so no node could ever have enrolled or
  streamed.
- **A revision the panel never issued was accepted as convergence**, letting a
  restored backup or pruned history advance `applied_revision` against an
  unverified hash.
- **The SSE stream never re-checked its session**, so a logged-out or revoked
  admin kept receiving live status. Fixed with a validation path that
  deliberately does not extend the idle window.
- **A read-only admin could not log out**, leaving them no way to end their own
  session.
- **The agent could leak an enrolment token into the audit log** through SSH
  bootstrap output.
- **The download endpoint followed symlinks** out of its directory, which would
  have served the master key unauthenticated.
- **`antimage-ctl backup` could never have worked** — it ran `VACUUM INTO`
  inside a transaction, which SQLite rejects.

### Known limitations

Node certificate auto-renewal, a TOTP enrolment UI, a global policy requiring
TOTP for super admins, scope-filtered audit views, single-use TOTP codes, a
metrics endpoint, and tested down migrations are all absent. Real protocol
adapters, subscriber management, traffic accounting, and quotas are out of scope
for SP1. See [README](README.md#known-limitations).

[0.1.0]: https://github.com/devprogrmer/antimage/releases/tag/v0.1.0

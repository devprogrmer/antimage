# antimage

**Languages:** **English** · [فارسی](README.fa.md) · [Русский](README.ru.md) · [简体中文](README.zh-CN.md) · [العربية](README.ar.md)

Self-hosted control plane for managing a fleet of VPN/proxy nodes from one
panel: multi-admin roles, scoped access, an append-only audit trail, and
desired-state reconciliation over mutually authenticated gRPC.

> **Status: SP1 — the control-plane spine.** This release delivers the
> foundation: authentication, authorization, audit, the node registry, mTLS
> enrolment, bootstrap, the adapter contract, health, and the UI shell. The
> only adapter that ships is a **stub** used to prove convergence end to end.
> Real protocol adapters, subscriber management, traffic accounting, and quotas
> are explicitly out of scope for SP1 — see [Known limitations](#known-limitations).

---

## Table of contents

[What it is](#what-it-is) · [Architecture](#architecture) · [Features](#features) ·
[Requirements](#requirements) · [Supported systems](#supported-operating-systems) ·
[Installation](#installation) · [Configuration](#configuration) · [Ports](#ports) ·
[TLS and mTLS](#tls-and-mtls) · [Authentication](#authentication) ·
[Authorization](#authorization) · [Adding a node](#adding-a-node) ·
[Binary downloads](#binary-downloads) · [Security model](#security-model) ·
[CLI](#cli-usage) · [API](#api-usage) · [Logging](#logging) ·
[Health checks](#health-checks) · [Troubleshooting](#troubleshooting) ·
[Upgrading](#upgrade-procedure) · [Backup](#backup-and-recovery) ·
[Uninstall](#uninstall) · [Development](#development-setup) · [Testing](#testing) ·
[Deployment](#deployment) · [Known limitations](#known-limitations) · [License](#license)

---

## What it is

antimage is two programs and a CLI:

- **`antimage-panel`** — the control plane. Serves the operator API and web UI
  over HTTP, and a gRPC control plane that agents dial into over mTLS.
- **`antimage-node`** — the agent. Runs on each managed server, enrols itself,
  then holds a long-lived stream to the panel and reconciles the host toward
  the desired state the panel publishes.
- **`antimage-ctl`** — local administration and recovery, talking directly to
  the panel's database. This is the way back in when the UI is unreachable or
  every admin is locked out.

The central design choice is **desired-state reconciliation over an
agent-dialled stream**, not imperative RPC. The panel publishes what a node
*should* look like; the agent decides how to get there and reports what it did.
That means nodes need no inbound port, an offline node self-heals when it
returns, and configuration drift is detected rather than silently overwritten.

## Architecture

```
                    ┌──────────────────────────────┐
   operator ──HTTP──►  antimage-panel              │
   (browser/CLI)     │  ├─ HTTP API + embedded SPA │  :8080
                     │  ├─ gRPC control plane      │  :8443  (mTLS)
                     │  ├─ SQLite (WAL)            │
                     │  └─ private CA              │
                     └───────────▲──────────────────┘
                                 │ agent dials out; no inbound port on the node
                     ┌───────────┴──────────────────┐
                     │  antimage-node (agent)       │
                     │  ├─ enrol (one-time token)   │
                     │  ├─ control stream (mTLS)    │
                     │  └─ adapter: observe → plan  │
                     │              → apply → verify│
                     └──────────────────────────────┘
```

**Revisions.** Every change to a node's desired state is committed through one
chokepoint that canonicalises the document (RFC 8785 JCS), hashes it with
SHA-256, and bumps `desired_revision` by exactly one. `applied_revision`
advances only when the agent reports convergence **and** the hash it applied
matches the hash the panel recorded. A revision match with a hash mismatch is
an integrity fault, never convergence.

**Two-layer authorization.** Every request passes an explicit `rbac.Check`
permission gate, and every scoped query independently applies a SQL scope
predicate. Forgetting either one is visible on its own, because each is tested
separately.

## Features

- Multi-admin with four built-in roles: `super_admin`, `admin`, `reseller`, `readonly`
- Per-node access scoping enforced in SQL, not only in handlers
- Append-only audit log covering privileged actions, authorization denials, and
  validation rejections
- Opaque server-side sessions (not JWTs), so revocation is immediate
- TOTP two-factor with single-use recovery codes
- Login rate limiting and account lockout
- Node enrolment with a single-use token and a private CA
- Revocation by allow-list: deleting a node locks its certificate out at once
- Desired-state reconciliation with drift detection and per-step apply reports
- Live node status over Server-Sent Events
- SSH bootstrap with host-key pinning, credentials never persisted
- Web UI with enforced right-to-left support and internationalisation

## Requirements

**Panel host**
- Linux x86-64 or ARM64
- ~200 MB disk for the binary, database, and audit log; grows with fleet size
- No external database, message broker, or cache — SQLite only

**Managed node**
- Debian 11/12/13 or Ubuntu 20.04/22.04/24.04, x86-64 or ARM64
- `systemd`, `curl`
- Outbound TCP to the panel's gRPC port. **No inbound port required.**

**Building from source**
- Go 1.26 or newer
- Node.js 20+ and npm (only to build the web UI)

## Supported operating systems

| Component | Supported | Verified |
|---|---|---|
| `antimage-node` | Debian 11/12/13, Ubuntu 20.04/22.04/24.04 (amd64, arm64) | `install.sh` refuses anything else by design |
| `antimage-panel` | Any Linux with the same architectures | Cross-compiled and tested in CI |
| Build host | Linux, macOS, Windows | Test suite runs on all three |

`install.sh` deliberately **refuses** unsupported distributions rather than
guessing at package names.

## Installation

### Clone and build

```bash
git clone https://github.com/devprogrmer/antimage.git
cd antimage
```

Build the web UI first — the panel embeds it:

```bash
cd web && npm ci && npm run build && cd ..
```

Then the binaries:

```bash
make build
```

Or without `make`:

```bash
CGO_ENABLED=0 go build -trimpath -o bin/antimage-panel ./cmd/antimage-panel
CGO_ENABLED=0 go build -trimpath -o bin/antimage-node  ./cmd/antimage-node
CGO_ENABLED=0 go build -trimpath -o bin/antimage-ctl   ./cmd/antimage-ctl
```

`CGO_ENABLED=0` is intentional: the SQLite driver is pure Go, so the binaries
are static and need no libc on the target.

### Start the panel

```bash
sudo mkdir -p /var/lib/antimage && sudo chmod 700 /var/lib/antimage
sudo ./bin/antimage-panel \
  --data-dir /var/lib/antimage \
  --http :8080 \
  --grpc :8443 \
  --grpc-hosts panel.example.com
```

On first start the panel generates its master key, its private CA, and its
database. It prints the CA fingerprint you will pin on nodes:

```
level=INFO msg="antimage-panel listening" http=:8080 grpc=:8443 ca_fingerprint=… grpc_cert_hosts=[panel.example.com]
```

> **`--grpc-hosts` must list the names agents actually dial.** They become the
> SANs of the panel's TLS certificate. A mismatch fails every node's handshake
> at once and is invisible until an agent tries.

### Create the first admin

```bash
sudo ./bin/antimage-ctl --data-dir /var/lib/antimage \
  create-admin admin 'a-long-passphrase' super_admin
```

Then open `http://localhost:8080` and sign in.

### Install the panel as a service

```bash
sudo cp bin/antimage-panel /usr/local/bin/
sudo useradd --system --home /var/lib/antimage --shell /usr/sbin/nologin antimage
sudo chown -R antimage:antimage /var/lib/antimage
sudo cp packaging/antimage-panel.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now antimage-panel
```

The unit runs as `User=antimage` with `NoNewPrivileges`, `ProtectSystem=strict`,
`ProtectHome`, and `PrivateTmp`. **You must create that user** — packaging does
not do it for you.

## Adding a node

### Bootstrap one-liner

Create the node in the UI (or with `antimage-ctl`), take the enrolment token,
and run this **on the node**:

```bash
curl -fsSL https://panel.example.com/install.sh | sudo bash -s -- \
  --panel https://panel.example.com \
  --token YOUR_ENROLMENT_TOKEN \
  --ca-fingerprint THE_PANEL_CA_FINGERPRINT
```

`install.sh` verifies the OS and architecture, downloads the agent and its
SHA-256, **verifies the checksum before installing**, writes
`/etc/antimage/node.yaml` at mode 0600, installs a systemd unit, and starts it.
Re-running upgrades in place without consuming a new token.

Passing `--ca-fingerprint` from an out-of-band channel is the strong path. If
you omit it, the script fetches it from the panel over HTTPS — trust on first
use, which a hijacked DNS record could defeat.

> **Before the one-liner works you must publish the agent binaries.** See
> [Binary downloads](#binary-downloads).

### Manual installation

```bash
sudo install -m 0755 antimage-node /usr/local/bin/antimage-node
sudo mkdir -p /etc/antimage /var/lib/antimage && sudo chmod 700 /var/lib/antimage
sudo tee /etc/antimage/node.yaml >/dev/null <<'YAML'
panel_url: https://panel.example.com:8443
token: YOUR_ENROLMENT_TOKEN
ca_fingerprint: THE_PANEL_CA_FINGERPRINT
state_dir: /var/lib/antimage
YAML
sudo chmod 600 /etc/antimage/node.yaml
sudo cp packaging/antimage-node.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now antimage-node
```

### SSH bootstrap from the panel

`POST /api/v1/nodes/{nodeID}/bootstrap-ssh` runs the installer over SSH in two
phases: the first call returns the host's key fingerprint for a human to
confirm, the second executes only against that pinned key. **SSH credentials
are never persisted** — no table, no column, no serialisation — and the key
material is wiped from memory before the request returns.

## Configuration

### `antimage-panel` flags

| Flag | Type | Default | Required | Purpose |
|---|---|---|---|---|
| `--data-dir` | path | `/var/lib/antimage` | no | Database, master key, downloads. Must be mode 0700. |
| `--http` | listen addr | `:8080` | no | Operator API and UI. Put a TLS terminator in front. |
| `--grpc` | listen addr | `:8443` | no | Agent control plane. Serves mTLS directly. |
| `--grpc-hosts` | CSV | `localhost,127.0.0.1` | **effectively yes** | DNS names and IPs agents dial; becomes the certificate SANs. The default only works for local testing. |
| `--version` | flag | — | no | Print version and exit. |

### `antimage-node` flags

| Flag | Type | Default | Purpose |
|---|---|---|---|
| `--config` | path | `/etc/antimage/node.yaml` | Agent configuration. |
| `--version` | flag | — | Print version and exit. |

### `/etc/antimage/node.yaml`

See [`packaging/node.yaml.example`](packaging/node.yaml.example).

| Key | Type | Required | Default | Purpose | Security note |
|---|---|---|---|---|---|
| `panel_url` | string | **yes** | — | Panel gRPC endpoint, `https://host:port` or `host:port`. | Rejected at startup if it carries a path, query, or non-https scheme. |
| `token` | string | first run only | — | Single-use enrolment token. | Cleared from the file once spent. Keep the file at 0600. |
| `ca_fingerprint` | string | **yes** | — | SHA-256 of the panel CA certificate, hex. | Startup **refuses** a config without it rather than falling back to the system trust store. |
| `state_dir` | path | no | `/var/lib/antimage` | Node key, certificate, managed state. | Created 0700; key and certificate 0600. |
| `node_id` | int | no | — | Written after enrolment. | Do not set by hand. |

### Environment variables

| Variable | Used by | Purpose | Security note |
|---|---|---|---|
| `ANTIMAGE_MASTER_KEY` | panel | Base64 32-byte master key, instead of the key file. | Encrypts TOTP secrets and the CA private key. A leaked database without this key yields neither. Prefer the 0600 file on disk unless your platform injects secrets by environment. |
| `ANTIMAGE_DEV_PROXY` | panel | Proxy UI requests to a Vite dev server. | **Development only.** Never set in production. |

## Ports

| Port | Component | Protocol | Exposure |
|---|---|---|---|
| 8080 | panel | HTTP | Operators. Terminate TLS in front of it. |
| 8443 | panel | gRPC over mTLS | Nodes. Must be reachable from every managed node. |
| — | node | none | The agent dials out. **No inbound port.** |

## TLS and mTLS

The control plane is mutually authenticated end to end.

### Trust model

The panel runs its **own private CA**, created on first start and stored
encrypted under the master key. It is not a public web PKI CA and issues only:

- one **server certificate** for the panel's gRPC listener, with the SANs from
  `--grpc-hosts`, valid 90 days, reissued on every panel start;
- one **client certificate per node**, `CN = <node id>`, valid one year.

### Certificate locations

| What | Where | Mode |
|---|---|---|
| Master key | `<data-dir>/master.key` (or `ANTIMAGE_MASTER_KEY`) | 0600 |
| CA certificate + sealed key | `panel_ca` table in `<data-dir>/antimage.db` | — |
| Panel server certificate | in memory, reissued each start | — |
| Node private key | `<state-dir>/node.key` | 0600 |
| Node certificate | `<state-dir>/node.crt` | 0600 |
| Pinned panel CA | `<state-dir>/panel-ca.crt` | 0600 |

### How enrolment works

1. The agent generates its keypair locally. **The private key never leaves the
   node and the panel never sees it.**
2. It dials the panel and verifies the presented chain contains a certificate
   whose SHA-256 matches `ca_fingerprint`. A hijacked DNS record yields nothing.
3. It sends the one-time token and a CSR.
4. The panel verifies the token is unused, unexpired, and bound to that node,
   signs a client certificate, records its fingerprint, and burns the token.
5. Everything afterwards uses mTLS.

The panel presents `[leaf, CA]` rather than the leaf alone, precisely so an
enrolling agent — which has no CA file yet — can find its pinned fingerprint in
the chain.

### Validation and revocation

The listener uses `VerifyClientCertIfGiven`, **not** `RequireAndVerifyClientCert`:
enrolment necessarily happens before a node holds any certificate. The control
service enforces the requirement per-RPC, and additionally checks the presented
fingerprint against `nodes.cert_fingerprint`.

**Revocation is an allow-list, not a CRL.** The panel is the only verifier, so
deleting a node removes its fingerprint and locks it out on the next
connection. There is no CRL to distribute and no OCSP responder to run.

### Expiration and rotation

| Certificate | Lifetime | Rotation |
|---|---|---|
| CA | 10 years | Manual. Replacing it re-enrols the fleet. |
| Panel server | 90 days | Automatic — reissued on every panel start. Restart the panel at least every 90 days. |
| Node client | 1 year | **Not yet automatic — see [Known limitations](#known-limitations).** |

### Verification commands

Fetch the fingerprint operators should pin:

```bash
curl -fsS https://panel.example.com/api/v1/ca-fingerprint
```

Inspect what the gRPC listener presents:

```bash
openssl s_client -connect panel.example.com:8443 -showcerts </dev/null 2>/dev/null | openssl x509 -noout -text
```

Confirm a node's own certificate:

```bash
sudo openssl x509 -in /var/lib/antimage/node.crt -noout -subject -dates
```

## Authentication

- Passwords hashed with **argon2id** (m=64 MB, t=3, p=4).
- Sessions are **opaque server-side tokens**, not JWTs, so revocation takes
  effect immediately. Only the SHA-256 of a token is stored.
- Cookies are `HttpOnly`, `Secure`, `SameSite=Strict`.
- **Idle timeout 4 hours; absolute lifetime 7 days.** Activity extends the idle
  window; nothing extends the absolute deadline.
- Login failures are rate limited per account and per IP, and lock out after 5
  failures. An unknown username costs the same time as a known one, so response
  timing does not reveal whether an account exists.
- **TOTP** is optional per admin. Once enrolled, a valid code is required and
  every branch the panel cannot verify **denies** rather than admitting on a
  password alone.
- Ten **single-use recovery codes** are issued at confirmation and shown once.

Enrol a second factor:

```bash
# returns {"secret":"…","provisioning_uri":"otpauth://…"}
curl -X POST https://panel.example.com/api/v1/auth/totp/enrol -b cookies.txt
# confirm with a code from your authenticator; returns the recovery codes once
curl -X POST https://panel.example.com/api/v1/auth/totp/confirm -b cookies.txt \
  -d '{"totp":"123456"}'
```

## Authorization

Four built-in roles:

| Role | Node read | Node write | Enrol | Service write | Audit read | Sessions |
|---|---|---|---|---|---|---|
| `super_admin` | ✅ all | ✅ | ✅ | ✅ | ✅ | ✅ |
| `admin` | ✅ scoped | ✅ | ✅ | ✅ | ✅ | own |
| `reseller` | ✅ scoped | — | — | ✅ | — | own |
| `readonly` | ✅ scoped | — | — | — | — | own |

Authorization is enforced twice, independently:

1. **Permission gate** — every handler calls `rbac.Check` before doing work.
2. **SQL scope predicate** — every scoped query filters by the caller's
   allow-list, so a handler that forgot its check still cannot read another
   admin's nodes.

Denials are written to the audit log with the attempted permission, method, and
path.

## Binary downloads

`install.sh` fetches the agent from the panel. Publish the binaries by placing
them in `<data-dir>/downloads`:

```bash
sudo mkdir -p /var/lib/antimage/downloads
sudo cp antimage-node-linux-amd64 /var/lib/antimage/downloads/
sha256sum antimage-node-linux-amd64 | awk '{print $1}' \
  | sudo tee /var/lib/antimage/downloads/antimage-node-linux-amd64.sha256
sudo chown -R antimage:antimage /var/lib/antimage/downloads
```

Only these four names are served, and the list is an **allow-list, not a
sanitiser**:

- `antimage-node-linux-amd64` and `.sha256`
- `antimage-node-linux-arm64` and `.sha256`

Anything else returns 404, including files that exist inside the directory. The
endpoint is unauthenticated by design — the binary is not a secret, and the
enrolment token is what authorises joining.

## Security model

| Property | How |
|---|---|
| Node keys | Generated on the node; the panel never sees a private key. |
| Panel impersonation | Agents pin the CA fingerprint; a hijacked DNS record yields nothing. |
| Revocation | Allow-list on `cert_fingerprint`; deleting a node locks it out immediately. |
| Secrets at rest | TOTP secrets and the CA key are sealed with AES-256-GCM under a master key held **outside** the database. |
| SSH credentials | Never persisted. No table, no column, no serialisation tag; wiped from memory after use. |
| Enrolment tokens | Single use, 30-minute expiry, stored hashed, burned on use, redacted from audit records. |
| Path traversal | Downloads use an allow-list **and** `os.OpenInRoot`, which refuses to leave the directory even via symlinks. |
| Audit integrity | Append-only; `audit_log` has no foreign key to `nodes`, so deleting a node cannot erase its own trail. |
| Drift | Managed files are checksummed; a hand edit is detected and corrected, not silently overwritten. |

Report vulnerabilities per [SECURITY.md](SECURITY.md).

## CLI usage

```
antimage-ctl [--data-dir DIR] <command> [arguments]

  create-admin   USERNAME PASSWORD ROLE   create an admin
  reset-password USERNAME PASSWORD        set a new password, revoke their sessions
  list-admins                             list admins with roles and status
  enroll-token   NODE_ID                  print a single-use enrolment token
  backup         DEST.db                  write a consistent database copy
  version                                 print the version
```

`reset-password` also clears the account's failed-login history, so the
attempts that locked an operator out do not keep them out afterwards.

## API usage

All API paths are under `/api/v1`. Authentication is a session cookie from
`POST /auth/login`.

```bash
# sign in
curl -c cookies.txt -X POST https://panel.example.com/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"…","totp":"123456"}'

# create a node, then mint a bootstrap command
curl -b cookies.txt -X POST https://panel.example.com/api/v1/nodes \
  -H 'Content-Type: application/json' \
  -d '{"name":"de-1","address":"203.0.113.10"}'

curl -b cookies.txt -X POST https://panel.example.com/api/v1/nodes/1/enroll-token
```

| Method | Path | Purpose |
|---|---|---|
| POST | `/auth/login` `/auth/logout` | Session lifecycle |
| GET | `/auth/me` | Current actor and permissions |
| POST | `/auth/totp/enrol` `/auth/totp/confirm` `/auth/totp/disable` | Second factor |
| GET/POST | `/nodes` | List and create nodes |
| GET/DELETE | `/nodes/{id}` | Node detail; delete locks the node out |
| POST | `/nodes/{id}/enroll-token` | Mint a single-use token and bootstrap command |
| POST | `/nodes/{id}/bootstrap-ssh` | Two-phase SSH bootstrap |
| GET | `/nodes/{id}/revisions` `/nodes/{id}/apply-runs` | History and per-step apply results |
| POST | `/nodes/{id}/services` | Create a service (bumps the revision) |
| PUT/DELETE | `/services/{id}` | Update or remove a service |
| GET | `/audit` `/sessions` | Audit trail; own sessions |
| DELETE | `/sessions/{id}` | Revoke one of your sessions |
| GET | `/events` | Live node status (SSE) |
| GET | `/ca-fingerprint` | Public trust anchor (unauthenticated) |

Errors use one envelope: `{"error":{"code":"…","message":"…"}}`. Every response
carries `X-Request-ID`, which also appears in the audit log.

## Logging

Structured logs on stderr via Go's `log/slog`. Under systemd:

```bash
sudo journalctl -u antimage-panel -f
sudo journalctl -u antimage-node -f
```

Operational events go to the **audit log** rather than stdout, queryable at
`GET /api/v1/audit`. Secrets are never logged: enrolment tokens are redacted
from bootstrap output, and TOTP secrets and recovery codes never enter an audit
record.

## Health checks

The live view is `GET /api/v1/events` (SSE), which pushes a status snapshot
every 3 seconds and **re-validates the session on every tick**, so a logout or
revoke ends the stream promptly.

Node status values:

| Status | Meaning |
|---|---|
| `pending` | Created, never contacted |
| `enrolling` | Token issued, not yet enrolled |
| `online` | Streaming and heartbeating |
| `degraded` | Connected, last apply failed or was partial |
| `integrity` | **The node applied a document whose hash the panel did not issue.** Investigate. |
| `offline` | No heartbeat for three intervals (90 s) |
| `disabled` | Administratively disabled |

## Troubleshooting

**Every node fails its handshake after a panel move.**
`--grpc-hosts` no longer matches what agents dial. Check the startup log line
(`grpc_cert_hosts=[…]`), correct the flag, restart. The panel reissues its
certificate on every start.

**`bad interpreter: /bin/bash^M` when running install.sh.**
The script was checked out with CRLF endings. The repository pins `*.sh` to LF
via `.gitattributes`; re-clone or run `dos2unix`.

**A TOTP-enrolled admin cannot sign in and the log says the box is missing.**
The panel started without its master key while encrypted secrets exist. This is
deliberate fail-closed behaviour — restore `master.key` or `ANTIMAGE_MASTER_KEY`.
Do not delete the key: TOTP secrets and the CA key are unrecoverable without it.

**A node is stuck `integrity`.**
The document it applied hashes to something the panel never issued. Inspect
`GET /api/v1/nodes/{id}/apply-runs`. The status is intentionally sticky — a
heartbeat will not clear it.

**Bootstrap fails at the download step.**
No binaries are published. See [Binary downloads](#binary-downloads).

**`cannot VACUUM from within a transaction` from a backup.**
Fixed in this release. Upgrade `antimage-ctl`.

## Upgrade procedure

```bash
# 1. Back up first — this is consistent and safe while the panel runs.
sudo antimage-ctl --data-dir /var/lib/antimage backup /var/backups/antimage-$(date +%F).db

# 2. Replace the panel binary and restart. Migrations run automatically.
sudo systemctl stop antimage-panel
sudo cp antimage-panel /usr/local/bin/
sudo systemctl start antimage-panel

# 3. Publish the new agent binaries.
sudo cp antimage-node-linux-amd64 /var/lib/antimage/downloads/
sha256sum antimage-node-linux-amd64 | awk '{print $1}' \
  | sudo tee /var/lib/antimage/downloads/antimage-node-linux-amd64.sha256

# 4. Upgrade a node in place — re-running is idempotent and consumes no token.
curl -fsSL https://panel.example.com/install.sh | sudo bash -s -- \
  --panel https://panel.example.com --token '' \
  --ca-fingerprint THE_PANEL_CA_FINGERPRINT
```

Database migrations are forward-only and run at startup. **Roll back by
restoring a backup, not by downgrading the binary.**

## Backup and recovery

```bash
sudo antimage-ctl --data-dir /var/lib/antimage backup /var/backups/antimage.db
sudo cp /var/lib/antimage/master.key /var/backups/master.key   # 0600, store separately
```

`backup` uses SQLite `VACUUM INTO`, producing a consistent copy while the panel
keeps running. It refuses to overwrite an existing file.

> **The database alone is not enough.** Without `master.key` the CA private key
> and every TOTP secret are unrecoverable. Back it up separately — storing it
> beside the database defeats the reason it lives outside the database.

Restore:

```bash
sudo systemctl stop antimage-panel
sudo cp /var/backups/antimage.db /var/lib/antimage/antimage.db
sudo cp /var/backups/master.key  /var/lib/antimage/master.key
sudo chown antimage:antimage /var/lib/antimage/*
sudo systemctl start antimage-panel
```

## Uninstall

On a node:

```bash
sudo systemctl disable --now antimage-node
sudo rm -f /etc/systemd/system/antimage-node.service /usr/local/bin/antimage-node
sudo rm -rf /etc/antimage /var/lib/antimage
sudo systemctl daemon-reload
```

Delete the node in the panel too — that removes its fingerprint from the
allow-list.

On the panel host:

```bash
sudo systemctl disable --now antimage-panel
sudo rm -f /etc/systemd/system/antimage-panel.service /usr/local/bin/antimage-panel
sudo rm -rf /var/lib/antimage      # destroys the database, CA, and master key
sudo userdel antimage
```

## Development setup

```bash
git clone https://github.com/devprogrmer/antimage.git && cd antimage
go mod download
cd web && npm ci && cd ..
```

Run the UI with hot reload against a live panel:

```bash
cd web && npm run dev          # terminal 1
ANTIMAGE_DEV_PROXY=http://localhost:5173 go run ./cmd/antimage-panel --data-dir ./tmp   # terminal 2
```

Regenerating protobuf code needs `buf`, installed **into your GOPATH, not
system-wide**:

```bash
go install github.com/bufbuild/buf/cmd/buf@latest
PATH="$PATH:$(go env GOPATH)/bin" make proto
```

## Testing

```bash
make test              # unit + integration, with -race
make e2e               # acceptance suite for the definition of done
make check-imports     # import boundaries and the SSH host-key policy
make check-rtl         # RTL and i18n gates
bash scripts/install_test.sh
cd web && npx vitest run && npm run lint
```

`make test` uses `-race`, which needs cgo. Without a C toolchain use
`go test ./... -count=1` and note that the race detector did not run.

The acceptance suite runs a real panel and a real agent over loopback with
genuine mTLS, covering all six definition-of-done items. It needs no Docker.

## Deployment

Put a TLS terminator in front of `:8080`; expose `:8443` directly, because the
panel serves mTLS there itself and a terminator would break client certificate
verification.

```
                 ┌── :443  → reverse proxy → :8080   (operators, HTTPS)
  panel host ────┤
                 └── :8443 → antimage-panel          (nodes, mTLS, direct)
```

Checklist: create the `antimage` user; `/var/lib/antimage` at 0700; back up
`master.key` separately; set `--grpc-hosts` to the public name; publish agent
binaries; restart at least every 90 days so the server certificate is reissued.

## Known limitations

These are real and deliberate for SP1:

- **Only the stub adapter ships.** It proves convergence, drift detection, and
  reporting end to end, but manages no real protocol. Real adapters are a later
  sub-project.
- **No subscriber management, traffic accounting, quotas, or subscription
  links.** Out of scope for SP1.
- **Node certificate auto-renewal is not implemented.** Certificates last a
  year; renewal at the halfway mark is designed but not built.
- **No TOTP enrolment UI.** The endpoints work; the SPA has no screen for them
  yet.
- **No global "require TOTP for super_admin" setting.** Deliberately deferred:
  a policy that denies login to super admins who have not enrolled can lock an
  operator out entirely, and it needs a designed escape hatch first.
- **Server-supplied enum values render untranslated** (`converged`, `ok`,
  `restart`). They are data, not UI strings.
- **TOTP codes are not single-use.** A code stays valid across the ±30 s skew
  window.
- **The audit view is not scope-filtered.** An `audit:read` holder sees all
  rows, including other admins' login IPs. `reseller` does not hold that
  permission.
- **No metrics endpoint** (Prometheus or otherwise).
- **Down migrations are untested.** Roll back by restoring a backup.

## License

**No license is declared yet.** Per the project specification, the licence
choice is the maintainer's and is a prerequisite for any public release; the
repository is private until then. Until a `LICENSE` file exists, no permission
to use, copy, modify, or distribute is granted.

The reference projects that informed antimage's functional behaviour — 3x-ui,
Rebecca, PasarGuard, vpn-ui, openvpn_webpanel_manager, L2tp-Gui-Panel — supplied
**no code, assets, schema, or documentation**. Every implementation here is
original.

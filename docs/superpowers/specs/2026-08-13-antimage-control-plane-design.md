# antimage — Control-Plane Spine (SP1) Design

**Date:** 2026-08-13
**Status:** Approved
**Scope:** Sub-project 1 of 8

---

## 1. Product and scope

antimage is a self-hosted VPN/proxy management panel. It manages three protocol
families — Xray/sing-box, OpenVPN, and L2TP/IPsec — across many nodes, with
multi-admin roles, reseller accounts, traffic quotas, expiry dates, live status,
subscriptions, and server health.

That full product spans several independent subsystems. Writing it as one
specification would produce a document nobody can implement or review. The work
therefore splits into eight sub-projects, each with its own spec, plan, and
build cycle.

| # | Sub-project | Delivers |
|---|---|---|
| **SP1** | **Control-plane spine** | **Auth, RBAC, audit, node registry, mTLS enrollment, bootstrap, adapter contract, health, UI shell. This document.** |
| SP2 | Xray/sing-box adapter | Inbound modelling, config generation, credential derivation, user CRUD, expiry |
| SP3 | Accounting and quotas | Delta ingestion, cross-node dedup, rollups, quota enforcement, resets, auto-freeze |
| SP4 | Subscription delivery | Tokens, revocation, v2ray/Clash/sing-box/`.ovpn` rendering, UA detection, rate limits |
| SP5 | OpenVPN adapter | In-Go PKI, issue/revoke/CRL, profile generation, CCD, management-interface accounting |
| SP6 | L2TP/IPsec adapter | strongSwan and xl2tpd config, PPP secrets, per-IP nftables accounting |
| SP7 | Observability depth | Node metrics, latency and uptime history, cert-expiry and quota alerting |
| SP8 | Reseller economics | Credit and quota allocation, sub-admin limits, per-reseller branding |

Build order runs SP1 → SP2 → SP3 → SP4 → SP5 → SP6, with SP7 and SP8 interleaved
once SP3 lands. SP5 and SP6 only implement the adapter contract, so they can
proceed in parallel after SP1.

SP1 ends with a runnable system: add a node, watch it enroll and converge, and
exercise the adapter contract through a stub. It deliberately ships no real
protocol adapter, because three adapters will depend on that contract and a
mistake in it must surface before they do.

## 2. Decisions

| Decision | Choice | Why |
|---|---|---|
| Protocols | Xray/sing-box + OpenVPN + L2TP/IPsec | Full product scope. Forces an adapter contract rather than a single hardcoded stack. |
| Control plane | Agent daemon, SSH used only to bootstrap | Real-time convergence, survives panel restarts, no polling-based drift. |
| Reconciliation | Desired-state over an agent-dialed stream | Nodes need no inbound port, offline nodes self-heal, out-of-band drift is detected. |
| Backend | Go panel + Go agent | One toolchain, two static binaries, UI embedded. Target footprint: panel under 100 MB RSS on a 1 GB VPS. |
| Identity | One user, many protocol credentials, one shared quota | One subscriber, one expiry, one link. Best admin and reseller UX. |
| Database | SQLite in WAL mode | One file, no daemon, trivial backup. Matches the single-binary install story. |
| Localization | EN + FA with full RTL from SP1 | Retrofitting RTL into a dense admin panel is never done cleanly. |
| Deployment | `install.sh` + systemd, Debian 11+/Ubuntu 20.04+ | Keeps provisioning code honest instead of guessing at distro differences. |

Rejected alternatives worth recording: imperative RPC control (a node offline
during a change stays silently wrong, so a resync path gets built anyway);
stateless JWT sessions (cannot revoke a reseller immediately); dual
SQLite/PostgreSQL support (a permanent 20–30% tax on all data-layer work).

## 3. Architecture and repository layout

Three binaries share one Go module and generated protobuf types, so panel and
agent cannot disagree about the wire format.

- `antimage-panel` — HTTP API, embedded React UI, gRPC control server, SQLite.
- `antimage-node` — the agent: dials the panel, runs the reconcile loop, owns local services.
- `antimage-ctl` — local CLI on the panel host for what the web UI must not be the only path to: create the first super admin, reset a locked-out password, back up and restore, print an enrollment token.

```
antimage/
├── cmd/{antimage-panel,antimage-node,antimage-ctl}/
├── proto/                        # .proto sources, hand-written
├── internal/
│   ├── panel/
│   │   ├── httpapi/              # chi router, handlers, DTOs
│   │   ├── auth/                 # argon2id, sessions, TOTP
│   │   ├── rbac/                 # roles, scopes, enforcement
│   │   ├── audit/
│   │   ├── nodes/                # registry, enrollment, revision store
│   │   ├── control/              # gRPC server, stream hub, dispatcher
│   │   └── store/                # sqlc output + goose migrations
│   ├── node/
│   │   ├── agent/                # stream client, reconcile loop
│   │   ├── adapter/              # NodeAdapter iface + registry + stub/
│   │   ├── sysinfo/              # load, uptime, disk, NIC counters
│   │   └── supervisor/           # systemd unit control
│   └── shared/{proto,version,ids}/
├── web/                          # React + TS + Vite → embed.FS
├── scripts/install.sh
└── docs/
```

Two boundaries are enforced, not merely intended:

1. **`internal/node/adapter` imports nothing from `internal/panel`.** The agent
   must compile and test without knowledge of the panel's database or business
   rules. Otherwise the adapter contract leaks panel concepts and SP5 and SP6
   inherit the damage. CI checks this with an import-graph rule.
2. **`control` owns all stream state.** HTTP handlers never touch a gRPC stream.
   They bump a revision in the store; `control` notices. "An admin clicked
   disable" and "a node reconnected" then flow through one path, not two.

The UI compiles to static assets embedded through `embed.FS`. Development mode
proxies to Vite so hot reload still works.

## 4. The adapter contract

`Plan` and `Apply` are separate methods. An adapter is never told how to change
the host; it receives desired state and reports what it would do. Lifecycle
differences between a long-lived Xray process and the managed OpenVPN and
strongSwan services live inside the adapter, not in the reconciler.

```go
type Adapter interface {
    Descriptor() Descriptor                                     // static identity + capabilities
    Observe(ctx) (Observed, error)                              // read host truth; never mutates
    Plan(ctx, desired Desired, observed Observed) (Plan, error) // pure, repeatable, no side effects
    Apply(ctx, step Step) (StepResult, error)                   // one step; must be idempotent
    Probe(ctx) (Health, error)                                  // cheap liveness, health cadence
}
```

### 4.1 Step-level disruption

Every step `Plan` emits carries its cost:

```go
type Disruption uint8
const (
    DisruptNone    Disruption = iota // Xray AddUser over gRPC; append chap-secrets
    DisruptReload                    // SIGHUP, swanctl --load-creds; sessions survive
    DisruptRestart                   // service restart; active sessions drop
)
```

Disruption belongs to the step, not the adapter. Adding a user is `DisruptNone`
on all three families: Xray takes it through `HandlerService.AlterInbound`,
`pppd` reads secrets per connection, and issuing an OpenVPN certificate touches
no running service. Changing an inbound's listen port is `DisruptRestart` on all
three. A per-adapter "needs restart" flag would be too blunt to express this.

The reconciler exploits the distinction: it debounces disruptive steps, so ten
edits in a minute coalesce into one restart, and it honours a per-node
maintenance window that defers `DisruptRestart` while still applying
`DisruptNone` immediately. Users get disabled at once; ports move at 04:00.

### 4.2 Capabilities

```go
type Caps struct {
    HotUserAdd      bool             // Xray: true
    SelfAccounting  bool             // Xray: true (stats API). OpenVPN/L2TP: false
    RequiresPKI     bool             // OpenVPN: true
    CredentialKinds []CredentialKind // UUID, X509, Password
    ServiceSchema   json.RawMessage  // JSON Schema for this adapter's service params
}
```

`ServiceSchema` keeps the panel free of protocol-specific config formats. The
adapter publishes a JSON Schema for its own parameters; the panel validates
against it on write and the UI renders the form from it. Adding SP6 means adding
an adapter, not editing panel code.

### 4.3 Ownership and drift

Adapters write configuration atomically — temporary file, then `rename` — and
stamp every managed file with a header marker and a content checksum recorded in
`Observed`. `Plan` can then distinguish "this file differs because desired state
changed" from "a human edited this file", and the panel reports the latter as
drift instead of silently overwriting it. Adapters never touch files they did
not create.

### 4.4 Failure handling

Steps carry dependencies and run in order: PKI, then service config, then user
push. A failed step retries with exponential backoff to a cap; beyond that the
node becomes `Degraded`, the underlying stderr surfaces in the UI, and
reconciliation drops to the slow timer instead of hot-looping. One failure never
blocks unrelated steps in the same plan.

## 5. Data model and the revision mechanism

Desired state is derived from relational tables, never stored as a blob. A
stored copy would create a second source of truth that drifts the first time
someone writes a migration.

Versioning uses a counter, `nodes.desired_revision`. The obvious race — read
revision R, then read rows that have since become R+1 — is closed by building the
document inside a single read transaction. SQLite in WAL mode provides a
consistent read snapshot, so revision and rows always agree.

### 5.1 Schema

```
admins            id, username, password_hash(argon2id), role_id, parent_admin_id,
                  status, totp_secret_enc, created_at
roles             id, name, is_builtin, permissions(JSON key set)
admin_scopes      admin_id, scope_type('node'|'service'), scope_id     -- allow-list
sessions          id, admin_id, token_hash, ip, user_agent, created_at,
                  expires_at, last_used_at, revoked_at
nodes             id, name, address, status, adapter_kinds, cert_fingerprint,
                  desired_revision, applied_revision, last_seen_at, last_error,
                  maintenance_window, enrolled_at
node_revisions    node_id, revision, created_at, actor_type, actor_admin_id,
                  actor_label, reason, doc_sha256
node_apply_runs   id, node_id, target_revision, started_at, finished_at, outcome
node_apply_steps  run_id, seq, step_kind, disruption, outcome, error, duration_ms
node_health       node_id, at, load1, mem_used, uptime_s, rtt_ms, adapter_status(JSON)
services          id, node_id, adapter_kind, params(JSON), enabled
enroll_tokens     token_hash, node_id, expires_at, used_at
audit_log         id, at, actor_type, actor_admin_id, actor_label, actor_ip,
                  request_id, action, target_type, target_id,
                  before(JSON), after(JSON), result
settings          key, value
```

`services` belongs to SP1 because the stub adapter needs real desired state to
converge on and RBAC scopes point at services. `Desired.Subjects` is wired but
stays empty until SP2, which exercises the contract without prematurely
inventing the user model.

`node_revisions` stores the document's SHA-256, not the document. That answers
"who changed this node, when, and why" for every revision at negligible cost,
and the agent's reported hash can be checked against it to prove convergence.

### 5.2 Invariants

1. **Single commit path.** Every mutation that can affect a node's desired
   document goes through `CommitNodeChange(tx, nodeID, actor, reason, mutate)`.
   It runs the mutation, rebuilds the canonical snapshot in the same write
   transaction, and updates `desired_revision` and inserts the `node_revisions`
   row together. This covers service changes, node settings, adapter changes,
   and future subject changes.
2. **Revisions track semantic change.** `CommitNodeChange` compares the new
   canonical hash against the latest `node_revisions.doc_sha256` and bumps only
   on difference. No-op detection is structural: nothing detects no-ops by hand,
   so nothing can forget to.
3. **Canonical serialization.** RFC 8785 (JCS): sorted keys, no insignificant
   whitespace, UTF-8, defined number formatting. Two additional rules — the
   document types use no `omitempty` (every field always present, absent means
   explicit `null`), and arrays sort by a stable key before serialization. The
   document carries a `schema_version`. Adding a field changes every node's hash
   and triggers a fleet-wide reconcile, so schema changes ship with a migration
   that recomputes stored hashes and a release note.
4. **Hash matches document exactly.** `doc_sha256` is computed from the exact
   canonical bytes for that revision, inside the same transaction. The agent
   receives those same bytes.
5. **One authoritative reader.** `BuildDesiredSnapshot(nodeID)` returns
   `{revision, document, sha256}`. Callers never assemble these independently.
6. **Reconciliation compares revision and hash.** A matching revision with a
   mismatched hash sets node status `Integrity`, refuses to advance
   `applied_revision`, logs at error level, and raises an alert. It is never
   treated as convergence.
7. **`applied_revision` advances only on complete success** — the apply run
   finishes and the agent's post-apply `Observe`→`Plan` returns empty. Partial
   application leaves `applied_revision` unchanged, marks the node `Degraded`,
   and remains inspectable through `node_apply_steps`.
8. **System actors are first-class.** `actor_type` is `'admin'`, `'system'`, or
   `'ctl'`; `actor_admin_id` is nullable; `actor_label` names the system actor
   (`enrollment`, `reconciler`, `migration`). A CHECK enforces
   `actor_type='admin' → actor_admin_id IS NOT NULL`. No synthetic admin rows.
9. **Audit atomicity.** Committed mutations are audited in the same transaction,
   so a rolled-back change leaves no audit row. Attempts that never commit —
   failed logins, authz denials, validation rejections, failed applies — are
   written by a separate best-effort path with `result='denied'|'failed'`,
   because those are the security-relevant events that must not be lost. Every
   record carries a `request_id` propagated through context from HTTP
   middleware.
10. **Constraints.** `admins.username UNIQUE COLLATE NOCASE`; `nodes.name
    UNIQUE`; `nodes.cert_fingerprint UNIQUE`; `sessions.token_hash UNIQUE`;
    `enroll_tokens.token_hash UNIQUE`; `PRIMARY KEY(node_id, revision)`;
    `CHECK(revision > 0)`; `CHECK(applied_revision <= desired_revision)`; and a
    trigger rejecting any `node_revisions` insert where
    `revision != max(revision) + 1`.

### 5.3 Store configuration

`journal_mode=WAL`, `foreign_keys=ON`, `busy_timeout=5000`. All writes funnel
through a single serialized writer, because SQLite permits exactly one.

## 6. Auth, RBAC, and audit

### 6.1 Authentication

argon2id with m=64 MB, t=3, p=4; constant-time comparison; identical error text
for unknown user and wrong password. Rate limiting applies to both account and
source IP with exponential backoff, because a reseller panel attracts
credential stuffing. Defaults: five failures per account in fifteen minutes and
twenty per source IP in fifteen minutes trigger backoff, doubling from one
second to a five-minute ceiling. Both are settings, not constants. TOTP is optional per admin with single-use recovery codes,
and a global setting can require it for `super_admin`. TOTP secrets and recovery
codes are encrypted at rest with AES-256-GCM under the master key.

The master key is 32 bytes generated at first run and written to
`/etc/antimage/master.key`, mode `0600`, owned by the panel's service user. It
lives outside the database on purpose: a leaked database backup then yields no
TOTP secrets and no CA key. An `ANTIMAGE_MASTER_KEY` environment variable
overrides the file for operators who inject secrets at deploy time. The panel
refuses to start if the key is missing while encrypted rows exist, rather than
silently generating a new one and orphaning them.

### 6.2 Sessions

32 bytes from `crypto/rand`, stored as SHA-256, carried in an
`HttpOnly; Secure; SameSite=Strict` cookie, with a 4-hour idle timeout and a
7-day absolute lifetime, both settings. Tokens rotate on privilege change. Admins list their
active sessions with IP and user agent and revoke individually; a super admin
revokes any. `SameSite=Strict` plus an Origin check covers CSRF without a token
exchange.

Sessions are opaque and server-side rather than JWTs so that revoking a reseller
takes effect immediately.

### 6.3 Permissions and scopes

Permission keys read `resource:verb` — `node:read`, `node:write`, `node:enroll`,
`service:write`, `admin:manage`, `role:manage`, `audit:read`, `settings:write`.
The four roles (super admin, admin, reseller, read-only) are templates that
populate `roles.permissions`, and a super admin can define custom roles. Roles
are data, not hardcoded branches.

**Enforcement runs in two layers.** Every handler passes through one chokepoint,
`authz.Check(ctx, actor, perm, target)`. List endpoints never rely on it, since
a handler-level check cannot filter rows: the store API takes an actor scope and
applies the `admin_scopes` allow-list as a SQL predicate. A reseller querying
nodes runs a query that cannot return nodes outside their grants even if a
handler omits its check. Single-layer authorization is how panels leak one
reseller's customers to another.

`super_admin` bypasses scope filtering by explicit rule. `readonly` also hits a
blanket write-rejecting middleware, redundant with permissions and cheap.
Resellers see only what `parent_admin_id` and their scopes allow; their
economics arrive in SP8.

### 6.4 What gets audited

All authentication events including failures and lockouts; every admin, role,
and scope change; node create, enroll, revoke, and delete; service changes;
settings writes; and every `antimage-ctl` invocation. Reads are not audited —
too noisy to be useful — except audit exports.

## 7. Node lifecycle

### 7.1 Credential policy

**antimage never stores SSH credentials.** They are supplied for one bootstrap
run, held in memory, and zeroed afterwards. Storing them would make the panel
database a root-key vault for the whole fleet, turning one panel compromise into
total fleet compromise. The cost is that re-provisioning requires re-entering
credentials.

### 7.2 Bootstrap

Bootstrap has two paths, and SSH is the optional one. The panel always offers a
`curl | bash` one-liner carrying an embedded enrollment token, which an admin
runs on the node directly. Many operators will not give a web panel root SSH,
and this means they need not. The SSH path wraps the identical script.

Either path detects distribution and architecture (Debian 11+/Ubuntu 20.04+,
amd64/arm64) and refuses anything else with a clear message rather than
guessing; downloads the agent binary matching the panel version and verifies its
signature; installs to `/usr/local/bin`; writes `/etc/antimage/node.yaml` with
the panel URL, the one-time token, and the pinned panel CA fingerprint; installs
and starts a systemd unit; and disconnects. It is idempotent, so re-running it
is the upgrade path.

Over SSH, host keys are verified trust-on-first-use with confirmation: the
fingerprint is shown to the admin and pinned on approval.
`InsecureIgnoreHostKey` appears nowhere in the codebase.

### 7.3 Enrollment

The panel is its own certificate authority. Its key is generated at first run,
mode `0600`, encrypted under the master key.

The agent generates its keypair locally; the private key never leaves the node
and the panel never sees it. The agent connects to the enrollment endpoint,
validates the panel against the CA fingerprint pinned in `node.yaml` — so a
hijacked DNS record yields nothing — and presents the one-time token with a CSR.
The panel verifies the token is unused, unexpired, and bound to that node id,
signs a client certificate with `CN = node_id`, records the fingerprint, and
burns the token. Tokens expire in 30 minutes by default. Everything afterwards
uses mTLS.

Certificates live one year and auto-renew at the halfway mark over the existing
channel.

**Revocation is an allow-list, not a CRL.** The panel is the only verifier, so it
accepts a connection only when the presented fingerprint matches
`nodes.cert_fingerprint`. Deleting a node locks it out immediately, with no CRL
distribution, OCSP, or stapling.

### 7.4 Steady state

The agent dials out and holds a bidirectional gRPC stream. It opens with
`Hello{node_id, agent_version, adapter descriptors, applied_revision,
doc_sha256}`, then reports heartbeats carrying health and sysinfo, and apply-run
results. The panel pushes `RevisionBump{revision}` and, later, operator
commands.

On a bump the agent calls the unary `GetDesiredSnapshot` and re-verifies the
SHA-256 itself before applying — cheap insurance against a truncated or buggy
response — then runs Observe → Plan → Apply.

Heartbeats run every 30 seconds. Reconciliation triggers on a revision bump, on
agent start, on adapter probe failure, and on a five-minute jittered timer.
Reconnects use exponential backoff with jitter capped at 60 seconds, so a panel
restart is not thundering-herded by 200 nodes.

`nodes.adapter_kinds` caches the adapter descriptors the agent reports in
`Hello`. The panel treats it as observed data, not configuration: it never
appears in the desired document and so never triggers a revision.

`Hello` carries a protocol version. Version skew surfaces as "agent upgrade
required" on the node's row and the panel serves the matching binary, rather
than letting the two sides misbehave subtly.

### 7.5 State machine

`Pending` → `Enrolling` → `Online`, with four terminal-ish states:

- `Degraded` — steps are failing; stderr is surfaced.
- `Integrity` — revision matches but hash does not.
- `Offline` — no heartbeat within three intervals, i.e. 90 seconds.
- `Disabled` — administratively suspended; the stream is refused.

Every transition is audited with a system actor.

## 8. UI shell

React with TypeScript and Vite, built to static assets and embedded through
`embed.FS`; development mode proxies to Vite. TanStack Query handles server
state, which is nearly all state. Tailwind, dark-first and dense: small type,
tight rows, monospace for identifiers and hashes, high information density. The
user is triaging a node at 03:00, not reading a marketing page.

**RTL is enforced mechanically.** Logical properties only (`ms-`, `me-`, `ps-`,
`pe-`, `text-start`), with an ESLint rule and a CI check that fail the build on
`ml-`, `pl-`, `left-`, `text-left`, and on literal strings in JSX. Retrofitting
RTL fails because nobody remembers the rules; a failing build remembers them.
`dir` flips on `<html>`, directional icons mirror, and Persian digits and Jalali
dates pass through one formatting module rather than scattered
`toLocaleString` calls.

SP1 screens: login with TOTP, node list, node detail, admins and roles, audit
log, settings, and my sessions.

Node detail earns its keep: current versus applied revision, a drift indicator,
revision history with actor and reason, the last apply run expanded to per-step
results with disruption level and stderr, and live health. That screen is why
the state machine and `node_apply_steps` exist.

Live status arrives by SSE from panel to browser, degrading to polling. Status
is shown by colour plus shape or label, never colour alone.

Visual identity is settled at implementation time.

## 9. Testing strategy

Separating `Plan` from `Apply` makes the reconciler testable: with a fake adapter
and a fake clock, tests are table-driven — desired × observed → expected plan —
with no processes, files, or network.

**Property tests cover the two invariants that fail quietly.**

- Canonicalization: permuting map insertion order and struct field order must
  never change the SHA-256.
- Convergence: applying a plan and re-planning must yield an empty plan, for
  arbitrary desired/observed pairs.

**Store tests run against real SQLite** with migrations applied, never mocks.
They include the no-op-produces-no-revision case and the monotonicity trigger.

**Authorization matrix**: role × permission × scope, including deliberate
negative tests where a handler check is omitted, proving the store-layer filter
still blocks out-of-scope rows. That test guards reseller isolation.

**Integration**: panel and agent in one process over a real gRPC connection with
the stub adapter. Covers enrollment, bump→converge, offline→reconnect→self-heal,
integrity mismatch, and partial apply leaving `applied_revision` behind.

**End-to-end**: `install.sh` in a Debian container, then enroll and converge.

**Security**: session revocation takes effect immediately; enrollment tokens are
single-use; rate limiting engages; host-key pinning rejects a changed key.

**CI**: `go test ./... -race`, `go vet`, golangci-lint, gosec, the import-graph
rule from §3, frontend typecheck, and the RTL and i18n lint gates.

## 10. Definition of done

SP1 is complete when an operator can:

1. Run the one-liner on a fresh VPS and watch the node enroll and reach `Online`.
2. Create a stub service and watch the revision bump and converge.
3. Kill the agent and see the node go `Offline`.
4. Restart it and watch it self-heal without intervention.
5. Hand-edit the stub's managed file and see drift reported.
6. Delete the node and confirm it is locked out.

All CI gates in §9 pass.

## 11. Out of scope for SP1

Real protocol adapters, user and subscriber management, traffic accounting,
quotas, subscription links, reseller economics, alerting, and backup scheduling.
Each belongs to a later sub-project in §1.

## 12. Licensing hygiene

The reference projects (3x-ui, Rebecca, PasarGuard, vpn-ui,
openvpn_webpanel_manager, L2tp-Gui-Panel) inform functional behaviour, UX, and
operational expectations. **No code, assets, schema, or documentation is copied
from them.** Every implementation in this repository is written original. Reading
a GPL or AGPL project to understand what a feature should do does not encumber
an independent implementation; copying its code does.

CI maintains a license inventory of Go and npm dependencies and fails on
licenses outside an approved list.

antimage declares no license during SP1, and the repository stays private until
the maintainer chooses one. That choice is a prerequisite for any public
release, not for implementation.

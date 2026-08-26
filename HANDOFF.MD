# Antimage — Engineering Handoff

**Written:** 2026-08-26
**Repository:** `C:\Users\amyrm\Downloads\antimage` (POSIX: `/c/Users/amyrm/Downloads/antimage`)
**Remote:** `https://github.com/devprogrmer/antimage.git`
**Branch:** `master` @ `e3e4b6a` · working tree clean · no open PRs
**Go module:** `github.com/amyrm/antimage` · Go 1.25.7

> ⚠️ **`master` is the trunk.** The GitHub default PR base (`sp1-control-plane-spine`)
> is far behind it. Always branch from `master` and set the PR base to `master`
> explicitly, or CI will run against a stale ancestor.

---

## 1. What Antimage is

A desired-state control plane for VPN/proxy fleets — the same product category as
3x-ui (Sanaei), Marzban, Pasargad, Rebecca and vpn-ui, with the explicit goal of
being more capable, more professional and better isolated than any of them.

Three binaries, one repo:

| Binary | Path | Role |
|---|---|---|
| `antimage-panel` | `cmd/antimage-panel` | Control plane: HTTP API, RBAC, SQLite store, embedded web UI |
| `antimage-node` | `cmd/antimage-node` | Node agent: talks gRPC/mTLS to the panel, drives local adapters |
| `antimage-ctl` | `cmd/antimage-ctl` | Operator CLI |

The web UI (Vite + React 19 + Tailwind v4) builds into `internal/panel/webui/dist`
and is `go:embed`-ed into the panel binary. **One binary ships the whole product.**
This was a deliberate decision — Next.js and a microservice split were both
considered and rejected.

### The architectural spine (do not route around this)

```
Observe → Plan → Apply → confirm
```

* The panel computes a **desired document** (`internal/panel/nodes/document.go`,
  currently `DocumentSchemaVersion = 3`) and hands it to the node.
* `CommitNodeChange` is **the only** code path permitted to move
  `desired_revision`. Nothing else may write it.
* Adapters (`internal/node/adapter/*`) own all protocol knowledge. The panel holds
  none: it validates service params against the adapter's published JSON Schema
  (`Caps.ServiceSchema`) and outbound params against `Caps.OutboundSchema`.
* **Adapter-derived UI:** if `adapter.Caps` does not declare a capability, the UI
  must not offer it. Never fake UI support for something the node cannot execute.

### Two-layer authorization (both layers required, always)

1. **`rbac.Check`** — the permission gate. A super admin bypasses **scope only,
   never permission**.
2. **An independent SQL scope predicate** — `store.SubjectScopeSQL` /
   `store.NodeScopeSQL`, bound with `store.ScopeArgs(sc)` which returns
   `[is_super, admin_id]` in that order.

`rbac.Target{Kind: TargetNode}` treats a non-super actor's `NodeIDs` as an
**exhaustive allow-list**.

Distinct from RBAC there is **identity/ownership scoping**, used where a resource
belongs to one operator rather than to a tenant:

* sessions → `WHERE admin_id = ?`
* user presets / service templates → `is_public = 1 OR created_by = ?`, plus a
  `created_by` check on mutation, with an `IsSuper` bypass

These are correct as-is. A previous audit that grepped for `rbac.Perm` flagged 12
preset/template/session routes as "unguarded"; **all 12 were false positives.**
Verify before claiming a hole here.

### Error-shape conventions

* **Reseller resources → 404, not 403** (anti-enumeration).
* **Nodes → 403.**
* Id-addressed handlers must check the **bare permission before the lookup**,
  otherwise the 403-vs-404 difference becomes an enumeration oracle. This was a
  real bug, fixed in the deployment routes.
* `WriteError` emits `{code, message, request_id}`. The `request_id` is the whole
  point of the audit screen's reference filter: an operator quotes the id off a
  failure screen and it resolves to the audit row.

---

## 2. Repository layout

```
cmd/                         three binaries
internal/
  node/
    adapter/                 xray, singbox, wireguard, hysteria2, l2tp
    adapterfactory/          adapter construction + registry
    agent/                   gRPC client, reconcile loop
    enforcement/             quota/expiry enforcement at the edge
    sysinfo/                 host facts reported upstream
  panel/
    audit/ auth/ bulk/ control/ dashboard/ deployment/ devices/
    httpapi/                 all HTTP handlers + router.go
    metrics/ nodes/ notify/ observability/ rbac/ resellers/
    service/ store/ subjects/ subscriptions/ templates/
    webui/                   go:embed target for the built UI
  shared/                    proto, secrets (box.Seal), version
  store/  testutil/
proto/                       buf-managed .proto sources (generated Go is committed)
scripts/                     check-imports.sh, check-rtl.sh, check-tokens.sh, install.sh, release.sh, build-release.sh
test/e2e/                    real-panel + real-agent acceptance suite (build tag: e2e)
web/                         Vite + React 19 frontend
docs/                        architecture, security, tenant isolation, premium roadmap
graphify-out/                generated knowledge graph (gitignored)
```

### Storage

SQLite via `modernc.org/sqlite`, **WAL + STRICT tables**, goose migrations run
inside `store.Open`. 32 migrations, `internal/panel/store/migrations/00001…00032`.

SQLite gotchas that have already bitten this codebase — read before writing a migration:

* `INTEGER PRIMARY KEY` **is** the rowid and is **reused** after a table is
  emptied, unless `AUTOINCREMENT`.
* **NULLs compare as distinct in a UNIQUE index**, which silently defeats
  `ON CONFLICT`. Migration `00030` had to use an expression index over
  `COALESCE(node_id,0), COALESCE(service_id,0)` — a plain composite PK would never
  have fired `ON CONFLICT` for NULL columns, reintroducing a **168× usage
  inflation** bug.
* A table rebuild does **not** carry `sqlite_sequence`; migration `00029`
  stashes and restores it explicitly.

### Accounting model

```
billable = raw × node_coef × service_coef × subject_coef × reseller_coef
```

Coefficients are integer **basis points** (`10000` = ×1.0), applied progressively
via `mulDiv` using `math/bits` to avoid overflow. **Billable is computed at read
time and never stored.** The API returns the factors alongside the result so the
UI renders the derivation rather than recomputing it.

Key files: `internal/panel/nodes/billable.go`, `billable_query.go`,
`quota_billable.go`, `accounting.go`.

### Frontend conventions

* React 19, Vite 8, Tailwind **v4** (CSS-first `@theme`, not a JS config),
  TanStack Query v5, react-router-dom v7, Radix primitives, cmdk, lucide-react,
  cva/clsx/tailwind-merge.
* `web/src/components/ui/*` are shadcn components **rewritten logical-first**.
  Upstream shadcn uses physical utilities (`left-`, `right-`, `pl-`, `mr-`) which
  `check-rtl.sh` rejects. **Do not paste shadcn upstream verbatim** — convert to
  `start`/`end`/`ms`/`me`/`ps`/`pe` first.
* 5 locales at **strict key parity**: `web/src/i18n/{en,fa,ru,zh-CN,ar}.json`.
  `t()` is type-checked against `en.json`. Copied-English values fail the parity
  test (only `app.name` is exempt).
* `web/src/setupTests.ts` polyfills `matchMedia`, `ResizeObserver` and
  `Element.prototype.scrollIntoView` for jsdom. Add polyfills there rather than
  working around jsdom gaps inside components.

---

## 3. Build, test and gate commands

Full local gate set (run all of these before opening a PR):

```bash
go build ./... && go vet ./... && golangci-lint run ./...
```

```bash
go vet -tags e2e ./... && go vet -tags "e2e realruntime" ./...
```

```bash
./scripts/check-imports.sh && ./scripts/check-rtl.sh && ./scripts/check-tokens.sh
```

```bash
go test ./... -race -count=1
```

```bash
cd web && npm run lint && npx vitest run && npm run build
```

Make targets: `test`, `lint`, `build`, `web`, `e2e`, `proto`, `proto-lint`,
`check-imports`, `check-rtl`, `check-tokens`, `sync-install`, `clean`.

### CI jobs (`.github/workflows/ci.yml`)

| Job | Contents |
|---|---|
| `go` | build · check-imports · check-rtl · check-tokens · vet · golangci-lint v2 · `go test ./... -race -timeout 25m` · e2e suite · linux amd64+arm64 cross-compile |
| `realruntime` | downloads real Xray, sing-box, wireguard-tools and Hysteria2, then runs the adapters **against the real binaries**. WireGuard interface lifecycle runs under `sudo` in its own step. |
| `web` | npm ci · oxlint · vitest · vite build · asserts the build leaves the tree clean |

Two build tags — `e2e` and `e2e realruntime` — are **invisible to untagged
vetting**. Run vet under both tags or tagged code rots silently.

### Verified status at `e3e4b6a`

* `tsc -b` → 0 · `oxlint` → 0 · `vite build` → 0
* **122 vitest tests across 13 files**, all passing
* `check-rtl`, `check-tokens`, `check-imports` → clean
* `go build`, `go vet` (untagged + both tag sets) → clean
* `golangci-lint` → 0 issues
* `go test ./...` → green across 39 packages

---

## 4. Non-negotiable working rules

These are standing constraints from the project owner. They are not style
preferences.

1. **No stubs (§77).** No TODO, placeholder, fake success, or hardcoded response
   standing in for required functionality. A linter-flagged "unused" helper means
   **unfinished production code** — wire it up, do not delete it.
2. **Never weaken a failing assertion.** A failing test means the production code
   is unfinished. Fix the code.
3. **Never weaken or remove authorization**, and never create a second
   authorization path. Keep authz in the existing service/authz architecture.
4. **Preserve strict tenant/reseller isolation.**
5. **Mutation-test every guard (§76).** A permission or scope check is not proven
   until reverting it makes a **named** test FAIL. Reference pattern:
   `internal/panel/httpapi/subject_bulk_permission_test.go`.
6. **Never replace real runtime execution with mocks, stubs, source inspection or
   coverage counts.**
7. **Never commit real credentials**, certificates, private keys, tokens,
   passwords, or machine-specific configuration.
8. **Never fake UI support for an adapter capability the node cannot execute.**
9. Keep the **RTL, i18n, RBAC, accessibility and typecheck gates intact.**
10. Everything goes through the service layer. Handlers own HTTP concerns only.

### The recurring failure pattern in this codebase

**Built-and-unreachable code coincides with built-and-unguarded code.** Every
time a feature was found to have no UI client, it also turned out to have a
missing or incomplete authorization check. Confirmed instances:

| Feature | Unreachable | Unguarded |
|---|---|---|
| Dashboard family | SSE queried five nonexistent columns | leaked **fleet-wide** data to any tenant; scoping was described in a comment but never implemented |
| Outbound `params` | no UI | returned upstream `private_key`/`password` to any `outbound:read` holder — a permission the reseller role has |
| Deployment routes | partially wired | 6 handlers, only 2 permission checks; `list` returned every deployment platform-wide |
| `SubjectFilters` (197 lines) + `/api/v2/subjects` | written for each other, never connected | — |
| `audit_log.before_json` / `after_json` | recorded since SP1, never selected | — |
| `UserPreset` / `ServiceTemplate` | emitted Go field names, not snake_case | — |

**When you find dead code here, assume it is also unguarded until you have
checked.** Then check whether the guard, once added, is mutation-tested.

---

## 5. Roadmap status

The plan lives in `docs/premium/IMPLEMENTATION-PLAN.md`.
Product spec: `docs/premium/PRODUCT-SPEC.md`. Matrix:
`docs/premium/FEATURE-MATRIX.md`.

### ✅ Complete and merged

| Phase | What landed | PRs |
|---|---|---|
| **A** — Design system & app shell | Tailwind v4 oklch token set, `@custom-variant dark`, `ThemeToggle` (3-state), real `<Routes>` + `RequirePermission`, RBAC-aware `NAV[]`, command palette (cmdk), `DataTable`, `ConfirmDialog`, `Sheet`/`Dialog`/`Tabs`/`DropdownMenu`, light/dark parity, `check-tokens.sh` gate | #48, #49, #50, #51 |
| **B** — Surface what exists | Reseller HTTP API + UI, node detail restructured into 7 tabs, deployments preview→validate→apply→rollback, alerts, sessions, audit search with before/after, user presets & service templates, `request_id` in every error body, node health history & control-plane metrics | #52–#57 |
| **C** — Accounting attribution & coefficients | Xray `stat.Email` attribution (`subject-%d.svc-%d@antimage`), `service_id` through deltas and rollups, coefficient basis points at 4 levels, `Billable`/`Threshold` with overflow-safe `mulDiv`, quota reset anchor + per-period billable window | #44, #45, #46 |
| **D** — WireGuard & Hysteria2 | Real install/restart; `realruntime` CI job proves it against real binaries; no "not yet implemented" strings remain in `internal/node/adapter/` | #28 |
| **E** — Inbound Studio | `Caps.ServiceSchema`-driven editor, progressive disclosure, schema-validated JSON mode, real-binary Studio test | #41, #43 |

Plus out-of-band security fixes: dashboard tenant-scope leak (#42), outbound
credential disclosure (#47).

### 🟡 Phase F — Outbounds and routing (partially done)

Document schema **v3** exists and ships. Done:

1. ✅ Schema v3 with `Outbounds` and `Routing`, both `omitempty` so a v2-shaped
   node serialises byte-identically.
2. ✅ `Caps.SupportsOutbounds`, `Caps.SupportsRouting`, `Caps.OutboundSchema`,
   with `internal/node/adapter/contract_test.go` enforcing the fail-closed
   invariants (declaring outbounds without kinds or without a schema is a test
   failure; routing without outbounds is a test failure).
3. ✅ Xray and sing-box implementations. WireGuard/Hysteria2/L2TP declare `false`
   and the UI hides outbound features on those nodes. `EgressPanel.tsx` exists.

**Remaining:**

4. ❌ Provider abstraction (§22), multi-location (§23), pools + selection (§24),
   outbound health (§25), failover (§26)
5. ❌ Routing rules ordering, conflict detection, and the simulator (§29)
6. ❌ Outbound coefficients folded into the Phase C computation (§27) — this is
   now **unblocked**, Phase C did the groundwork

> **Do item 2 of §7 below (seal outbound params at rest) BEFORE starting §22.**
> §22 is explicitly about modelling credentials above an outbound, and it would
> multiply an existing at-rest plaintext problem.

### ❌ Phases G–O — not started

| Phase | Content | Depends on |
|---|---|---|
| **G** | Subscription groups, templates, custom hostname, Clash Meta verification | B |
| **H** | Plans and client groups, built on `user_presets` | B |
| **I** | Quota state machine made explicit; configurable warning thresholds | C |
| **J** | API keys and scopes; webhooks with signing, retry, dead-letter | B |
| **K** | Automation engine: triggers and actions | I, J |
| **L** | Audit integrity chaining; revision centre; certificate centre | B |
| **M** | Backup and restore — **documented in `docs/BACKUP-RESTORE.md` but NOT implemented** | L |
| **N** | Topology map, geo enrichment, advanced analytics | C, F |
| **O** | Full E2E + realruntime pass over the whole product workflow | all |

---

## 6. Known bugs and gaps

Ordered by severity. None of these are blocking `master`; all are real.

### 6.1 — Rotate the leaked inference-gateway API key `[security, do this]`

A file `Claude.reg` in the repo root previously contained a **live
inference-gateway API key**. It is no longer present in the working tree and
`*.reg` is in `.gitignore`, and it was **never committed** — but the key sat in a
working directory and should be treated as exposed. **Rotate it.**
Do not reintroduce it anywhere in the repo.

### 6.2 — Outbound `params` are stored unsealed at rest `[security]`

`internal/panel/store/migrations/00025_egress.sql`:

```sql
params TEXT NOT NULL DEFAULT '{}',
```

Redaction **on read** was fixed in #47 (`internal/panel/httpapi/secret_params.go`,
`RedactedValue = "__redacted__"`, `redactParams` fails **closed** on an unknown
schema). But the values themselves are plaintext in SQLite. The CA private key
already uses `box.Seal` (`internal/panel/nodes/ca.go`), and subject secrets use it
via `internal/panel/subjects/subjects.go` — outbounds should use the same
`internal/shared/secrets` box. **Fix before Phase F §22.**

### 6.3 — Three dashboard metrics are hardcoded to zero `[correctness / no-stubs violation]`

`internal/panel/httpapi/dashboard_stream.go`:

* `NodeMetric.CPUPercent` and `RAMPercent` are explicitly assigned `0` (lines
  ~276–277).
* `DashboardMetrics.BandwidthMbps` is declared and never assigned.

The `node_health` table records `load1` and `mem_used` — **neither is a
percentage**, so the conversion needs the host's core count and total memory
(available via `internal/node/sysinfo`). Either compute them properly or remove
the fields; shipping three zeroes as live telemetry is a §77 violation.

### 6.4 — `scripts/build-release.sh` is not executable `[build break]`

```
100644 scripts/build-release.sh     ← wrong
100755 scripts/check-imports.sh
100755 scripts/check-rtl.sh
100755 scripts/check-tokens.sh
100755 scripts/install.sh
100755 scripts/release.sh
```

It documents itself as `./scripts/build-release.sh`, which fails with
`Permission denied` on a fresh Linux clone. Fix with:

```bash
git update-index --chmod=+x scripts/build-release.sh
```

**Note for anyone working on Windows:** `chmod +x` does **not** reach the git
index. Only `git update-index --chmod=+x` does. This has already caused one CI
failure (`check-tokens.sh` committed 100644).

### 6.5 — `check-rtl.sh` produces false positives on prose `[tooling]`

The gate greps for physical Tailwind utility names. It fires on ordinary English
containing those substrings — a test named `"right-to-left"` and the string
`right-0` inside a code comment both tripped it. Both were fixed by rewording, not
by weakening the gate. **This will recur.** If it blocks you, reword the prose or
make the pattern require a Tailwind-class context — do not relax what it checks.

### 6.6 — Four junk files are tracked in git `[cleanup]`

```
fix_alert_tests.sh
openclaw-workspace-state.json
test-output.txt
test-results.txt
```

All are one-off artifacts, all committed. `test-output.txt` and `test-results.txt`
are captured test logs and are **byte-for-byte identical** (`diff -q` confirms).
They also pollute the graphify corpus. Delete and gitignore.

### 6.7 — Documentation drift `[docs]`

* `docs/premium/PHASE-C-ACCOUNTING-PLAN.md` says rollups stay unchanged. That
  decision was **superseded**: node and service dimensions were added to
  `usage_rollups_hourly` / `_daily` in migration `00030`.
* `docs/premium/FEATURE-MATRIX.md` lists Outbound/Routing Studio as "no UI" while
  `web/src/components/EgressPanel.tsx` exists.
* `docs/premium/IMPLEMENTATION-PLAN.md` opens with *"Nothing here is
  implemented"* — five phases later, that line is false.

### 6.8 — `AdapterInfo` has no JSON tags `[latent]`

`AdapterInfo` at `internal/panel/nodes/convergence.go:172` is an internal type with no
JSON tags. It is not currently serialised onto the wire, so nothing is broken —
but the moment it is, it will emit Go field names. This is exactly how the
`UserPreset`/`ServiceTemplate` bug shipped. Add tags before exposing it.

### 6.9 — All four translated READMEs are stuck at SP1 `[docs, user-facing]`

`README.md` describes v1.0.0 with five working adapters. `README.fa.md`,
`README.ru.md`, `README.zh-CN.md` and `README.ar.md` all still carry the SP1
status banner — *"the only adapter shipped is a **stub**… real protocol adapters,
subscriber management, traffic accounting and quotas are explicitly out of scope"*.
Verified: `README.ru.md:10`, `README.zh-CN.md:8`, `README.fa.md:11`.

The `web` CI job enforces **i18n key parity for UI strings**, but **nothing checks
that translated prose tracks the English source**. Four of the five languages a
user can pick therefore advertise a product two years of work out of date. Either
retranslate or add a staleness check.

Related, lower severity: the Go module path is `github.com/amyrm/antimage` while
every README badge and clone URL points at `github.com/devprogrmer/antimage`. The
README instructs `git clone`, so nothing is broken today — but `go install` or
`go get` against the advertised path would fail.

### 6.10 — Documentation contradicts the tree in three places `[docs]`

Surfaced while indexing the corpus, all verified:

* `.claude/PRODUCTION_GAP_REGISTER.md` (GAP-002) says *"No systemd units, Docker
  Compose, or Kubernetes manifests"* and `FINAL-PRODUCTION-VERIFICATION.md` lists
  *"No docker-compose.yml"* as a blocker — `docker-compose.yml` exists at the repo
  root.
* `README.md` states CI/CD is *"Not yet configured"* — `.github/workflows/ci.yml`
  defines three jobs.
* Readiness scores conflict across documents (70/100, 85/100, 89/100). These look
  like different assessment dates rather than an error, but nothing says so.

### 6.11 — One unexplained e2e failure, not reproduced `[unknown]`

A single e2e suite run failed early in the session. It passed on the three
subsequent runs and the output was truncated, so **the failing test is unknown**.
Recorded here rather than dropped. If you see an intermittent e2e failure, capture
the full output — it may be a real flake worth chasing.

---

## 7. Traps that have already cost time

Read this section. Each item is a mistake that was actually made here.

1. **`golangci-lint` is not covered by `go vet`.** `rowserrcheck` failed CI on a
   PR that passed `go build` + `go vet` + the full test suite locally. Install and
   run `golangci-lint` locally before pushing.
2. **Never pipe an exit-code check.** `npx tsc -b | head` reports **`head`'s**
   exit code, not `tsc`'s. This nearly produced a false "typecheck green" claim.
3. **Verify string replacements landed.** A wiring script printed success while
   its replacements silently matched nothing, causing 5 palette tests to fail for
   a completely misleading reason. Use the editing tools, and confirm the diff.
4. **Test harnesses lie too.** `NodeReconciliation.test.tsx`'s fetch stub matched
   exact paths only, so `?limit=100` 404'd, the component rendered its empty
   state, and the test reported it as a component bug. Give fetch stubs a
   bare-path fallback.
5. **Don't compare error bodies byte-for-byte.** Two security tests broke when
   `request_id` was added. They were replaced with `indistinguishableError(t, body)`
   comparing decoded `code|message` — which is **stricter**, because the old
   string equality would have passed on two *empty* bodies.
6. **Guess nothing about locale strings.** Persian "cancel" is `لغو`, not
   `انصراف`. `subjects.create` is "Create User", not "New user".
   `studio.paramsDocument` is "Parameters document". Read the catalogue.
7. **A quota that never enforces is an enforcement hole.** The quota sweeper
   initially skipped subjects with no `quota_period_start`. It now falls back to
   `COALESCE(s.quota_period_start, s.created_at)`.
8. **Mutations that were NOT caught on the first pass** (all now covered — keep
   these in mind when writing guard tests):
   * dropping `ServiceId` in the agent's and the panel's proto conversions
   * repairing rollups at the wrong grain (`TestRepairDoesNotInflateAcrossServices`,
     800 → 1600 bytes)
   * reverting a rollback target to `TargetNone` — permission tests pass either
     way; **only a caller who *has* the permission distinguishes them**
   * a reset sweeper ignoring the stored period length / not advancing
     `quota_period_start`
   * `redactParams` failing **open** on an unknown schema

---

## 8. Suggested next steps

In dependency order, highest value first:

1. **Rotate the exposed API key** (§6.1). Five minutes, and it is a live
   credential.
2. **Seal outbound params at rest** (§6.2) using `internal/shared/secrets`. Small,
   self-contained, and a hard prerequisite for Phase F §22.
3. **Fix or remove the three zeroed dashboard metrics** (§6.3) — currently a §77
   violation sitting on the main screen.
4. **`git update-index --chmod=+x scripts/build-release.sh`** (§6.4) and delete the
   four junk files (§6.6). One trivial commit.
5. **Reconcile the drifted docs** (§6.7, §6.10) so the next agent inherits an
   accurate plan rather than one that says "nothing here is implemented" — and
   **retranslate the four non-English READMEs** (§6.9), which are the most
   visible wrong claim in the whole repo.
6. **Phase F item 6** — fold outbound coefficients into the Phase C computation.
   Phase C already built `Billable`, `Factors` and `mulDiv`; this is the cheapest
   remaining Phase F item and it closes §27.
7. **Phase F items 4–5** — provider abstraction, pools, health, failover, routing
   conflict detection and the simulator. This is the large one; expect several
   PRs.
8. Then **G–O** in the table order above.

---

## 9. Knowledge graph

`graphify-out/` holds a generated knowledge graph of the whole corpus
(gitignored, regenerated on demand):

| File | Contents |
|---|---|
| `graph.html` | interactive graph — open in a browser |
| `GRAPH_REPORT.md` | audit report: god nodes, surprising connections, suggested questions |
| `graph.json` | raw graph data (GraphRAG-ready) |
| `manifest.json` | per-file hashes for incremental rebuild |
| `cache/` | hashed AST + semantic extraction cache |

Refresh with `/graphify . --update`.

**Environment note:** the Python that graphify was originally registered against
(`C:\Users\amyrm\AppData\Local\Programs\Python\Python313`) **no longer exists on
this machine.** The working interpreter is the `uv`-managed one at
`C:\Users\amyrm\AppData\Roaming\uv\tools\graphifyy\Scripts\python.exe`, and
`graphify-out/.graphify_python` has been repointed to it. If graphify fails with
"No such file or directory", that sidecar is stale — re-resolve it.

---

## 10. Where to read next

| Question | File |
|---|---|
| Why is it built this way? | `docs/premium/ARCHITECTURE-DECISION.md` |
| What is the product supposed to be? | `docs/premium/PRODUCT-SPEC.md` |
| What is done and what isn't? | `docs/premium/FEATURE-MATRIX.md` (see §6.7 — partly stale) |
| What is the build order? | `docs/premium/IMPLEMENTATION-PLAN.md` |
| How does tenant isolation work? | `docs/TENANT-ISOLATION.md` |
| Security model | `docs/SECURITY.md` |
| HTTP surface | `docs/API.md`, and `internal/panel/httpapi/router.go` |
| Accounting design | `docs/premium/PHASE-C-ACCOUNTING-PLAN.md` (see §6.7) |
| Backup/restore | `docs/BACKUP-RESTORE.md` — **documented, not implemented** |
| Change history | `CHANGELOG.md`, `docs/implementation-log.md` |

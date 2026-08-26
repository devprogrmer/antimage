# Premium Layer — Implementation Plan

Phases, ordered by dependency rather than by the specification's numbering.
Each phase is one branch and one PR off `master`, with its own tests.

Phases A-E are implemented; Phase F is in progress. This document is the sequence proposed for
approval after the analysis phase (§86).

---

## Rules that apply to every phase

- **Branch `premium/<phase>`, one PR, never push to `master`.**
- **No stubs (§77).** No TODO, placeholder, fake success, or hardcoded response.
  An "unused" helper flagged by the linter means unfinished production code —
  wire it, do not delete it.
- **Mutation-test every guard (§76).** A permission or scope check is not proven
  until reverting it makes a *named* test fail. Pattern:
  `internal/panel/httpapi/subject_bulk_permission_test.go`.
- **Everything through the service layer (§3.3).** Handlers own HTTP concerns
  only. `CommitNodeChange` stays the sole path to `desired_revision`.
- **Adapter-derived UI (§68).** If `adapter.Caps` does not declare it, the UI
  does not offer it.
- **Gates before every PR:**
  ```bash
  go build ./... && go vet ./... && golangci-lint run ./...
  ./scripts/check-imports.sh && ./scripts/check-rtl.sh
  go test ./... -race -count=1
  cd web && npm run lint && npx vitest run && npm run build
  ```
  `-race` needs cgo, unavailable on the current machine — CI covers it, and that
  is stated rather than glossed (§87).

---

## Phase A — Design system and application shell

**Why first:** every later screen depends on it, and it touches no backend, so it
carries no correctness risk.

- shadcn/ui vendored onto the existing Vite + Tailwind build. No new runtime
  dependency; `go:embed` deployment unchanged (§79).
- Replace the `useState` route switch in `web/src/App.tsx` with real routing.
  `react-router-dom` is already a dependency and already used by inner routes.
- RBAC-aware navigation driven by `GET /auth/me`.
- Command palette, detail drawer, confirm dialog, data table with configurable
  and sortable columns, filter bar.
- Light theme brought to parity with dark.

**Exit:** shell renders every existing route; i18n and RTL gates pass; keyboard
navigation works end to end; no visual regression in the five locales.

## Phase B — Surface what already exists

**Why second:** the highest value per unit of risk in the whole plan. All of this
is built and tested at the service layer and unreachable from any UI.

- **Reseller HTTP API** — the engine (`resellers.ProvisionSubject`, credit
  ledger, ownership) has *zero routes*. Add them through the service layer with
  the tenant-scope rules in `docs/TENANT-ISOLATION.md`, and the UI for records,
  balance, ledger and provisioning.
- Node detail tabs backed by existing endpoints: capabilities, adapters,
  maintenance, apply runs, revisions, health history, metrics, reconciliation.
- Deployments: preview → validate → apply → rollback, using
  `POST /deployments/preview` and `/validate`.
- Alerts, sessions, audit search with before/after.
- User presets and service templates.
- Return `RequestID` in error responses. `WriteError` currently emits only
  `{code, message}`, so a failed action gives the operator no id to quote and no
  way to find the matching audit row. Small, self-contained, and a prerequisite
  for the §70 failure screen.

**Exit:** every route in `router.go` is reachable from the UI or explicitly
recorded as intentionally headless. Reseller routes have mutation-tested scope
and permission guards.

## Phase C — Accounting: attribution and coefficients

Implements **AD-2**.

- Migration: `service_id` on `usage_deltas` (nullable, `ON DELETE SET NULL`);
  `usage_coefficient` on `nodes`, `services`, `subjects`, `resellers` as integer
  basis points defaulting to `10000` (×1.0).
- Adapters attribute usage to a service. Xray already tags per-user via
  `stat.Email`; the attribution exists at the edge and is discarded on ingest.
- Billable computed at read time, never stored. API returns the factors with the
  result so the UI renders the derivation rather than recomputing it (§3.1).
- Decide and document whether quota enforces on raw or billable. Recommendation:
  billable, with coefficients defaulting to ×1.0 so no existing deployment
  changes behaviour silently.

**Exit:** §11's worked example renders from real data; rollups unchanged;
accounting tests cover a non-unity coefficient at every level.

## Phase D — WireGuard and Hysteria2 install/restart

Implements **AD-3**. Already tracked as a separate task.

**Exit:** both adapters install and restart against real binaries; the
`realruntime` job covers them; no "not yet implemented" strings remain in
`internal/node/adapter/`.

## Phase E — Inbound Studio

**Depends on D** — an editor that offers a protocol whose adapter cannot apply it
is a fake feature layer.

- Editor driven by `adapter.Caps.ServiceSchema`. Every adapter already publishes
  a JSON Schema, it crosses gRPC, and `nodes.ValidateServiceParams` already
  validates against it. This is UI work on an existing contract.
- Progressive disclosure: basic fields, then transport, then security.
- Schema-validated JSON mode (§13) — the same schema, so raw editing cannot
  bypass control-plane validation.
- Transport and security surfaces limited to what adapters actually emit today:
  TCP, ws, gRPC, TLS, REALITY. **XHTTP, mKCP, HTTPUpgrade and fallbacks are
  adapter gaps** and are not offered until the adapters support them.

**Exit:** creating an inbound for every supported protocol produces a node that
converges; unsupported options are absent, not disabled.

## Phase F — Desired document v3: outbounds and routing

Implements **AD-1**. The largest phase; expect it to span several PRs.

1. Document schema v3 with `Outbounds` and `Routing`, both `omitempty` so a node
   with neither is byte-identical to v2.
2. `Caps.SupportsOutbounds`, `Caps.SupportsRouting`, `OutboundSchema`; a
   `contract_test.go` case asserting a declaring adapter implements them.
3. Xray and sing-box implementations. WireGuard, Hysteria2 and L2TP declare
   `false` and the UI hides outbound features on those nodes.
4. Provider abstraction (§22), pools and selection (§24), health (§25),
   failover (§26).
5. Routing rules, ordering, conflict detection; the simulator (§29).
6. Outbound coefficients fold into the Phase C computation.

**Exit:** an outbound configured in the panel is observable on the node, drift is
detected when it is changed out of band, and the simulator explains a real
selection.

## Phases G–O

Sequenced after the foundations above.

| Phase | Content | Depends on |
|---|---|---|
| **G** | Subscription groups, templates, custom hostname, Clash Meta verification | B |
| **H** | Plans and client groups, built on `user_presets` | B |
| **I** | Quota state machine made explicit, configurable warning thresholds | C |
| **J** | API keys and scopes; webhooks with signing, retry, dead-letter | B |
| **K** | Automation engine: triggers and actions | I, J |
| **L** | Audit integrity chaining; revision centre; certificate centre | B |
| **M** | Backup and restore — currently documented but **not implemented** | L |
| **N** | Topology map, geo enrichment, advanced analytics, §85 features | C, F |
| **O** | Full E2E and realruntime pass over the whole product workflow | all |

---

## Honest scope note

This is a large body of work — realistically several months, not one pass. The
phases are ordered so that value lands early: Phase B alone makes a substantial
amount of already-built, already-tested backend usable, and carries far less
risk than Phase F.

Per §83, the Premium Layer is not complete when the UI looks complete. It is
complete when actions reach the backend, the backend enforces authorisation, the
control plane stays authoritative, nodes reconcile, and the runtime and E2E
suites pass. Anything not delivered will be listed as unsupported rather than
advertised.

# Premium Layer — Product Specification

Information architecture, workflows and design rules for the operator console.

The audience is a professional infrastructure operator running a fleet, not a
casual admin. Density is a feature. Every screen answers "what is true, what
changed, and what can I do about it".

---

## 1. Terminology

The UI must use one word per concept, and it must be the backend's word.
Divergence here is how a dashboard starts lying.

| Backend term | UI term | Never call it |
|---|---|---|
| Subject | User | Client, account |
| Service | Inbound | Config, listener |
| Node | Node | Server (except in prose) |
| Desired revision | Desired | Target |
| Applied revision | Applied | Current |
| Drift | Drift | Out of date |
| Reseller | Reseller | Tenant (tenant is the isolation boundary) |
| Apply run | Apply | Deploy (deployment is the multi-node object) |

"Raw traffic" and "billable traffic" are always labelled. Neither ever appears
unqualified as "traffic" once coefficients exist.

## 2. Information architecture

Navigation is RBAC-filtered: an item is hidden when the actor holds none of the
permissions its screens require. Hiding is cosmetic — the backend remains
authoritative (§74), and a hidden route that is navigated to directly must still
return 403/404 from the API.

```
Overview            dashboard, alerts, fleet health
Nodes               list, detail, desired vs actual, revisions, apply runs
Users               subjects, devices, sessions, connections
Inbounds            service editor per node, adapter-driven
Outbounds     (F)   providers, pools, health          [requires AD-1]
Routing       (F)   rules, simulator                  [requires AD-1]
Subscriptions       tokens, formats, groups, templates
Traffic             raw and billable, per node/user/inbound
Quotas              state, thresholds, resets
Resellers           records, credit ledger, provisioning
Templates           service templates, user presets
Monitoring          metrics, health history
Alerts              triage and remediation
Audit               searchable, before/after
Backups       (M)
Certificates  (M)
Automation    (K)
API           (J)   keys and scopes
Settings
```

Phase letters mark screens that do not exist yet; see IMPLEMENTATION-PLAN.md.

## 3. Core workflows

### 3.1 Every mutation is a four-beat

The spec's Observe → Plan → Apply → Verify is already the backend's shape. The
UI must expose it rather than hide it behind a spinner:

```
Edit  →  Preview (what will change, which nodes, what restarts)
      →  Apply   (revision N created)
      →  Verify  (applied == desired, or drift with a reason)
```

`POST /deployments/preview` and `/deployments/validate` already exist and must
back the preview step. Nothing destructive proceeds without it.

### 3.2 Disruption must be stated before the click

`adapter.Caps.HotUserAdd` is false for sing-box and Hysteria2 — adding a user
restarts the process and drops sessions. The confirm dialog must say so, sourced
from the capability, not from a hardcoded list:

> Adding this user restarts **sing-box** on DE-01. Active sessions will drop.

### 3.3 Explainability (§85)

The defining UX property. Every non-obvious state carries a "why":

| Question | Source |
|---|---|
| Why is this user frozen? | `subjects.frozen_reason`, quota sweeper audit record |
| Why is this counted 2×? | the coefficient factors returned with billable traffic |
| Why is this node unhealthy? | `node_health`, `failed_reconcile_streak`, `last_error` |
| Why did this revision fail? | `node_apply_steps` — per-step result |
| Why can this user reach this service? | `subject_services` grant + tenant scope |

These are answered from records that already exist. The work is surfacing them,
not inventing them.

### 3.4 Failure is a first-class screen

Per §70, a failed action shows what failed, why, request id, node, revision,
adapter, step, and what can be retried.

Most of the material exists. `node_apply_steps` already records
`step_kind`, `disruption`, `outcome`, `error` and `duration_ms` per step, which
covers "which adapter, which step, why". `RequestID` threads through the audit
records.

**One gap:** `RequestID` is *not* returned to the client. `WriteError`
(`internal/panel/httpapi/errors.go:20`) emits only `{error: {code, message}}`,
so an operator reading a failure has no id to quote and no way to find the
matching audit row. Adding it to the error body is a small, self-contained
prerequisite for this screen and belongs in Phase B.

## 4. Design system

**Stack:** shadcn/ui (Radix + Tailwind) on the existing Vite build. Chosen over a
component library with a runtime because shadcn vendors source into the repo —
no new runtime dependency, deterministic builds, and the single-binary embed of
§79 is unaffected.

**Rules**

- Dark is the default; light is fully supported, not an afterthought.
- Density over whitespace. Tables are the primary surface.
- Colour never carries meaning alone — status is glyph + text + colour (§65).
- Destructive actions are never the default focus target.
- Optimistic UI only where the backend cannot refuse. Anything that can 403, 404
  or fail validation shows a pending state and waits.
- Skeletons for first load; never a full-page spinner on refresh.

**Layout primitives:** table with configurable and sortable columns, detail
drawer, modal for confirmation only, side panel for context, wizard for
multi-step creation, command palette.

## 5. Internationalisation

Already enforced by `scripts/check-rtl.sh` and the locale-parity test, and the
existing rules extend to every new screen:

- No literal strings in JSX. All text through `t()`.
- Logical Tailwind utilities only — `ms-`/`me-`/`ps-`/`pe-`/`start-`/`end-`/
  `text-start`/`text-end`. Never `ml-`, `left-`, `text-left`.
- Numbers through `formatNumber`, timestamps through `formatTimestamp`,
  relative time through `formatRelativeTime` — Persian and Arabic digit systems
  and Arabic's leading time marker are handled there, not at call sites.
- `t()` has no interpolation and no plural support. Phrase around it (label plus
  count) rather than concatenating; Russian needs three plural forms and Arabic
  six.
- Five locales stay at parity: en, fa, ru, zh-CN, ar. A key added to one is added
  to all, and the "actually translates" test rejects English copied across.

## 6. Accessibility (§65)

- Every interactive element reachable and operable by keyboard; visible focus.
- Icon-only controls carry `aria-label` (the sort-order toggle in
  `SubjectFilters.tsx` is the pattern).
- Contrast meets WCAG AA in both themes.
- `prefers-reduced-motion` respected.
- Tables use real `<th>` with scope; forms use real `<label>`.

## 7. Security UX (§66)

- Secrets are masked by default and revealed by explicit action, which is
  audited. `GET /subjects/{id}/credentials/{kind}` already audits by kind, never
  by value — the UI must not cache the revealed value.
- Subscription links are credentials. Anywhere one is shown it is labelled as
  such; the Telegram `/config` reply is the reference wording.
- Recovery codes display once. No screen re-renders them.
- No secret in a URL, a query string or a log line.

## 8. Performance (§67)

Targets: 5,000 users, 500 nodes, 10,000 inbounds, large audit logs.

- Server-side pagination, filtering and sorting. `GET /api/v2/subjects` already
  does this and is the template.
- Virtualised rows beyond ~200.
- SSE for live figures (`/dashboard/stream` exists); polling only as fallback.
- Never fetch a full table to count it.

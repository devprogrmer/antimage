# Premium Layer — Architecture Decisions

Three architecture gaps block whole families of the Premium specification. Each
is recorded here with the constraint it runs into, the options considered, and a
recommendation. Nothing here is implemented yet.

---

## AD-1. The desired document cannot express outbounds or routing

### The constraint

`internal/panel/nodes/document.go`:

```go
type Document struct {
    SchemaVersion int       `json:"schema_version"`
    Revision      int64     `json:"revision"`
    NodeID        int64     `json:"node_id"`
    Services      []Service `json:"services"`
    Subjects      []Subject `json:"subjects"`
}
```

That is the entire contract between panel and node. A `Service` is
`{ID, Kind, Enabled, Params}` — an *inbound*. There is no outbound, no routing
rule, no provider, no balancer.

Specification §21–§30 — Outbound Studio, Provider Manager, multi-location,
pools, health, failover, outbound accounting, Routing Studio, simulator and
chaining — all describe state that a node must actually run. None of it can be
represented today.

### Why this cannot be worked around in the panel

The tempting shortcut is to model outbounds panel-side and render them into each
service's existing `Params` blob. That breaks two invariants the project depends
on:

1. **The document stops describing the node.** Drift detection compares observed
   state against the document. Outbounds rendered into `Params` would be
   invisible to `Observe`, so an operator editing `/etc/xray/config.json` by hand
   could change routing and the panel would report "in sync".
2. **Routing is not per-service.** A routing rule matches across inbounds and
   selects an outbound. Encoding it inside one service's params forces either
   duplication across services or an arbitrary owner, and the reconciler has no
   way to order them.

### Decision

**Extend the document. Bump `SchemaVersion` to 3.**

```go
type Document struct {
    SchemaVersion int        `json:"schema_version"`
    Revision      int64      `json:"revision"`
    NodeID        int64      `json:"node_id"`
    Services      []Service  `json:"services"`
    Subjects      []Subject  `json:"subjects"`
    Outbounds     []Outbound `json:"outbounds,omitempty"`
    Routing       *Routing   `json:"routing,omitempty"`
}
```

`omitempty` on both is deliberate and load-bearing: a node with no outbounds and
no routing must produce **byte-identical** output to schema v2, so existing nodes
do not see a spurious checksum change and reconcile for no reason. The canonical
ordering invariant (§ "invariant 3" in `buildSubjects`) applies to the new
collections too — sort by id.

### Consequences

- **Adapter contract grows.** Each adapter must decide whether it supports
  outbounds and routing, and declare it. Add to `adapter.Caps`:
  `SupportsOutbounds bool`, `SupportsRouting bool`, and an `OutboundSchema`
  alongside the existing `ServiceSchema`. Adapters that declare `false` must be
  sent a document with those fields omitted, and the panel must refuse to attach
  an outbound to a node whose adapters cannot apply one — the same fail-closed
  posture as `RequiresPKI`.
- **Xray and sing-box can implement this; WireGuard, Hysteria2 and L2TP largely
  cannot.** The UI must derive availability from `Caps`, per §68, rather than
  offering outbounds everywhere.
- **Backward compatibility.** Agents on the old schema must keep working. The
  node reports its supported schema version at Hello; the panel builds the
  highest document version that node understands. This is the same negotiation
  the existing `SchemaVersion` field was introduced for.
- **`contract_test.go` must gain a case** asserting an adapter that declares
  `SupportsOutbounds` actually implements the outbound path, mirroring
  `TestSelfAccountingMatchesTheImplementation`.

### Rejected

- *Panel-side rendering into `Params`* — breaks drift detection, as above.
- *A separate outbound document* — two documents means two revisions and no
  atomic "this is the node's intended state", which is the property the whole
  design rests on.

---

## AD-2. Accounting cannot express coefficients

### The constraint

`internal/panel/store/migrations/00011_accounting.sql`:

```sql
CREATE TABLE usage_deltas (
    id          INTEGER PRIMARY KEY,
    node_id     INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    subject_id  INTEGER NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
    sequence    INTEGER NOT NULL,
    uplink_bytes   INTEGER NOT NULL CHECK (uplink_bytes >= 0),
    downlink_bytes INTEGER NOT NULL CHECK (downlink_bytes >= 0),
    created_at  INTEGER NOT NULL,
    UNIQUE (node_id, sequence)
) STRICT;
```

Two problems for §11 and §27:

1. **No coefficient exists anywhere in the schema or the code.** A repo-wide
   search for `coefficient`, `multiplier` and `billable` returns nothing.
2. **No `service_id`.** Usage is attributed to `(node, subject)` only. An
   *inbound* coefficient cannot be applied to traffic that is not attributed to
   an inbound. The data to compute it is not being recorded.

Note the Xray adapter already tags usage per user via `stat.Email`
(`xray/accounting.go`), and that email is per-inbound in Xray's model — so the
information exists at the edge and is being discarded on the way in.

### Decision

**Two-part migration, in this order.**

**Part 1 — attribute usage to a service.** Add `service_id INTEGER REFERENCES
services(id) ON DELETE SET NULL` to `usage_deltas`. Nullable, because historical
rows cannot be back-attributed and inventing an attribution would corrupt the
ledger. `SET NULL` rather than `CASCADE` for the same reason the credit ledger
uses it: deleting an inbound must not erase the record that traffic was billed
through it.

**Part 2 — coefficients as first-class columns**, one per level the spec names:

| Level | Column |
|---|---|
| Node | `nodes.usage_coefficient` |
| Service (inbound) | `services.usage_coefficient` |
| Subject (user) | `subjects.usage_coefficient` |
| Reseller | `resellers.usage_coefficient` |
| Outbound | deferred to AD-1 |

Stored as integer basis points (`10000` = ×1.0), never floats. The credit ledger
already refuses floats for money and states why; billable traffic is money.

### Billable computation

Raw bytes stay authoritative and immutable. Billable is **derived, never
stored** — the same discipline as the reseller balance, which is `SUM(delta)`
and never a cached column. A stored billable figure would drift the moment a
coefficient changed, and would silently rewrite history.

```
billable = raw
         × node_coefficient
         × service_coefficient
         × subject_coefficient
         × reseller_coefficient
```

§11 requires the calculation be shown, not hidden. The API must return the
factors alongside the result so the UI can render the derivation rather than
recomputing it client-side (§3.1: the frontend must not invent authoritative
state).

### Consequences

- Quota enforcement must decide **which** figure it enforces on. Recommendation:
  quota is billable, since that is what the operator sold. This is a behaviour
  change for existing deployments and must be called out in release notes, with
  coefficients defaulting to ×1.0 so nothing changes until an operator sets one.
- Rollups (`usage_rollups_hourly`/`_daily`) aggregate raw. Billable is computed
  at read time from the rollup plus the coefficients in force.

---

## AD-3. Two adapters cannot install or restart

### The constraint

`wireguard/apply.go` and `hysteria2/apply.go` return, for install and restart:

```
"install requires desired service context (not yet implemented)"
```

In Hysteria2 the real implementation exists but is unreachable —
`applyWithDesired` → `writeConfigAndApply` → `recordApplied` is never called.
`adapter.Step` carries `Payload json.RawMessage`, which looks like the intended
carrier for desired state, and neither adapter uses it.

### Decision

Fix before any Inbound Studio ships. An editor that offers WireGuard or
Hysteria2 would create services that can never be applied — a fake feature layer,
which §77 forbids.

Already tracked as a separate task. The work is to thread desired state to
`Apply` following whichever pattern sing-box and Xray already use, not to invent
a third.

---

## AD-4. Database backends (§78)

### Decision

**Remain SQLite-only. Record the gap; do not abstract the store.**

§78 says not to introduce a migration framework blindly and to document rather
than pretend. The current design is coherent and load-bearing:

- WAL mode via `modernc.org/sqlite` — pure Go, no cgo, which is what keeps the
  single static binary of §79
- `STRICT` tables — type enforcement SQLite does not otherwise give
- A single write connection with `Store.Write(ctx, fn)` serialising transactions.
  Several correctness arguments in the codebase depend on this, including
  `audit.InTx` vs `audit.BestEffort` — `BestEffort` takes the write connection
  and would deadlock if called from inside a write transaction.

### What supporting PostgreSQL would actually cost

Recorded so the decision can be revisited with real numbers rather than
optimism:

1. 24 goose migrations use SQLite-specific syntax (`STRICT`, table-rebuild
   `ALTER` patterns in `00022`).
2. The single-writer assumption disappears; every "this is safe because only one
   writer exists" argument needs re-derivation, and the deadlock rule above
   becomes a different rule.
3. `INTEGER PRIMARY KEY` rowid semantics, `COLLATE NOCASE`, and
   `strftime`-flavoured time handling all differ.
4. Two dialects means two sets of migrations and a compatibility test matrix.

None of this is impossible; it is simply not free, and no requirement currently
justifies it. A deployment large enough to need Postgres is the trigger to
reopen this.

---

## Cross-cutting: no shadow architecture (§3.3)

The service layer (`internal/panel/service/`) is already the shared orchestration
path, and both HTTP and the Telegram bot use it. Every premium surface —
reseller API, outbound management, routing, webhooks, automation — must enter
through it. Specifically:

- Handlers own HTTP concerns only. Permission checks, tenant scope, transactions,
  republishing and audit live in the service.
- `CommitNodeChange` remains the only path that may bump `desired_revision`.
- New guards get a mutation test: reverting the guard must make a **named** test
  fail (§76). This is already the standard used in
  `subject_bulk_permission_test.go` and `subjects_bulk_schema_test.go`.

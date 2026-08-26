# Phase C — Accounting attribution and coefficients: migration plan

Implements **AD-2**. Read alongside `ARCHITECTURE-DECISION.md` §AD-2 and
`IMPLEMENTATION-PLAN.md` §Phase C.

This plan revises those in one respect: **Phase C cannot start where they assume
it starts.** Two defects in the existing ingest path were confirmed by running
the code, and coefficient work built on top of either would produce wrong bills.
They are Phase C0 below.

---

## C0. Two blockers, confirmed by execution

Both were found by running a probe against a real store, not by reading. Every
existing accounting test uses a **single-subject** report, which is why neither
surfaces today.

### C0.1 — A report carrying more than one subject is rejected in full

`usage_deltas` carries `UNIQUE (node_id, sequence)` (migration `00011`), and
`IngestUsageReport` inserts **one row per sample** under a single sequence
(`internal/panel/nodes/accounting.go:43`). The second sample collides:

```
insert usage delta: constraint failed: UNIQUE constraint failed:
usage_deltas.node_id, usage_deltas.sequence (2067)
rows=0 alice_used=0 bob_used=0   (expected rows=2 alice=200 bob=400)
```

The insert runs inside `st.Write`, so the whole transaction rolls back: not a
partial write but total loss of the report, silent to the node, which has
already advanced its counters. A node reports every user it serves in one
message, so on any node with two or more active users this is the normal path.

The idempotency key is right in intent — `(node_id, sequence)` is what makes
at-least-once delivery exact — but it is enforced on the wrong grain. Uniqueness
belongs on `(node_id, sequence, subject_id)`, with the "have we seen this
sequence" guard that already precedes the loop remaining the idempotency check.

**This gets worse under Phase C, not better.** Once usage is attributed per
service, one subject on two inbounds produces two rows in one report, so the
collision would occur even for a single-user node.

### C0.2 — `RollupHourly` is not idempotent

It selects `WHERE created_at < ?` with **no lower bound** and merges with
`ON CONFLICT DO UPDATE SET x = x + excluded.x`. Running it twice over the same
deltas counts them twice:

```
rollup total after 1 run=100, after 2 runs=200
```

Nothing in the sweeper prevents a second run over an overlapping window — a
restart mid-hour, a retry, a manual invocation. `PruneUsageDeltas` only removes
rows past a retention cutoff far longer than an hour, so it is not the guard.

This matters more under AD-2 than it does now. AD-2 specifies billable is
computed at read time **from the rollup**; a doubled rollup silently doubles a
customer's bill, and because raw deltas are pruned on a retention schedule the
error eventually becomes unreconstructable.

The fix is a watermark: record the highest `created_at` (or delta `id`) folded
into rollups and aggregate the half-open interval above it, so re-running is a
no-op rather than an addition.

**Exit for C0:** a multi-subject report ingests completely; `RollupHourly` run
twice over identical input leaves identical totals. Both proven by tests that
fail before the fix.

---

## C1. Migration

One file, `00026_accounting_coefficients.sql`, in the project's existing goose
Up/Down form.

### Up

```sql
-- +goose Up

-- Part 1: attribute usage to a service.
ALTER TABLE usage_deltas
    ADD COLUMN service_id INTEGER REFERENCES services(id) ON DELETE SET NULL;

CREATE INDEX usage_deltas_service ON usage_deltas (service_id, created_at);

-- Part 2: coefficients as integer basis points. 10000 = x1.0.
ALTER TABLE nodes     ADD COLUMN usage_coefficient INTEGER NOT NULL DEFAULT 10000;
ALTER TABLE services  ADD COLUMN usage_coefficient INTEGER NOT NULL DEFAULT 10000;
ALTER TABLE subjects  ADD COLUMN usage_coefficient INTEGER NOT NULL DEFAULT 10000;
ALTER TABLE resellers ADD COLUMN usage_coefficient INTEGER NOT NULL DEFAULT 10000;
```

Four properties this relies on:

- **`service_id` is nullable.** Historical rows cannot be back-attributed, and
  inventing an attribution would corrupt the ledger. A NULL means "recorded
  before attribution existed", which is a true statement; any default would be a
  false one.
- **`ON DELETE SET NULL`, not `CASCADE`.** Deleting an inbound must not erase the
  record that traffic was billed through it — the same reasoning the credit
  ledger uses.
- **Basis points, never floats.** The credit ledger already refuses floats for
  money and says why. Billable traffic is money. `10000` keeps ×0.0001
  resolution in integers, and the product of four of them is exact.
- **`DEFAULT 10000` is what makes this migration behaviour-preserving.** Every
  existing row bills at ×1.0, so no deployment changes until an operator sets a
  coefficient.

No `CHECK (usage_coefficient >= 0)` is proposed for the same reason `STRICT` is
used elsewhere rather than a wall of constraints: the write path validates and
the API is the only writer. If that changes, the constraint should be added then.

### Down

```sql
-- +goose Down
ALTER TABLE resellers DROP COLUMN usage_coefficient;
ALTER TABLE subjects  DROP COLUMN usage_coefficient;
ALTER TABLE services  DROP COLUMN usage_coefficient;
ALTER TABLE nodes     DROP COLUMN usage_coefficient;
DROP INDEX IF EXISTS usage_deltas_service;
ALTER TABLE usage_deltas DROP COLUMN service_id;
```

### Atomicity

SQLite executes DDL transactionally, and goose wraps a migration in one
transaction, so the six `ALTER`s land together or not at all. There is no
partially-migrated state to recover from and no online backfill to coordinate —
this is the payoff of choosing "nullable plus default" over "backfill then
tighten". Every statement is metadata-only; none rewrites a table.

The `ALTER TABLE ... DROP COLUMN` statements require SQLite ≥ 3.35. Migration
`00025` already uses `DROP COLUMN` in its Down, so this introduces no new floor.

### Rollback

The honest characterisation is **structurally clean, semantically lossy**.

Down restores the previous schema exactly. What it does not restore is data:
dropping `usage_deltas.service_id` discards attribution recorded while the
migration was live, and dropping the coefficient columns discards operator
configuration. Rolling back after a coefficient has been set and traffic has
been billed under it means the next forward migration starts from ×1.0 with no
record that anything else was ever in force.

That is acceptable for a schema rollback during deployment and **not** acceptable
as an operational undo. The distinction should be in the release notes in those
words. Two consequences:

1. Rollback is safe only while all coefficients are still ×1.0 — that is, before
   an operator has configured anything. There is a cheap check for this:
   `SELECT COUNT(*) FROM nodes WHERE usage_coefficient <> 10000` and the same for
   the other three tables. Worth shipping as a preflight note.
2. Past that point, the recovery path is a restore from backup, not a Down
   migration. `BACKUP-RESTORE.md` is the procedure.

---

## C2. Ingest attributes usage to a service

The Xray adapter already tags usage per user via `stat.Email`, and that email is
per-inbound in Xray's model (`xray/accounting.go`). The attribution exists at the
edge and is discarded on the way in. C2 stops discarding it: `UsageDelta` gains
a `ServiceID`, the parser resolves the inbound tag to a service id, and
`IngestUsageReport` writes it.

Unresolvable tags write NULL rather than failing the report. A tag can outlive
the service it named — an inbound removed while a report was in flight — and
losing an entire node's accounting because one tag no longer resolves trades a
small attribution gap for a large data loss.

Depends on C0.1: without the uniqueness fix, per-service rows guarantee the
collision described above.

## C3. Billable computed at read time

```
billable = raw × node_coef × service_coef × subject_coef × reseller_coef
```

Never stored — the same discipline as the reseller balance, which is `SUM(delta)`
and never a cached column. A stored billable figure drifts the moment a
coefficient changes and silently rewrites history.

Arithmetic is integer throughout: four basis-point factors multiplied into a
`raw × k₁k₂k₃k₄ / 10000⁴` expression. At `int64`, `raw` up to ~10¹⁴ bytes with
all four coefficients at ×1.0 is safe, but the intermediate product overflows
well before that with large coefficients. Divide progressively rather than
multiplying all five terms first, and add a test at the boundary — an overflow
here is a bill, not a rendering artifact.

§11 requires the calculation be **shown, not hidden**. The API returns the four
factors alongside the result so the UI renders the derivation; §3.1 forbids the
frontend recomputing authoritative state.

## C4. Quota: raw or billable — a decision with a hidden cost

AD-2 recommends billable, "since that is what the operator sold". That is right,
and the plans do not note what it costs.

Quota is enforced by `QuotaEnforcementSweeper`
(`internal/panel/nodes/quota.go:33`) on `quota_used_bytes >= quota_bytes`.
`quota_used_bytes` is a **stored counter incremented at ingest**
(`accounting.go:52`) and it holds **raw** bytes. So:

- Enforcing on billable while the counter holds raw means the sweeper's predicate
  is comparing the wrong quantity, and coefficients would have no effect on
  enforcement — the exact class of bug that made `max_quota_bytes` decorative in
  the reseller engine, where a number was recorded somewhere other than the state
  the decision reads from.
- Recomputing billable per sweep from rollups is correct but turns a single
  indexed predicate into a join across four coefficient tables on every pass.
- Storing billable in the counter contradicts AD-2's "derived, never stored", and
  reintroduces the drift-on-coefficient-change problem.

**Recommendation:** keep `quota_used_bytes` raw and authoritative, and have the
sweeper compare against a **coefficient-adjusted threshold** rather than an
adjusted usage figure — `quota_used_bytes >= quota_bytes × 10000⁴ / (k₁k₂k₃k₄)`.
It is algebraically the same comparison, keeps the stored counter raw and
immutable, needs no recomputation of history, and leaves the predicate indexable.

Note the behaviour this implies and put it in the release notes: changing a
coefficient retroactively changes when an existing subject hits quota, because
the threshold moves rather than the usage. That is the correct semantics —
the operator changed the price — but it will surprise someone.

## C5. Rollups

`usage_rollups_hourly` and `_daily` were extended with node_id and service_id
dimensions in migration 00030. The original statement that rollups remain
unchanged was superseded: rollups were extended with node_id and service_id dimensions in migration 00030.
Billable is computed at read time from the rollup plus the coefficients in force.
No migration touches them.

Depends on C0.2. Billable-from-rollup is only as sound as the rollup.

---

## Sequence and exit

| Step | Blocks | Exit |
|---|---|---|
| C0.1 uniqueness grain | C2 | multi-subject report ingests completely |
| C0.2 rollup watermark | C5, C3 | a second run leaves totals unchanged |
| C1 migration | C2, C3 | schema present; all coefficients ×1.0; no behaviour change |
| C2 attribution | C3 | usage carries `service_id`; unresolvable tags NULL, report survives |
| C3 billable | C4 | §11's worked example renders from real data |
| C4 quota decision | — | documented; non-unity coefficient moves the freeze point |

**Phase C exit (from IMPLEMENTATION-PLAN.md), unchanged:** §11's worked example
renders from real data; rollups unchanged; accounting tests cover a non-unity
coefficient at every level.

Add to it: a test per C0 defect that fails before its fix, and an overflow test
at the `int64` boundary in C3.

## Recommended PR split

1. **C0** — the two ingest defects. Self-contained, no schema change, ships
   independently of Phase C and should, because C0.1 is live data loss today.
2. **C1** — migration alone, with `10000` everywhere and no reader. Provably a
   no-op; easy to review and to revert.
3. **C2** — attribution through the adapter and ingest.
4. **C3 + C4** — billable computation, the API shape that returns the factors,
   and the quota threshold change. These land together because C4's behaviour
   change is only meaningful once C3 exists.

Keeping C1 alone is what makes rollback cheap: the window in which Down is
lossless is exactly the window before C2 writes attribution and an operator sets
a coefficient.

# SP3 design decisions — accounting and quotas

Status: accepted, 2026-08-19.
Scope: delta ingestion, cross-node dedup, rollups, quota enforcement, resets,
auto-freeze. The roadmap gives SP3 one line; this record is the spec.

Every decision below is grounded in behaviour observed from a real Xray binary,
not from documentation. SP2 shipped two defects that came from reasoning about
what a proxy "should" accept, and both survived a full test suite because the
tests encoded the same assumption as the code.

## What the real runtime actually does

Established by running Xray 26.3.27 and pushing real traffic through a real
VLESS tunnel:

```
user>>>subject-1@antimage>>>traffic>>>uplink       78
user>>>subject-1@antimage>>>traffic>>>downlink     20116
```

Facts this pins down:

- Counters are **per user, keyed by the inbound's `email` field**, which is
  already `subject-<id>@antimage` from SP2. Accounting needs no new identifier.
- Counters are **cumulative since process start**, and Xray restarts lose them.
  SP2 restarts Xray on every revocation and every shape change, so resets are
  routine, not exceptional.
- Stats require config antimage does not currently generate: `stats{}`,
  `api{services:["StatsService"]}`, a `dokodemo-door` API inbound, a routing
  rule binding that inbound to the api outbound, `policy.levels.0.statsUser*`,
  and at least one real outbound.

That last point makes two SP2 limitations blocking rather than cosmetic, and
SP3 closes both:

- **outbounds**: without one, traffic never egresses. A node could bind and
  authenticate while proxying nothing. SP3 generates a `freedom` outbound.
- **API inbound**: with it, `ExecRuntime.APIAddress` is non-empty, so
  `Caps.HotUserAdd` becomes true in production. SP2's hot-add path exists but
  has never been reachable on a real node.

## Decision 1 — the node computes deltas; the panel never sees a raw counter

**Chosen.** The agent reads cumulative counters, keeps a per-user cursor on
disk, computes deltas itself, and ships deltas. The panel stores and sums
deltas and never reasons about a proxy's counter.

Rejected: querying with Xray's `reset: true`, which returns the delta and
zeroes the counter atomically. It is simpler and it is wrong for billing —
the reset is committed the instant the query returns, so a report that fails
in flight destroys that traffic with no way to recover it. Traffic that
silently vanishes is worse than traffic counted late.

Rejected: shipping cumulative values and differencing panel-side. It forces
the panel to model per-process counter generations for every adapter, and it
breaks the moment two adapters disagree about what a counter means.

**Restart detection.** A counter that moves backwards means the process
restarted. The delta is then the new absolute value, not a negative number.
Traffic accrued between the last poll and the restart is unrecoverable — no
polling design can recover it — so the poll interval bounds the loss and is
deliberately short.

**Durability.** Deltas are persisted on the node before they are sent and
cleared only when the panel acknowledges them. An agent restart, a stream
drop, or a panel outage delays accounting; none of them lose it.

## Decision 2 — usage reporting is an optional adapter capability

**Chosen.** A separate interface, implemented only by adapters that can
account for themselves:

```go
type UsageReporter interface {
    Usage(ctx context.Context) ([]UsageSample, error)
}
```

`Caps.SelfAccounting` — which has existed unused since SP1 Task 14 — declares
it, and the agent type-asserts. Xray implements it; sing-box does not.

Rejected: adding `Usage` to the `Adapter` interface. sing-box, OpenVPN and
L2TP would need stub methods returning nothing, and a stub that returns no
samples is indistinguishable from a user with no traffic. SP5 and SP6 account
through completely different mechanisms (management interface, nftables); an
interface that pretends they are all the same would leak into every one.

## Decision 3 — at-least-once delivery, made exact by an idempotency key

**Chosen.** Every report carries `(node_id, sequence)` where sequence is
monotonic per node. The panel records applied sequences and ignores a repeat.

This is what "cross-node dedup" in the roadmap actually requires. A subject on
three nodes has three independent delta streams that must be **summed**, not
deduplicated — they are genuinely different traffic. The duplication risk is a
*retry* of one node's report after an ambiguous failure, and a per-node
sequence removes it exactly. Deduplicating by subject across nodes would
silently undercount every multi-node user.

## Decision 4 — quota is enforced panel-side, by omission

**Chosen.** Reuse SP2 decision 2 unchanged. A subject over quota is frozen:
`enabled` goes to 0, `frozen_at` and reason are stamped, the ordinary
convergence path removes them from every node's document, hash-verified and
audited.

The alternative — pushing quotas to nodes for local enforcement — needs every
adapter to implement enforcement, and a node partitioned from the panel would
enforce a stale quota forever. Accepted limitation, identical to expiry: an
offline node keeps serving until it reconnects.

**Freezing is restart-class on Xray**, because it is a revocation, and SP2
proved a revoked user survives on the running process otherwise.

## Decision 5 — resets are an explicit timestamp, not a computed calendar

**Chosen.** Each subject carries `quota_bytes`, `quota_used_bytes` and
`quota_reset_at`. A sweep past `quota_reset_at` zeroes usage, advances the
timestamp by the period, unfreezes anyone frozen for quota alone, and audits it.

Rejected: deriving the window from a calendar rule at query time ("this
month"). It makes historical answers depend on when they are asked, cannot
express "reset on the 3rd because that is when they paid", and turns every
quota query into date arithmetic against a moving clock.

Advancing a stored timestamp is idempotent, auditable, and answers "when does
my quota reset" with a column rather than a computation.

## Decision 6 — raw deltas are kept briefly, rollups indefinitely

**Chosen.** Three tiers: raw deltas for recent forensics, hourly rollups for
charts, daily rollups for billing history. Raw deltas are pruned on a retention
window; rollups are not.

Keeping raw per-poll rows forever would dominate the database on a busy fleet
and answer no question that an hourly rollup does not. Keeping only a running
total would make "why was this user billed for 400GB" unanswerable, which is
the question that actually gets asked.

## Invariants SP3 adds

10. A usage delta is applied at most once, keyed by `(node_id, sequence)`.
11. Usage is never decreased by ingestion; a backwards counter is a restart,
    not a credit.
12. Only the quota sweeper may freeze for quota, and only `CommitNodeChange`
    may publish the resulting document.

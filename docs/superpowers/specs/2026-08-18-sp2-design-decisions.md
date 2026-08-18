# SP2 design decision record — Xray/sing-box adapter

Date: 2026-08-18
Status: accepted
Author: engineering (autonomous)

SP1 shipped the control-plane spine and deliberately left three decisions to
SP2 because they could not be settled without a real adapter in front of them.
This record settles them. Each was chosen for the least future migration risk
across SP3 (accounting) and SP4 (subscription delivery), which both consume
whatever SP2 decides.

---

## Decision 1 — credentials are STORED SEALED, not derived

**Chosen:** generate credential material with `crypto/rand`, seal it with
AES-256-GCM under the existing master key (`internal/shared/secrets`), and
store the ciphertext in `subject_credentials`. A per-credential `rotation`
counter allows rotating one credential without touching any other.

**Rejected:** deriving credentials deterministically, e.g.
`uuid = HKDF(master_seed, subject_id, inbound_id)`, storing nothing.

Derivation is attractive because the database then holds no credential
material at all. But sealing already achieves that property — a leaked backup
without the master key yields nothing either way — so derivation buys no
additional confidentiality while costing three things that matter:

1. **Blast radius on key loss.** The master key is already documented as
   unrecoverable and must be backed up separately. Today losing it costs the
   CA key and TOTP secrets. Under derivation it would additionally invalidate
   *every user's connection credentials fleet-wide, simultaneously* — every
   client reconfigured by hand. Sealing keeps the loss bounded to what is
   already bounded.

2. **Import is impossible.** Adoption means migrating from an existing 3x-ui,
   Marzban or PasarGuard deployment, which means preserving the UUIDs users
   already have in their clients. A derived credential is a pure function of
   its inputs and cannot be set to an arbitrary pre-existing value. That alone
   disqualifies derivation for a panel that wants to be chosen over the
   incumbents.

3. **Rotation needs stored state anyway.** Rotating one user's credential
   under derivation requires a per-subject generation counter as an extra
   derivation input — which must be persisted. Once state is stored, the
   simplicity argument for derivation is gone.

**Consequences for later sub-projects.** SP4 renders subscription links by
unsealing on demand, which requires the panel to hold the master key at
request time — it already does, for TOTP. SP3 correlates accounting deltas by
`subject_id`, never by credential value, so credential rotation does not break
accounting continuity. Both are unaffected by this choice, which is the point.

**Security note.** Credential plaintext exists only in memory, during document
assembly and subscription rendering. It is never logged, never audited, and
never returned by a list endpoint — only by an explicit single-credential
reveal, which is itself audited.

---

## Decision 2 — expiry is enforced PANEL-SIDE by omission

**Chosen:** an expired subject is omitted from the desired document. The
revision bumps, the node converges, and the user is removed from the running
configuration by the same machinery that applies every other change.

**Rejected:** encoding an expiry timestamp into the generated protocol config
and letting the proxy enforce it.

The whole SP1 architecture is "the panel publishes desired state; the node
converges toward it". Expiry expressed as *a change in desired state* inherits
every property that machinery already has and that SP1 already tests:

- the revision bumps exactly once, through `CommitNodeChange`;
- the change is audited with an actor and a reason;
- convergence is verified by hash, so a node that fails to remove the user is
  visibly non-converged rather than silently still serving them;
- drift detection notices if someone re-adds the user by hand.

Config-side expiry has none of that. It also couples the panel to per-protocol
semantics — Xray has no uniform per-user expiry across inbound types — which
is exactly the coupling the adapter contract exists to prevent.

**Mechanism.** A panel-side sweeper (`subjects.ExpirySweeper`) runs on a timer,
finds subjects whose `expires_at` has passed and are still enabled, and routes
the disable through `CommitNodeChange` for each affected node. Going through
the chokepoint is mandatory: it is the only path allowed to bump
`desired_revision`, and it is what makes the expiry auditable.

**Accepted limitation, documented rather than hidden.** A node that is offline
when a subject expires keeps serving that subject until it reconnects. This is
inherent to any pull-based agent architecture — an unreachable node cannot be
updated by any mechanism — and the node's `offline` status makes the condition
visible to an operator. SP3 may add a node-local expiry backstop; it is not
needed for correctness here and would duplicate authority over the same fact.

---

## Decision 3 — disruption is per-adapter and declared by capability

**Chosen:**

| Change | Xray | sing-box |
|---|---|---|
| Add a user | `DisruptNone` via the Xray gRPC HandlerService | `DisruptRestart` |
| Remove or revoke a user | `DisruptRestart` | `DisruptRestart` |
| Enable or disable an inbound | `DisruptRestart` | `DisruptRestart` |
| Change port, protocol, or TLS | `DisruptRestart` | `DisruptRestart` |
| Rewrite config with no live change | `DisruptReload` | `DisruptReload` |

**Addition and removal are not symmetric, and treating them as one row is a
security bug.** Xray keeps serving a user until it is explicitly told to stop;
deleting their credential from the config file has no effect on an established
session. A revocation planned as a cheap, `DisruptNone` change would therefore
rewrite the file without the revoked user, report the plan converged, and leave
that user connected indefinitely — while the panel showed them revoked.

The hot path is consequently taken only when a change is *purely additive*: the
inbound's shape is unchanged, the runtime declares `HotUserAdd`, and nobody is
being removed. Answering the last question needs state a checksum cannot carry,
so the `.applied` sidecar records the user set the runtime was last restarted
with, not just the configuration hash. An unreadable or missing sidecar counts
as "unknown" and forces a restart, which is the safe direction.

Regression tests: `TestRevokingAUserActuallyReachesTheRuntime` in both adapter
packages, and `TestSP2RevocationReachesTheRunningProxy` end to end.

Xray declares `Caps.HotUserAdd: true`; sing-box declares `false`.

That capability flag already exists in the Task 14 `Descriptor` and was added
for precisely this asymmetry. Declaring it is not decoration: the panel records
it in `nodes.adapter_kinds` at Hello, so the UI can tell an operator *before*
they click that adding a user to this node will or will not drop sessions.

**Why the asymmetry is real and not laziness.** Xray exposes a gRPC
HandlerService with `AlterInbound`/`AddUser`/`RemoveUser`, so a user can be
added to a running instance without touching the config file or the process.
sing-box has no equivalent stable management API for user mutation; the honest
implementation rewrites the config and restarts. Claiming otherwise would
produce an adapter that reports convergence while the running process still
serves the old user set — the exact class of lie SP1's confirmation pass exists
to catch.

**Interaction with the reconciler, verified in source.**
`Reconciler.Converge` defers a plan only when `plan.MaxDisruption() >=
DisruptRestart` and the maintenance window is closed. It defers the *whole
plan*, by design, so a plan containing only `DisruptNone` steps applies
immediately. The consequence for SP2 is concrete: on Xray, adding a user
during business hours applies at once; on sing-box the same operation waits
for the window if one is configured. That is a genuine operational difference
between the two backends and it must be surfaced, not smoothed over.

**Fallback.** If the Xray API is unreachable, the adapter does not silently
fall back to a restart with `DisruptNone` still declared on the step. It plans
the step as `DisruptRestart`, so the reconciler's window gate applies
correctly. Mis-declaring disruption is worse than being disruptive.

---

## Cross-cutting: what SP2 must not change

- No new path may write `nodes.desired_revision` or insert into
  `node_revisions`. `CommitNodeChange` remains the only one.
- The document adds subjects to a field that already exists and is already
  hashed. `DocumentSchemaVersion` stays at 1 **only if** the serialised shape
  of an empty subject list is unchanged; populating a field that already
  serialises as `null` does not alter the hash of a node with no subjects.
  Verified by test.
- Credential plaintext never enters an audit record, a log line, or a list
  response.
- The two-layer authorization pattern extends to subjects: a permission gate
  plus an independent SQL scope predicate.

# Tenant isolation — maintenance reference

How multi-tenant access control works in the panel, which endpoints are
protected, and what to do when adding a new one.

Branch: `reseller-engine`. Last verified: 2026-08-23.

## The two layers

Every protected operation passes through **both**. Neither is sufficient alone.

| Layer | Question it answers | Where |
|---|---|---|
| 1. Permission | May this actor perform this KIND of operation? | `rbac.Check` / `d.authorize` / `d.requirePermission` |
| 2. Scope | Does this specific row exist, as far as this actor is concerned? | SQL predicate, bound parameters |

`PermSubjectWrite` says a reseller may edit customers. It does **not** say they
may edit *any* customer. That second question is the scope layer, and it is
answered in SQL so that a handler bug is not sufficient to cross tenants.

## The predicates

Both are static constants in `internal/panel/store/reseller_query.go`. They are
never built at runtime; `is_super` and `admin_id` are bound parameters, so no
caller can widen the filter.

```sql
-- SubjectScopeSQL
(? = 1 OR subjects.id IN (
    SELECT rs.subject_id
      FROM reseller_subjects rs
      JOIN resellers r ON r.id = rs.reseller_id
     WHERE r.admin_id = ?))

-- resellerScopePredicate
(? = 1 OR resellers.admin_id = ?)
```

Bind with `store.ScopeArgs(sc)`, which returns the parameters in order. Do not
write `boolToInt(sc.IsSuper), sc.AdminID` by hand — transposing them grants
super powers to admin id 1.

### Four properties that carry the security

1. **Out-of-scope is indistinguishable from missing.** Both are `sql.ErrNoRows`
   at the store layer and **404** at the API. A 403 would confirm the id is
   real, letting one tenant walk the id space to count a competitor's customers.
2. **Phrased as "owned by me", never "not owned by someone else".** A subject
   with no `reseller_subjects` row is platform-owned and is invisible to every
   tenant. The negative phrasing leaks all of them.
3. **A zero `rbac.Scope` sees nothing.** `rbac.ScopeOf(nil)` returns the zero
   value, so a handler that forgot to authenticate gets an empty result rather
   than the whole table. A missing auth check is a bug, not a breach.
4. **Bulk filters, it does not reject.** An out-of-scope id in a batch is
   silently dropped. Rejecting the batch would reveal that the id exists,
   reintroducing the same enumeration oracle.

## Protected endpoints

All 21 subject-bearing routes, plus device revoke.

| Endpoint | Method | Permission | Scope mechanism |
|---|---|---|---|
| `/subjects` | GET | `subject:read` | `List(ctx, scope)` |
| `/subjects` | POST | `subject:write` | n/a — creation assigns ownership |
| `/subjects/{id}` | GET | `subject:read` | `Get(ctx, scope, id)` |
| `/subjects/{id}` | PUT | `subject:write` | `requireSubjectInScope` |
| `/subjects/{id}` | DELETE | `subject:write` | `requireSubjectInScope` |
| `/subjects/{id}/credentials/{kind}` | GET | `credential:reveal` | `requireSubjectInScope` |
| `/subjects/{id}/credentials/{kind}/rotate` | POST | `subject:write` | `requireSubjectInScope` |
| `/subjects/{id}/enable` · `/disable` | POST | `subject:write` | `requireSubjectInScope` |
| `/subjects/{id}/freeze` · `/unfreeze` | POST | `subject:write` | `requireSubjectInScope` |
| `/subjects/{id}/devices` | GET | `subject:read` | `requireSubjectInScope` |
| `/subjects/{id}/connections` | GET | `subject:read` | `requireSubjectInScope` |
| `/subjects/{id}/enforcement` | GET | `subject:read` | `requireSubjectInScope` |
| `/subjects/export` | GET | `subject:read` | `SubjectScopeSQL` in the query |
| `/subjects/import` | POST | `subject:write` | none — see gap 1, imports are platform-owned |
| `/subjects/bulk/enable` | POST | `subject:write` | `scopeFilterSubjectIDs` |
| `/subjects/bulk/delete` | POST | `subject:write` | `scopeFilterSubjectIDs` |
| `/subjects/bulk/extend` | POST | `subject:write` | `scopeFilterSubjectIDs` |
| `/subjects/bulk/reset-traffic` | POST | `subject:write` | `scopeFilterSubjectIDs` |
| `/subjects/bulk/set-quota` | POST | `subject:write` | `scopeFilterSubjectIDs` |
| `/devices/{id}/revoke` | POST | `subject:write` | resolves owning subject, then `requireSubjectInScope` |

Reseller-side reads are scoped by `resellerScopePredicate`:
`ListResellersScoped`, `GetResellerScoped`, `BalanceScoped`, `ListLedgerScoped`.

## RBAC mapping

| Permission | super_admin | admin | reseller | readonly |
|---|:--:|:--:|:--:|:--:|
| `reseller:read` | ✅ | ✅ | ❌ | ❌ |
| `reseller:write` | ✅ | ✅ | ❌ | ❌ |
| `credit:grant` | ✅ | ❌ | ❌ | ❌ |

- **`credit:grant` is separate from `reseller:write`** because minting credit is
  the only operation that creates value from nothing. If one permission covered
  both, anyone who can rename a reseller could pay themselves.
- **The reseller role holds no `reseller:*` permission.** A tenant is not an
  administrator of tenancy. A reseller reading their own record is served by
  *scope* through `/me`, not by permission. Granting `reseller:read` would let
  one tenant enumerate the others.

## Adding a new subject endpoint

1. Add the permission check (`d.authorize` or `rbac.Check`).
2. Add the scope check:
   - single id → `d.requireSubjectInScope(w, r, actor, id)`
   - id list → `d.scopeFilterSubjectIDs(r, ids)`
   - query → `WHERE ... AND ` + `store.SubjectScopeSQL`, bound with `store.ScopeArgs`
   - keyed by something else (device, token) → resolve the owning subject first
3. Deny with **404**, never 403.
4. Add a case to `TestEverySubjectEndpointIsTenantScoped`.

## Tests

| File | Covers |
|---|---|
| `store/reseller_scope_test.go` | Predicate correctness: cross-tenant, platform-owned, zero scope, indistinguishability, read/write gate agreement |
| `httpapi/subject_tenant_isolation_test.go` | List, get, reveal, and the three mutation paths, end to end |
| `httpapi/subject_surface_isolation_test.go` | Every remaining endpoint: devices, connections, enforcement, disable, freeze, bulk, export |
| `httpapi/subject_bulk_permission_test.go` | The bulk endpoints' permission gate, independent of scope: an owner without `subject:write` is refused, the check precedes body parsing, and a holder of `subject:write` still passes |
| `httpapi/subjects_bulk_schema_test.go` | That bulk, export and import actually reach the database: real column writes, the delete republish, CSV round-trip |
| `rbac/reseller_perm_test.go` | Privilege separation of `credit:grant`; reseller holds no tenancy permissions |
| `resellers/resellers_test.go` | Ledger invariants, atomic provisioning, idempotency |

All mutation-verified: reverting a guard makes a named test fail.

## Known gaps

1. **`POST /subjects/import` is not scoped.** It creates subjects and does not
   assign ownership, so imported rows are platform-owned and invisible to every
   tenant. Not a leak, but a reseller cannot use it and an admin import will not
   belong to anyone. Needs an owner parameter before resellers go live.

   Now reachable: gap 4 made import actually work, where before it failed at
   SQL on every row. It requires `subject:write`, so only an actor who could
   already create subjects can reach it — but a reseller who imports will not
   see what they imported.
2. **CLOSED.** Bulk endpoints had no permission check, only the scope filter.
   All five now call `d.authorize(..., PermSubjectWrite, ...)` before reading
   the request body. The actor that exposed this owns a subject but holds no
   `subject:write`: scope alone waved its own ids straight through. Proven by
   `TestBulkEndpointsRequireSubjectWrite` — swapping the permission constant to
   `PermSubjectRead` turns all five 403s back into 200s and fails it by name.
3. **CLOSED.** The three dead handlers in `subjects_activity.go`
   (`handleSubjectActivity`, `handleSubjectConnections`, `handleSubjectDevices`)
   have been deleted; they are no longer defined or routed.
4. **CLOSED.** The bulk, export and import handlers referenced `disabled`,
   `frozen`, `node_id` and `updated_at`, none of which exist. The real columns
   are `enabled` (inverted sense) and `frozen_at` (a nullable timestamp); there
   is no `updated_at` at all, and `node_id` lives on `services`, not
   `subject_services`. Every bulk operation failed at SQL and export returned
   500. (`subscription_token` was listed here once but does exist — migration
   00012 adds it.)

   Repairing the columns alone was not enough. `bulk/delete` collected the
   affected nodes into a map and discarded it, so a working DELETE would have
   left every node serving a deleted subject until an unrelated change bumped
   its revision. `enable`, `extend` and `delete` now go through the service
   layer, which owns the transaction and republishes through `CommitNodeChange`.
   `reset-traffic` and `set-quota` deliberately do not: quota is not part of the
   desired document, so there is nothing for a node to observe.

   Import additionally had no permission check and bound NULL into the
   `NOT NULL` `note` column. Both fixed.

   Proven by `subjects_bulk_schema_test.go`, which asserts on the database and
   on the `failed` counter rather than the status code — these endpoints
   returned 200 while doing nothing, so a status-only test passed against the
   broken version.

Gap 4 mattered for the security story: several endpoints appeared safe during
testing only because they were broken. Fixing the schema without the scope and
permission guards would have turned them into live holes, which is why gap 2
was closed first.

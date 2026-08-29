# Antimage User Management — Gap Audit vs Rebecca (Production Baseline)

Date: 2026-08-29
Auditor: Agent

## Summary

Antimage backend is **architecturally superior** to Rebecca in many respects:
- Sealed credentials (AES-GCM), no plaintext in DB
- Proper RBAC with scope isolation, not just role checks
- Multi-node desired-state control plane with revision bumping
- Adapter registry, deployment orchestration, observability depth
- Accounting with usage_deltas, rollups, quota enforcement
- Reseller credit ledger (append-only), not mutable balance
- Device tracking, active connections, connection audit log

Frontend, however, is **significantly weaker** than Rebecca for user-management UX. Rebecca's Users page is a professional commercial panel; Antimage's is a basic CRUD table.

This document audits every capability from Phase 1 of the task.

---

## 1. Capability Matrix

| Capability | Exists? | Backend Complete? | API? | DB Model? | Frontend? | Usable? | Connected to Runtime? | Stub/Fake? | Production Gap |
|---|---|---|---|---|---|---|---|---|---|
| Users (list) | Yes | Yes | GET /api/v1/subjects + /api/v2/subjects paginated | subjects table | Subjects.tsx DataTable | Partially — no owner, service, node, protocol columns | Yes via scope | No | Missing columns, filters, sorting, bulk UX |
| User detail | Yes | Partial | GET /api/v1/subjects/:id | subjects | SubjectDetail.tsx | Partial — shows quota, sub, creds, devices but no tabs, no traffic chart, no audit | Yes | No | Needs tabs: Overview, Creds, Sub, Traffic, Connections, Activity, Audit |
| Create user | Yes | Yes | POST /api/v1/subjects | subjects + credentials + subject_services | CreateSubjectForm sheet | Basic — name, note, expire_days, quota_gb, service_ids checkboxes | Yes — mints uuid/password, bumps node revisions | No | Missing IP limit, device limit, periodic quota, protocol selection, owner selection, credential import options, notes, Telegram ID |
| Edit user | Partial | Yes | PUT /api/v1/subjects/:id | UpdateInput | Only enable/disable in detail | No dedicated edit form | Yes | No | Needs full edit workflow (name, note, expiry, quota, services, limits) |
| Delete/disable user | Yes | Yes | DELETE, POST /disable, /enable, /freeze | enabled, frozen_at | Yes via table + detail | Usable | Yes via CommitNodeChange | No | Needs confirmation dialogs, bulk delete |
| User status | Partial | Yes | Computed from enabled, expired_at, frozen_at | enabled, expired_at, frozen_at, quota_used | StatusBadge simple | Shows active/disabled/expired only | Yes | No | Missing: limited (quota exceeded), expiring soon, on_hold, online |
| Expiry | Yes | Yes | expires_at field | expires_at, expired_at | Shows date + days left | Partial | Yes — sweeper retires | No | Needs: remaining time prominent, expiry filter, add/remove days actions |
| Traffic quota | Yes | Yes | quota_bytes | quota_bytes | QuotaBar | Partial | Yes — usage_deltas + rollups | No | Needs remaining, usage graph, per-node breakdown |
| Traffic usage | Yes | Partial | quota_used_bytes from subjects, rollups hourly/daily | usage_deltas, rollups | QuotaBar shows used/total | No charts | Yes — real accounting from nodes | No | Missing: upload/download split, daily usage chart, node breakdown, protocol breakdown |
| Periodic traffic limits | Partial | Partial | quota_period_start, quota_period_seconds columns exist (00031) | quota_period_start, quota_period_seconds | No UI | Not usable | Partial — sweeper exists but UI missing | Stub UI | Need UI for reset strategy (no, daily, weekly, monthly), on_hold expire duration |
| IP limit | Partial | Yes DB | max_ips column exists | subjects.max_ips | No UI | No | Partial — enforcement exists in node/enforcement | No | Need UI + API exposure in DTO |
| Services | Yes | Yes | /api/v1/services | services table | Service selection in create form | Basic checkboxes | Yes — desired doc | No | Need better service presentation: protocol, port, node, enabled |
| Inbounds | Partial | Yes (as services) | Same as services | services.params JSON | Same | Basic | Yes | No | Rebecca has explicit inbound concept; Antimage maps service= inbound. Need inbound studio UX improvement |
| Nodes | Yes | Yes | /api/v1/nodes | nodes | Nodes.tsx, NodeDetail | Usable | Yes | No | Need node filter in users page |
| Subscriptions | Yes | Yes | /api/v1/subjects/:id/subscription + /api/v1/subscribe/:token | subscription_token | Shows URL + QR in detail | Partial | Yes — gatherServers builds real servers from services+hosts | No | Needs share links per inbound, Clash/sing-box/V2Ray links, copy actions |
| Share links | Partial | Yes | subscription rendering generates VLESS/VMess/Trojan links | Via subscriptions renderers | Only subscription URL, not individual links | Partial | Yes | Need to expose links array like Rebecca |
| QR codes | Yes | Yes | /api/v1/subscribe/:token/qr + /api/v1/subjects/:id/subscription returns qr_url | token | img tag in detail | Usable | Yes | No | Needs modal, not inline only |
| User credentials | Yes | Yes | GET /credentials/:kind + rotate | subject_credentials sealed | Reveal buttons | Usable but basic | Yes | No | Needs credential regeneration, revocation, copy, display with security (no-store) |
| Multi-protocol users | Yes | Yes | service_ids array | subject_services | Checkboxes allow multi | Yes | Yes | No | Needs UX: protocol icons, node grouping |
| Multiple inbounds | Yes | Yes | service_ids | subject_services | Same | Yes | Yes | No | OK |
| User-to-service relationships | Yes | Yes | subject_services join | subject_services | Yes | Yes | Yes | No | OK |
| Admin ownership | Partial | Partial | admins table, but subject ownership via reseller_subjects only for resellers; platform-owned subjects have no owner | reseller_subjects | Not shown in users table | No | Yes — scope checks | No | Need owner column, filter, display |
| Reseller ownership | Yes | Yes | reseller_subjects + /api/v1/resellers/:id/subjects provision | resellers, ledger | Resellers page exists but not linked to users page | Partial | Yes | No | Need reseller filter, owner chip |
| Admin limits | Partial | Yes for resellers | max_subjects, max_quota_bytes, credit_floor | resellers | Reseller detail shows limits | Partial | Yes | No | Need admin-level limits UI |
| User limits | Yes DB | Yes | max_devices, max_ips, max_connections | subjects | No UI | No | Partial | Stub | Need UI for all limits |
| Data limits | Yes | Yes | quota_bytes | subjects.quota_bytes | Yes | Partial | Yes | No | Need add/remove traffic actions |
| User lifecycle | Partial | Partial | Sweeper for expiry exists | expired_at, frozen_at | No visual lifecycle | Partial | Partial | No | Need: Created → Active → Limited → Expiring → Expired → Disabled → Deleted with backend jobs |
| Auto expiration | Yes | Yes | Sweeper in subjects/sweeper.go | expires_at | No UI indication of auto | Yes — Sweep() disables and bumps nodes | No | Works, but needs dashboard visibility |
| Auto disable | Partial | Yes via freeze | frozen_at | frozen_at | Freeze/unfreeze buttons | Partial | Yes | No | Need quota enforcement sweeper (quota_enforcement) |
| Auto deletion | No | No | Rebecca has auto_delete_in_days | Missing column | No | No | No | Missing | Need to add column + sweeper |
| Renew | Partial | Yes via bulk extend | POST /subjects/bulk/extend | expires_at | BulkActions has extend | Partial | Yes | No | Need single-user add days, remove days, extend expiry UI |
| Add traffic | Partial | Yes via bulk set-quota | POST /subjects/bulk/set-quota | quota_bytes | BulkActions has set-quota | Partial | Yes | No | Need add/remove traffic (incremental), not just set |
| Add days | Partial | Same as renew | extend | expires_at | Bulk extend | Partial | Yes | No | Need add/remove days single + bulk |
| Usage charts | Partial | Yes backend | /dashboard/traffic-chart, /dashboard/top-users | rollups | Dashboard shows metrics but not per-user chart | No per-user | Yes | No | Need per-user daily usage graph, node breakdown, protocol breakdown |
| Search | Partial | Yes v2 | /api/v2/subjects?search= | LIKE on name, note | SubjectFilters has search | Partial | Yes | No | Need username search, UUID search (requires credential search), ID search |
| Filters | Partial | Yes | status, traffic_min/max, quota_status, expires_before/after, tag | Implemented in handleListSubjectsV2 | Basic filters | Partial | Yes | No | Need service filter, node filter, protocol filter, owner filter, expiry filter, traffic filter |
| Bulk actions | Yes | Yes | /bulk/enable, /disable, /delete, /extend, /reset-traffic, /set-quota | Via service layer | BulkActions component exists | Partial — not all actions wired | Yes | No | Need bulk add traffic, bulk add days, bulk service change, export |
| Audit history | Yes | Yes | /api/v1/audit | audit_log | Audit.tsx page exists | Usable but not per-user | Yes | No | Need per-user audit tab in detail |
| User activity | Partial | Yes | connection_audit_log, active_connections | connection_audit_log | Devices tab shows devices, not activity | Partial | Yes | No | Need activity timeline: created, edited, enabled, disabled, traffic changed, expiry changed, sub regenerated, credential changes |
| Online/offline status | Partial | Yes | active_connections, subject_devices last_seen | active_connections | No online indicator in users table | No | Yes | No | Need online badge, current connections count |
| Last connection | Partial | Yes | subject_devices.last_seen_at, last_ip | subject_devices | Devices table shows last_seen | Partial | Yes | No | Need last online column in users table |
| Connection/IP information | Partial | Yes | /subjects/:id/devices, /connections, /enforcement | subject_devices, active_connections | Devices tab | Partial | Yes | No | Need IPs, node, protocol, connection time, last seen |
| Subscription regeneration | Yes | Yes | POST /subscription/revoke rotates token | subscription_token | Revoke button exists | Usable | Yes | No | Needs regenerate with confirmation |

---

## 2. Rebecca Reference — What We Must Match

### Users Page (Rebecca)
- Table columns: username, status dot, usage bar (used/total + %), expiry countdown, service name, admin owner, online status, subscription links, actions (edit, usage, QR, copy sub, reset usage, revoke sub)
- Filters: search (username), status (active, expired, limited, disabled, on_hold), service filter, admin owner filter, advanced filters (last online, status age, created before)
- Sorting: username, used_traffic, data_limit, expire, created_at
- Pagination: server-side, with total, active_total, status_breakdown, usage_total, online_total
- Bulk: AdvancedUserActions modal with dry-run, scopes, statuses
- Quick edit modal, QR dialog, reset usage dialog, revoke sub dialog
- Empty states, loading skeletons

### Create User (Rebecca UserDialog)
- Fields: username, status, service_id (required), data_limit (GB), expire (date), ip_limit, data_limit_reset_strategy (no_reset, daily, weekly, monthly), on_hold_expire_duration, note, telegram_id, contact_number, flow, credential_key (UUID), auto_delete_in_days, next_plans (auto renewal)
- Validation, generation of UUID
- After creation: subscription_url, links per inbound, QR

### User Detail (Rebecca)
- Not a separate page but dialog with tabs: overview, subscription (links + QR + copy), usage chart, IPs (online IPs with node, protocol, inbound_tag, connections)
- Actions: edit, delete, reset usage, revoke subscription, add traffic, extend expiry

### Services / Inbounds
- Services page groups inbounds; each service has multiple inbounds with tags
- Hosts manager: per-service hosts with remark, address, port, SNI, security, fingerprint, etc.

### Nodes
- Nodes page with status, usage, online badge, version, etc.

### Admins / Resellers
- Admins table with role, permissions editor, service limits, traffic limits, user limits, status, 2FA
- Permissions granular: user:create, user:edit, user:delete, etc.
- Admin usage analytics

### Dashboard
- Statistics: total users, active, online, expiring soon, expired, disabled, traffic used, traffic remaining, active nodes, unhealthy nodes
- Charts: user growth, daily traffic, active users, traffic by node, traffic by service, expiration trends
- Recent actions feed

### Subscriptions
- subscription_url per user, plus subscription_urls per client type (v2ray, clash, singbox)
- link_data with UUID/password per inbound
- QR code
- Clash, sing-box configs

---

## 3. Antimage Current IA

Existing nav:
- Dashboard (real-time SSE, metrics)
- Nodes
- Subjects (Users)
- Resellers (gated reseller:read)
- Hosts (gated service:read)
- Fleet (node topology, drift, certs, bootstrap)
- Observability (alerts, fleet summary)
- Audit (gated audit:read)
- Templates
- Admins (gated admin:manage)
- Settings
- Profile

Users must become first-class:
- Dashboard (user-oriented + infra)
- Users (All, Active, Expired, Disabled, Limited, Online) — filter presets
- Services
- Inbounds (or as part of Services)
- Nodes
- Subscriptions (maybe not separate, but as part of users)
- Traffic / Usage
- Admins
- Resellers
- Audit Logs
- Settings

---

## 4. Critical Gaps for Production

### P0 — Must Fix for Definition of Done
1. **Users page lacks essential columns**: owner, service, node, protocol, remaining traffic, remaining time, IP limit, current connections, last online
2. **Search incomplete**: no UUID search, ID search, service/node/protocol/owner filters
3. **Create user workflow is primitive**: missing IP limit, device limit, periodic quota, credential generation UI, multi-step
4. **User detail missing tabs**: no Traffic chart, no Connections/IPs with node/protocol, no Activity/Audit timeline, no Credentials management with regen/revoke
5. **Quick actions missing**: Add Traffic (incremental), Remove Traffic, Add Days, Remove Days, Reset Usage, Regenerate Subscription, Copy Subscription, Show QR modal, Delete with confirmation
6. **Subscription system not first-class**: only URL + QR, no per-inbound share links, no Clash/sing-box/V2Ray links array, no copy actions
7. **Traffic management not connected to UI**: real accounting exists but per-user usage chart missing, node breakdown missing, protocol breakdown missing
8. **Lifecycle not visible**: no expiring soon, no limited status, no auto-deletion
9. **Admin/Reseller permissions not granular in UI**: permissions exist backend but frontend doesn't enforce/show granular toggles
10. **Dashboard not user-oriented**: only infra metrics, missing user growth, expiring soon, etc.

### P1 — Important for Commercial Parity
- Periodic quota reset UI + backend sweeper for reset
- Auto-delete with auto_delete_in_days
- Next plans / auto renewal
- On-hold status
- Online/offline status with real-time
- Export functionality (exists backend but not in new UI)
- Bulk add traffic / add days
- Usage charts per user
- Audit per user

### P2 — Nice to Have / Exceeds Rebecca
- Better RBAC UI with granular permission editor
- Better multi-node support (node scope)
- Better observability (fleet, drift, etc. already exceeds)
- Command palette already exists, good
- RTL, dark/light, i18n already exists

---

## 5. Implementation Plan

### Phase A — Backend Foundations (1-2 days)
- Add missing columns: auto_delete_in_days, data_limit_reset_strategy, on_hold_expire_duration, telegram_id, status computed, etc.
- New migration 00036_user_management_complete.sql
- Enhance subjectDTO to include owner, service_ids, node_ids, protocol, remaining, IP limit, device limit, current connections, last online, created at, status details
- Add endpoints:
  - POST /subjects/:id/add-traffic (incremental)
  - POST /subjects/:id/remove-traffic
  - POST /subjects/:id/add-days
  - POST /subjects/:id/remove-days
  - POST /subjects/:id/reset-traffic
  - GET /subjects/:id/traffic (daily usage, node breakdown)
  - GET /subjects/:id/activity (audit + connection events)
  - GET /subjects/:id/ips (online IPs)
  - Enhanced v2 search: service filter, node filter, protocol filter, owner filter, UUID search, ID search
- Enhance service layer: AddTraffic, AddDays, etc.
- Ensure RBAC granular permissions: users.view, users.create, etc. (map existing perms)

### Phase B — Users Page Professional (2-3 days)
- Rewrite Subjects.tsx:
  - Columns: username (link), status (with dot + badge), owner, service, protocol, node, traffic used/limit/remaining with bar, expiry + remaining, IP limit, connections, last online, created at, quick actions
  - Search: username, UUID, ID
  - Filters: service, node, protocol, status, expiry, traffic, owner
  - Sorting: all columns server-side
  - Pagination: server-side with total
  - Bulk selection + bulk actions: enable, disable, delete, add traffic, add days, reset traffic, set quota, export
  - Export CSV
  - Empty states, skeleton loading, error states
- SubjectFilters.tsx enhancement

### Phase C — Create User Workflow (1-2 days)
- Multi-step modal or page:
  - Step 1 Identity: username (with random gen), UUID generation (auto), notes, status
  - Step 2 Limits: traffic limit (GB), expiry (date + days), IP limit, device limit, periodic quota (no, daily, weekly, monthly), on_hold
  - Step 3 Service: select service(s), inbound(s), node(s), protocol, multi-protocol support
  - Step 4 Review + Subscription preview
- After creation: show subscription URL, share links, QR, copy actions, Clash/sing-box/V2Ray configs

### Phase D — User Detail (2 days)
- Tabs: Overview, Credentials, Subscription, Traffic, Connections, Activity, Audit
- Overview: status, expiry, traffic, remaining, current connections, last seen, assigned services/nodes
- Credentials: UUID/password, regen, revoke, copy
- Subscription: URL, QR, configs, copy, regen, revoke
- Traffic: total upload/download, remaining, usage graph (daily), node breakdown, protocol breakdown
- Connections: active connections, IPs, node, protocol, time, last seen
- Activity: timeline of events
- Audit: admin changes

### Phase E — Quick Actions + Lifecycle (1 day)
- Enable, Disable, Add Traffic, Remove Traffic, Add Days, Remove Days, Extend Expiry, Reset Usage, Regenerate Subscription, Revoke Credentials, Copy Subscription, Show QR, Delete
- Confirmation dialogs
- Toast notifications

### Phase F — Admin/Reseller + RBAC (1 day)
- Enhance Admins page with granular permissions editor
- Reseller limits, service scope, node scope, ownership, enabled/disabled
- Tenant isolation tests

### Phase G — Dashboard (1 day)
- User-oriented metrics: Total Users, Active, Online, Expiring Soon, Expired, Disabled, Traffic Used/Remaining, Active/Unhealthy Nodes
- Charts: user growth, daily traffic, active users, traffic by node/service, expiration trends
- Recent activity feed

### Phase H — Testing + Verification (1 day)
- Backend unit, service, API, authz, tenant isolation, lifecycle, quota tests
- Frontend: Users page, create, edit, detail, filters, bulk, sub, traffic, permissions
- go test ./... + npm test + npm run build

---

## 6. Security Review Checklist
- Credential leakage: ensure list endpoints never return credentials, only reveal endpoint with no-store
- UUID leakage: same
- Cross-tenant: scope checks on every subject endpoint, 404 not 403
- IDOR: test accessing other tenant's subject
- Authz bypass: test without perms
- Traffic modification: only with users.add_traffic perm
- Subscription access: token auth, no session, but ensure frozen/expired subjects can't get sub
- User modification: scope + perm
- Admin scope bypass: test reseller accessing platform subjects

---

## 7. Definition of Done Checklist (from task)
- [ ] Create user from Web UI
- [ ] Assign service
- [ ] Assign inbound
- [ ] Assign node
- [ ] Generate credentials
- [ ] Set traffic limit
- [ ] Set expiry
- [ ] Set IP limit
- [ ] Generate subscription
- [ ] Copy subscription
- [ ] Show QR
- [ ] View user detail
- [ ] View real traffic
- [ ] View remaining traffic
- [ ] View expiry
- [ ] Enable user
- [ ] Disable user
- [ ] Add traffic
- [ ] Add days
- [ ] Regenerate subscription
- [ ] Revoke credentials
- [ ] Delete user
- [ ] View activity
- [ ] View audit history
- [ ] Manage users according to admin/reseller permissions
- All connected to real backend, no mocks, no fake data, no hard-coded runtime, no placeholder buttons.

---

## 8. Current Verdict

Antimage is **70% backend-complete, 30% frontend-complete** for user management.

Backend has strong foundations but needs:
- Incremental traffic/days endpoints
- Enhanced search filters
- Per-user traffic/activity/IPs endpoints
- Auto-delete + periodic reset

Frontend needs complete rewrite of Users page, Create workflow, Detail page, Dashboard.

Next step: implement backend missing pieces, then frontend.

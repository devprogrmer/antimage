# Premium Layer — Feature Matrix

Status of every capability requested in the Premium Management Layer specification,
assessed against the repository rather than against the UI.

Last inventoried: 2026-08-24, at `master` = `75a35ab`.

## How to read this

**Status** uses the specification's own vocabulary (§0):

| Code | Meaning |
|---|---|
| **B** | Already implemented in backend |
| **CP** | Already implemented in adapter / control plane |
| **P** | Partially implemented |
| **UI-** | Backend-ready, UI missing |
| **gap-B** | Backend gap |
| **gap-A** | Adapter gap |
| **gap-ARCH** | Architecture gap |
| **NEW** | New premium feature |

**Benchmark** names panels that ship something comparable. These come from
working knowledge to a May 2026 cutoff and are **not verified against current
releases**. They are directional, for prioritisation only. Marked `?` where I
would want to check a release before relying on it. Applying §56's own rule to
this document: it does not claim parity it has not measured.

Every non-gap row cites a path or route. If a row cites nothing, it is a gap.

---

## 1. Control plane and node lifecycle

| Feature | Benchmark | Status | Evidence |
|---|---|---|---|
| Desired-state reconciliation | — (antimage differentiator) | **CP** | `internal/panel/nodes/`, `desired_revision`/`applied_revision` |
| Revisions + history | Rebecca? | **B** | `node_revisions`, `GET /nodes/{id}/revisions` |
| Apply runs + per-step results | — | **B** | `node_apply_runs`, `node_apply_steps`, `GET /nodes/{id}/apply-runs` |
| Drift detection | — | **CP** | `Observe`/`Plan` per adapter; `GET /nodes/{id}/reconciliation` |
| Deployment preview / validate / rollback | — | **B** | `POST /deployments/preview`, `/validate`, `/{id}/rollback` |
| Node enrolment, private CA, mTLS | — | **CP** | `internal/panel/control/enroll_service.go`, `panel_ca` |
| Bootstrap over SSH | Marzban?, 3X-UI? | **B** | `POST /nodes/{id}/bootstrap-ssh` |
| Maintenance mode | Rebecca? | **B** | `maintenance_mode`/`_reason`/`_entered_at`, `POST /nodes/{id}/maintenance` |
| Node tags | Marzban? | **B** | `nodes.tags_json` |
| Node health + metrics + rollups | Marzban, PasarGuard | **B** | `node_health`, `node_metrics`, `node_health_rollups_{hourly,daily}` |
| Self-healing actions (sync, restart, reconcile) | — | **B** | `POST /nodes/{id}/sync`, `/restart`, `/reconciliation` |
| Node capacity score / smart placement (§85) | — | **NEW** | — |
| Node region / provider / ASN / geo (§54) | Marzban, 3X-UI | **gap-B** | `nodes` has no region or provider column |

## 2. Adapters and protocols

| Feature | Benchmark | Status | Evidence |
|---|---|---|---|
| Adapter contract (Descriptor/Observe/Plan/Apply/Probe) | — | **CP** | `internal/node/adapter/adapter.go` |
| Capability declaration | — | **CP** | `adapter.Caps{HotUserAdd, SelfAccounting, RequiresPKI, CredentialKinds, ServiceSchema}` |
| **Per-adapter JSON Schema for service params** | — | **CP** | every adapter sets `ServiceSchema`; validated by `nodes.ValidateServiceParams` (`internal/panel/nodes/schema.go:49`) |
| Capability discovery API (§69) | — | **B** | `GET /nodes/{id}/adapters`, `GET /nodes/{id}/capabilities` |
| Xray: VLESS / VMess / Trojan | all | **CP** | `internal/node/adapter/xray/inbound.go` |
| Xray: TLS, REALITY, ws, grpc, sniffing | 3X-UI, PasarGuard | **CP** | `xray/inbound.go` — `Security`, `Dest`, `ServerNames`, `ShortIDs`, `Path`, `Host` |
| sing-box: VLESS / VMess / Trojan / Shadowsocks | PasarGuard | **CP** | `internal/node/adapter/singbox/inbound.go` |
| WireGuard | PasarGuard, vpn-ui | **P** | Observe/Plan/accounting work; **install & restart do not** |
| Hysteria2 | PasarGuard | **P** | same — install & restart return "not yet implemented" |
| L2TP/IPsec | vpn-ui? | **P** | adapter exists; `plan.go` has no drift detection (TODO), `probe.go` checks no ports |
| **WireGuard/Hysteria2 install + restart** | — | **gap-A** | `wireguard/apply.go:37,46,55`, `hysteria2/apply.go:17,22` |
| XHTTP / mKCP / HTTPUpgrade (§12) | 3X-UI | **gap-A** | `xray.Network` does not enumerate them |
| Fallbacks / multi-protocol port (§14) | 3X-UI | **gap-A** | no fallback modelling in `xray/inbound.go` |
| Real-runtime verification | — | **CP** | CI `realruntime` job; `test/e2e/sp2_realruntime_test.go` |

## 3. Users, devices, sessions

| Feature | Benchmark | Status | Evidence |
|---|---|---|---|
| Subject CRUD, enable/disable, freeze/unfreeze | all | **B** | `internal/panel/service/subjects.go`, `/subjects/*` |
| Credential seal + audited reveal + rotate | — | **B** | AES-256-GCM; `GET /subjects/{id}/credentials/{kind}`, `/rotate` |
| Devices + fingerprints | Rebecca, Marzban | **B** | `subject_devices`, `GET /subjects/{id}/devices`, `POST /devices/{id}/revoke` |
| Connections / IP history | Rebecca | **B** | `active_connections`, `connection_audit_log`, `GET /subjects/{id}/connections` |
| Sessions (admin) | Marzban | **B** | `sessions`, `GET /sessions`, `DELETE /sessions/{id}` |
| Enforcement policy (device/IP/conn/speed limits) | 3X-UI, Rebecca | **B** | `subjects.max_devices/max_ips/max_connections/speed_limit_*`; in the desired document (schema v2) |
| Bulk operations | all | **B** | `/subjects/bulk/{enable,delete,extend,reset-traffic,set-quota}` |
| Import / export CSV | Marzban, 3X-UI | **B** | `/subjects/import`, `/subjects/export` |
| Paginated search + filters | all | **B** | `GET /api/v2/subjects` |
| HWID binding | Rebecca? | **P** | device fingerprint exists; HWID semantics not modelled |
| Device country / ASN (§16, §18) | Rebecca? | **gap-B** | no geo enrichment |
| **User Studio UI** | all | **UI-** | only `Subjects.tsx`, `SubjectDetail.tsx` |

## 4. Accounting and quotas

| Feature | Benchmark | Status | Evidence |
|---|---|---|---|
| Usage collection from adapters | all | **CP** | `UsageReporter`; `usage_deltas` idempotent on `(node_id, sequence)` |
| Hourly / daily rollups | Marzban | **B** | `usage_rollups_hourly`, `usage_rollups_daily` |
| Quota enforcement + freeze | all | **B** | `internal/panel/nodes/quota.go` — `QuotaEnforcementSweeper` |
| Periodic quota reset | Marzban, Rebecca | **B** | `QuotaResetSweeper`, `subjects.quota_reset_at` |
| Quota warning thresholds (§19) | Rebecca | **P** | `alerts.alert_type` has `quota_warning`; thresholds not configurable |
| **Traffic coefficients (§11)** | 3X-UI, Rebecca | **gap-ARCH** | no coefficient anywhere; `usage_deltas` has **no `service_id`**, so per-inbound attribution is impossible |
| **Billable vs raw traffic** | 3X-UI | **gap-ARCH** | depends on the above |
| Quota state machine (§20) as an explicit model | — | **P** | transitions happen; not modelled or exposed as states |

## 5. Subscriptions

| Feature | Benchmark | Status | Evidence |
|---|---|---|---|
| V2Ray / Clash / sing-box output | all | **B** | `internal/panel/subscriptions/{v2ray,clash,singbox}.go` |
| Client detection by User-Agent | Marzban | **B** | `subscriptions.DetectFormat` |
| Token issue / rotate / revoke | all | **B** | `internal/panel/subjects/tokens.go` |
| QR code | Marzban, 3X-UI | **B** | `GET /subscribe/{token}/qr` |
| Rate limiting | Marzban | **B** | `subscriptions/ratelimit.go` |
| Clash Meta / Mihomo variant | Marzban, PasarGuard | **P** | Clash generator exists; Meta-specific fields unverified |
| Subscription groups (§32) | Rebecca? | **gap-B** | — |
| Subscription templates / remarks (§33) | Marzban, 3X-UI | **gap-B** | — |
| Custom hostname per subscription | Marzban | **gap-B** | no public-URL concept until `ANTIMAGE_PUBLIC_URL` (bot only) |

## 6. Outbounds, providers, routing — the largest gap

Every row here is blocked on the same thing: `nodes.Document` carries only
`Services` and `Subjects`. There is no outbound or routing concept in the
control plane, so none of this can be expressed as desired state today.

| Feature | Benchmark | Status |
|---|---|---|
| Outbound Studio (§21) | 3X-UI, vpn-ui | **gap-ARCH** |
| Provider abstraction (§22) | vpn-ui | **gap-ARCH** |
| Multi-location outbound (§23) | vpn-ui | **gap-ARCH** |
| Outbound pools + selection (§24) | vpn-ui | **gap-ARCH** |
| Outbound health (§25) | vpn-ui | **gap-ARCH** |
| Failover (§26) | vpn-ui | **gap-ARCH** |
| Outbound accounting (§27) | 3X-UI | **gap-ARCH** |
| Routing Studio (§28) | 3X-UI | **gap-ARCH** |
| Routing simulator (§29) | — | **NEW** + gap-ARCH |
| Outbound chaining (§30) | 3X-UI | **gap-ARCH** |
| WARP / NordVPN / Tor integrations | 3X-UI, vpn-ui | **gap-ARCH** |

## 7. Multi-tenancy, resellers, RBAC

| Feature | Benchmark | Status | Evidence |
|---|---|---|---|
| RBAC, backend-enforced | Marzban (partial) | **B** | 16 permissions, 4 roles, `internal/panel/rbac/` |
| Tenant isolation, two-layer | — | **B** | `docs/TENANT-ISOLATION.md`; SQL predicate + permission gate; 404-not-403 |
| Reseller records, credit ledger, ownership | Rebecca | **B** | `resellers`, append-only `reseller_credit_ledger`, `reseller_subjects` |
| Atomic provisioning with credit floor + ceilings | Rebecca | **B** | `resellers.ProvisionSubject` |
| **Reseller HTTP API** | Rebecca | **UI-** | **zero reseller routes in `router.go`** — engine unreachable |
| Service-scoped reseller limits (§37) | Rebecca? | **gap-B** | limits are per-reseller, not per-service |
| Permission matrix UI (§40) | Marzban | **UI-** | — |
| API keys (§61) | Marzban, 3X-UI | **gap-B** | no `api_keys` table |

## 8. Security, audit, observability

| Feature | Benchmark | Status | Evidence |
|---|---|---|---|
| TOTP 2FA + recovery codes | Marzban, 3X-UI | **B** | `/auth/totp/*`, `admin_recovery_codes` |
| Session management + login attempts | Marzban | **B** | `sessions`, `login_attempts` |
| Audit log with before/after | Rebecca | **B** | `audit_log`, `internal/panel/audit/` |
| Per-step apply results (§70) | — | **B** | `node_apply_steps` — `step_kind`, `disruption`, `outcome`, `error`, `duration_ms` |
| Request id in error responses (§70) | — | **gap-B** | `WriteError` returns only `{code, message}` (`httpapi/errors.go:20`) |
| Immutable / chained audit (§43) | — | **P** | append-only in practice; no integrity chaining |
| Alerts | Marzban | **P** | `alerts` supports only `cert_expiry`, `quota_warning`, `quota_exceeded`; no ack/mute/assign |
| Observability dashboard | Marzban, PasarGuard | **P** | data + `Observability.tsx`; no time range, filters or drill-down |
| Webhooks (§49) | Rebecca? | **gap-B** | — |
| Automation engine (§46) | — | **gap-B** / NEW |
| Backup / restore (§50) | Rebecca, Marzban, 3X-UI | **gap-B** | `docs/BACKUP-RESTORE.md` exists; **no implementation** |
| Certificate centre (§51) | 3X-UI | **P** | node CA + `cert_expiry` alerts only |

## 9. Frontend

| Feature | Status | Evidence |
|---|---|---|
| Design system (§4) | **gap-UI** | Tailwind only; no shadcn/ui |
| Application shell + nav (§5) | **gap-UI** | `App.tsx` is a 5-value `useState` switch |
| Command palette (§6) | **gap-UI** | — |
| Dashboard (§7) | **P** | `Dashboard.tsx` + SSE `/dashboard/stream` |
| i18n, 5 locales, RTL gate | **B** | `web/src/i18n/`, `scripts/check-rtl.sh` |
| Accessibility (§65) | **P** | some `aria-label`; no systematic audit |
| Virtualization / perf (§67) | **gap-UI** | — |
| Topology map (§53) | **gap-UI** | — |
| Single-binary embed (§79) | **B** | `go:embed` of `internal/panel/webui/dist` |

## 10. Not planned / documented gaps

| Item | Decision |
|---|---|
| PostgreSQL / MySQL (§78) | **Documented gap.** SQLite-only by design — WAL, STRICT tables, `modernc.org/sqlite`, single-writer `Store.Write`. See ARCHITECTURE-DECISION.md. |
| Payment processing (§38) | Not implemented and not simulated. The ledger is an accounting-ready abstraction; no payment rails are claimed. |
| Migration importers (§56) | Planned late; will report unmappable fields rather than claiming full fidelity. |

---

## Summary

- **Substantially more backend exists than the UI suggests.** The largest immediate
  win is not new features but exposing what is already built and tested:
  the reseller engine, deployments/preview/rollback, apply-runs, revisions,
  capabilities, maintenance mode, alerts, sessions and audit.
- **Three architecture gaps gate whole families**: the desired document has no
  outbound/routing; accounting cannot express coefficients; two adapters cannot
  install or restart.
- **`ServiceSchema` is the sleeper asset.** Every adapter already publishes a JSON
  Schema for its service params, it crosses gRPC, and the panel validates against
  it. The adapter-aware Inbound Studio (§68) and schema-validated JSON mode (§13)
  are mostly UI work on an existing contract, not new backend.

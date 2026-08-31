# Competitive audit — Rebecca, vpn-ui, OVPN Manager

**Date:** 2026-08-30
**Reviewed at:** shallow clones of each repo's default branch
**Purpose:** identify what Antimage is missing, and what it already does better.

> **Scope honesty.** "Line by line" across these three repos is ~1,500 files and
> well over 100k lines. This audit is not that. It is a structured pass:
> repository survey, licence check, architecture comparison, and close reading of
> the security-critical and feature-defining paths (authorization, user lifecycle,
> quota/limit enforcement, install/update channel). Everything asserted below was
> read in the source, not inferred from a README. Where I did not read something,
> it is not claimed.

---

## 0. Licensing — read this before writing any Antimage code

| Panel | Licence | What that means for us |
|---|---|---|
| **Rebecca** | **AGPL-3.0** | Strongest copyleft. Copying any code into Antimage forces Antimage to become AGPL **and** to offer source to every network user. |
| **vpn-ui** | **GPL-3.0** | Copying code forces Antimage to become GPL. |
| **OVPN Manager** | **Proprietary — "All Rights Reserved"** | Copying anything is straightforward copyright infringement. No permission is granted at all. |

**Working rule adopted for all follow-on work: clean-room.** We may study these
panels to learn *what features exist and what problems they solve*. We may not
copy code, structure-for-structure translations, or distinctive comments.
Every Antimage feature derived from this audit gets implemented from the
requirement, in Antimage's own architecture. Facts and feature ideas are not
copyrightable; expression is.

### ⚠️ Antimage itself has no LICENSE file

`README.md` renders a licence badge that links to `LICENSE`, and that file does
not exist in the repository. Under default copyright that makes Antimage "all
rights reserved" — which contradicts how it is presented, and means nobody can
legally self-host it. **This needs a decision and a file before any public
release.** It is a business decision, not an engineering one.

---

## 1. OVPN Manager (`eylandoo/openvpn_webpanel_manager`) — not reviewable

The README advertises "a comprehensive, self-hosted web panel built on Flask"
supporting OpenVPN, Ocserv, L2TP/IPsec, WireGuard and Sing-box, with PostgreSQL,
resellers and multi-node.

**The repository contains none of that.** It is 13 files:

```
DejTunnel.sh  backhaul.sh  install_cisco.sh  install_l2tp.sh
install_singbox.sh  install_wireguard.sh  mtu_finder.sh
openvpn_status.py  template.conf  vpn_manager.sh  wg-tunnel-manager.sh
LICENSE  README.md
```

Ten bash scripts, one 4,117-line Python status script, one config template. **The
Flask panel source is not published.** There is nothing to review, and no
feature claim in that README can be verified against code.

### What the installer actually does

`vpn_manager.sh:37-41` and `:660`:

```bash
wget -q -O /root/install_vpn.sh       https://eylanpanel.top/install_vpn.sh
wget -q -O /root/install_web_panel.sh https://eylanpanel.top/install_web_panel.sh
...
wget -q -O /root/update_app.sh https://eylanpanel.top/update_app.sh && chmod +x /root/update_app.sh && /root/update_app.sh
```

The real panel is fetched from a single third-party domain and executed as root.
`grep -rniE "sha256sum|sha1sum|md5sum|gpg --verify|checksum" *.sh` returns
**nothing** — there is no integrity check anywhere in the repository. Transport
is HTTPS, so this is not a plaintext-interception problem; the issue is that the
executed code is never visible to the operator, is not pinned to any version or
hash, and can change at any time on the vendor's side. `update_app.sh` makes that
a permanent channel.

**Conclusion:** exclude from the comparison. Use it as a counter-example — the
opposite of what Antimage's reviewable, hash-pinned, CI-verified release path is
for. **Do not install it on any machine that matters**, and specifically not on a
host holding Antimage keys.

---

## 2. Rebecca (`rebeccapanel/Rebecca`) — the real benchmark

671 files, 313 Go + 122 TSX. **The same stack as Antimage** (Go backend, React
dashboard, buf/protobuf, Docker), which makes it the most directly comparable.

Modules under `internal/app/`:

```
admin  ads  api  backup  dashboard  logging  migrations  node  nodeclient
nodecontroller  nordvpn  online  outboundsub  settings  system  telegram
usage  user  warp  webhook  xrayconfig
```

### 2.1 Where Rebecca is ahead of Antimage

#### a) An explicit user state machine — Antimage's biggest structural gap

`internal/app/user/lifecycle.go` (719 lines) drives a single `UserStatus` column
through named states: `active`, `limited`, `expired`, `on_hold`, `deleted`, with
a `last_status_change` timestamp and batched sweepers for each transition.

Antimage has **no status column at all.** State is implied by four independent
columns — `enabled`, `expires_at`, `frozen_at`, and the quota triple
(`quota_bytes`, `quota_used_bytes`, `quota_period_start`). There is no single
source of truth, no recorded transition, and no way to ask "why is this user
off?" without reconstructing it from four places. This is precisely what the
roadmap calls **Phase I — quota state machine made explicit**, and this audit
raises its priority.

#### b) `on_hold` — start the clock on first connection

Rebecca can create a user whose validity period does not begin until they first
connect (`UserStatusOnHold`, with a hold timeout). This is a headline feature in
this product category: it lets a reseller sell a credential today that starts its
30 days whenever the customer actually uses it.

**Antimage has no equivalent.** `expires_at` is absolute from creation.

#### c) Anti-fraud: a delete cap

`user/permissions.go`:

```go
DeleteCapExceededMessage = "User traffic is greater than the allowed delete limit."
```

An admin below a certain trust level cannot delete a user who has already
consumed significant traffic — closing the obvious reseller fraud of deleting
heavy users before settlement. **Antimage has nothing like this.** Given that
Antimage bills resellers on computed billable traffic, this is a real hole.

#### d) Granular per-admin capability flags

Beyond role, Rebecca gates individual capabilities: `AllowUnlimitedData`,
`AllowUnlimitedExpire`, `AllowNextPlan`, `SetFlow`, `AllowCustomKey`,
`CreateOnHold`, `ResetUsage`, `AdvancedActions`.

`AllowUnlimitedData`/`AllowUnlimitedExpire` matter most: without them a reseller
can mint themselves an uncapped account. Antimage's RBAC is *architecturally*
stronger (see 2.2) but has no equivalent of "may create users, but not unlimited
ones".

#### e) Modules Antimage has planned but not built

| Rebecca module | Antimage status |
|---|---|
| `backup/service.go` (1,489 lines) | **Phase M — documented in `docs/BACKUP-RESTORE.md`, not implemented** |
| `webhook/dispatcher.go` | **Phase J — not started** |
| `warp/`, `nordvpn/` | **Phase F §22 provider abstraction — not started** |
| `online/active.go` | Antimage tracks devices/IPs at the edge but has no "who is online now" surface |
| `ads/` | announcements to users — not in Antimage's plan at all |

### 2.2 Where Antimage is ahead of Rebecca

These are real advantages and should not be traded away while closing the gaps
above.

* **Two-layer authorization.** Antimage pairs `rbac.Check` with an *independent*
  SQL scope predicate (`store.SubjectScopeSQL` / `NodeScopeSQL`). Rebecca's model
  is role-preset booleans resolved in `RoleDefaultPermissions(role)` — a single
  layer. A missed check in Rebecca is a data leak; in Antimage the scope
  predicate still constrains the query.
* **Desired-state reconciliation with a single write path.** `CommitNodeChange`
  is the only thing that may move `desired_revision`. Rebecca's `nodecontroller`
  is a more conventional push model.
* **Mutation-tested guards (§76).** A permission check in Antimage is not
  considered proven until reverting it makes a *named* test fail.
* **Real-binary CI.** Antimage's `realruntime` job downloads actual Xray,
  sing-box, WireGuard and Hysteria2 and runs the adapters against them, with a
  build tag that makes a missing binary a *failure* rather than a skip.
* **Localization discipline.** Antimage enforces five locales at strict key
  parity and type-checks `t()` against `en.json`. Rebecca hardcodes Persian
  user-facing strings directly in Go source (`user/permissions.go` contains
  `"لیمیت حجم شما به پایان رسید"` as a Go constant) — those strings can never be
  translated without a code change.
* **Secrets sealed at rest.** Antimage seals subject credentials and the CA key
  with `box.Seal`. (Note: outbound `params` are still plaintext — see
  `HANDOFF.MD` §6.2. Fix that; do not let it become the counter-example.)

---

## 3. vpn-ui (`Sir-MmD/vpn-ui`) — the protocol-breadth benchmark

856 files: 348 Go, 143 HTML, 58 Python, plus vendored AmneziaWG C sources.
Notable for how much of the design is written down — 25 plan documents
(`ip-limiter-plan.md`, `speed-limit-plan.md`, `device-limit-plan.md`,
`reseller-plan.md`, `control-plane-framework.md`, …).

### 3.1 Protocol coverage is far wider than Antimage's

| | Protocols |
|---|---|
| **Antimage (5 adapters)** | Xray (VLESS/VMess/Trojan/SS, REALITY), sing-box, WireGuard, Hysteria2, L2TP/IPsec |
| **vpn-ui** | OpenVPN, IKEv2 (strongSwan + Libreswan), L2TP, **PPTP**, **SSTP**, **OpenConnect/ocserv**, WireGuard, **AmneziaWG**, **SSH**, SOCKS, Xray, **NaiveProxy**, **TUIC**, **GRE tunnels** |

Antimage's gaps, roughly in order of market demand for this segment:

1. **OpenConnect / ocserv (Cisco AnyConnect)** — heavily used in Iran; both other
   panels support it.
2. **OpenVPN** — the single most-requested protocol in this category.
3. **IKEv2** — native client on iOS/macOS/Windows, no app install needed.
4. **AmneziaWG** — censorship-resistant WireGuard fork; increasingly the answer
   when plain WG is DPI-blocked.
5. **TUIC**, **SSH**, **PPTP/SSTP** — long tail.

Each is a new adapter behind Antimage's existing `adapter.Caps` contract, so the
architecture already accommodates them. The cost is per-adapter real-binary CI,
not redesign.

### 3.2 Antimage already matches vpn-ui on limits — verified, not stubbed

vpn-ui devotes three plan documents to IP limiting, device limiting and speed
limiting. Antimage implements all three for real:

* `internal/node/enforcement/enforcement.go` — enforces `MaxIPs` and
  `MaxConnections`, rejecting with a reason string
* `internal/node/adapter/xray/policy.go` — converts `SpeedLimitUpKbps` to
  bytes/sec and applies it
* `internal/node/agent/enforcement.go` — carries the policy from the document

This was checked specifically because columns without enforcement would be a §77
violation. They are wired end to end.

---

## 4. Prioritized gap list for Antimage

Ordered by value ÷ risk. Items 1–3 are new findings from this audit; the rest
were already on the roadmap and this audit changes their priority.

| # | Gap | Source | Roadmap |
|---|---|---|---|
| 1 | **Explicit subject state machine** — one `status` column with recorded transitions, replacing four implied columns | Rebecca `user/lifecycle.go` | **Phase I**, promoted to next |
| 2 | **`on_hold` / start-on-first-connection** | Rebecca | new, folds into Phase I |
| 3 | **Delete cap + per-admin capability flags** (`allow_unlimited_data`, `allow_unlimited_expire`) — anti-fraud | Rebecca `user/permissions.go` | new, folds into Phase H |
| 4 | Seal outbound `params` at rest | Antimage self-audit | `HANDOFF.MD` §6.2 — **do before F §22** |
| 5 | Backup & restore | Rebecca `backup/` | Phase M (documented, not built) |
| 6 | Webhooks with signing/retry/dead-letter | Rebecca `webhook/` | Phase J |
| 7 | Provider abstraction (WARP/NordVPN-style) | Rebecca `warp/`, `nordvpn/` | Phase F §22 |
| 8 | "Online now" view | Rebecca `online/` | Phase N |
| 9 | **OpenConnect/ocserv adapter** | vpn-ui | new adapter |
| 10 | **OpenVPN adapter** | vpn-ui | new adapter |
| 11 | **IKEv2 adapter** | vpn-ui | new adapter |
| 12 | **AmneziaWG adapter** | vpn-ui | new adapter |
| 13 | Add a LICENSE file | this audit | blocks public release |

---

## 5. What this audit did not cover

Stated so the next person knows where the edges are:

* Rebecca's React dashboard (122 TSX files) — not read.
* Rebecca's `nodecontroller` / `nodeclient` reconciliation internals — only the
  module boundary was examined, not the protocol.
* vpn-ui's Go backend beyond the protocol inventory and its plan documents.
* vpn-ui's vendored AmneziaWG C sources.
* OVPN Manager's 4,117-line `openvpn_status.py` — skimmed for structure only;
  the panel it belongs to is not published, so reading it in depth buys little.
* No dynamic testing of any of the three. Nothing was installed or run.

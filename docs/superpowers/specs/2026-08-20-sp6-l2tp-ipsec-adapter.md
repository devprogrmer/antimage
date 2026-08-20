# antimage SP6 — L2TP/IPsec Adapter

**Date:** 2026-08-20  
**Status:** Planning  
**Scope:** Sub-project 6 of 8

---

## 1. Context and Dependencies

SP6 implements the third and final protocol family in antimage's adapter contract: L2TP/IPsec. This adapter completes the protocol triad established in the control-plane design (SP1 §1).

**Dependencies on SP1–SP5:**

- **SP1:** Adapter contract interface, reconciliation loop, service model, convergence tracking
- **SP2:** Subject/credential model, credential kinds, sealed credential storage
- **SP3:** Accounting ingestion, usage delta reporting, quota enforcement integration
- **SP4:** (No direct dependency; subscriptions don't typically cover L2TP/IPsec)
- **SP5:** Adapter registry, connection metrics, Prometheus metrics integration

**What exists:**

- `internal/node/adapter/adapter.go` — adapter interface with Plan/Apply separation
- `internal/node/adapter/xray/` — reference implementation with hot-add, self-accounting
- `internal/node/adapter/singbox/` — second reference implementation
- `internal/node/adapter/stub/` — minimal file-based adapter showing ownership markers
- `internal/panel/subjects/` — credential generation, sealing, subject lifecycle
- `internal/panel/nodes/accounting.go` — usage delta ingestion (SP3)
- Credential kinds: `uuid`, `password` (SP2)

**What SP6 adds:**

- L2TP/IPsec adapter implementing the adapter contract
- strongSwan configuration generation (IPsec tunnel)
- xl2tpd configuration generation (L2TP layer)
- PPP secrets management (`/etc/ppp/chap-secrets`)
- Per-IP nftables accounting (external accounting, not self-reporting)
- Password-based credential kind (already exists; SP6 consumes it)

---

## 2. Protocol Overview

L2TP/IPsec is a two-layer VPN stack:

1. **IPsec (strongSwan):** Establishes an encrypted tunnel using IKEv2 and pre-shared keys (PSK) or certificate-based authentication. SP6 uses PSK for simplicity.
2. **L2TP (xl2tpd):** Runs inside the IPsec tunnel, providing PPP sessions to clients.
3. **PPP authentication:** Uses CHAP secrets stored in `/etc/ppp/chap-secrets`.

**Authentication flow:**

1. Client initiates IKEv2 handshake with PSK (IPsec layer)
2. IPsec tunnel established
3. Client initiates L2TP session over the tunnel
4. xl2tpd spawns PPP session
5. PPP authenticates against `/etc/ppp/chap-secrets` (username/password)
6. PPP assigns IP address to client
7. Traffic flows through the tunnel

**Traffic accounting:**

Unlike Xray (which has a stats API) and OpenVPN (which will have management interface accounting in a future enhancement), L2TP/IPsec requires **external accounting via nftables**. The adapter:

- Creates nftables rules per assigned IP address
- Periodically reads nftables counters
- Computes deltas and reports via the `UsageReporter` interface (SP3)
- Persists cursor state to survive agent restarts

---

## 3. Design Decisions

### Decision 1: PSK vs Certificate-Based IPsec Auth

**Choice:** Pre-shared key (PSK) per subject.

**Why:**

- Simpler configuration (no CA integration required)
- Each subject gets a unique PSK, so revocation is clean
- Matches the credential model: one password-like secret per user
- Sufficient security for the target use case (personal/small-org VPNs)

**Rejected:** Certificate-based auth would require either:

- Integrating with the panel's CA (tight coupling; the adapter must not import `internal/panel`)
- Running a separate CA in the adapter (complexity, key distribution)

### Decision 2: Password Credential Kind

**Choice:** Use existing `password` credential kind from SP2.

**Why:**

- L2TP authentication uses username/password (CHAP secrets)
- IPsec PSK is also a password-like secret
- No new credential kind needed; SP2 already generates strong passwords

**Implementation:** Each subject gets:

- One `password` credential used for both CHAP secrets and IPsec PSK
- Username = subject name (sanitized for PPP/strongSwan compatibility)

### Decision 3: Per-IP Accounting via nftables

**Choice:** External accounting using nftables counters, not self-reporting.

**Why:**

- L2TP/IPsec has no built-in accounting API
- PPP accounting exists (`radacct`) but requires RADIUS, which is out of scope
- nftables counters are kernel-level, reliable, and match the accounting contract

**Implementation:**

- On PPP session start (xl2tpd hook), create nftables rules for that client IP
- Adapter periodically (every 60s) reads counters, computes deltas
- Reports deltas via `UsageReporter` interface to panel
- On PPP session end, rules remain (counters frozen) until next reconciliation cleans stale IPs

### Decision 4: IP Address Allocation

**Choice:** Static IP pool defined per service, assigned by xl2tpd from the pool.

**Why:**

- Simple, predictable, no external DHCP dependency
- Matches typical L2TP deployment pattern
- IP range is part of service params (operator-configured)

**Schema:** Service params include `ip_range` (e.g., "10.8.0.2-10.8.0.254"), `local_ip` (server endpoint).

### Decision 5: Config File Ownership

**Choice:** Adapter writes `/etc/strongswan/ipsec.conf`, `/etc/xl2tpd/xl2tpd.conf`, `/etc/ppp/chap-secrets`.

**Why:**

- Standard locations, no custom paths needed
- Ownership markers (like xray/stub adapters) detect drift
- Atomic writes via temp + rename pattern

**Constraints:**

- Adapter must run as root (or have capabilities for netfilter/ppp)
- Existing configs are detected as drift (not managed by antimage)

### Decision 6: Service Restart vs Reload

**Choice:**

- Adding/removing users: `DisruptReload` (reload CHAP secrets, swanctl --load-creds)
- Changing service params (ports, IP ranges): `DisruptRestart` (full service restart)

**Why:**

- strongSwan supports credential reload without dropping tunnels (`swanctl --load-creds`)
- xl2tpd re-reads config on SIGHUP
- Changing network params requires full restart

### Decision 7: Multiple Services per Node

**Choice:** One L2TP/IPsec service per node (enforced by adapter logic).

**Why:**

- strongSwan and xl2tpd are system-level daemons, not multi-instance by default
- Running multiple instances requires complex namespace/port isolation
- Single-service model matches typical deployment

**Implementation:** Adapter's `Plan` method returns an error if multiple L2TP services are enabled on the same node.

---

## 4. Adapter Contract Implementation

### 4.1 Descriptor

```go
Descriptor() adapter.Descriptor {
    return adapter.Descriptor{
        Kind:    "l2tp",
        Version: "1",
        Caps: adapter.Caps{
            HotUserAdd:      true,  // CHAP reload, swanctl --load-creds
            SelfAccounting:  false, // Uses external nftables accounting
            RequiresPKI:     false,
            CredentialKinds: []adapter.CredentialKind{adapter.CredPassword},
            ServiceSchema:   l2tpServiceSchema,
        },
    }
}
```

### 4.2 Service Schema

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["ip_range", "local_ip", "psk"],
  "properties": {
    "ip_range": {
      "type": "string",
      "pattern": "^[0-9.]+\\-[0-9.]+$",
      "description": "Client IP range (e.g., 10.8.0.2-10.8.0.254)"
    },
    "local_ip": {
      "type": "string",
      "format": "ipv4",
      "description": "Server local IP for L2TP (e.g., 10.8.0.1)"
    },
    "psk": {
      "type": "string",
      "minLength": 16,
      "description": "Pre-shared key for IPsec (separate from per-user CHAP passwords)"
    },
    "dns_servers": {
      "type": "array",
      "items": {"type": "string", "format": "ipv4"},
      "description": "DNS servers pushed to clients"
    }
  }
}
```

**Note:** `psk` is a shared secret for the IPsec tunnel itself (all clients use it), separate from per-user CHAP passwords.

### 4.3 Observe

Reads:

1. `/etc/strongswan/ipsec.conf` — check for ownership marker, compute checksum
2. `/etc/xl2tpd/xl2tpd.conf` — check for ownership marker, compute checksum
3. `/etc/ppp/chap-secrets` — check for ownership marker, compute checksum
4. Service status: `systemctl is-active strongswan` and `xl2tpd`

Returns `ObservedService`:

- `Present`: true if configs exist
- `Managed`: true if ownership marker found
- `Checksum`: SHA-256 of managed content

### 4.4 Plan

Diffs desired vs observed:

- If service enabled and not present → `StepInstallConfigs` (DisruptRestart)
- If service disabled and present → `StepRemoveConfigs` (DisruptRestart)
- If checksum differs → determine if only users changed:
  - Users only → `StepReloadCredentials` (DisruptReload)
  - Service params changed → `StepUpdateConfigs` (DisruptRestart)
- If drift detected (managed but checksum mismatched) → report via Health, plan replacement

### 4.5 Apply

Step kinds:

- **StepInstallConfigs:** Write all three config files atomically, start services
- **StepUpdateConfigs:** Write configs, restart services
- **StepReloadCredentials:** Write `/etc/ppp/chap-secrets`, run `swanctl --load-creds`, SIGHUP xl2tpd
- **StepRemoveConfigs:** Stop services, remove managed files
- **StepSetupAccounting:** Create nftables table/chain for L2TP traffic accounting
- **StepCleanStaleRules:** Remove nftables rules for IPs no longer assigned

### 4.6 Probe

Checks:

- strongSwan service running: `systemctl is-active strongswan`
- xl2tpd service running: `systemctl is-active xl2tpd`
- Listening ports: UDP 500 (IKE), UDP 4500 (NAT-T), UDP 1701 (L2TP)

Returns `Health{OK: true}` if all checks pass.

### 4.7 Usage (UsageReporter interface)

Implements SP3's `UsageReporter` interface:

```go
func (a *Adapter) Usage(ctx context.Context) ([]adapter.UsageSample, error)
```

**Implementation:**

1. Read nftables counters for all L2TP client IPs
2. Load previous counters from cursor file (`/var/lib/antimage/l2tp-accounting.json`)
3. Compute deltas (handle counter wraps, detect restarts)
4. Map IPs back to subject IDs (via active PPP sessions or lease file)
5. Return samples, persist new cursor state

**Edge cases:**

- Counter wrap (64-bit counters, extremely unlikely but check for backwards movement)
- Agent restart: cursor file survives, no duplicate reporting
- Service restart: counters reset to zero, detected by comparing timestamps

---

## 5. File Structure and Ownership

### 5.1 Config Files

**`/etc/strongswan/ipsec.conf`:**

```
# antimage-managed: service_id=123 checksum=abc123...
# DO NOT EDIT: this file is managed by antimage

config setup
    charondebug="ike 1, knl 1, cfg 0"
    uniqueids=no

conn antimage-l2tp
    keyexchange=ikev2
    ike=aes256-sha256-modp2048!
    esp=aes256-sha256!
    left=%any
    leftsubnet=0.0.0.0/0
    right=%any
    rightsubnet=10.8.0.0/24
    authby=psk
    type=transport
    auto=add
```

**`/etc/xl2tpd/xl2tpd.conf`:**

```
; antimage-managed: service_id=123 checksum=def456...
; DO NOT EDIT: this file is managed by antimage

[global]
port = 1701

[lns default]
ip range = 10.8.0.2-10.8.0.254
local ip = 10.8.0.1
require chap = yes
refuse pap = yes
require authentication = yes
name = antimage-l2tp
pppoptfile = /etc/ppp/options.xl2tpd
length bit = yes
```

**`/etc/ppp/chap-secrets`:**

```
# antimage-managed: service_id=123 checksum=789abc...
# DO NOT EDIT: this file is managed by antimage

alice    *    secret123abc    *
bob      *    secret456def    *
```

**`/etc/ppp/options.xl2tpd`:**

```
# antimage-managed: service_id=123 checksum=xyz...
# DO NOT EDIT: this file is managed by antimage

require-mschap-v2
ms-dns 8.8.8.8
ms-dns 8.8.4.4
nomppe
nodefaultroute
proxyarp
lcp-echo-interval 30
lcp-echo-failure 4
```

### 5.2 Cursor and State Files

**`/var/lib/antimage/l2tp-accounting.json`:**

```json
{
  "last_poll": 1692547200,
  "counters": {
    "10.8.0.2": {"rx": 1048576, "tx": 2097152},
    "10.8.0.3": {"rx": 524288, "tx": 1048576}
  }
}
```

**`/var/lib/antimage/l2tp-sessions.json`:**

Maps active PPP sessions to subject IDs (updated by PPP ip-up/ip-down hooks).

---

## 6. nftables Accounting Implementation

### 6.1 Table Structure

```nftables
table inet antimage_l2tp {
    chain input {
        type filter hook input priority 0; policy accept;
        ip saddr 10.8.0.2 counter packets 1234 bytes 1048576
        ip saddr 10.8.0.3 counter packets 5678 bytes 524288
    }
    chain output {
        type filter hook output priority 0; policy accept;
        ip daddr 10.8.0.2 counter packets 2345 bytes 2097152
        ip daddr 10.8.0.3 counter packets 6789 bytes 1048576
    }
}
```

### 6.2 PPP Hooks

**`/etc/ppp/ip-up.d/antimage`:**

```bash
#!/bin/bash
# Called when PPP session starts
# Args: $1=interface $4=client_ip $5=remote_ip $6=username
echo "$6 $5" >> /var/lib/antimage/l2tp-active-sessions.log
# Notify agent to create nftables rules (via signal or polling)
```

**`/etc/ppp/ip-down.d/antimage`:**

```bash
#!/bin/bash
# Called when PPP session ends
# Args: same as ip-up
sed -i "/^$6 $5$/d" /var/lib/antimage/l2tp-active-sessions.log
```

**Alternative (simpler):** Adapter polls `/var/run/xl2tpd/l2tp-control` or parses `xl2tpd` logs to detect active sessions.

### 6.3 Usage Delta Reporting

Every 60 seconds (configurable):

1. Run `nft list table inet antimage_l2tp -a` (JSON output)
2. Parse counters for each IP
3. Load previous counters from cursor file
4. Compute `delta_rx = current_rx - prev_rx`, `delta_tx = current_tx - prev_tx`
5. Map IPs to subject IDs via session log
6. Build `[]adapter.UsageSample` and return
7. Save current counters to cursor file

---

## 7. State Transitions and Lifecycle

### 7.1 Service Enabled

1. Panel commits service change → revision bump
2. Agent receives bump, calls `GetDesiredSnapshot`
3. Adapter `Observe` finds no configs
4. Adapter `Plan` emits `StepInstallConfigs` (DisruptRestart)
5. Agent `Apply` writes configs, starts services
6. Adapter `Probe` checks service health
7. Adapter `Usage` starts reporting traffic (if subjects exist)

### 7.2 User Added

1. Panel adds subject → revision bump
2. Adapter `Observe` reads current configs
3. Adapter `Plan` detects user list change → `StepReloadCredentials` (DisruptReload)
4. Agent `Apply` writes `/etc/ppp/chap-secrets`, runs `swanctl --load-creds`, SIGHUPs xl2tpd
5. No session disruption; new user can connect immediately

### 7.3 User Removed

1. Panel disables subject → revision bump
2. Adapter `Plan` emits `StepReloadCredentials`
3. Agent `Apply` updates CHAP secrets, reloads
4. Active sessions for that user continue (PPP session already authenticated)
5. New connections from that user are rejected

**Note:** Forcibly disconnecting active sessions requires additional logic (killing PPP process for that user). SP6 does NOT implement immediate disconnect; that's a future enhancement.

### 7.4 Service Params Changed

1. Panel updates service params (IP range, DNS, PSK) → revision bump
2. Adapter `Plan` emits `StepUpdateConfigs` (DisruptRestart)
3. Agent `Apply` writes all configs, restarts services
4. All active sessions drop (expected for param changes)

### 7.5 Service Disabled

1. Panel disables service → revision bump
2. Adapter `Plan` emits `StepRemoveConfigs` (DisruptRestart)
3. Agent `Apply` stops services, removes managed files
4. nftables rules remain (no traffic to count)

---

## 8. Integration Points

### 8.1 With SP1 (Adapter Contract)

- Implements `adapter.Adapter` interface
- Registers in `internal/node/adapter/registry.go`
- Reports capabilities via `Descriptor`

### 8.2 With SP2 (Subjects/Credentials)

- Consumes `password` credential kind
- Subject name → PPP username (sanitized: lowercase, alphanumeric + underscore only)
- Credential value → CHAP secret and IPsec PSK

### 8.3 With SP3 (Accounting/Quotas)

- Implements `adapter.UsageReporter` interface
- Reports traffic deltas every 60s
- Panel ingests via `nodes.IngestUsageReport`
- Quota enforcement: panel disables subject when quota exhausted, revision bump triggers credential reload

### 8.4 With SP4 (Subscriptions)

- No direct integration (L2TP/IPsec typically not subscription-based)
- Future: Could generate `.mobileconfig` profiles for iOS/macOS

### 8.5 With SP5 (Adapter Registry/Metrics)

- Adapter version and capabilities reported in `Hello` message
- Probe results feed into `node_health` table
- Prometheus metrics: `antimage_adapter_health{node_id,kind="l2tp"}`

---

## 9. Testing Strategy

### 9.1 Unit Tests

- Config generation: desired → rendered output, check syntax
- Checksum computation: matches stub/xray pattern
- Ownership marker parsing
- IP → subject ID mapping logic
- nftables counter delta computation (including wrap, restart detection)

### 9.2 Property Tests

- Convergence: applying a plan yields empty plan on re-planning
- Canonicalization: config rendering is deterministic

### 9.3 Integration Tests

- Full Observe → Plan → Apply cycle with temp directories
- Drift detection: hand-edit config, observe reports drift
- Reload vs restart: verify disruption levels

### 9.4 End-to-End Tests

- Real strongSwan + xl2tpd in container
- Connect with L2TP/IPsec client (e.g., `ipsec` + `xl2tpc`)
- Verify tunnel establishment, IP assignment, traffic flow
- Add user mid-session, verify no disruption
- Read nftables counters, verify accounting

**Scope:** E2E tests are optional for SP6 (complex setup). Integration tests suffice.

---

## 10. Security Considerations

### 10.1 Credential Handling

- PSK and CHAP secrets are sealed at rest (SP2 encryption)
- Config files written with mode `0600`, owned by root
- Secrets never logged, never appear in audit logs

### 10.2 Service Isolation

- strongSwan and xl2tpd run as system services (root required for PPP/netfilter)
- No privilege escalation needed (adapter already runs as root)

### 10.3 Drift and Tampering

- Ownership markers detect external edits
- Adapter reports drift via Health, refuses to apply until resolved (admin must approve overwrite)

### 10.4 Accounting Integrity

- nftables rules are kernel-level, tamper-resistant
- Cursor file integrity: checksums stored alongside counters (future enhancement)
- Agent restart does not lose accounting data (cursor persists)

---

## 11. Out of Scope for SP6

- **Certificate-based IPsec auth:** PSK only
- **RADIUS integration:** No centralized AAA
- **IPv6 support:** IPv4 only for SP6
- **Multiple L2TP services per node:** One service per node
- **Immediate session disconnect on revocation:** User removal prevents new connections but doesn't kill active sessions
- **Advanced strongSwan features:** No split tunneling, custom routing, or IKEv1
- **macOS/iOS `.mobileconfig` generation:** Future SP4 extension

---

## 12. Definition of Done

SP6 is complete when:

1. L2TP adapter implements all five adapter interface methods
2. Adapter registers in `internal/node/adapter/registry.go`
3. Config generation produces valid strongSwan/xl2tpd/PPP configs
4. Ownership markers and checksums work (drift detection)
5. Disruption levels correct: reload for users, restart for params
6. nftables accounting implemented, `UsageReporter` interface functional
7. Unit tests cover config rendering, delta computation, IP mapping
8. Integration test covers Observe → Plan → Apply cycle
9. `go test ./...` passes
10. `go vet ./...` passes
11. `golangci-lint run` reports 0 issues
12. No regressions in SP1–SP5 behavior
13. Adapter can be enabled on a test node, service starts, basic connectivity verified

---

## 13. Implementation Phases

**Phase A: Adapter Skeleton**

- Create `internal/node/adapter/l2tp/adapter.go`
- Implement `Descriptor`, define service schema
- Register in adapter registry
- Minimal `Observe`/`Plan`/`Apply`/`Probe` stubs

**Phase B: Config Generation**

- strongSwan `ipsec.conf` renderer
- xl2tpd config renderer
- CHAP secrets renderer (`/etc/ppp/chap-secrets`)
- PPP options renderer
- Ownership markers and checksums

**Phase C: Reconciliation Logic**

- `Observe` implementation (read configs, check service status)
- `Plan` implementation (diff logic, disruption levels)
- `Apply` implementation (write configs, start/stop/reload services)
- `Probe` implementation (health checks)

**Phase D: Accounting**

- nftables table/chain setup
- PPP hook scripts (ip-up/ip-down)
- Counter polling logic
- Delta computation and cursor persistence
- `Usage` method implementation

**Phase E: Testing and Verification**

- Unit tests for all components
- Integration tests
- Manual verification with real services
- Documentation updates

---

## 14. Files Created/Modified

**Created:**

- `internal/node/adapter/l2tp/adapter.go`
- `internal/node/adapter/l2tp/adapter_test.go`
- `internal/node/adapter/l2tp/config.go` — config rendering
- `internal/node/adapter/l2tp/config_test.go`
- `internal/node/adapter/l2tp/accounting.go` — nftables logic
- `internal/node/adapter/l2tp/accounting_test.go`
- `internal/node/adapter/l2tp/service.go` — systemctl wrappers
- `internal/node/adapter/l2tp/hooks/ip-up` — PPP hook script
- `internal/node/adapter/l2tp/hooks/ip-down` — PPP hook script

**Modified:**

- `internal/node/adapter/registry.go` — register L2TP adapter
- `docs/superpowers/specs/2026-08-13-antimage-control-plane-design.md` — update with SP6 completion

---

## 15. Dependencies (External)

- **strongSwan:** `apt install strongswan` (Debian/Ubuntu)
- **xl2tpd:** `apt install xl2tpd`
- **nftables:** `apt install nftables` (already in modern Debian/Ubuntu)
- **PPP:** `apt install ppp` (typically pre-installed)

Agent checks for these during startup; adapter reports health degraded if missing.

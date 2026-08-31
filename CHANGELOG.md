# Changelog

All notable changes to antimage will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.0.0] - 2026-08-23

### 🎉 First Stable Public Release

This is the first production-ready public release of antimage, a self-hosted VPN/proxy fleet control plane with desired-state reconciliation.

### Highlights

- **Production-Ready Control Plane**: Multi-admin RBAC, TOTP 2FA, audit logging, node management
- **5 Protocol Adapters**: Xray (VLESS/VMess/Trojan), WireGuard, Hysteria2, L2TP/IPsec, sing-box
- **Real-Time Enforcement**: Quota enforcement (<1ms), connection limits, traffic accounting
- **Subscription System**: V2Ray and Clash format generation with automatic updates
- **Deployment Orchestration**: Canary and rolling deployments with automatic rollback
- **Zero Inbound Ports**: Agents dial out over mTLS—firewall-friendly architecture

### Added

#### Control Plane
- Multi-admin authentication with Argon2id password hashing
- RBAC with four built-in roles: `super_admin`, `admin`, `reseller`, `readonly`
- TOTP two-factor authentication with recovery codes
- Opaque server-side sessions with immediate revocation
- Login rate limiting and account lockout after 5 failed attempts
- Append-only audit log for privileged actions and authorization denials
- Per-node access scoping enforced at SQL level
- Server-Sent Events (SSE) for real-time node status updates
- React-based web UI with RTL support and i18n (English, Farsi, Russian, Chinese, Arabic)

#### Node Agent
- Autonomous reconciliation agent with desired-state convergence
- mTLS authentication with private CA and certificate pinning
- One-time token enrolment with automatic certificate issuance
- Self-healing when offline nodes return
- SSH bootstrap with host-key pinning
- Configuration drift detection via SHA-256 hash verification
- Hot reload support (protocol-dependent)

#### Protocol Adapters
- **Xray**: Full E2E verification with Stats API integration, hot user add/remove, traffic accounting
- **WireGuard**: Config generation with peer management and `wg show` integration
- **Hysteria2**: QUIC-based protocol with native bandwidth configuration
- **L2TP/IPsec**: strongSwan + xl2tpd with PSK authentication and nftables accounting
- **sing-box**: Universal proxy platform with multi-protocol support

#### Traffic Management
- Real-time traffic accounting (uplink/downlink bytes per user)
- Instant quota enforcement at admission (<1ms latency)
- Connection limit tracking with immediate termination on violation
- External tc-based speed limit enforcement (Linux)
- Live user revocation with active session termination

#### Subscriptions
- V2Ray format: Base64-encoded vmess://, vless://, trojan:// links
- Clash format: YAML configuration with proxy groups
- Multi-protocol support: Xray, WireGuard, Hysteria2
- Dynamic traffic display in subscription (e.g., "1.2GB / 10GB")

#### Deployment System
- Canary deployment: Roll out to subset first, then proceed
- Rolling deployment: Sequential updates with configurable batch size
- All-at-once deployment: Immediate update to all nodes
- Automatic rollback on convergence failure or timeout
- Deployment history and status tracking
- Crash recovery for stale in-progress deployments
- Timeout enforcement for stuck deployments

#### Security
- Path traversal protection on file operations
- Symlink attack prevention
- Private CA with SHA-256 fingerprint verification
- Certificate allow-list revocation
- Secret encryption with master key
- Audit immutability (no deletions permitted)

#### Observability
- Structured logging with slog
- Health check endpoints
- Node status tracking (online/offline/maintenance/error)
- Revision convergence monitoring
- Configuration drift detection
- Deployment progress tracking

#### CLI Tools
- `antimage-ctl`: Local administration (create-admin, reset-password, backup, list-admins)
- `antimage-panel`: Control plane server
- `antimage-node`: Agent daemon

#### API
- RESTful HTTP API with comprehensive endpoints
- Session-based authentication
- Real-time SSE event stream for node status
- Deployment management endpoints
- Subscription generation endpoints

### Fixed
- Migration 00022 now preserves services during nodes table recreation
- Xray email parser supports connection suffix format (e.g., `subject-1@antimage-2`)
- Deployment RBAC checks now properly audit authorization denials
- Middleware allows handler-level permission checks with proper audit logging
- Test suite stability improvements across all packages

### Known Limitations
- **Xray Speed Limits**: Native upSpeed/downSpeed fields not enforced (use external tc)
- **Hysteria2**: Runtime verification NOT PERFORMED (no Linux test environment)
- **WireGuard**: Traffic accounting available but not integrated with Enforcer
- **L2TP/IPsec**: nftables accounting available but not integrated with Enforcer
- **sing-box**: No hot reload, requires full restart for user changes
- **Scale**: Single panel only, no horizontal scaling
- **Performance**: NOT VERIFIED beyond test workloads
- **Platform**: Linux production only, Windows/macOS for development

### Testing
- 38 packages with 400+ tests, all passing
- `go test ./...` ✅
- `go vet ./...` ✅
- Unit, integration, and E2E tests for core components
- Runtime verification for Xray adapter with real traffic
- Database migration tests with upgrade path verification

### Documentation
- Comprehensive README with architecture diagrams
- Protocol support matrix with honest status indicators
- Installation and quick start guides
- Configuration reference
- API documentation
- Security model documentation
- Upgrade and rollback instructions

### Breaking Changes from v0.1.0
- Database schema migration 00022 adds `maintenance` status
- Deployment endpoints now require explicit RBAC authorization
- API responses include additional metadata fields

### Upgrade Path
1. Backup database: `antimage-ctl backup`
2. Stop services: `systemctl stop antimage-panel antimage-node`
3. Replace binaries with v1.0.0 versions
4. Start panel: `systemctl start antimage-panel` (migrations run automatically)
5. Verify health: `curl http://localhost:8080/health`
6. Restart nodes: `systemctl start antimage-node`

### Rollback Procedure
If upgrade fails, restore binaries and database from backup, then restart services.

---

## [0.1.0] - 2024-XX-XX

### Initial Development Release

Foundation implementation (SP1-SP7):
- Control plane spine (SP1)
- Xray and sing-box adapters (SP2)
- Accounting and quotas (SP3)
- Subscription delivery (SP4)
- Enhanced adapter management (SP5)
- L2TP/IPsec adapter (SP6)
- Observability improvements (SP7)

---

[1.0.0]: https://github.com/devprogrmer/antimage/releases/tag/v1.0.0
[0.1.0]: https://github.com/devprogrmer/antimage/releases/tag/v0.1.0

# Phase 3: WireGuard Adapter Implementation Plan

## Overview
Implement WireGuard VPN adapter for antimage control plane. WireGuard is essential for competitive parity - it's the most requested protocol after Xray/V2Ray.

## Architecture

### Adapter Structure
```
internal/node/adapter/wireguard/
├── adapter.go          # Adapter implementation (Observe/Plan/Apply/Verify)
├── adapter_test.go     # Unit tests
├── config.go           # WireGuard config generation
├── config_test.go      # Config generation tests
├── accounting.go       # Traffic statistics via wg show
├── probe.go            # Health checking
└── service.go          # Service descriptor
```

### Integration Points

**Control Plane (Panel):**
- Service params: interface name, listen port, private key, subnet
- Validation via JSON schema
- Peer credentials stored encrypted (like other protocols)

**Node Agent:**
- Uses wg-quick for interface management
- Config files in /etc/wireguard/antimage-{port}.conf
- systemd service per interface: wg-quick@antimage-{port}

**Traffic Accounting:**
- Parse `wg show {interface} transfer` output
- Extract per-peer RX/TX bytes
- Report via gRPC like Xray accounting

## Implementation Tasks

### Task 1: Service Descriptor & Schema
- [ ] Define WireGuard service params struct
- [ ] JSON schema for panel validation
- [ ] Adapter descriptor registration
- [ ] Credential kind: keypair (public/private)

### Task 2: Config Generation
- [ ] Generate wg-quick config format
- [ ] Peer section per subject
- [ ] AllowedIPs allocation
- [ ] DNS configuration
- [ ] PostUp/PostDown scripts (optional)

### Task 3: Observe Implementation
- [ ] Check if interface exists
- [ ] Verify config file matches desired
- [ ] Parse current peer list
- [ ] Checksum comparison for drift

### Task 4: Apply Implementation  
- [ ] Write config to /etc/wireguard/
- [ ] Create systemd service override
- [ ] Enable wg-quick@interface
- [ ] Restart service on config change

### Task 5: Traffic Accounting
- [ ] Parse `wg show` output
- [ ] Map peers to subject IDs
- [ ] Implement UsageReporter interface
- [ ] Report RX/TX deltas

### Task 6: Health Probing
- [ ] Check systemd service status
- [ ] Verify interface is up
- [ ] Count active peers
- [ ] Detect handshake failures

### Task 7: Testing
- [ ] Config generation determinism
- [ ] Drift detection
- [ ] Convergence scenarios
- [ ] Integration test with real wg-quick

## Technical Decisions

### D101: wg-quick vs wg command
**Decision:** Use wg-quick  
**Rationale:**  
- Handles interface setup/teardown automatically
- Manages routing and iptables rules
- systemd integration built-in
- Standard tool operators expect

### D102: Config file location
**Decision:** `/etc/wireguard/antimage-{port}.conf`  
**Rationale:**
- Namespaced to avoid conflicts
- wg-quick convention
- Port-based naming matches Xray pattern

### D103: Peer public key as email
**Decision:** Use base64(public_key) as email tag  
**Rationale:**
- WireGuard public key uniquely identifies peer
- No separate "email" concept in WireGuard
- Enables accounting correlation

### D104: Disruption classification
**Restart required:**
- Private key change
- Listen port change
- Interface subnet change

**Hot reload possible:**
- Add/remove peers
- Update peer AllowedIPs

### D105: AllowedIPs allocation
**Decision:** Sequential allocation from subnet  
**Example:** 10.8.0.0/24 → peers get 10.8.0.2, 10.8.0.3, ...  
**Rationale:**
- Simple and deterministic
- No complex IPAM required
- Subject ID determines IP offset

## Dependencies

**System Requirements:**
- wireguard-tools package
- kernel module or userspace implementation
- systemd

**Panel Requirements:**
- Master key for keypair encryption
- Subjects table for peer management
- Accounting infrastructure (already exists)

## Testing Strategy

### Unit Tests
- Config generation with various peer counts
- Drift detection scenarios
- Accounting output parsing

### Integration Tests  
- Full Observe→Plan→Apply→Verify cycle
- wg-quick interaction (mocked in CI)
- Traffic statistics collection

### E2E Tests (optional)
- Real WireGuard connection from client
- Traffic flow verification
- Quota enforcement

## Estimated Effort
- Task 1-2: 4 hours (schema, config generation)
- Task 3-4: 6 hours (observe, apply, systemd)
- Task 5: 3 hours (accounting)
- Task 6: 2 hours (health checks)
- Task 7: 4 hours (testing)

**Total:** 19 hours (~2-3 days)

## Success Criteria
- [ ] Can create WireGuard service via panel
- [ ] Node agent converges WireGuard config
- [ ] Peers can connect and route traffic
- [ ] Traffic accounting reports correctly
- [ ] Drift detection works
- [ ] All tests pass with -race

## Notes
- WireGuard requires elevated privileges (CAP_NET_ADMIN)
- Some cloud providers block UDP (WireGuard's protocol)
- Consider adding kernel vs userspace detection
- MTU configuration may need tuning per environment

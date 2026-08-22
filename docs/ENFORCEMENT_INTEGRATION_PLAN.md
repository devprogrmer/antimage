# Xray Enforcement Integration Plan

## Problem Statement

The enforcement engine is ready, but Xray connections don't reach it. We need to intercept connection attempts, check policies, and register/reject connections atomically.

## Xray Architecture Analysis

### Connection Flow

1. Client connects to Xray inbound (VLESS/VMess/Trojan)
2. Xray authenticates user (checks UUID/password)
3. Xray proxies traffic
4. Connection terminates

### Integration Points

**Option A: Xray API Hook (IF EXISTS)**
- Some proxies support pre-auth callbacks
- Would need to check Xray docs for auth plugin support
- **Status:** Unknown - need to verify

**Option B: External Authentication Layer**
- Place authentication proxy in front of Xray
- Proxy checks enforcement, then forwards to Xray
- **Status:** Complex, adds latency

**Option C: Accounting-Based Enforcement**
- Use Xray stats API to track active connections
- Periodically query and enforce limits
- **Status:** Reactive, not preventive (connection already established)

**Option D: Config-Based Pre-filtering**
- Generate user list based on current enforcement status
- Only users below limits appear in config
- **Status:** Requires frequent config reloads, disruptive

**Option E: Hybrid - Accounting + Periodic Validation**
- Accept all authenticated connections initially
- Periodically check enforcement status
- Terminate violating connections via RemoveUser API
- **Status:** Best-effort, not preventive

## Recommended Approach: Option E (Hybrid)

### Why?

1. **No Xray modification required** - works with standard Xray
2. **Uses existing API** - RemoveUser already tested
3. **Practical** - preventive enforcement needs Xray core changes
4. **Honest** - we can document limitations clearly

### How It Works

```
1. Xray accepts connection (user authenticated)
2. Xray stats API reports connection via QueryStats
3. Node agent periodically queries stats
4. Agent calls enforcer.CheckAndRegisterConnection for each new connection
5. If policy violation detected, agent calls RemoveUser to disconnect
6. Classification: BEST_EFFORT (brief window where violation exists)
```

### Implementation Steps

1. **Add connection tracking to Xray adapter**
   - Track known connections via QueryStats
   - Detect new connections by comparing with previous state
   - Extract device ID from user metadata (if available)

2. **Implement enforcement loop**
   - Run every 5-10 seconds (configurable)
   - Query Xray stats
   - Check each active connection against enforcer
   - Terminate violating connections

3. **Handle device ID**
   - Initially: use subject ID as device ID (no multi-device support)
   - Future: extract from TLS client cert or custom header
   - Document limitation clearly

4. **Add connection cleanup**
   - On disconnect, call enforcer.UnregisterConnection
   - Handle Xray restart (clear all tracked connections)

5. **Add metrics**
   - Connections accepted
   - Connections rejected
   - Policy violations detected
   - Average enforcement latency

## Alternative: True Preventive Enforcement

**Requires:** Xray core modification or external auth layer

### External Auth Layer Architecture

```
Client → Auth Proxy → Xray
            ↓
        Enforcer
```

**Auth Proxy Responsibilities:**
1. TLS termination
2. Extract client certificate or device ID from custom header
3. Call enforcer.CheckAndRegisterConnection
4. If allowed, proxy connection to Xray
5. If rejected, close connection with error

**Advantages:**
- True preventive enforcement
- No race window
- Works with any protocol

**Disadvantages:**
- Complex to implement
- Adds latency
- Another component to maintain
- TLS re-encryption overhead

**Recommendation:** Implement Option E (hybrid) first, evaluate auth proxy later if needed.

## Device ID Extraction

### Challenge

Xray doesn't expose device IDs natively. We need a mechanism.

### Options

**A. TLS Client Certificate Fingerprint**
- Require mutual TLS
- Use cert fingerprint as device ID
- **Pro:** Cryptographically secure, can't be spoofed
- **Con:** Requires client cert setup, not all clients support

**B. Custom Header/Metadata**
- Client sends device ID in custom header
- **Pro:** Simple, works with all clients
- **Con:** Can be spoofed easily

**C. Subject-Only (No Device Tracking)**
- Use subject ID as device ID (all connections = 1 "device")
- Device limit becomes connection limit
- **Pro:** Simple, works immediately
- **Con:** No true multi-device support

**D. IP-Based Heuristic**
- Use source IP as device ID
- **Pro:** Works without client changes
- **Con:** Dynamic IPs break it, NAT causes false positives

### Recommended: Option C Initially, Migrate to A/B

**Phase 1:** Use subject ID as device ID
- Gets enforcement working immediately
- Document limitation: "device limit = connection limit for Xray"
- MaxDevices effectively becomes MaxConnections

**Phase 2:** Add proper device ID extraction
- Evaluate TLS client cert vs custom header
- Implement based on user requirements
- Update documentation

## Implementation Tasks

### Task 1: Add Connection Tracking to Xray Adapter

File: `internal/node/adapter/xray/enforcement.go` (new file)

```go
// ConnectionTracker tracks active Xray connections for enforcement.
type ConnectionTracker struct {
	adapter   *Adapter
	enforcer  *enforcement.Enforcer
	lastStats map[string]UserStat // email -> last seen stats
	mu        sync.Mutex
}

func (ct *ConnectionTracker) Sync(ctx context.Context) error {
	// Query current stats
	stats, err := ct.adapter.rt.QueryStats(ctx)
	if err != nil {
		return err
	}

	// Detect new/removed connections
	current := make(map[string]UserStat)
	for _, stat := range stats {
		current[stat.Email] = stat
		
		// Check if this is a new connection
		if _, exists := ct.lastStats[stat.Email]; !exists {
			ct.handleNewConnection(ctx, stat.Email)
		}
	}

	// Detect disconnections
	for email := range ct.lastStats {
		if _, stillActive := current[email]; !stillActive {
			ct.handleDisconnection(ctx, email)
		}
	}

	ct.lastStats = current
	return nil
}
```

### Task 2: Integrate with Node Agent

File: `internal/node/agent/enforcement.go` (extend existing)

Add enforcement sync loop that calls Xray connection tracker.

### Task 3: Update Documentation

- Mark Xray enforcement as BEST_EFFORT
- Document enforcement window (5-10 seconds)
- Document device ID limitation
- Update capability matrix

### Task 4: Add E2E Tests

- Test connection limit enforcement (with delay window)
- Test speed limit enforcement
- Test policy update behavior
- Test revocation

## Timeline Estimate

- Task 1 (Connection Tracking): 4 hours
- Task 2 (Agent Integration): 2 hours
- Task 3 (Documentation): 1 hour
- Task 4 (E2E Tests): 4 hours
- **Total: ~11 hours (1.5 days)**

## Success Criteria

1. ✅ Connection exceeding limit is terminated within 10 seconds
2. ✅ Speed limits are enforced (measurable with iperf)
3. ✅ Policy updates take effect within 1 reconciliation cycle
4. ✅ Revoked devices are disconnected within 10 seconds
5. ✅ All tests pass
6. ✅ Documentation updated with honest limitations

## Next Steps

1. Implement ConnectionTracker
2. Add enforcement loop to agent
3. Write E2E tests
4. Update documentation
5. Commit with clear message about BEST_EFFORT classification

**Status:** Ready to implement

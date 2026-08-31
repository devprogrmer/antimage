# L2TP/IPsec Adapter Implementation Completion

**Date:** 2026-08-26  
**Task:** Complete L2TP/IPsec adapter implementation with drift detection and port checks

## Summary

Successfully implemented drift detection in `plan.go` and port checking in `probe.go` for the L2TP/IPsec adapter, bringing it to feature parity with other adapters in the antimage project.

## Changes Made

### 1. Drift Detection (`internal/node/adapter/l2tp/plan.go`)

**What Changed:**
- Modified Case 3 in the `Plan()` function to handle both managed and unmanaged services
- Added explicit drift detection when `observedService.Managed == false`
- Plans a `StepUpdateConfigs` with `DisruptRestart` when config drift is detected

**Implementation Details:**
- Uses SHA-256 checksums to compare desired vs observed state
- Combines checksums from all 5 config files (ipsec.conf, ipsec.secrets, xl2tpd.conf, chap-secrets, options.xl2tpd)
- When files exist but lack the antimage management marker, drift is detected
- Returns `ActionRestart` (via `StepUpdateConfigs` + `DisruptRestart`) to restore managed state

**Pattern Reference:**
- Follows the WireGuard adapter pattern (lines 200-206 in `wireguard/plan.go`)
- Mirrors the "config drift detected" approach used across adapters

### 2. Port Checking (`internal/node/adapter/l2tp/probe.go`)

**What Changed:**
- Implemented `isPortListening()` function to check UDP port availability
- Added port checks for all three L2TP/IPsec ports in `Probe()`
- Returns detailed health status including port state

**Ports Checked:**
- UDP 500: IKE (Internet Key Exchange) - strongSwan
- UDP 4500: NAT-T (NAT Traversal) - strongSwan  
- UDP 1701: L2TP - xl2tpd

**Implementation Details:**
- Uses `net.ListenPacket()` to attempt binding to each port
- If bind fails with "address already in use", port is listening (expected state)
- Cross-platform: handles Windows and Unix error message differences
- Helper function `containsAny()` for flexible error string matching
- Alternative `isPortReachable()` function provided for future use

### 3. Test Coverage

**New Test Files:**

#### `internal/node/adapter/l2tp/plan_test.go` (6 tests)
- `TestPlanDriftDetection`: Verifies drift detection plans a restart
- `TestPlanManagedServiceChecksum`: Tests checksum comparison logic
- `TestPlanInstallRemove`: Tests install/remove planning
- `TestPlanMultipleServices`: Validates single-service constraint

#### `internal/node/adapter/l2tp/probe_test.go` (5 tests)
- `TestIsPortListening`: Tests port detection with active listener
- `TestIsPortListeningUnusedPort`: Tests detection of unused ports
- `TestIsPortListeningStandardPorts`: Documents expected behavior for L2TP ports
- `TestIsPortReachable`: Tests alternative reachability check
- `TestPortListeningErrorHandling`: Tests error handling
- `BenchmarkIsPortListening`: Performance benchmark

**Test Results:**
```
PASS
ok  	github.com/amyrm/antimage/internal/node/adapter/l2tp	0.941s
```

All tests pass, including existing tests (no regressions).

## Files Modified

1. **internal/node/adapter/l2tp/plan.go**
   - Added drift detection logic in Case 3
   - Plans restart when unmanaged service detected
   - Removed obsolete Case 4 comment

2. **internal/node/adapter/l2tp/probe.go**
   - Added imports: `fmt`, `net`, `strings`, `time`
   - Implemented `isPortListening()` function
   - Implemented `containsAny()` helper
   - Implemented `isPortReachable()` alternative
   - Updated `Probe()` to check all three required ports
   - Enhanced health detail message

## Files Created

1. **internal/node/adapter/l2tp/plan_test.go** (6,302 bytes)
2. **internal/node/adapter/l2tp/probe_test.go** (4,617 bytes)

## Verification

✅ All tests pass  
✅ Project builds successfully (`go build ./cmd/antimage-node`)  
✅ No regressions in existing tests  
✅ Follows established adapter patterns  
✅ Cross-platform compatible (Windows/Linux)

## Technical Notes

### Drift Detection Algorithm
1. Render desired configs with current params and subjects
2. Compute SHA-256 checksum for each of 5 config files
3. Combine checksums with ":" separator
4. Hash the combined string to get single service checksum
5. Compare against `observedService.Checksum` from Observe()
6. If mismatch or `!Managed`, plan restart to restore state

### Port Detection Strategy
Uses the "try to bind" approach rather than parsing netstat/ss output:
- More reliable and portable
- No dependency on external commands
- Works consistently across platforms
- Handles UDP correctly (connectionless protocol)

### Design Decisions
- **Why restart on drift?** Files edited by hand may have syntax errors or conflicting settings. A full restart ensures clean, validated config.
- **Why all three ports?** Each port serves a distinct role in the L2TP/IPsec stack. All must be listening for the VPN to function.
- **Why UDP?** L2TP and IPsec use UDP protocol exclusively for these control channels.

## References

- **Feature Matrix:** `docs/premium/FEATURE-MATRIX.md` line 64
- **Handoff Doc:** `HANDOFF.MD`
- **Pattern Reference:** `internal/node/adapter/wireguard/plan.go` (drift detection)
- **Design:** SP6 design decisions (PSK auth, one service per node, CHAP credentials)

## Next Steps (Future Work)

1. **Real-time testing:** Test with live strongSwan and xl2tpd services
2. **Enhanced `detectParamsChange()`:** Currently conservative (always returns true). Could be refined to distinguish param changes from user-only changes for more granular reload vs restart decisions.
3. **Port monitoring:** Consider adding prometheus metrics for port health
4. **Integration tests:** Add E2E tests that actually start services and verify connectivity

## Compliance

✅ Matches reference implementations (WireGuard adapter)  
✅ Follows project coding standards  
✅ Includes comprehensive test coverage  
✅ Documentation comments follow Go conventions  
✅ No breaking changes to existing API

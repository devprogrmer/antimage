# Milestone 6: Node Actions API - IMPLEMENTATION COMPLETE

## Status: ✅ Ready for Testing

### Implemented Endpoints (5)

**1. POST /api/v1/nodes/{id}/restart**
- Triggers node restart via agent
- Records `restart_requested` event
- RBAC: `nodes:write` permission + node scope

**2. POST /api/v1/nodes/{id}/sync**
- Forces immediate config synchronization  
- Records `sync_requested` event
- RBAC: `nodes:write` permission + node scope

**3. POST /api/v1/nodes/{id}/maintenance**
- Enable/disable maintenance mode
- Updates: `maintenance_mode`, `maintenance_reason`, `maintenance_entered_at`
- Changes status to `maintenance` or `online`
- Records `maintenance_enter` / `maintenance_exit` event
- RBAC: `nodes:write` permission + node scope

**4. POST /api/v1/nodes/{id}/enable**
- Re-enables disabled node
- Changes status from `disabled` to `pending`
- Records `node_enabled` event
- RBAC: `nodes:write` permission + node scope

**5. POST /api/v1/nodes/{id}/disable**
- Disables node (stops accepting connections)
- Changes status to `disabled`
- Records `node_disabled` event with reason
- RBAC: `nodes:write` permission + node scope

### Authorization Pattern ✅
```go
actor := rbac.ActorFromContext(r.Context())
target := rbac.Target{Kind: rbac.TargetNode, ID: nodeID}
if !d.authorize(w, r, actor, rbac.NodesWrite, target) {
    return
}
```

### Audit Logging ✅
All actions recorded to `node_events` table:
- Event types: `restart_requested`, `sync_requested`, `maintenance_enter/exit`, `node_enabled/disabled`
- Includes: admin_id, timestamp, action details, reason
- Severity levels: `info`, `warning`

### Routes Registered ✅
All 5 endpoints registered in `router.go` under authenticated routes.

### Build Status ✅
```
✅ go build ./cmd/antimage-panel - SUCCESS
✅ go test ./internal/panel/nodes/... - 200/200 PASS (M5 verified)
```

## Next Steps for M6 Completion

1. **Write Tests** (nodes_actions_test.go):
   - TestHandleRestartNode
   - TestHandleSyncNode
   - TestHandleSetNodeMaintenance (enable/disable)
   - TestHandleEnableNode
   - TestHandleDisableNode
   - TestNodeActionsUnauthorized (RBAC denial)
   - TestNodeActionsOutOfScope (scope filtering)

2. **Security Audit**:
   - Verify RBAC enforcement on all endpoints
   - Test scope filtering (admin can't act on other admin's nodes)
   - Verify audit trail for all actions
   - Test with super_admin vs regular admin

3. **Integration Tests**:
   - End-to-end action flow
   - Maintenance mode state transitions
   - Enable/disable lifecycle

4. **Review & Commit**:
   - Run all tests
   - Security regression tests
   - Final verification

---

**Implementation complete. Tests pending.**

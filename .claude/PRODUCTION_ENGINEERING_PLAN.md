# Antimage Production Engineering Plan

**Owner:** Full Stack Lead Engineer  
**Mission:** Transform to production-grade enterprise VPN platform  
**Status:** EXECUTING  

## Phase 0: Forensic Audit - COMPLETE

### Repository State
- **Branch:** sp7-observability
- **Commits:** 20+ commits, enforcement work recent
- **Structure:** Panel + Node + Adapters + Web UI
- **Test Packages:** 36 packages
- **Frontend Files:** 14 TypeScript files
- **TODOs/FIXMEs:** 5 remaining
- **Migrations:** 18 (ordered correctly)

### Architecture Analysis

**Panel (Control Plane):**
- ✅ Authentication (sessions, TOTP)
- ✅ RBAC (roles, permissions)
- ✅ Audit trail (append-only)
- ✅ Node registry
- ✅ mTLS enrollment
- ✅ Desired state management
- ✅ REST API
- ✅ gRPC server
- ⚠️ Web UI (basic shell, needs completion)

**Node Agent:**
- ✅ gRPC client
- ✅ Reconciliation engine
- ✅ Health reporting
- ✅ Enforcement engine (SP7)
- ✅ Adapter framework

**Adapters:**
- ✅ Xray (complete with enforcement)
- ✅ WireGuard (complete)
- ✅ Hysteria2 (complete)
- ✅ L2TP/IPsec (complete)
- ⚠️ Sing-box (partial)
- ✅ Stub (reference)
- ❌ OpenVPN (missing)
- ❌ AmneziaWG (missing)

**Subjects/Users:**
- ✅ CRUD operations
- ✅ Credentials (UUID, password, etc.)
- ✅ Device tracking (SP7)
- ✅ Enforcement limits (SP7)
- ⚠️ Expiration (incomplete)
- ⚠️ Freeze/disable (needs verification)
- ❌ Bulk operations (missing)
- ❌ Tags (missing)

**Accounting/Quotas:**
- ✅ Traffic collection
- ✅ Usage reporting
- ⚠️ Quota enforcement (partial)
- ❌ Auto-freeze on quota (missing)
- ❌ Traffic warnings (missing)
- ❌ Period reset (missing)

**Subscriptions:**
- ✅ Database schema
- ✅ Basic subscription API
- ⚠️ V2Ray format (needs verification)
- ⚠️ Clash format (needs verification)
- ❌ Sing-box format (missing)
- ❌ QR codes (missing)
- ❌ Token rotation (missing)
- ❌ Rate limiting (missing)

**Deployment:**
- ✅ Desired state revisions
- ✅ Apply runs tracking
- ⚠️ Rollback (foundation only)
- ❌ Dry run (missing)
- ❌ Diff/preview (missing)
- ❌ Staged rollout (missing)
- ❌ Health gates (missing)

**Observability:**
- ✅ Database schema
- ✅ Metrics collection
- ⚠️ Alerts (partial)
- ❌ Dashboards (missing)
- ❌ Real-time monitoring (missing)

**Security:**
- ✅ Password hashing
- ✅ Session management
- ✅ TOTP 2FA
- ✅ mTLS node auth
- ⚠️ RBAC enforcement (needs audit)
- ⚠️ Rate limiting (partial)
- ❌ CSRF protection (needs verification)
- ❌ Security headers (needs verification)

## Phase 1: Critical Path Implementation

### Priority P0 (Production Blockers)

**1. API Security Hardening (2 hours)**
- [ ] Add RBAC checks to all device endpoints
- [ ] Add subject ownership validation
- [ ] Add rate limiting middleware
- [ ] Add pagination to list endpoints
- [ ] Security audit of all panel endpoints
- [ ] CSRF token verification

**2. User Lifecycle Completion (4 hours)**
- [ ] Implement freeze/unfreeze
- [ ] Implement disable/enable
- [ ] Verify expiration enforcement
- [ ] Add bulk operations API
- [ ] Add user tags
- [ ] Add user notes
- [ ] Verify service access actually blocked

**3. Quota Enforcement (3 hours)**
- [ ] Auto-freeze on quota exceeded
- [ ] Traffic warnings at 80%/90%
- [ ] Period reset logic
- [ ] Quota bypass flag for premium
- [ ] Verify quota affects service access

**4. Subscription Engine (6 hours)**
- [ ] Verify V2Ray subscription output
- [ ] Implement Clash format
- [ ] Implement Sing-box format
- [ ] QR code generation
- [ ] Token rotation
- [ ] Subscription rate limiting
- [ ] Analytics/tracking

**5. Frontend Critical Pages (8 hours)**
- [ ] Dashboard with real metrics
- [ ] User management UI
- [ ] Device management UI
- [ ] Traffic monitoring
- [ ] Alerts/notifications
- [ ] Settings page

**Total P0: 23 hours**

### Priority P1 (Important Features)

**6. Deployment System (6 hours)**
- [ ] Diff/preview before apply
- [ ] Dry run mode
- [ ] Rollback UI
- [ ] Deployment history viewer
- [ ] Health gates

**7. Node Management (4 hours)**
- [ ] Node groups/tags
- [ ] Maintenance mode
- [ ] Quarantine mode
- [ ] Node health dashboard
- [ ] Alert when node offline

**8. Backup/Restore (3 hours)**
- [ ] Database backup command
- [ ] Configuration backup
- [ ] Restore verification
- [ ] Disaster recovery docs

**9. CLI Completion (2 hours)**
- [ ] Bootstrap command
- [ ] Diagnostics
- [ ] Health check
- [ ] Backup/restore commands

**Total P1: 15 hours**

### Priority P2 (Enhancement)

**10. Advanced Accounting (4 hours)**
- [ ] Per-protocol stats
- [ ] Peak bandwidth tracking
- [ ] Historical charts
- [ ] Export reports

**11. Alerting (3 hours)**
- [ ] Webhook integration
- [ ] Telegram notifications
- [ ] Email alerts
- [ ] Alert rules engine

**12. Missing Adapters (8 hours)**
- [ ] Complete Sing-box adapter
- [ ] OpenVPN adapter (if feasible)
- [ ] AmneziaWG adapter

**Total P2: 15 hours**

## Feature Matrix (Current State)

| Feature | Designed | Implemented | Wired | Database | API | Frontend | Node | Tests | Security | Status | Missing |
|---------|----------|-------------|-------|----------|-----|----------|------|-------|----------|--------|---------|
| **Users** |
| CRUD | ✅ | ✅ | ✅ | ✅ | ✅ | ⚠️ | ✅ | ✅ | ⚠️ | PARTIAL | UI, Security |
| Freeze | ✅ | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ | N/A | MISSING | Everything |
| Expiration | ✅ | ⚠️ | ⚠️ | ✅ | ⚠️ | ❌ | ⚠️ | ❌ | N/A | PARTIAL | Enforcement, UI |
| Bulk Ops | ✅ | ❌ | ❌ | N/A | ❌ | ❌ | N/A | ❌ | N/A | MISSING | Everything |
| Tags | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | N/A | ❌ | N/A | MISSING | Everything |
| **Devices** |
| Tracking | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ | ✅ | ✅ | ⚠️ | PARTIAL | UI, Auth |
| Revoke | ✅ | ✅ | ⚠️ | ✅ | ✅ | ❌ | ⚠️ | ✅ | ⚠️ | PARTIAL | Runtime, UI |
| Fingerprint | ✅ | ⚠️ | ⚠️ | ✅ | ✅ | ❌ | ⚠️ | ⚠️ | N/A | PARTIAL | True device ID |
| **Enforcement** |
| Speed Limits | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ | ✅ | ✅ | ✅ | BEST_EFFORT | UI, Verification |
| Connection Limits | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ | ✅ | ✅ | ✅ | BEST_EFFORT | UI |
| Device Limits | ✅ | ⚠️ | ⚠️ | ✅ | ✅ | ❌ | ⚠️ | ⚠️ | ✅ | PROPAGATED | Fingerprint |
| IP Limits | ✅ | ⚠️ | ❌ | ✅ | ✅ | ❌ | ❌ | ⚠️ | ✅ | UNSUPPORTED | Protocol limit |
| **Accounting** |
| Traffic Collection | ✅ | ✅ | ✅ | ✅ | ✅ | ⚠️ | ✅ | ✅ | ✅ | COMPLETE | Charts |
| Quotas | ✅ | ⚠️ | ⚠️ | ✅ | ⚠️ | ❌ | ⚠️ | ⚠️ | ✅ | PARTIAL | Auto-freeze |
| Warnings | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | N/A | MISSING | Everything |
| Reset | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | N/A | MISSING | Everything |
| **Subscriptions** |
| V2Ray | ✅ | ⚠️ | ⚠️ | ✅ | ⚠️ | ❌ | N/A | ❌ | ⚠️ | PARTIAL | Verification |
| Clash | ✅ | ❌ | ❌ | ✅ | ❌ | ❌ | N/A | ❌ | ❌ | MISSING | Implementation |
| Sing-box | ✅ | ❌ | ❌ | ✅ | ❌ | ❌ | N/A | ❌ | ❌ | MISSING | Implementation |
| QR Codes | ✅ | ❌ | ❌ | N/A | ❌ | ❌ | N/A | ❌ | N/A | MISSING | Implementation |
| Rate Limit | ✅ | ❌ | ❌ | N/A | ❌ | N/A | N/A | ❌ | ❌ | MISSING | Implementation |
| **Deployment** |
| Apply | ✅ | ✅ | ✅ | ✅ | ✅ | ⚠️ | ✅ | ✅ | ✅ | COMPLETE | UI |
| Rollback | ✅ | ⚠️ | ⚠️ | ✅ | ⚠️ | ❌ | ⚠️ | ❌ | ✅ | PARTIAL | Implementation |
| Dry Run | ✅ | ❌ | ❌ | N/A | ❌ | ❌ | ❌ | ❌ | N/A | MISSING | Implementation |
| Diff | ✅ | ❌ | ❌ | N/A | ❌ | ❌ | ❌ | ❌ | N/A | MISSING | Implementation |
| **Observability** |
| Metrics | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ | ✅ | ⚠️ | ✅ | PARTIAL | Dashboards |
| Alerts | ✅ | ⚠️ | ⚠️ | ✅ | ⚠️ | ❌ | ⚠️ | ❌ | ⚠️ | PARTIAL | Rules, UI |
| Logs | ✅ | ⚠️ | ⚠️ | ⚠️ | ⚠️ | ❌ | ⚠️ | ❌ | ⚠️ | PARTIAL | Aggregation |

## Execution Plan

**Immediate (Today):**
1. Fix API security (device endpoints)
2. Implement user freeze/disable
3. Implement quota auto-freeze
4. Add RBAC audit

**Week 1:**
1. Complete subscription engine
2. Build critical frontend pages
3. Add deployment diff/rollback
4. Complete backup/restore

**Week 2:**
1. Advanced accounting features
2. Alerting system
3. Complete missing adapters
4. Security hardening

**Continuous:**
- Test every change
- Commit logical units
- Update documentation
- Security review
- Performance monitoring

## Success Criteria

**Production Ready:**
- [ ] All P0 items complete
- [ ] 95%+ test coverage on critical paths
- [ ] Security audit clean
- [ ] Performance benchmarks pass
- [ ] Documentation complete
- [ ] Deployment tested
- [ ] Backup/restore verified
- [ ] Load testing passed

**Next Level:**
- [ ] All P1 items complete
- [ ] Advanced features working
- [ ] Multi-tenancy ready
- [ ] Reseller support
- [ ] High availability
- [ ] Auto-scaling

## Starting Execution

**First Task:** API Security Hardening
**ETA:** 2 hours
**Status:** STARTING NOW

---

*This plan updates continuously as work progresses.*

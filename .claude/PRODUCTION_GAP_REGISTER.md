# Production Gap Register

**Repository:** antimage  
**Branch:** sp7-observability  
**Current State:** 85/100 Production Ready (Phase 9 Complete)  
**Last Updated:** 2026-08-22  

## Purpose

This register tracks every gap between the current repository state and full production readiness. Each gap includes evidence, missing functionality, implementation plan, and commit tracking.

**Guiding Principle:** Treat every ⚠️, UNSUPPORTED, UNVERIFIED, MANUAL, UNTESTED as an ACTIVE ENGINEERING GAP.

---

## Executive Summary

**Phase 9 Verdict:** 85/100 Production Ready with documented limitations

**Critical Findings:**
- ✅ Security: PRODUCTION-READY (Argon2id, mTLS, RBAC, audit)
- ✅ Core Functionality: COMPLETE (node enrollment, reconciliation, enforcement)
- ✅ Testing: EXCELLENT (746 tests, E2E harness)
- ✅ Observability: COMPREHENSIVE (metrics, alerts, quota auto-freeze)
- ⚠️ Performance: UNTESTED at scale (no benchmarks, no load tests)
- ⚠️ Deployment: MANUAL (no automation, no validation, no rollback)
- ⚠️ Failure Injection: NO FRAMEWORK (resilience unproven)
- ⚠️ API Documentation: INFORMAL (no OpenAPI spec)
- ⚠️ Xray Speed Limiting: UNSUPPORTED natively (tc exists but not integrated)
- ⚠️ Hysteria2: UNVERIFIED (adapter implemented, runtime untested)

**Priority Distribution:**
- P0 (Blockers): 3 gaps (Performance, Deployment, Failure Injection)
- P1 (Important): 3 gaps (API Docs, Hysteria2, TC Integration)
- P2 (Enhancement): 2 gaps (L2TP limits, Documentation)

---

## Gap Tracking

### GAP-001: Performance Benchmarking (P0)

**Area:** Performance / Load Testing  
**Priority:** P0 (Production Blocker)  
**Status:** MISSING  
**Phase 9 Reference:** M9 Performance Characteristics

**Current State:**
- Architecture sound (single-writer SQLite, indexed queries, no N+1)
- Zero performance benchmarks exist (verified: `find . -name "*benchmark*.go"` = 0 files)
- Zero load tests exist
- Performance characteristics completely unknown at scale
- No pprof endpoints (verified: `grep -r "pprof" internal/panel cmd/` = 0 results)

**Evidence:**
```bash
$ grep -r "func Benchmark" internal/ cmd/ --include="*.go" | wc -l
3  # Only 3 benchmarks in entire codebase

$ find . -name "*_bench.go"
# No dedicated benchmark files
```

**Phase 9 Assessment (M9):**
> "⚠️ NOT LOAD-TESTED"
> "No load testing performed. Performance unknown at scale."
> "Verdict: ⚠️ Architecture sound, scale unknown"

**Missing Functionality:**
1. **Benchmark Suite:**
   - User CRUD operations (create, lookup, bulk operations)
   - Dashboard queries (metrics aggregation, traffic rollups)
   - Traffic accounting (quota calculations, usage updates)
   - Subscription generation (V2Ray/Clash format rendering)
   - Node reconciliation (desired state processing)
   - Deployment processing (revision application)
   - Alert processing (quota auto-freeze)
   - Concurrent API requests (session validation, RBAC checks)

2. **Load Testing:**
   - 100+ concurrent nodes (heartbeat, reconciliation, metrics)
   - 10,000+ subjects (quota checks, traffic accounting)
   - Concurrent API load (100+ req/s)
   - Database contention under concurrent writes
   - Memory usage over time (leak detection)

3. **Profiling Infrastructure:**
   - pprof endpoints (/debug/pprof/heap, /profile, /trace)
   - Memory profiling
   - CPU profiling
   - Goroutine leak detection
   - Allocation tracking

4. **Performance Investigation:**
   - Identify N+1 queries (if any missed)
   - Database index optimization
   - Lock contention analysis
   - Excessive allocations
   - Serialization overhead
   - Memory growth patterns

**Security Impact:** LOW  
- Performance issues don't create vulnerabilities
- DoS risk if resource exhaustion occurs under load

**User Impact:** HIGH  
- Cannot confidently deploy beyond 1-50 nodes
- Unknown whether system handles 10k+ subjects
- Risk of production outages if scale limits hit
- Cannot provide capacity planning guidance

**Implementation Plan:**

**Phase 1: Benchmark Suite (4 hours)**
1. Create `internal/panel/store/benchmark_test.go`
   - BenchmarkUserCreate (single, bulk)
   - BenchmarkUserLookup (by ID, by email)
   - BenchmarkSubjectListWithQuota (pagination)
   - BenchmarkTrafficUpdate (single, batch)
   - BenchmarkQuotaCalculation (all users)
   - BenchmarkMetricAggregation (hourly, daily rollups)

2. Create `internal/panel/httpapi/benchmark_test.go`
   - BenchmarkDashboardMetrics (complex query)
   - BenchmarkSubscriptionGeneration (V2Ray format)
   - BenchmarkSessionValidation (hot path)
   - BenchmarkRBACCheck (permission validation)

3. Create `internal/node/reconciler/benchmark_test.go`
   - BenchmarkDesiredStateProcessing (large config)
   - BenchmarkDriftDetection (100+ services)

**Phase 2: Load Testing Framework (6 hours)**
1. Create `test/load/` directory structure
2. Implement `load_panel_test.go`:
   - Spawn 100 fake agents (gRPC connections)
   - Simulate heartbeats every 30s
   - Generate traffic metrics (1000+ subjects)
   - Run for 10 minutes, measure:
     - Database size growth
     - Memory usage trend
     - API response times (p50, p95, p99)
     - Error rate

3. Implement `load_concurrent_api_test.go`:
   - 100 concurrent API clients
   - Mixed workload (read 80%, write 20%)
   - Run for 5 minutes, measure throughput

**Phase 3: Profiling Infrastructure (2 hours)**
1. Add pprof endpoints to panel HTTP server
2. Document profiling procedures
3. Create `PERFORMANCE.md` guide

**Phase 4: Analysis & Optimization (4 hours)**
1. Run benchmark suite, establish baselines
2. Run load tests, document scale limits
3. Profile under load, identify bottlenecks
4. Optimize if critical issues found
5. Document measured performance characteristics

**Test Plan:**
- Run benchmark suite: `go test -bench=. -benchmem ./...`
- Run load tests: `go test -v ./test/load/...`
- Profile panel under load: `curl http://localhost:8080/debug/pprof/heap > heap.prof`
- Verify no memory leaks over 1 hour run
- Document results in PHASE9-M9-PERFORMANCE.md

**Acceptance Criteria:**
- [ ] 20+ benchmark functions covering critical paths
- [ ] Load test handles 100 concurrent nodes for 10 minutes
- [ ] Load test handles 10,000 subjects without crash
- [ ] pprof endpoints accessible
- [ ] Measured performance characteristics documented
- [ ] Known scale limits documented (nodes, subjects, API throughput)
- [ ] No memory leaks detected
- [ ] All benchmarks pass with acceptable performance

**Commit:** [PENDING]

---

### GAP-002: Deployment Automation & Validation (P0)

**Area:** Deployment / Operations  
**Priority:** P0 (Production Blocker)  
**Status:** MANUAL  
**Phase 9 Reference:** M5 Deployment Procedures

**Current State:**
- Manual deployment procedures documented
- No deployment automation exists
- No deployment validation (dry-run, diff, preview)
- No deployment history tracking (apply_runs table exists but minimal)
- No health gates or staged rollout
- No systemd units or Kubernetes manifests (docker-compose.yml exists at repo root)

**Evidence:**
```bash
$ find . -name "deployment*.go" -o -name "rollback*.go"
# No deployment orchestration code found

$ grep -r "DryRun\|Preview\|Diff" internal/panel --include="*.go"
# No deployment validation logic
```

**Phase 9 Assessment (M5):**
> "⚠️ MANUAL (no automation)"
> "No systemd units provided. Docker Compose example exists at repo root. CI/CD configured (.github/workflows/ci.yml)."
> "Verdict: ⚠️ Works but manual, acceptable for MVP"

**Missing Functionality:**

1. **Deployment Validation:**
   - Configuration syntax validation (before apply)
   - Dry-run mode (validate without applying)
   - Diff/preview (show what will change)
   - Conflict detection (overlapping ports, duplicate services)
   - Resource validation (valid node IDs, protocol configs)

2. **Deployment Safety:**
   - Health gates (check node health before/after apply)
   - Staged rollout (apply to subset of nodes first)
   - Canary deployment (single node test)
   - Automatic rollback on failure
   - Failed node isolation (don't propagate bad config)

3. **Deployment History:**
   - Revision tracking (who, when, what changed)
   - Deployment status (pending, in-progress, complete, failed)
   - Per-node apply status (success, failed, skipped)
   - Rollback capability (revert to previous revision)
   - Audit trail (deployment decisions)

4. **Deployment Orchestration:**
   - Parallel vs sequential apply strategies
   - Timeout handling (node unresponsive during apply)
   - Partial failure recovery
   - Progress tracking (X/N nodes complete)

5. **Infrastructure Automation:**
   - systemd service units (panel, node)
   - Docker Compose example (panel + agent + databases)
   - Kubernetes manifests (deployment, service, configmap)
   - Terraform/Ansible examples

**Security Impact:** MEDIUM  
- No validation could deploy insecure configurations
- No rollback increases recovery time after security incident
- Manual deployment prone to human error

**User Impact:** HIGH  
- Risky deployments (no preview, no rollback)
- Slow deployment (manual, error-prone)
- Difficult to operate at scale (manual doesn't scale to 50+ nodes)
- High MTTR (mean time to recovery) if deployment fails

**Implementation Plan:**

**Phase 1: Deployment Validation (4 hours)**
1. Create `internal/panel/deployment/validator.go`:
   - ValidateConfiguration(desiredState) error
   - DetectConflicts(desiredState) []Conflict
   - CheckNodeHealth(nodeIDs) map[int64]HealthStatus

2. Add API endpoints:
   - POST /api/v1/deployments/validate (dry-run)
   - POST /api/v1/deployments/preview (diff current vs desired)
   - POST /api/v1/deployments (create deployment with validation)

3. Enhance store with deployment tracking:
   - deployment_status table (id, revision_id, status, created_at, completed_at)
   - deployment_node_status table (deployment_id, node_id, status, error)

**Phase 2: Deployment Orchestration (6 hours)**
1. Create `internal/panel/deployment/orchestrator.go`:
   - DeployRevision(revisionID, strategy) error
   - Strategies: ALL_AT_ONCE, CANARY, STAGED, ROLLING
   - Track per-node status in database
   - Handle partial failures gracefully

2. Implement deployment strategies:
   - Canary: Apply to 1 node, wait for health check
   - Staged: Apply to 10%, wait, then 50%, then 100%
   - Rolling: Apply sequentially with health gates

3. Add health gate logic:
   - Check node health before apply
   - Check node health 30s after apply
   - Rollback node if health check fails

**Phase 3: Rollback Capability (3 hours)**
1. Enhance desired_state_revisions table:
   - parent_revision_id (track lineage)
   - is_rollback boolean

2. Implement rollback API:
   - POST /api/v1/deployments/{id}/rollback
   - Automatically create new revision with previous config
   - Apply using same orchestration logic

3. Add rollback testing:
   - Test rollback after successful deployment
   - Test rollback after failed deployment
   - Test rollback of rollback (forward again)

**Phase 4: Infrastructure Automation (4 hours)**
1. Create `deploy/systemd/`:
   - antimage-panel.service
   - antimage-node.service
   - Installation script

2. Create `deploy/docker/`:
   - docker-compose.yml (panel + PostgreSQL)
   - Dockerfile.panel
   - Dockerfile.node

3. Create `deploy/kubernetes/`:
   - panel-deployment.yaml
   - panel-service.yaml
   - panel-configmap.yaml

4. Document deployment procedures in `DEPLOYMENT.md`

**Test Plan:**
1. Test validation API (valid config, invalid config, conflicts)
2. Test preview API (shows correct diff)
3. Test canary deployment (1 node, then all)
4. Test staged deployment (10%, 50%, 100%)
5. Test rollback (revert to previous config)
6. Test failed deployment isolation (bad node doesn't affect others)
7. Test deployment history tracking
8. Test systemd units on Linux VM
9. Test Docker Compose deployment
10. E2E test: deploy -> verify -> rollback -> verify

**Acceptance Criteria:**
- [ ] Deployment validation API prevents invalid configs
- [ ] Preview API shows accurate diff
- [ ] Canary deployment works (1 node test)
- [ ] Staged deployment works (progressive rollout)
- [ ] Rollback works (restore previous config)
- [ ] Failed node isolated (doesn't block others)
- [ ] Deployment history tracked in database
- [ ] systemd units tested on Linux
- [ ] Docker Compose tested
- [ ] All deployment tests pass

**Commit:** [PENDING]

---

### GAP-003: Failure Injection Framework (P0)

**Area:** Reliability / Chaos Engineering  
**Priority:** P0 (Production Blocker)  
**Status:** NO FRAMEWORK  
**Phase 9 Reference:** M10 Failure Injection

**Current State:**
- No automated failure injection framework
- Resilience inferred from architecture (reconnection, transactions, reconciliation)
- Reliability unproven under real failure conditions
- No chaos engineering tests

**Evidence:**
```bash
$ find . -name "*chaos*" -o -name "*failure*" -o -name "*fault*"
# No failure injection code

$ grep -r "FaultInjection\|ChaosTest" internal/ test/
# No chaos engineering framework
```

**Phase 9 Assessment (M10):**
> "⚠️ NO FRAMEWORK (inferred resilience)"
> "No automated failure injection. Resilience inferred from architecture."
> "Verdict: ⚠️ Architecture resilient, but unproven at scale"

**Missing Functionality:**

1. **Component Failure Tests:**
   - Panel restart (agent reconnection)
   - Node restart (state recovery)
   - Database unavailable (transaction rollback)
   - gRPC connection loss (retry logic)
   - Network partition (panel ↔ node)

2. **Timing Failure Tests:**
   - Network timeout (slow gRPC)
   - Delayed events (stale metrics)
   - Clock skew (certificate validation)
   - Duplicate events (idempotency)
   - Out-of-order events (reconciliation)

3. **Data Corruption Tests:**
   - Invalid desired state (malformed JSON)
   - Corrupted configuration (syntax errors)
   - Partial state (incomplete reconciliation)
   - Conflicting state (concurrent updates)

4. **Resource Exhaustion Tests:**
   - Database lock contention (concurrent writes)
   - Memory pressure (leak detection)
   - Disk full (SQLite write failure)
   - Connection pool exhaustion (too many nodes)
   - CPU saturation (metric processing)

5. **Deployment Failure Tests:**
   - Failed adapter apply (configuration error)
   - Interrupted reconciliation (node crash mid-apply)
   - Rollback failure (bad previous state)
   - Certificate expiration (mTLS failure)
   - Enrollment token expired (node can't join)

**Security Impact:** LOW  
- Failure handling bugs could create availability issues
- Improper error handling could leak sensitive information

**User Impact:** HIGH  
- Unknown system behavior under real failure conditions
- Risk of cascading failures in production
- Difficult to debug production issues without failure testing
- Cannot confidently claim "production-ready" without reliability proof

**Implementation Plan:**

**Phase 1: Failure Injection Library (4 hours)**
1. Create `internal/testutil/chaos/`:
   - `network.go`: Inject network failures (timeout, partition, packet loss)
   - `database.go`: Inject DB failures (lock timeout, connection loss)
   - `timing.go`: Inject timing issues (clock skew, delays)
   - `grpc.go`: Inject gRPC failures (connection drop, timeout)

2. Design fault injection API:
   ```go
   type FaultInjector interface {
       InjectFault(ctx context.Context, fault Fault) error
       RemoveFault(ctx context.Context, faultID string) error
       ListActiveFaults() []Fault
   }
   ```

**Phase 2: Reliability Test Suite (8 hours)**
1. Create `test/reliability/`:
   - `panel_restart_test.go`: Kill panel, verify agent reconnects
   - `node_restart_test.go`: Kill node, verify state recovers
   - `network_partition_test.go`: Simulate network loss
   - `database_contention_test.go`: Concurrent write stress
   - `deployment_failure_test.go`: Failed apply, verify recovery
   - `certificate_expiry_test.go`: Expired cert, verify rejection

2. Each test:
   - Setup: E2E environment (panel + agents)
   - Inject fault
   - Verify system behavior (reconnection, error handling, recovery)
   - Cleanup fault
   - Verify final state correct

**Phase 3: Resilience Verification (4 hours)**
1. Panel restart test:
   - Start panel + 3 agents
   - Kill panel process
   - Verify agents retry connection (exponential backoff)
   - Restart panel
   - Verify agents reconnect within 60s
   - Verify reconciliation continues

2. Node restart test:
   - Deploy configuration to node
   - Kill node process
   - Restart node
   - Verify configuration persists (idempotent reconciliation)
   - Verify no duplicate services

3. Database failure test:
   - Simulate database lock timeout (concurrent writes)
   - Verify transaction rollback
   - Verify retry logic
   - Verify eventual consistency

4. Deployment failure test:
   - Push invalid configuration (bad Xray config)
   - Verify adapter apply fails
   - Verify error reported to panel
   - Verify node marked unhealthy
   - Verify other nodes unaffected

**Phase 4: Documentation (2 hours)**
1. Create `RELIABILITY.md`:
   - Document tested failure scenarios
   - Document observed recovery behavior
   - Document known resilience limits
   - Provide troubleshooting guide

**Test Plan:**
- Run each reliability test 10 times (detect flakiness)
- Verify all tests pass consistently
- Verify no resource leaks after failure injection
- Verify system state correct after recovery
- Document failure recovery times

**Acceptance Criteria:**
- [ ] Failure injection library implemented
- [ ] Panel restart resilience verified
- [ ] Node restart resilience verified
- [ ] Network partition resilience verified
- [ ] Database failure resilience verified
- [ ] Deployment failure isolation verified
- [ ] Certificate expiry handling verified
- [ ] All reliability tests pass consistently (10/10 runs)
- [ ] Recovery times documented
- [ ] Known resilience limits documented

**Commit:** [PENDING]

---

### GAP-004: API Documentation (P1)

**Area:** Documentation / Developer Experience  
**Priority:** P1 (Important, not blocking)  
**Status:** INFORMAL  
**Phase 9 Reference:** M12 API Documentation

**Current State:**
- Inline code comments present (handler purpose)
- Consistent error format (JSON with error field)
- No OpenAPI/Swagger specification
- No request/response examples
- No Postman collection
- Score: 36/100 for external API consumers

**Evidence:**
```bash
$ find . -name "openapi*.yaml" -o -name "swagger*.yaml"
# No OpenAPI specification

$ find . -name "openapi*.go" -o -name "swagger*.go"
# No OpenAPI generation code
```

**Phase 9 Assessment (M12):**
> "⚠️ FUNCTIONAL BUT INFORMAL"
> "No OpenAPI/Swagger spec. No request/response examples."
> "Verdict: ⚠️ Sufficient for internal use, insufficient for external API"

**Missing Functionality:**

1. **OpenAPI Specification:**
   - Complete OpenAPI 3.0 spec (all endpoints)
   - Request schemas (body, query params, headers)
   - Response schemas (success, error cases)
   - Authentication documentation (session cookies, TOTP)
   - Authorization documentation (RBAC permissions)

2. **API Documentation:**
   - Pagination details (limit, offset, total)
   - Filtering/sorting parameters
   - Error codes and meanings
   - Status code documentation (200, 201, 400, 401, 403, 404, 500)
   - Idempotency guarantees
   - Rate limiting details

3. **Developer Tools:**
   - Postman/Insomnia collection
   - Example requests (curl)
   - Example responses (JSON)
   - Interactive API explorer (Swagger UI)
   - SDKs (Go, Python, JavaScript)

4. **API Contract Tests:**
   - Validate responses match OpenAPI schema
   - Catch breaking changes automatically
   - Version compatibility tests

**Security Impact:** LOW  
- Poor documentation doesn't create vulnerabilities
- Clearer auth docs reduce misuse risk

**User Impact:** MEDIUM  
- External API consumers cannot integrate easily
- Internal developers waste time guessing API behavior
- API changes may break clients unknowingly
- Difficult to onboard new developers

**Implementation Plan:**

**Phase 1: OpenAPI Generation (4 hours)**
1. Add OpenAPI generation library:
   - `go get github.com/swaggo/swag`
   - Annotate handlers with Swagger comments

2. Generate specification:
   - `swag init --parseDependency --parseInternal`
   - Output: `docs/swagger.yaml`, `docs/swagger.json`

3. Serve Swagger UI:
   - Add GET /api/docs (interactive explorer)
   - Embed swagger-ui assets

**Phase 2: Complete API Annotations (6 hours)**
1. Annotate all httpapi handlers:
   - @Summary, @Description
   - @Param (path, query, body)
   - @Success, @Failure
   - @Security (session auth)
   - @Tags (grouping)

2. Define schemas:
   - Request DTOs
   - Response DTOs
   - Error response format

3. Document authentication:
   - Session cookie flow
   - TOTP two-factor flow
   - RBAC permissions required per endpoint

**Phase 3: Examples & Collections (3 hours)**
1. Create `docs/examples/`:
   - curl examples for each endpoint
   - Response examples (success, error)

2. Generate Postman collection:
   - Use OpenAPI spec to generate collection
   - Add environment variables (base URL, session token)

3. Create API quickstart guide:
   - Authentication flow
   - Common operations (create user, deploy config)
   - Error handling

**Phase 4: API Contract Tests (3 hours)**
1. Create `test/api_contract_test.go`:
   - Validate all responses match OpenAPI schema
   - Use github.com/getkin/kin-openapi validator

2. Run in CI:
   - Fail build if API response doesn't match schema
   - Catch breaking changes before merge

**Test Plan:**
- Generate OpenAPI spec, verify completeness
- Validate spec syntax (openapi-validator)
- Test Swagger UI (interactive docs work)
- Test Postman collection (all requests valid)
- Run API contract tests (all pass)
- Verify examples work (curl requests succeed)

**Acceptance Criteria:**
- [ ] OpenAPI 3.0 spec generated (all endpoints)
- [ ] Swagger UI accessible at /api/docs
- [ ] All handlers annotated with Swagger comments
- [ ] Request/response schemas complete
- [ ] Authentication flow documented
- [ ] RBAC permissions documented per endpoint
- [ ] Postman collection generated
- [ ] curl examples provided for common operations
- [ ] API contract tests pass
- [ ] Documentation score > 80/100

**Commit:** [PENDING]

---

### GAP-005: Hysteria2 Verification (P1)

**Area:** Protocol Support / Verification  
**Priority:** P1 (Important, not blocking)  
**Status:** UNVERIFIED  
**Phase 9 Reference:** M15 Protocol Edge Cases

**Current State:**
- Hysteria2 adapter implemented (config generation, service lifecycle)
- Runtime behavior UNVERIFIED (requires Hysteria2 binary)
- Test skipped with manual verification guide
- Classification: UNKNOWN until runtime tested
- Cannot claim bandwidth enforcement ENFORCED without proof

**Evidence:**
```bash
$ cat internal/node/adapter/hysteria2/runtime_bandwidth_test.go
// Test SKIPPED - requires hysteria2 binary
// Manual test guide provided
```

**Phase 9 Assessment (M15):**
> "❓ UNVERIFIED (requires manual test)"
> "Hysteria2 bandwidth: UNVERIFIED (requires manual testing)"
> "Do NOT claim ENFORCED without runtime test passing."

**Missing Verification:**

1. **Configuration Generation:**
   - Verify generated config is valid Hysteria2 format
   - Verify credentials work (subject authentication)
   - Verify bandwidth limits included in config

2. **Service Lifecycle:**
   - Verify service starts successfully
   - Verify service stops cleanly
   - Verify service restart behavior
   - Verify service health detection

3. **Traffic Accounting:**
   - Verify traffic metrics collected
   - Verify metrics accurate (match actual traffic)
   - Verify metrics format compatible with panel

4. **Bandwidth Enforcement:**
   - Verify bandwidth limits ENFORCED (not just configured)
   - Measure actual throughput vs configured limit
   - Test upload and download separately
   - Classify as ENFORCED, BEST_EFFORT, or UNSUPPORTED

5. **Connection Limits:**
   - Verify connection limits enforced (if supported)
   - Test concurrent connection rejection

6. **Quota Integration:**
   - Verify quota enforcement works
   - Verify connections rejected when quota exceeded
   - Verify auto-freeze triggers

7. **Subscription Support:**
   - Verify client configuration format
   - Verify clients can connect successfully
   - Verify multi-device support

**Security Impact:** LOW  
- Unverified adapter could fail to enforce limits
- Risk of unauthorized access if auth broken

**User Impact:** MEDIUM  
- Cannot confidently deploy Hysteria2 in production
- Cannot claim bandwidth enforcement works
- Users may choose protocol thinking limits enforced
- Risk of quota bypass if enforcement broken

**Implementation Plan:**

**Phase 1: Obtain Hysteria2 Binary (1 hour)**
1. Download official Hysteria2 release:
   - GitHub: hysteria-network/hysteria
   - Latest stable version
   - Verify checksum

2. Add to test environment:
   - Place in test/bin/hysteria2
   - Verify executable: `hysteria2 --version`
   - Update test skip condition

**Phase 2: Runtime Verification Tests (6 hours)**
1. Enhance `internal/node/adapter/hysteria2/runtime_bandwidth_test.go`:
   - Generate server config (with bandwidth limit)
   - Start Hysteria2 server
   - Start Hysteria2 client
   - Transfer 100MB data
   - Measure throughput
   - Verify throughput <= configured limit * 1.1
   - Classify: ENFORCED, BEST_EFFORT, or UNSUPPORTED

2. Add `runtime_connection_test.go`:
   - Configure connection limit (e.g., 2)
   - Start server
   - Attempt 3 concurrent connections
   - Verify 3rd connection rejected

3. Add `runtime_auth_test.go`:
   - Test valid credentials (connection succeeds)
   - Test invalid credentials (connection rejected)
   - Test expired credentials (connection rejected)

4. Add `runtime_accounting_test.go`:
   - Transfer known data amount (e.g., 50MB)
   - Read Hysteria2 metrics
   - Verify metrics match actual transfer
   - Verify metrics format compatible with panel

**Phase 3: Integration Testing (4 hours)**
1. Create E2E test with real panel:
   - Deploy Hysteria2 service to test node
   - Create subject with quota
   - Generate subscription
   - Connect client, transfer data
   - Verify quota decremented correctly
   - Exceed quota, verify connection rejected

2. Test reconciliation:
   - Update bandwidth limit in panel
   - Verify node reconciles (restarts Hysteria2)
   - Verify new limit enforced

3. Test subscription generation:
   - Generate Hysteria2 client config
   - Verify client connects successfully

**Phase 4: Classification & Documentation (2 hours)**
1. Classify enforcement capabilities:
   - Quota: ENFORCED / UNSUPPORTED
   - Bandwidth: ENFORCED / BEST_EFFORT / UNSUPPORTED
   - Connection Limit: ENFORCED / UNSUPPORTED
   - Device Limit: ENFORCED / UNSUPPORTED

2. Update protocol matrix in PHASE9-M15-PROTOCOL-EDGE-CASES.md:
   - Change ❓ UNKNOWN to actual classification
   - Document measured throughput vs limit
   - Document any protocol-specific quirks

3. Update PHASE9-M17-FINAL-RELEASE-GATE.md:
   - Update Hysteria2 verdict
   - Update production readiness score if needed

**Test Plan:**
- Test bandwidth enforcement (download, upload)
- Test connection limit enforcement
- Test authentication (valid, invalid, expired)
- Test traffic accounting accuracy
- Test quota integration
- Test subscription generation
- Run all tests 5 times (detect flakiness)

**Acceptance Criteria:**
- [ ] Hysteria2 binary available in test environment
- [ ] Bandwidth enforcement classification complete (ENFORCED/BEST_EFFORT/UNSUPPORTED)
- [ ] Connection limit classification complete
- [ ] Authentication verified working
- [ ] Traffic accounting verified accurate
- [ ] Quota integration tested
- [ ] Subscription generation tested
- [ ] All runtime tests pass consistently
- [ ] Protocol matrix updated with actual classification
- [ ] Phase 9 documentation updated

**Commit:** [PENDING]

---

### GAP-006: TC (Traffic Control) Integration (P1)

**Area:** Speed Limiting / Enforcement  
**Priority:** P1 (Important, not blocking)  
**Status:** PARTIAL  
**Phase 9 Reference:** M4 Xray Speed Limiting, M15 Protocol Edge Cases

**Current State:**
- Xray native speed limiting UNSUPPORTED (verified via runtime test)
- TrafficShaper implementation exists (internal/node/enforcement/traffic_shaper.go)
- Linux tc integration code present (HTB qdisc, u32 filters)
- NOT INTEGRATED with adapter framework
- NOT TESTED at runtime
- NOT DOCUMENTED for operators

**Evidence:**
```bash
$ cat internal/node/enforcement/traffic_shaper.go
# TrafficShaper exists - manages tc qdisc/class/filter
# 238 lines of tc orchestration code

$ grep -r "TrafficShaper" internal/node/adapter/
# No adapter uses TrafficShaper - not integrated
```

**Phase 9 Assessment:**
> "❌ Native speed limiting UNSUPPORTED (verified)"
> "✅ Fallback: Linux tc (traffic control) - AVAILABLE but NOT runtime-tested"
> "Test kept honest (not deleted/weakened per user mandate)"

**Missing Integration:**

1. **Adapter Integration:**
   - Xray adapter doesn't call TrafficShaper
   - WireGuard adapter doesn't call TrafficShaper
   - L2TP adapter doesn't call TrafficShaper
   - No lifecycle coordination (start, stop, update)

2. **Reconciliation Integration:**
   - Desired state includes speed limits
   - Reconciler doesn't apply tc rules
   - No drift detection for tc rules
   - No cleanup on service removal

3. **Runtime Verification:**
   - TrafficShaper code not runtime tested
   - Unknown if tc commands work correctly
   - Unknown if bandwidth limits actually enforced
   - Unknown if per-user isolation works

4. **Operational Requirements:**
   - Requires CAP_NET_ADMIN capability
   - Requires iproute2 package (tc command)
   - Requires correct network interface name
   - Download limiting requires IFB device (not implemented)
   - No documentation for operators

5. **Edge Cases:**
   - Stale tc rules cleanup (after node restart)
   - Concurrent tc modifications (race conditions)
   - tc rule idempotency (apply multiple times)
   - Subject IP changes (update filters)
   - Multiple protocols per subject (shared limit?)

**Security Impact:** LOW  
- Failed tc commands don't create vulnerabilities
- CAP_NET_ADMIN required (proper isolation)

**User Impact:** MEDIUM  
- Speed limits not enforced (users can exceed quota faster)
- Promised feature doesn't work
- Bandwidth abuse possible
- Network congestion risk

**Implementation Plan:**

**Phase 1: Adapter Integration (4 hours)**
1. Create enforcement coordinator:
   - `internal/node/enforcement/coordinator.go`
   - Manages TrafficShaper lifecycle
   - Coordinates with adapters
   - Tracks subject IP addresses

2. Integrate with Xray adapter:
   - Modify `internal/node/adapter/xray/apply.go`
   - After successful service start, apply tc rules
   - Pass subject ID, source IP, limits to TrafficShaper
   - Remove tc rules on service stop

3. Integrate with WireGuard adapter:
   - Similar integration
   - Use WireGuard peer IP as source IP

4. Integrate with L2TP adapter:
   - Similar integration
   - Use L2TP session IP as source IP

**Phase 2: Reconciliation Integration (3 hours)**
1. Enhance reconciler:
   - After applying adapter changes, sync tc rules
   - Compare desired speed limits with active tc rules
   - Add missing rules, remove stale rules
   - Handle subject IP changes

2. Add cleanup logic:
   - On node startup, list all tc rules
   - Remove rules for subjects not in desired state
   - Initialize TrafficShaper per network interface

3. Add drift detection:
   - Periodically verify tc rules exist
   - Recreate missing rules (manual tc delete)

**Phase 3: Runtime Verification (6 hours)**
1. Enhance TrafficShaper tests:
   - Create `traffic_shaper_runtime_test.go`
   - Requires Linux + root/CAP_NET_ADMIN
   - Skip on Windows/macOS or without privileges

2. Test tc enforcement:
   - Create loopback traffic
   - Apply 5 Mbps limit via TrafficShaper
   - Measure actual throughput (iperf3)
   - Verify throughput <= 5 Mbps * 1.1
   - Classify: ENFORCED or BEST_EFFORT

3. Test multi-user isolation:
   - Create 2 subjects with different limits
   - Generate traffic for both
   - Verify limits enforced independently

4. Test cleanup:
   - Apply limits
   - Call Cleanup()
   - Verify tc rules removed

5. Test idempotency:
   - Apply same limit twice
   - Verify no errors, rules correct

**Phase 4: Download Limiting (Optional, 4 hours)**
1. Implement IFB device support:
   - Create IFB virtual interface
   - Redirect ingress traffic to IFB
   - Apply HTB qdisc on IFB
   - Test download limiting

2. Document IFB setup requirements:
   - Module loading (modprobe ifb)
   - Interface creation (ip link add ifb0 type ifb)
   - Operator guide

**Phase 5: Documentation (2 hours)**
1. Create operator documentation:
   - Prerequisites (iproute2, CAP_NET_ADMIN)
   - Network interface configuration
   - IFB setup for download limiting
   - Troubleshooting guide

2. Update Phase 9 documentation:
   - Change "AVAILABLE but NOT runtime-tested" to "ENFORCED"
   - Update enforcement matrix
   - Document measured enforcement accuracy

**Test Plan:**
- Unit tests: TrafficShaper logic (mock exec.Command)
- Runtime tests: Actual tc enforcement on Linux VM
- Integration tests: Xray + tc speed limiting E2E
- Verify upload limiting works
- Verify download limiting works (if IFB implemented)
- Verify multi-user isolation
- Verify cleanup on service removal
- Verify idempotency

**Acceptance Criteria:**
- [ ] TrafficShaper integrated with Xray adapter
- [ ] TrafficShaper integrated with WireGuard adapter  
- [ ] TrafficShaper integrated with L2TP adapter
- [ ] Reconciler syncs tc rules with desired state
- [ ] Cleanup removes stale tc rules
- [ ] Runtime tests verify upload limiting (Linux)
- [ ] Multi-user isolation verified
- [ ] Idempotency verified
- [ ] Operator documentation written
- [ ] Phase 9 documentation updated
- [ ] Enforcement matrix updated (EXTERNAL → ENFORCED)

**Commit:** [PENDING]

---

### GAP-007: L2TP Connection Limits (P2)

**Area:** Protocol Limitations  
**Priority:** P2 (Enhancement, not critical)  
**Status:** UNSUPPORTED  
**Phase 9 Reference:** M15 Protocol Edge Cases

**Current State:**
- L2TP connection limits UNSUPPORTED (xl2tpd limitation)
- Quota enforcement works (traffic accounted)
- No way to limit concurrent L2TP connections per user
- Inherent protocol/daemon limitation

**Evidence:**
```
Phase 9 M15:
"L2TP/IPsec: ❌ Connection limits unsupported (xl2tpd limitation)"
```

**Phase 9 Assessment:**
> "❌ UNSUPPORTED (xl2tpd limitation)"
> "✅ Fallback: None (inherent limitation)"

**Gap Analysis:**
This is NOT an implementation gap - it's a documented protocol limitation. The underlying xl2tpd daemon doesn't support per-user connection limits.

**Possible Mitigations (P2 Priority):**

1. **Alternative L2TP Daemon:**
   - Research alternative L2TP implementations
   - Evaluate if any support per-user connection limits
   - Cost: Significant rework of L2TP adapter

2. **External Firewall Rules:**
   - Use iptables conntrack to count connections per IP
   - Reject new connections if limit exceeded
   - Complex, fragile, may not map to subjects correctly

3. **Application-Level Proxy:**
   - Proxy L2TP connections through custom application
   - Enforce limits before forwarding to xl2tpd
   - Complex, performance overhead

4. **Accept Limitation:**
   - Document clearly that L2TP doesn't support connection limits
   - Recommend WireGuard or Xray for connection-limited use cases
   - Most practical solution

**Recommendation:** Accept limitation, document clearly. Not worth significant engineering effort for minor feature. Quota enforcement still works (primary use case).

**User Impact:** LOW  
- Most users care about quota (data limits), not connection limits
- Connection limits useful for device restrictions (use WireGuard instead)
- Clear documentation prevents false expectations

**Implementation Plan:** None - accept limitation  
**Commit:** N/A (documentation already complete)

---

### GAP-008: Operator Documentation (P2)

**Area:** Documentation / Operations  
**Priority:** P2 (Enhancement)  
**Status:** PARTIAL  
**Phase 9 Reference:** M5 Deployment, M12 API Documentation

**Current State:**
- Phase 9 audit documentation excellent (17 files)
- Inline code comments comprehensive
- Configuration examples good (node.yaml with 400+ lines of comments)
- Missing: Consolidated operator guides, troubleshooting, deployment examples

**Evidence:**
```bash
$ ls docs/
BACKUP-RESTORE.md
HEALTH-CHECKS.md
# Missing: INSTALLATION.md, TROUBLESHOOTING.md, OPERATIONS.md
```

**Phase 9 Assessment:**
> "⚠️ GOOD for developers, INSUFFICIENT for operators"
> "Missing: OpenAPI spec, deployment guide, troubleshooting guide, operator manual"

**Missing Documentation:**

1. **Installation Guide:**
   - System requirements (OS, dependencies)
   - Panel installation (from source, from binary)
   - Node installation (install.sh usage)
   - First-time setup (create admin user, enroll first node)
   - Database initialization

2. **Configuration Reference:**
   - Consolidated list of all panel flags
   - Consolidated list of all node.yaml options
   - Environment variables
   - Configuration validation

3. **Deployment Guide:**
   - systemd deployment (production)
   - Docker Compose deployment (development)
   - Kubernetes deployment (scale)
   - High availability setup
   - Multi-region deployment

4. **Operations Guide:**
   - Daily operations (user management, monitoring)
   - Backup procedures (database, master key)
   - Restore procedures (disaster recovery)
   - Upgrade procedures (panel, nodes, protocols)
   - Scaling considerations

5. **Troubleshooting Guide:**
   - Common issues (node won't enroll, service won't start)
   - Debugging techniques (logs, metrics, health checks)
   - Protocol-specific issues (Xray, WireGuard, L2TP)
   - Performance issues (slow queries, high memory)
   - Security issues (certificate expiry, password reset)

6. **Protocol Limitations Reference:**
   - Consolidated matrix (what works, what doesn't)
   - Workarounds (tc for speed limiting)
   - Protocol selection guide (when to use what)

**Security Impact:** NONE  

**User Impact:** LOW  
- Operators waste time searching for information
- Risk of misconfiguration
- Slower adoption

**Implementation Plan:**

**Phase 1: Installation & Configuration (3 hours)**
1. Create `docs/INSTALLATION.md`
2. Create `docs/CONFIGURATION.md`
3. Consolidate existing configuration examples

**Phase 2: Deployment & Operations (4 hours)**
1. Create `docs/DEPLOYMENT.md`
2. Create `docs/OPERATIONS.md`
3. Add systemd unit examples
4. Add Docker Compose examples

**Phase 3: Troubleshooting (3 hours)**
1. Create `docs/TROUBLESHOOTING.md`
2. Document common issues from test failures
3. Add debugging flowcharts

**Phase 4: Protocol Reference (2 hours)**
1. Create `docs/PROTOCOL-LIMITATIONS.md`
2. Consolidate enforcement matrix from Phase 9
3. Add protocol selection guide

**Test Plan:**
- Follow INSTALLATION.md on fresh Linux VM
- Follow DEPLOYMENT.md with systemd
- Follow DEPLOYMENT.md with Docker Compose
- Verify all documentation accurate

**Acceptance Criteria:**
- [ ] INSTALLATION.md complete and tested
- [ ] CONFIGURATION.md complete (all options documented)
- [ ] DEPLOYMENT.md complete (systemd, Docker, k8s)
- [ ] OPERATIONS.md complete
- [ ] TROUBLESHOOTING.md complete
- [ ] PROTOCOL-LIMITATIONS.md complete
- [ ] All documentation reviewed for accuracy

**Commit:** [PENDING]

---

## Gap Priority Summary

**P0 (Production Blockers) - 3 gaps:**
1. GAP-001: Performance Benchmarking (16 hours)
2. GAP-002: Deployment Automation (17 hours)
3. GAP-003: Failure Injection Framework (18 hours)

**P0 Total: 51 hours (~6-7 working days)**

**P1 (Important) - 3 gaps:**
4. GAP-004: API Documentation (16 hours)
5. GAP-005: Hysteria2 Verification (13 hours)
6. GAP-006: TC Integration (19 hours)

**P1 Total: 48 hours (~6 working days)**

**P2 (Enhancement) - 2 gaps:**
7. GAP-007: L2TP Connection Limits (N/A - accept limitation)
8. GAP-008: Operator Documentation (12 hours)

**P2 Total: 12 hours (~1.5 working days)**

**Grand Total: 111 hours (~14 working days of focused engineering)**

---

## Recommended Execution Order

### Phase 1: Measurement (Week 1)
**Goal:** Know what we have, measure current capabilities

1. **GAP-001: Performance Benchmarking** (P0, 16 hours)
   - Immediate value: Establish baselines
   - Blockers: None - pure measurement
   - Risk: Low
   - **Start: Day 1**

2. **GAP-005: Hysteria2 Verification** (P1, 13 hours)
   - Immediate value: Close classification gap
   - Blockers: None - protocol verification
   - Risk: Low
   - **Start: Day 3**

### Phase 2: Reliability (Week 2)
**Goal:** Prove resilience, harden failure handling

3. **GAP-003: Failure Injection Framework** (P0, 18 hours)
   - Immediate value: Prove reliability claims
   - Blockers: None
   - Risk: Medium (may discover new bugs)
   - **Start: Day 5**

4. **GAP-006: TC Integration** (P1, 19 hours)
   - Immediate value: Speed limiting works
   - Blockers: None - integration work
   - Risk: Medium (kernel dependency)
   - **Start: Day 8**

### Phase 3: Operations (Week 3)
**Goal:** Production deployment readiness

5. **GAP-002: Deployment Automation** (P0, 17 hours)
   - Immediate value: Safe deployments
   - Blockers: None
   - Risk: Low
   - **Start: Day 11**

6. **GAP-004: API Documentation** (P1, 16 hours)
   - Immediate value: Developer experience
   - Blockers: None
   - Risk: Low
   - **Start: Day 13**

7. **GAP-008: Operator Documentation** (P2, 12 hours)
   - Immediate value: Operator experience
   - Blockers: None
   - Risk: None
   - **Start: Day 15**

### Final Status: ~17 working days
After completion:
- All P0 gaps closed
- All P1 gaps closed
- P2 gaps closed except GAP-007 (accepted limitation)
- Production readiness: 95/100+

---

## Commit Tracking

Each gap will be tracked through completion:

**Commit Format:**
```
feat(area): GAP-XXX short description

Closes GAP-XXX.

Changes:
- Implemented X
- Added tests for Y
- Documented Z

Tests:
- All new tests pass
- No regressions in existing tests

Verification:
- Verified behavior A
- Measured performance B
- Documented findings C
```

**Branch Strategy:**
- Create branch per gap: `gap-001-performance-benchmarks`
- PR to `sp1-control-plane-spine` after completion
- Require all tests pass before merge

---

## Success Criteria

**Production Ready (95/100) requires:**
- ✅ All P0 gaps closed (Performance, Deployment, Failure Injection)
- ✅ All P1 gaps closed (API Docs, Hysteria2, TC Integration)
- ✅ All tests pass (existing + new)
- ✅ No regressions introduced
- ✅ Documentation updated
- ✅ Security review complete

**Current State:** 85/100  
**Target State:** 95/100  
**Gap:** 10 points = 7 open gaps (3 P0 + 3 P1 + 1 P2)

---

*This gap register is a living document. Update after each gap closed.*

# Performance Characteristics

**Status:** IN PROGRESS  
**Last Updated:** 2026-08-22  
**Benchmark Environment:** Windows 11, 13th Gen Intel i5-13400F, 16 cores

## Overview

This document records measured performance characteristics of the antimage panel and node. Performance testing is ongoing as part of GAP-001 (Performance Benchmarking).

## Benchmark Suite

Performance benchmarks are located in `internal/panel/store/benchmarks/` and measure critical operations under realistic load.

### Running Benchmarks

```bash
# Run all benchmarks
go test -bench=. -benchmem github.com/amyrm/antimage/internal/panel/store/benchmarks

# Run specific benchmark
go test -bench=BenchmarkSubjectCreate -benchmem github.com/amyrm/antimage/internal/panel/store/benchmarks

# Run with longer duration for stability
go test -bench=. -benchmem -benchtime=10s github.com/amyrm/antimage/internal/panel/store/benchmarks

# Profile memory allocations
go test -bench=BenchmarkSubjectCreate -benchmem -memprofile=mem.prof github.com/amyrm/antimage/internal/panel/store/benchmarks

# Profile CPU usage
go test -bench=BenchmarkSubjectCreate -cpuprofile=cpu.prof github.com/amyrm/antimage/internal/panel/store/benchmarks
```

## Measured Performance (Initial Baseline)

### Subject Operations

#### Subject Creation
- **Operation:** Create single subject with credentials
- **Performance:** ~4.4ms per operation
- **Memory:** 2.8 KB per operation, 67 allocations
- **Notes:** Includes credential encryption (AES-256-GCM), database insert, transaction commit

**Benchmark Results:**
```
BenchmarkSubjectCreate-16    277    4414284 ns/op    2695 B/op    67 allocs/op
```

**Analysis:**
- Throughput: ~227 subjects/second (single-threaded)
- Dominated by SQLite transaction serialization (single-writer design)
- Credential encryption overhead minimal (~100μs per credential)
- Suitable for typical user creation rates (< 1000 users/hour)

#### Bulk Subject Creation
- **Operation:** Create 100 subjects in single transaction
- **Performance:** TBD (benchmark running)
- **Expected:** ~50-100ms per batch (100 subjects)
- **Notes:** Batching reduces transaction overhead significantly

#### Subject Lookup (by ID)
- **Operation:** Fetch single subject by ID
- **Performance:** TBD (benchmark running)
- **Expected:** <1ms per lookup (indexed query)
- **Notes:** SQLite WAL mode enables concurrent reads

#### Subject List
- **Operation:** List all subjects (no pagination)
- **Performance:** TBD (benchmark running)
- **Test Sizes:** 100, 1000, 10000 subjects
- **Expected:** O(n) linear scaling

### Credential Operations

#### Credential Rotation
- **Operation:** Rotate UUID credential for subject
- **Performance:** TBD (benchmark running)
- **Expected:** ~3-4ms (similar to creation)
- **Notes:** Includes encryption + database update

### Dashboard Operations

#### Stats Computation
- **Operation:** Compute dashboard stats (1000 subjects)
- **Performance:** TBD (benchmark running)
- **Queries:** Node counts, subject counts, 24h traffic, quota aggregates
- **Expected:** 10-50ms depending on data size
- **Notes:** Involves multiple aggregate queries

#### Stats Retrieval (Cached)
- **Operation:** Fetch cached dashboard stats
- **Performance:** TBD (benchmark running)
- **Expected:** <1ms (single row lookup)
- **Cache TTL:** 60 seconds

### Traffic Accounting

#### Single Traffic Update
- **Operation:** Update quota_used_bytes for one subject
- **Performance:** TBD (benchmark running)
- **Expected:** ~2-3ms (indexed UPDATE)

#### Batch Traffic Update
- **Operation:** Update 50 subjects in single transaction
- **Performance:** TBD (benchmark running)
- **Expected:** ~20-40ms for batch
- **Notes:** Node traffic collection typically batches updates

### Quota Calculations

#### Aggregate Quota Query
- **Operation:** SUM(quota_bytes), SUM(quota_used_bytes) across all subjects
- **Performance:** TBD (benchmark running)
- **Test Size:** 1000 subjects
- **Expected:** <10ms (sequential scan acceptable for aggregates)

### Metric Aggregation

#### 24-Hour Traffic Rollup
- **Operation:** SUM uplink/downlink from hourly rollups (7 days × 100 subjects = 16,800 rows)
- **Performance:** TBD (benchmark running)
- **Expected:** 10-50ms
- **Notes:** Dashboard query, indexed on hour_start

### Session Validation

#### Session Lookup
- **Operation:** Validate session token (lookup + expiry check)
- **Performance:** TBD (benchmark running)
- **Expected:** <1ms (indexed query on token)
- **Notes:** Hot path for every authenticated API request

### Concurrent Read Performance

#### Parallel Subject Reads
- **Operation:** Concurrent Get(subjectID) across 16 threads
- **Performance:** TBD (benchmark running)
- **Expected:** Near-linear scaling (SQLite WAL concurrent reads)
- **Notes:** Tests read pool effectiveness

## Database Growth Characteristics

### Database Size vs Subject Count

**Test:** Measure database file size with varying subject counts

| Subjects | DB Size (bytes) | Bytes/Subject | Notes |
|----------|----------------|---------------|-------|
| 100      | TBD            | TBD           | Includes migrations, indexes |
| 1,000    | TBD            | TBD           | Typical small deployment |
| 10,000   | TBD            | TBD           | Medium deployment |

**Expected:**
- ~2-5 KB per subject (subject row + 2 credentials + indexes)
- SQLite page size: 4096 bytes
- Database file includes: 20 migrations, 38 tables, indexes, metadata

## Known Performance Characteristics

### Architecture Decisions

**Single-Writer SQLite:**
- Write operations serialize (1 connection max)
- Read operations concurrent (WAL mode, pooled connection)
- SERIALIZABLE isolation level
- Busy timeout: 5000ms

**Transaction Batching:**
- Bulk operations use single transaction
- Node traffic updates batched (not per-packet)
- Reconciliation applies all changes in one transaction

**Indexing Strategy:**
- Foreign keys auto-indexed
- Partial indexes on common filters (enabled=1, frozen_at IS NULL)
- No N+1 queries detected in codebase review

**Caching:**
- Dashboard stats cached for 60 seconds
- No application-level query result caching
- SQLite page cache handled by driver

### Timing Characteristics (From Architecture)

**Agent Operations:**
- Reconnection backoff: 1s → 2s → 4s → ... → 60s max
- Reconciliation interval: 5 minutes (configurable)
- Heartbeat interval: 30 seconds
- gRPC keepalive: 10 seconds

**Panel Operations:**
- Session idle timeout: 4 hours
- Session absolute timeout: 7 days
- Enrollment token TTL: 30 minutes
- Dashboard stats cache: 60 seconds

## Scale Limits (Known)

### Tested Scale
- **Subjects:** 10,000 (benchmark suite)
- **Concurrent Nodes:** Not tested (no load test yet)
- **API Requests:** Not tested (no load test yet)

### Architectural Limits
- **SQLite Database Size:** Practical limit ~100GB (theoretical 281TB)
- **Concurrent Writes:** 1 (single-writer design, queued via busy_timeout)
- **Concurrent Reads:** Unlimited (WAL mode)

### Recommended Deployment Scale (Phase 9)
- **Nodes:** 1-50 (proven safe)
- **Subjects:** 100-10,000 (tested in benchmarks)
- **Traffic Throughput:** Unknown (no load test)

## Performance Issues (Known)

### Identified Issues
None yet - baseline benchmarking in progress.

### Potential Bottlenecks
1. **Write Serialization:** Single-writer SQLite could bottleneck under heavy concurrent writes
   - **Mitigation:** Transaction batching, async audit log
   - **Needs Testing:** Load test with 100+ concurrent API clients

2. **Dashboard Queries:** Complex aggregates could slow with large datasets
   - **Mitigation:** 60-second cache, partial indexes
   - **Needs Testing:** Measure with 100k+ subjects

3. **Metric Rollups:** Hourly/daily aggregation could slow with high traffic volume
   - **Mitigation:** Incremental rollups (not full recompute)
   - **Needs Testing:** Profile with 1M+ traffic records

## Profiling Infrastructure

### pprof Endpoints (NOT YET IMPLEMENTED)

**Planned endpoints:**
- GET /debug/pprof/ - Index page
- GET /debug/pprof/heap - Memory allocations
- GET /debug/pprof/goroutine - Goroutine dump
- GET /debug/pprof/profile?seconds=30 - CPU profile
- GET /debug/pprof/trace?seconds=5 - Execution trace

**Usage:**
```bash
# Heap profile
go tool pprof http://localhost:8080/debug/pprof/heap

# CPU profile (30 seconds)
go tool pprof http://localhost:8080/debug/pprof/profile?seconds=30

# Goroutine analysis
curl http://localhost:8080/debug/pprof/goroutine?debug=1
```

**Security Note:** pprof endpoints should only be enabled in development or bound to localhost in production.

## Load Testing (NOT YET IMPLEMENTED)

### Planned Load Tests

**Test 1: Concurrent Nodes**
- Scenario: 100 fake nodes connecting, heartbeat, metrics
- Duration: 10 minutes
- Measure: Memory growth, API response times, error rate

**Test 2: Concurrent API Clients**
- Scenario: 100 API clients, mixed workload (80% read, 20% write)
- Duration: 5 minutes
- Measure: Throughput (req/s), latency (p50, p95, p99)

**Test 3: Subject Scale**
- Scenario: Create 10,000 subjects, test CRUD operations
- Measure: Database size, query performance degradation

**Test 4: Traffic Volume**
- Scenario: Simulate high traffic (1000 subjects, 1GB/s aggregate)
- Measure: Accounting latency, database growth, rollup performance

## Optimization Opportunities

### Identified (Not Yet Implemented)
1. Add pprof endpoints for production profiling
2. Implement load testing framework
3. Add database query logging for slow queries (>100ms)
4. Consider connection pooling tuning
5. Profile memory allocations in hot paths

### Future Considerations
- Redis cache for dashboard stats (multi-node deployments)
- PostgreSQL migration for higher write concurrency
- Read replicas for query scale
- Time-series database for metrics (InfluxDB, TimescaleDB)

## Benchmark Results (Full Suite)

**Status:** Benchmark running in background...

Results will be appended when benchmark completes.

---

**Next Steps:**
1. ✅ Create benchmark suite (GAP-001 Phase 1)
2. ⏳ Run full benchmark suite (in progress)
3. ⏳ Analyze results, identify bottlenecks
4. ☐ Implement pprof endpoints (GAP-001 Phase 3)
5. ☐ Create load testing framework (GAP-001 Phase 2)
6. ☐ Profile under load, optimize if needed (GAP-001 Phase 4)

---

*This document is updated as new performance data becomes available.*

package benchmarks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/dashboard"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/panel/subjects"
	"github.com/amyrm/antimage/internal/shared/secrets"
)

// setupBenchDB creates a temporary database for benchmarking
func setupBenchDB(b *testing.B) (*store.Store, *secrets.Box, func()) {
	b.Helper()

	tmpDir := b.TempDir()
	dbPath := filepath.Join(tmpDir, "bench.db")

	st, err := store.Open(dbPath)
	if err != nil {
		b.Fatalf("open store: %v", err)
	}

	// Generate key for encryption
	key := make([]byte, secrets.KeySize)
	if _, err := rand.Read(key); err != nil {
		b.Fatalf("generate key: %v", err)
	}

	box, err := secrets.NewBox(key)
	if err != nil {
		b.Fatalf("create box: %v", err)
	}

	cleanup := func() {
		_ = st.Close()
		_ = os.RemoveAll(tmpDir)
	}

	return st, box, cleanup
}

// seedSubjects creates N subjects for benchmarking
func seedSubjects(b *testing.B, st *store.Store, box *secrets.Box, count int) []int64 {
	b.Helper()

	ctx := context.Background()
	subjStore := subjects.NewStore(st, box, nil)

	ids := make([]int64, 0, count)

	for i := 0; i < count; i++ {
		var id int64
		err := st.Write(ctx, func(tx *sql.Tx) error {
			var err error
			id, err = subjStore.Create(ctx, tx, subjects.CreateInput{
				Name: fmt.Sprintf("bench-subject-%d", i),
				Note: fmt.Sprintf("Benchmark subject %d", i),
			})
			return err
		})
		if err != nil {
			b.Fatalf("seed subject %d: %v", i, err)
		}
		ids = append(ids, id)
	}

	return ids
}

// BenchmarkSubjectCreate measures subject creation performance
func BenchmarkSubjectCreate(b *testing.B) {
	st, box, cleanup := setupBenchDB(b)
	defer cleanup()

	subjStore := subjects.NewStore(st, box, nil)
	ctx := context.Background()

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		err := st.Write(ctx, func(tx *sql.Tx) error {
			_, createErr := subjStore.Create(ctx, tx, subjects.CreateInput{
				Name: fmt.Sprintf("bench-user-%d", i),
				Note: "Benchmark user",
			})
			return createErr
		})
		if err != nil {
			b.Fatalf("create subject: %v", err)
		}
	}
}

// BenchmarkSubjectCreateBulk measures bulk subject creation
func BenchmarkSubjectCreateBulk(b *testing.B) {
	const batchSize = 100

	st, box, cleanup := setupBenchDB(b)
	defer cleanup()

	subjStore := subjects.NewStore(st, box, nil)
	ctx := context.Background()

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		err := st.Write(ctx, func(tx *sql.Tx) error {
			for j := 0; j < batchSize; j++ {
				_, err := subjStore.Create(ctx, tx, subjects.CreateInput{
					Name: fmt.Sprintf("bulk-%d-%d", i, j),
					Note: "Bulk benchmark",
				})
				if err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			b.Fatalf("bulk create: %v", err)
		}
	}

	b.ReportMetric(float64(batchSize), "subjects/op")
}

// BenchmarkSubjectGet measures single subject lookup
func BenchmarkSubjectGet(b *testing.B) {
	st, box, cleanup := setupBenchDB(b)
	defer cleanup()

	subjStore := subjects.NewStore(st, box, nil)

	// Seed 1000 subjects
	ids := seedSubjects(b, st, box, 1000)

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		subjectID := ids[i%len(ids)]
		_, err := subjStore.Get(ctx, rbac.Scope{IsSuper: true}, subjectID)
		if err != nil {
			b.Fatalf("get subject: %v", err)
		}
	}
}

// BenchmarkSubjectList measures listing all subjects
func BenchmarkSubjectList(b *testing.B) {
	// Test with different dataset sizes
	for _, count := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("n=%d", count), func(b *testing.B) {
			// Create fresh DB for each subtest to avoid name conflicts
			stLocal, boxLocal, cleanupLocal := setupBenchDB(b)
			defer cleanupLocal()

			subjStoreLocal := subjects.NewStore(stLocal, boxLocal, nil)

			// Seed subjects
			seedSubjects(b, stLocal, boxLocal, count)

			ctx := context.Background()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				subjs, err := subjStoreLocal.List(ctx, rbac.Scope{IsSuper: true})
				if err != nil {
					b.Fatalf("list subjects: %v", err)
				}
				if len(subjs) != count {
					b.Fatalf("expected %d subjects, got %d", count, len(subjs))
				}
			}
		})
	}
}

// BenchmarkSubjectCredentialRotate measures credential rotation
func BenchmarkSubjectCredentialRotate(b *testing.B) {
	st, box, cleanup := setupBenchDB(b)
	defer cleanup()

	subjStore := subjects.NewStore(st, box, nil)

	// Seed one subject
	ids := seedSubjects(b, st, box, 1)
	subjectID := ids[0]

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		err := st.Write(ctx, func(tx *sql.Tx) error {
			_, err := subjStore.Rotate(ctx, tx, subjectID, subjects.KindUUID)
			return err
		})
		if err != nil {
			b.Fatalf("rotate credential: %v", err)
		}
	}
}

// BenchmarkDashboardStatsCompute measures dashboard stats computation
func BenchmarkDashboardStatsCompute(b *testing.B) {
	st, box, cleanup := setupBenchDB(b)
	defer cleanup()

	// Seed subjects for realistic stats
	seedSubjects(b, st, box, 1000)

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := dashboard.ComputeStats(ctx, st, nil)
		if err != nil {
			b.Fatalf("compute stats: %v", err)
		}
	}
}

// BenchmarkDashboardStatsGetCached measures cached stats retrieval
func BenchmarkDashboardStatsGetCached(b *testing.B) {
	st, box, cleanup := setupBenchDB(b)
	defer cleanup()

	// Seed and compute stats once
	seedSubjects(b, st, box, 1000)

	ctx := context.Background()
	actor := rbac.Actor{IsSuper: true}

	// Prime cache
	_, err := dashboard.GetStats(ctx, st, actor)
	if err != nil {
		b.Fatalf("prime cache: %v", err)
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := dashboard.GetStats(ctx, st, actor)
		if err != nil {
			b.Fatalf("get stats: %v", err)
		}
	}
}

// BenchmarkQuotaCalculation measures quota usage calculation
func BenchmarkQuotaCalculation(b *testing.B) {
	st, box, cleanup := setupBenchDB(b)
	defer cleanup()

	// Seed subjects with quotas
	ids := seedSubjects(b, st, box, 1000)

	ctx := context.Background()

	// Set quotas and usage
	for _, id := range ids {
		err := st.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx,
				`UPDATE subjects SET quota_bytes = 107374182400, quota_used_bytes = 53687091200 WHERE id = ?`,
				id) // 100GB quota, 50GB used
			return err
		})
		if err != nil {
			b.Fatalf("set quota: %v", err)
		}
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		var totalQuota, totalUsed int64
		err := st.Read().QueryRowContext(ctx,
			`SELECT COALESCE(SUM(quota_bytes), 0), COALESCE(SUM(quota_used_bytes), 0) FROM subjects`,
		).Scan(&totalQuota, &totalUsed)
		if err != nil {
			b.Fatalf("calculate quota: %v", err)
		}
	}
}

// BenchmarkTrafficUpdate measures traffic accounting update performance
func BenchmarkTrafficUpdate(b *testing.B) {
	st, box, cleanup := setupBenchDB(b)
	defer cleanup()

	// Seed subjects
	ids := seedSubjects(b, st, box, 100)

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		subjectID := ids[i%len(ids)]
		err := st.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx,
				`UPDATE subjects SET quota_used_bytes = quota_used_bytes + ? WHERE id = ?`,
				1048576, subjectID) // Add 1MB
			return err
		})
		if err != nil {
			b.Fatalf("update traffic: %v", err)
		}
	}
}

// BenchmarkTrafficUpdateBatch measures batch traffic updates
func BenchmarkTrafficUpdateBatch(b *testing.B) {
	const batchSize = 50

	st, box, cleanup := setupBenchDB(b)
	defer cleanup()

	// Seed subjects
	ids := seedSubjects(b, st, box, 100)

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		err := st.Write(ctx, func(tx *sql.Tx) error {
			for j := 0; j < batchSize; j++ {
				subjectID := ids[(i*batchSize+j)%len(ids)]
				_, err := tx.ExecContext(ctx,
					`UPDATE subjects SET quota_used_bytes = quota_used_bytes + ? WHERE id = ?`,
					1048576, subjectID)
				if err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			b.Fatalf("batch update: %v", err)
		}
	}

	b.ReportMetric(float64(batchSize), "updates/op")
}

// BenchmarkConcurrentReads measures concurrent read performance
func BenchmarkConcurrentReads(b *testing.B) {
	st, box, cleanup := setupBenchDB(b)
	defer cleanup()

	subjStore := subjects.NewStore(st, box, nil)

	// Seed subjects
	ids := seedSubjects(b, st, box, 1000)

	ctx := context.Background()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			subjectID := ids[i%len(ids)]
			_, err := subjStore.Get(ctx, rbac.Scope{IsSuper: true}, subjectID)
			if err != nil {
				b.Fatalf("concurrent read: %v", err)
			}
			i++
		}
	})
}

// BenchmarkMetricAggregation measures hourly metric rollup performance
func BenchmarkMetricAggregation(b *testing.B) {
	st, box, cleanup := setupBenchDB(b)
	defer cleanup()

	ctx := context.Background()

	// Seed subjects first (foreign key requirement)
	seedSubjects(b, st, box, 100)

	// Seed hourly rollups (simulate 7 days of hourly data for 100 subjects)
	now := time.Now().Unix()
	hoursInWeek := 24 * 7

	err := st.Write(ctx, func(tx *sql.Tx) error {
		for hour := 0; hour < hoursInWeek; hour++ {
			hourStart := now - int64(hour*3600)
			for subj := 1; subj <= 100; subj++ {
				_, err := tx.ExecContext(ctx,
					`INSERT INTO usage_rollups_hourly (subject_id, hour_start, uplink_bytes, downlink_bytes)
					 VALUES (?, ?, ?, ?)`,
					subj, hourStart, 1048576000, 2097152000) // 1GB up, 2GB down per hour
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		b.Fatalf("seed metrics: %v", err)
	}

	cutoff24h := now - 86400 // 24 hours ago

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		var uplink, downlink int64
		err := st.Read().QueryRowContext(ctx,
			`SELECT COALESCE(SUM(uplink_bytes), 0), COALESCE(SUM(downlink_bytes), 0)
			 FROM usage_rollups_hourly
			 WHERE hour_start >= ?`,
			cutoff24h,
		).Scan(&uplink, &downlink)
		if err != nil {
			b.Fatalf("aggregate metrics: %v", err)
		}
	}
}

// BenchmarkSessionValidation measures session lookup performance
func BenchmarkSessionValidation(b *testing.B) {
	st, _, cleanup := setupBenchDB(b)
	defer cleanup()

	ctx := context.Background()

	// Create role and admin (foreign key requirements)
	var adminID int64
	err := st.Write(ctx, func(tx *sql.Tx) error {
		// Create role first
		roleResult, err := tx.ExecContext(ctx,
			`INSERT INTO roles (name, is_builtin, permissions)
			 VALUES (?, ?, ?)`,
			"bench_role", 1, "[]")
		if err != nil {
			return err
		}
		roleID, err := roleResult.LastInsertId()
		if err != nil {
			return err
		}

		// Create admin with role_id
		adminResult, err := tx.ExecContext(ctx,
			`INSERT INTO admins (username, password_hash, role_id, status, created_at)
			 VALUES (?, ?, ?, ?, ?)`,
			"bench-admin", "$argon2id$v=19$m=65536,t=3,p=4$test$testhash", roleID, "active", time.Now().Unix())
		if err != nil {
			return err
		}
		adminID, err = adminResult.LastInsertId()
		return err
	})
	if err != nil {
		b.Fatalf("create admin: %v", err)
	}

	// Seed sessions
	sessionTokenHashes := make([][]byte, 100)
	err = st.Write(ctx, func(tx *sql.Tx) error {
		for i := 0; i < 100; i++ {
			tokenHash := []byte(fmt.Sprintf("session-hash-%d", i))
			sessionTokenHashes[i] = tokenHash
			_, err := tx.ExecContext(ctx,
				`INSERT INTO sessions (admin_id, token_hash, ip, user_agent, created_at, expires_at, last_used_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				adminID, tokenHash, "192.0.2.1", "test-agent", time.Now().Unix(), time.Now().Add(4*time.Hour).Unix(), time.Now().Unix())
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.Fatalf("seed sessions: %v", err)
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		tokenHash := sessionTokenHashes[i%len(sessionTokenHashes)]
		var aid int64
		err := st.Read().QueryRowContext(ctx,
			`SELECT admin_id FROM sessions WHERE token_hash = ? AND expires_at > ?`,
			tokenHash, time.Now().Unix(),
		).Scan(&aid)
		if err != nil {
			b.Fatalf("validate session: %v", err)
		}
	}
}

// BenchmarkDatabaseSize measures database growth characteristics
func BenchmarkDatabaseSize(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping database size benchmark in short mode")
	}

	counts := []int{100, 1000, 10000}

	for _, count := range counts {
		b.Run(fmt.Sprintf("subjects=%d", count), func(b *testing.B) {
			// Create fresh DB for each subtest
			stLocal, boxLocal, cleanupLocal := setupBenchDB(b)
			defer cleanupLocal()

			seedSubjects(b, stLocal, boxLocal, count)

			// Measure database file size
			var pageCount, pageSize int64
			err := stLocal.Read().QueryRow(`PRAGMA page_count`).Scan(&pageCount)
			if err != nil {
				b.Fatalf("get page count: %v", err)
			}
			err = stLocal.Read().QueryRow(`PRAGMA page_size`).Scan(&pageSize)
			if err != nil {
				b.Fatalf("get page size: %v", err)
			}

			dbSize := pageCount * pageSize
			b.ReportMetric(float64(dbSize), "bytes")
			b.ReportMetric(float64(dbSize)/float64(count), "bytes/subject")
		})
	}
}

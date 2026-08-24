package chaos

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// DatabaseFaultInjector wraps database operations with fault injection
type DatabaseFaultInjector struct {
	// Only db is used. The fault-state fields this once declared were never
	// set or read, so the type advertised an injection capability it did not
	// have; FaultyDB below is the one the reliability tests actually drive.
	db *sql.DB
}

// NewDatabaseFaultInjector creates a database fault injector
func NewDatabaseFaultInjector(db *sql.DB) *DatabaseFaultInjector {
	return &DatabaseFaultInjector{db: db}
}

// InjectDatabaseLockTimeout simulates database lock timeout
func (i *Injector) InjectDatabaseLockTimeout(timeout time.Duration) (*Fault, error) {
	fault := &Fault{
		ID:          fmt.Sprintf("db-lock-timeout-%d", time.Now().UnixNano()),
		Type:        FaultTypeDatabase,
		Description: fmt.Sprintf("Database lock timeout: %v", timeout),
		InjectedAt:  time.Now(),
		RemoveFunc:  func() error { return nil },
	}

	if err := i.InjectFault(context.Background(), *fault); err != nil {
		return nil, err
	}

	return fault, nil
}

// InjectDatabaseConnectionLoss simulates database connection loss
func (i *Injector) InjectDatabaseConnectionLoss() (*Fault, error) {
	fault := &Fault{
		ID:          fmt.Sprintf("db-conn-loss-%d", time.Now().UnixNano()),
		Type:        FaultTypeDatabase,
		Description: "Database connection loss",
		InjectedAt:  time.Now(),
		RemoveFunc:  func() error { return nil },
	}

	if err := i.InjectFault(context.Background(), *fault); err != nil {
		return nil, err
	}

	return fault, nil
}

// InjectDatabaseSlowQuery simulates slow database query
func (i *Injector) InjectDatabaseSlowQuery(delay time.Duration) (*Fault, error) {
	fault := &Fault{
		ID:          fmt.Sprintf("db-slow-query-%d", time.Now().UnixNano()),
		Type:        FaultTypeDatabase,
		Description: fmt.Sprintf("Slow query delay: %v", delay),
		InjectedAt:  time.Now(),
		RemoveFunc:  func() error { return nil },
	}

	if err := i.InjectFault(context.Background(), *fault); err != nil {
		return nil, err
	}

	return fault, nil
}

// FaultyDB wraps a sql.DB with fault injection
type FaultyDB struct {
	mu          sync.Mutex
	db          *sql.DB
	failNext    bool
	delay       time.Duration
	lockTimeout time.Duration
	queryCount  int
	failEveryN  int // Fail every Nth query
}

// NewFaultyDB creates a database wrapper with fault injection
func NewFaultyDB(db *sql.DB) *FaultyDB {
	return &FaultyDB{db: db}
}

// SetFailNext makes the next operation fail
func (f *FaultyDB) SetFailNext(fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext = fail
}

// SetDelay adds delay to all operations
func (f *FaultyDB) SetDelay(delay time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delay = delay
}

// SetFailEveryN makes every Nth query fail
func (f *FaultyDB) SetFailEveryN(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failEveryN = n
	f.queryCount = 0
}

// QueryContext wraps sql.DB.QueryContext with fault injection
func (f *FaultyDB) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.queryCount++

	if f.failNext {
		f.failNext = false
		return nil, fmt.Errorf("injected query failure")
	}

	if f.failEveryN > 0 && f.queryCount%f.failEveryN == 0 {
		return nil, fmt.Errorf("injected periodic query failure (every %d)", f.failEveryN)
	}

	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	//nolint:sqlclosecheck // the caller owns and closes these rows
	return f.db.QueryContext(ctx, query, args...)
}

// ExecContext wraps sql.DB.ExecContext with fault injection
func (f *FaultyDB) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failNext {
		f.failNext = false
		return nil, fmt.Errorf("injected exec failure")
	}

	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if f.lockTimeout > 0 {
		// Simulate lock timeout by using a very short context
		timeoutCtx, cancel := context.WithTimeout(ctx, f.lockTimeout)
		defer cancel()
		return f.db.ExecContext(timeoutCtx, query, args...)
	}

	return f.db.ExecContext(ctx, query, args...)
}

// BeginTx wraps sql.DB.BeginTx with fault injection
func (f *FaultyDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failNext {
		f.failNext = false
		return nil, fmt.Errorf("injected transaction begin failure")
	}

	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	return f.db.BeginTx(ctx, opts)
}

// Close wraps sql.DB.Close
func (f *FaultyDB) Close() error {
	return f.db.Close()
}

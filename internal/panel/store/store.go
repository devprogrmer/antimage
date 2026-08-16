// Package store owns the SQLite connection lifecycle and schema migrations.
//
// SQLite permits exactly one writer. We therefore keep two handles: a pooled
// read handle for concurrent queries, and a write handle capped at one
// connection so write transactions serialize instead of colliding on
// SQLITE_BUSY.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	"github.com/amyrm/antimage/internal/panel/store/migrations"
)

type Store struct {
	read  *sql.DB
	write *sql.DB
}

// dsn builds a connection string carrying the pragmas from the spec's
// global constraints. They must be on the DSN, not issued as statements,
// so every pooled connection gets them.
func dsn(path string) string {
	return fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)",
		path,
	)
}

func Open(path string) (*Store, error) {
	write, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open write handle: %w", err)
	}
	write.SetMaxOpenConns(1)

	read, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		_ = write.Close()
		return nil, fmt.Errorf("open read handle: %w", err)
	}

	s := &Store{read: read, write: write}
	if err := s.migrate(); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.Up(s.write, "."); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}

// Read returns the pooled read-only handle. Never write through it.
func (s *Store) Read() *sql.DB { return s.read }

// Write runs fn inside a transaction on the single write connection.
// It commits when fn returns nil and rolls back otherwise. If fn panics,
// the transaction is rolled back before the panic propagates, so the sole
// write connection is never left permanently checked out.
//
// Warning: fn must not call Write (directly, or indirectly through
// something like audit.BestEffort) on this same Store — the single write
// connection is already checked out, so the nested call would block
// waiting for it and, absent a bounded context, could hang forever.
func (s *Store) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// Close closes both handles unconditionally and joins any errors from
// either, so a double-fault on shutdown does not lose diagnostics.
func (s *Store) Close() error {
	rerr := s.read.Close()
	werr := s.write.Close()
	return errors.Join(rerr, werr)
}

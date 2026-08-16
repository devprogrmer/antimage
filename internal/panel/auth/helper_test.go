package auth

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func insertTestAdmin(t *testing.T, s *store.Store) int64 {
	t.Helper()
	var id int64
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`INSERT INTO roles (id, name, is_builtin, permissions) VALUES (1, 'super_admin', 1, '[]')`,
		); err != nil {
			return err
		}
		res, err := tx.Exec(
			`INSERT INTO admins (username, password_hash, role_id, created_at)
			 VALUES ('tester', 'x', 1, ?)`, time.Now().Unix())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("insertTestAdmin: %v", err)
	}
	return id
}

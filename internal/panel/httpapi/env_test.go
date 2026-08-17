package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/auth"
	"github.com/amyrm/antimage/internal/panel/control"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

type testEnv struct {
	store   *store.Store
	handler http.Handler
}

func itoa64(i int64) string { return strconv.FormatInt(i, 10) }

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	now := func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	h := NewRouter(Deps{
		Store:    s,
		Sessions: auth.NewSessions(s, now),
		Limiter:  auth.NewLimiter(s, now),
		Hub:      control.NewHub(),
		Now:      now,
	})
	return &testEnv{store: s, handler: h}
}

func (e *testEnv) seedAdmin(t *testing.T, username, password, role string) int64 {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	perms, err := json.Marshal(rbac.BuiltinRoles()[role])
	if err != nil {
		t.Fatalf("marshal perms: %v", err)
	}

	var adminID int64
	err = e.store.Write(context.Background(), func(tx *sql.Tx) error {
		var roleID int64
		err := tx.QueryRow(`SELECT id FROM roles WHERE name = ?`, role).Scan(&roleID)
		if err == sql.ErrNoRows {
			res, err := tx.Exec(
				`INSERT INTO roles (name, is_builtin, permissions) VALUES (?, 1, ?)`,
				role, string(perms))
			if err != nil {
				return err
			}
			roleID, err = res.LastInsertId()
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		res, err := tx.Exec(
			`INSERT INTO admins (username, password_hash, role_id, created_at) VALUES (?,?,?,?)`,
			username, hash, roleID, time.Now().Unix())
		if err != nil {
			return err
		}
		adminID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("seedAdmin: %v", err)
	}
	return adminID
}

func (e *testEnv) do(t *testing.T, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://panel.local")
	req.Host = "panel.local"
	if token != "" {
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func (e *testEnv) get(t *testing.T, path, token string) *httptest.ResponseRecorder {
	return e.do(t, http.MethodGet, path, "", token)
}

func (e *testEnv) post(t *testing.T, path, body, token string) *httptest.ResponseRecorder {
	return e.do(t, http.MethodPost, path, body, token)
}

// login returns the session cookie value.
func (e *testEnv) login(t *testing.T, username, password string) string {
	t.Helper()
	res := e.post(t, "/api/v1/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`, "")
	if res.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", res.Code, res.Body)
	}
	for _, c := range res.Result().Cookies() {
		if c.Name == auth.CookieName {
			return c.Value
		}
	}
	t.Fatal("no session cookie in login response")
	return ""
}

package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/amyrm/antimage/internal/panel/auth"
	"github.com/amyrm/antimage/internal/panel/store"
)

func newCtlEnv(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "antimage.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

func TestCreateAdminSeedsRolesAndHashesPassword(t *testing.T) {
	s, _ := newCtlEnv(t)
	ctx := context.Background()

	if err := createAdmin(ctx, s, "root", "s3cret", "super_admin"); err != nil {
		t.Fatalf("createAdmin: %v", err)
	}

	var hash, role string
	if err := s.Read().QueryRow(
		`SELECT a.password_hash, r.name FROM admins a JOIN roles r ON r.id = a.role_id
		  WHERE a.username = 'root'`).Scan(&hash, &role); err != nil {
		t.Fatalf("read admin: %v", err)
	}
	if role != "super_admin" {
		t.Errorf("role = %q, want super_admin", role)
	}
	if strings.Contains(hash, "s3cret") {
		t.Fatal("the password was stored in plaintext")
	}
	ok, err := auth.VerifyPassword(hash, "s3cret")
	if err != nil || !ok {
		t.Errorf("stored hash does not verify: %v", err)
	}

	var roles int
	_ = s.Read().QueryRow(`SELECT count(*) FROM roles`).Scan(&roles)
	if roles != 4 {
		t.Errorf("roles seeded = %d, want 4", roles)
	}
}

// Usernames are case-insensitive: admins.username is COLLATE NOCASE with a
// matching unique index, so Root and root cannot both exist.
func TestCreateAdminRejectsDuplicateUsername(t *testing.T) {
	s, _ := newCtlEnv(t)
	ctx := context.Background()
	if err := createAdmin(ctx, s, "root", "pw", "super_admin"); err != nil {
		t.Fatalf("first createAdmin: %v", err)
	}
	err := createAdmin(ctx, s, "ROOT", "pw", "super_admin")
	if err == nil {
		t.Fatal("duplicate username accepted; usernames are case-insensitive")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("err = %v, want it to name the duplicate", err)
	}
}

func TestCreateAdminRejectsUnknownRole(t *testing.T) {
	s, _ := newCtlEnv(t)
	if err := createAdmin(context.Background(), s, "x", "pw", "wizard"); err == nil {
		t.Fatal("unknown role accepted")
	}
	var n int
	_ = s.Read().QueryRow(`SELECT count(*) FROM admins`).Scan(&n)
	if n != 0 {
		t.Errorf("admins = %d, want 0: a rejected role must not create an account", n)
	}
}

func TestResetPasswordChangesHashAndRevokesSessions(t *testing.T) {
	s, _ := newCtlEnv(t)
	ctx := context.Background()
	if err := createAdmin(ctx, s, "root", "old", "super_admin"); err != nil {
		t.Fatalf("createAdmin: %v", err)
	}

	sessions := auth.NewSessions(s, nil)
	var adminID int64
	_ = s.Read().QueryRow(`SELECT id FROM admins WHERE username='root'`).Scan(&adminID)
	token, err := sessions.Create(ctx, adminID, "10.0.0.1", "test")
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	if err := resetPassword(ctx, s, "root", "new"); err != nil {
		t.Fatalf("resetPassword: %v", err)
	}

	var hash string
	_ = s.Read().QueryRow(`SELECT password_hash FROM admins WHERE username='root'`).Scan(&hash)
	if ok, _ := auth.VerifyPassword(hash, "new"); !ok {
		t.Error("new password does not verify")
	}
	if ok, _ := auth.VerifyPassword(hash, "old"); ok {
		t.Error("old password still verifies")
	}
	// A stolen session must not outlive the password it was created with.
	if _, err := sessions.Lookup(ctx, token); err == nil {
		t.Error("existing session survived a password reset")
	}
}

// Reset is the lockout recovery path, so it must also clear the failed-login
// history. Otherwise the very attempts that locked the operator out would keep
// them out after they had fixed the password.
func TestResetPasswordClearsTheAccountLockout(t *testing.T) {
	s, _ := newCtlEnv(t)
	ctx := context.Background()
	if err := createAdmin(ctx, s, "root", "old", "super_admin"); err != nil {
		t.Fatalf("createAdmin: %v", err)
	}
	limiter := auth.NewLimiter(s, nil)
	for i := 0; i < auth.AccountFailureLimit; i++ {
		if err := limiter.RecordFailure(ctx, "root", "10.0.0.1"); err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
	}
	if wait, _ := limiter.Check(ctx, "root", "10.0.0.1"); wait <= 0 {
		t.Fatal("precondition failed: the account is not locked out")
	}

	if err := resetPassword(ctx, s, "root", "new"); err != nil {
		t.Fatalf("resetPassword: %v", err)
	}

	var n int
	_ = s.Read().QueryRow(
		`SELECT count(*) FROM login_attempts WHERE kind='account' AND subject='root'`).Scan(&n)
	if n != 0 {
		t.Errorf("login_attempts rows = %d, want 0 after a reset", n)
	}
}

func TestResetPasswordRejectsUnknownAdmin(t *testing.T) {
	s, _ := newCtlEnv(t)
	if err := resetPassword(context.Background(), s, "ghost", "pw"); err == nil {
		t.Fatal("reset accepted an admin that does not exist")
	}
}

func TestCtlActionsAreAuditedAsCtlActor(t *testing.T) {
	s, _ := newCtlEnv(t)
	ctx := context.Background()
	if err := createAdmin(ctx, s, "root", "pw", "super_admin"); err != nil {
		t.Fatalf("createAdmin: %v", err)
	}
	var actorType, action string
	if err := s.Read().QueryRow(
		`SELECT actor_type, action FROM audit_log ORDER BY id DESC LIMIT 1`,
	).Scan(&actorType, &action); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if actorType != "ctl" || action != "admin.create" {
		t.Errorf("audit = %s/%s, want ctl/admin.create", actorType, action)
	}
}

// The brief shipped no test for backup at all, which is how it went unnoticed
// that VACUUM INTO cannot run inside a transaction. Routing it through
// store.Write failed every single time with "cannot VACUUM from within a
// transaction".
func TestBackupWritesAReadableCopy(t *testing.T) {
	s, dir := newCtlEnv(t)
	ctx := context.Background()
	if err := createAdmin(ctx, s, "root", "pw", "super_admin"); err != nil {
		t.Fatalf("createAdmin: %v", err)
	}

	dest := filepath.Join(dir, "backup.db")
	if err := backup(ctx, s, dest); err != nil {
		t.Fatalf("backup: %v", err)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("backup file is empty")
	}

	// The copy must be a working database carrying the same rows, not merely
	// a file that exists.
	copied, err := sql.Open("sqlite", dest)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer func() { _ = copied.Close() }()

	var username string
	if err := copied.QueryRow(`SELECT username FROM admins`).Scan(&username); err != nil {
		t.Fatalf("read from backup: %v", err)
	}
	if username != "root" {
		t.Errorf("backup contains admin %q, want root", username)
	}
}

func TestBackupRefusesToOverwrite(t *testing.T) {
	s, dir := newCtlEnv(t)
	dest := filepath.Join(dir, "existing.db")
	if err := os.WriteFile(dest, []byte("precious"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if err := backup(context.Background(), s, dest); err == nil {
		t.Fatal("backup overwrote an existing file")
	}
	body, _ := os.ReadFile(dest)
	if string(body) != "precious" {
		t.Error("the existing file was modified")
	}
}

func TestListAdminsRendersRoleAndStatus(t *testing.T) {
	s, _ := newCtlEnv(t)
	ctx := context.Background()
	if err := createAdmin(ctx, s, "root", "pw", "super_admin"); err != nil {
		t.Fatalf("createAdmin: %v", err)
	}
	if err := createAdmin(ctx, s, "seller", "pw", "reseller"); err != nil {
		t.Fatalf("createAdmin: %v", err)
	}

	var buf bytes.Buffer
	if err := listAdmins(ctx, s, &buf); err != nil {
		t.Fatalf("listAdmins: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"USERNAME", "root", "super_admin", "seller", "reseller", "active"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

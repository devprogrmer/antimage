package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/auth"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

func ctlActor() audit.Actor {
	return audit.Actor{Type: audit.ActorCtl, Label: "antimage-ctl"}
}

// seedRoles inserts the four built-in role templates if they are absent, and
// refreshes their permissions if the templates changed between releases.
func seedRoles(ctx context.Context, tx *sql.Tx) error {
	for name, perms := range rbac.BuiltinRoles() {
		encoded, err := json.Marshal(perms)
		if err != nil {
			return fmt.Errorf("encode %s permissions: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO roles (name, is_builtin, permissions) VALUES (?, 1, ?)
			 ON CONFLICT(name) DO UPDATE SET permissions = excluded.permissions`,
			name, string(encoded)); err != nil {
			return fmt.Errorf("seed role %s: %w", name, err)
		}
	}
	return nil
}

// isUniqueViolation matches only SQLite's uniqueness error, so an unrelated
// failure is not reported to the operator as a duplicate name.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "UNIQUE CONSTRAINT FAILED")
}

func createAdmin(ctx context.Context, s *store.Store, username, password, role string) error {
	if _, ok := rbac.BuiltinRoles()[role]; !ok {
		return fmt.Errorf("unknown role %q; choose super_admin, admin, reseller, or readonly", role)
	}
	if strings.TrimSpace(username) == "" || password == "" {
		return errors.New("username and password are required")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	return s.Write(ctx, func(tx *sql.Tx) error {
		if err := seedRoles(ctx, tx); err != nil {
			return err
		}
		var roleID int64
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM roles WHERE name = ?`, role).Scan(&roleID); err != nil {
			return fmt.Errorf("look up role: %w", err)
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO admins (username, password_hash, role_id, created_at) VALUES (?,?,?,?)`,
			username, hash, roleID, time.Now().UTC().Unix())
		if err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("an admin named %q already exists", username)
			}
			return fmt.Errorf("create admin: %w", err)
		}
		adminID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		return audit.InTx(ctx, tx, "", ctlActor(), audit.Record{
			Action: "admin.create", TargetType: "admin",
			TargetID: sql.NullInt64{Int64: adminID, Valid: true},
			After:    map[string]any{"username": username, "role": role},
			Result:   "ok",
		})
	})
}

// resetPassword is the lockout recovery path. It revokes every session for the
// account in the same transaction, so a stolen session cannot outlive the
// password it was created with, and clears the account's failed-login history
// so the operator is not locked out by the very attempts that brought them here.
func resetPassword(ctx context.Context, s *store.Store, username, password string) error {
	if password == "" {
		return errors.New("password is required")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	now := time.Now().UTC().Unix()

	return s.Write(ctx, func(tx *sql.Tx) error {
		var adminID int64
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM admins WHERE username = ? COLLATE NOCASE`, username).Scan(&adminID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("no admin named %q", username)
			}
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE admins SET password_hash = ? WHERE id = ?`, hash, adminID); err != nil {
			return fmt.Errorf("update password: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE sessions SET revoked_at = ? WHERE admin_id = ? AND revoked_at IS NULL`,
			now, adminID); err != nil {
			return fmt.Errorf("revoke sessions: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM login_attempts WHERE kind = 'account' AND subject = ?`,
			strings.ToLower(username)); err != nil {
			return fmt.Errorf("clear lockout: %w", err)
		}
		return audit.InTx(ctx, tx, "", ctlActor(), audit.Record{
			Action: "admin.reset_password", TargetType: "admin",
			TargetID: sql.NullInt64{Int64: adminID, Valid: true}, Result: "ok",
		})
	})
}

func listAdmins(ctx context.Context, s *store.Store, out io.Writer) error {
	rows, err := s.Read().QueryContext(ctx,
		`SELECT a.username, r.name, a.status FROM admins a
		   JOIN roles r ON r.id = a.role_id ORDER BY a.username`)
	if err != nil {
		return fmt.Errorf("list admins: %w", err)
	}
	defer func() { _ = rows.Close() }()

	_, _ = fmt.Fprintf(out, "%-24s %-14s %s\n", "USERNAME", "ROLE", "STATUS")
	for rows.Next() {
		var username, role, status string
		if err := rows.Scan(&username, &role, &status); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "%-24s %-14s %s\n", username, role, status)
	}
	// Without this a mid-iteration failure prints a short list and exits 0,
	// which an operator would read as "these are all the admins".
	return rows.Err()
}

// backup writes a consistent copy while the panel keeps running.
//
// VACUUM INTO must NOT run inside a transaction: SQLite rejects it with
// "cannot VACUUM from within a transaction", so routing this through
// store.Write — which wraps its callback in one — fails every single time. It
// runs on the read handle instead, which is correct in both directions:
// VACUUM INTO does not modify the source, and keeping it off the single write
// connection means a backup cannot block the panel from serving writes.
func backup(ctx context.Context, s *store.Store, destination string) error {
	if destination == "" {
		return errors.New("destination is required")
	}
	if _, err := os.Stat(destination); err == nil {
		return fmt.Errorf("%s already exists; refusing to overwrite", destination)
	}
	if _, err := s.Read().ExecContext(ctx, `VACUUM INTO ?`, destination); err != nil {
		return fmt.Errorf("vacuum into %s: %w", destination, err)
	}
	return nil
}

// setDeleteCap limits how much traffic a customer may have carried before this
// admin is refused permission to delete them.
//
// A reseller is billed on their customers' traffic, and usage rows cascade with
// the subject -- so deleting a heavy user before settlement destroys the debt
// along with the evidence. "none" removes the cap, which is the default every
// admin has.
//
// Lives here rather than behind an HTTP route because admin management is CLI
// only in this panel: there is no create-admin endpoint either, and adding one
// route for the cap alone would put half of admin management on the web and
// half on the terminal.
func setDeleteCap(ctx context.Context, s *store.Store, username, capArg string) (string, error) {
	var capBytes any
	shown := "no cap"
	if capArg != "none" {
		n, err := strconv.ParseInt(capArg, 10, 64)
		if err != nil {
			return "", fmt.Errorf("cap must be a byte count or \"none\": %w", err)
		}
		if n < 0 {
			return "", errors.New("cap cannot be negative; use \"none\" to remove it")
		}
		// 0 is deliberately allowed: it means "may not delete a customer who
		// has used anything at all", which is a real setting for an untrusted
		// reseller.
		capBytes = n
		shown = fmt.Sprintf("%d bytes", n)
	}

	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE admins SET delete_cap_bytes = ? WHERE username = ?`,
			capBytes, username)
		if err != nil {
			return fmt.Errorf("set delete cap: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("no admin named %q", username)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return shown, nil
}

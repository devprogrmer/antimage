package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

// LoadActor resolves an admin id into the permissions and scope allow-lists
// that authorization runs against.
//
// Shared by every entry point. The HTTP layer calls it once per request from a
// session; the Telegram bot calls it once per command from a linked account.
// Both get the same actor, so a reseller's reach through a chat is exactly
// their reach through a browser -- no more, and no less.
//
// Duplicating this per caller is how the two drift: a permission added to the
// HTTP loader and forgotten in the bot's copy would silently grant less, and
// the reverse would silently grant more.
//
// An admin whose status is not 'active' does not load at all, so suspending an
// account cuts off every channel at once rather than only the one that checks.
func LoadActor(ctx context.Context, db *store.Store, adminID int64) (*rbac.Actor, error) {
	var roleName, rawPerms string
	err := db.Read().QueryRowContext(ctx,
		`SELECT r.name, r.permissions
		   FROM admins a JOIN roles r ON r.id = a.role_id
		  WHERE a.id = ? AND a.status = 'active'`, adminID).Scan(&roleName, &rawPerms)
	if err != nil {
		return nil, fmt.Errorf("load admin %d: %w", adminID, err)
	}

	var perms []rbac.Permission
	if err := json.Unmarshal([]byte(rawPerms), &perms); err != nil {
		return nil, fmt.Errorf("decode permissions: %w", err)
	}

	actor := &rbac.Actor{
		AdminID:    adminID,
		RoleName:   roleName,
		IsSuper:    roleName == "super_admin",
		Perms:      make(map[rbac.Permission]struct{}, len(perms)),
		NodeIDs:    map[int64]struct{}{},
		ServiceIDs: map[int64]struct{}{},
	}
	for _, p := range perms {
		actor.Perms[p] = struct{}{}
	}

	rows, err := db.Read().QueryContext(ctx,
		`SELECT scope_type, scope_id FROM admin_scopes WHERE admin_id = ?`, adminID)
	if err != nil {
		return nil, fmt.Errorf("load scopes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var kind string
		var id int64
		if err := rows.Scan(&kind, &id); err != nil {
			return nil, fmt.Errorf("scan scope: %w", err)
		}
		switch kind {
		case "node":
			actor.NodeIDs[id] = struct{}{}
		case "service":
			actor.ServiceIDs[id] = struct{}{}
		}
	}
	return actor, rows.Err()
}

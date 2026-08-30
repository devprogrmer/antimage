package subscriptions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

// Group is a named, reusable selection of protocols.
type Group struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Protocols   []string `json:"protocols"`
	IsPublic    bool     `json:"is_public"`
	CreatedBy   *int64   `json:"created_by"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
}

// ErrGroupNotFound covers both "no such group" and "not yours".
//
// One error for both, like the subject lookup: a distinct "forbidden" would
// confirm the id is real and let one reseller enumerate another's tiers.
var ErrGroupNotFound = errors.New("subscription group not found")

// ownershipSQL is the same rule user_presets and service_templates use:
// public, or created by this admin.
const ownershipSQL = `(is_public = 1 OR created_by = ?)`

const groupColumns = `id, name, description, protocols_json, is_public, created_by, created_at, updated_at`

func scanGroup(row interface{ Scan(...any) error }) (Group, error) {
	var g Group
	var raw string
	if err := row.Scan(&g.ID, &g.Name, &g.Description, &raw,
		&g.IsPublic, &g.CreatedBy, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return Group{}, err
	}
	// A malformed list reads as empty, which means "every protocol". That is
	// the safe direction: an over-broad subscription is a support question, an
	// empty one is an outage.
	if err := json.Unmarshal([]byte(raw), &g.Protocols); err != nil {
		g.Protocols = nil
	}
	return g, nil
}

// ListGroups returns the groups this admin may use.
func ListGroups(ctx context.Context, db *store.Store, actor rbac.Actor) ([]Group, error) {
	rows, err := db.Read().QueryContext(ctx,
		`SELECT `+groupColumns+` FROM subscription_groups
		  WHERE `+ownershipSQL+` ORDER BY name`, actor.AdminID)
	if err != nil {
		return nil, fmt.Errorf("query subscription groups: %w", err)
	}
	defer func() { _ = rows.Close() }()

	groups := []Group{}
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, fmt.Errorf("scan subscription group: %w", err)
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

// GetGroup returns one group, or ErrGroupNotFound if it is missing or not this
// admin's to see.
func GetGroup(ctx context.Context, db *store.Store, actor rbac.Actor, id int64) (Group, error) {
	g, err := scanGroup(db.Read().QueryRowContext(ctx,
		`SELECT `+groupColumns+` FROM subscription_groups
		  WHERE id = ? AND `+ownershipSQL, id, actor.AdminID))
	if errors.Is(err, sql.ErrNoRows) {
		return Group{}, ErrGroupNotFound
	}
	if err != nil {
		return Group{}, fmt.Errorf("query subscription group: %w", err)
	}
	return g, nil
}

// GroupInput is a create or update.
type GroupInput struct {
	Name        string
	Description string
	Protocols   []string
	IsPublic    bool
}

// validate refuses a protocol this panel cannot produce.
//
// A group naming "quic" would silently exclude everything -- nothing matches
// it, so every subscription built from that group comes out empty and nobody
// can see why from the group itself. Refusing at write time is the only place
// the operator is still looking at what they typed.
func (in GroupInput) validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return errors.New("subscription group name is required")
	}
	known := map[string]bool{}
	for _, p := range KnownProtocols() {
		known[p] = true
	}
	for _, p := range in.Protocols {
		if !known[p] {
			return fmt.Errorf("unknown protocol %q; a group naming it would "+
				"match nothing and produce an empty subscription", p)
		}
	}
	return nil
}

func CreateGroup(
	ctx context.Context, db *store.Store, actor rbac.Actor, in GroupInput, now time.Time,
) (Group, error) {
	if err := in.validate(); err != nil {
		return Group{}, err
	}
	raw, err := json.Marshal(orEmpty(in.Protocols))
	if err != nil {
		return Group{}, err
	}

	var id int64
	err = db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO subscription_groups
			   (name, description, protocols_json, is_public, created_by, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			in.Name, in.Description, string(raw), in.IsPublic,
			actor.AdminID, now.Unix(), now.Unix())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return Group{}, fmt.Errorf("insert subscription group: %w", err)
	}
	return GetGroup(ctx, db, actor, id)
}

func UpdateGroup(
	ctx context.Context, db *store.Store, actor rbac.Actor, id int64,
	in GroupInput, now time.Time,
) (Group, error) {
	if err := in.validate(); err != nil {
		return Group{}, err
	}
	raw, err := json.Marshal(orEmpty(in.Protocols))
	if err != nil {
		return Group{}, err
	}

	err = db.Write(ctx, func(tx *sql.Tx) error {
		// Ownership is checked inside the write, not before it: a check that
		// happens in a separate read can be raced by a change of owner.
		var createdBy sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			`SELECT created_by FROM subscription_groups WHERE id = ?`, id).
			Scan(&createdBy); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrGroupNotFound
			}
			return err
		}
		// Reading a public group is allowed; CHANGING one is not, unless it is
		// yours or you are a super admin. Otherwise any reseller could edit the
		// tier every other reseller sells on.
		if !actor.IsSuper && (!createdBy.Valid || createdBy.Int64 != actor.AdminID) {
			return ErrGroupNotFound
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE subscription_groups
			    SET name = ?, description = ?, protocols_json = ?, is_public = ?, updated_at = ?
			  WHERE id = ?`,
			in.Name, in.Description, string(raw), in.IsPublic, now.Unix(), id)
		return err
	})
	if err != nil {
		return Group{}, err
	}
	return GetGroup(ctx, db, actor, id)
}

func DeleteGroup(ctx context.Context, db *store.Store, actor rbac.Actor, id int64) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		var createdBy sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			`SELECT created_by FROM subscription_groups WHERE id = ?`, id).
			Scan(&createdBy); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrGroupNotFound
			}
			return err
		}
		if !actor.IsSuper && (!createdBy.Valid || createdBy.Int64 != actor.AdminID) {
			return ErrGroupNotFound
		}
		// The subjects sold on this group are NOT deleted: the foreign key is
		// ON DELETE SET NULL, so they fall back to receiving everything they
		// are granted. Cascading would turn "remove a tier" into "delete its
		// customers".
		_, err := tx.ExecContext(ctx, `DELETE FROM subscription_groups WHERE id = ?`, id)
		return err
	})
}

// FilterForSubject returns the filter a subject's group implies.
//
// A subject with no group, or one whose group has been deleted, gets NoFilter
// -- everything they are granted. That is the pre-group behaviour and the only
// safe fallback: a missing group must not silently empty a live subscription.
func FilterForSubject(ctx context.Context, db *store.Store, subjectID int64) (Filter, error) {
	var raw sql.NullString
	err := db.Read().QueryRowContext(ctx, `
		SELECT g.protocols_json
		  FROM subjects s
		  LEFT JOIN subscription_groups g ON g.id = s.subscription_group_id
		 WHERE s.id = ?`, subjectID).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return NoFilter(), nil
		}
		return NoFilter(), fmt.Errorf("read subscription group: %w", err)
	}
	if !raw.Valid {
		return NoFilter(), nil
	}
	var protocols []string
	if err := json.Unmarshal([]byte(raw.String), &protocols); err != nil {
		return NoFilter(), nil
	}
	return Filter{Protocols: protocols}, nil
}

// orEmpty keeps a nil slice out of the stored JSON, so the column always holds
// a list and never the four characters "null" -- which unmarshals to nil and
// would work, but makes the column unreadable to anything but Go.
func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

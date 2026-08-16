package rbac

import "errors"

// ErrForbidden is the only authorization error callers see. Handlers must not
// distinguish "no permission" from "out of scope" in responses, because the
// difference discloses the existence of resources.
var ErrForbidden = errors.New("forbidden")

type TargetKind int

const (
	TargetNone TargetKind = iota
	TargetNode
	TargetService
)

type Target struct {
	Kind TargetKind
	ID   int64
}

// Actor is the authenticated admin plus their resolved permissions and scope
// allow-lists. It is built once per request by the auth middleware.
type Actor struct {
	AdminID    int64
	RoleName   string
	IsSuper    bool
	Perms      map[Permission]struct{}
	NodeIDs    map[int64]struct{}
	ServiceIDs map[int64]struct{}
}

// Check answers whether a holds p over t.
//
// Order matters: permission first, then scope. A super admin bypasses scope
// but still needs the permission, so a custom role stripped of a permission
// is honoured even for supers.
func Check(a *Actor, p Permission, t Target) error {
	if a == nil {
		return ErrForbidden
	}
	if _, ok := a.Perms[p]; !ok {
		return ErrForbidden
	}
	if t.Kind == TargetNone || a.IsSuper {
		return nil
	}

	// A non-super actor's allow-list is exhaustive. Empty means nothing,
	// never everything — the inverted default is how panels leak data.
	switch t.Kind {
	case TargetNode:
		if _, ok := a.NodeIDs[t.ID]; ok {
			return nil
		}
	case TargetService:
		if _, ok := a.ServiceIDs[t.ID]; ok {
			return nil
		}
	}
	return ErrForbidden
}

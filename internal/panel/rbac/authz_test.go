package rbac

import (
	"errors"
	"testing"
)

func actor(role string, super bool, perms []Permission, nodes ...int64) *Actor {
	a := &Actor{
		AdminID:    1,
		RoleName:   role,
		IsSuper:    super,
		Perms:      map[Permission]struct{}{},
		NodeIDs:    map[int64]struct{}{},
		ServiceIDs: map[int64]struct{}{},
	}
	for _, p := range perms {
		a.Perms[p] = struct{}{}
	}
	for _, n := range nodes {
		a.NodeIDs[n] = struct{}{}
	}
	return a
}

func TestSuperAdminBypassesScope(t *testing.T) {
	a := actor("super_admin", true, BuiltinRoles()["super_admin"])
	if err := Check(a, PermNodeWrite, Target{Kind: TargetNode, ID: 999}); err != nil {
		t.Errorf("super admin denied on unscoped node: %v", err)
	}
}

func TestMissingPermissionIsDenied(t *testing.T) {
	a := actor("readonly", false, BuiltinRoles()["readonly"], 1)
	err := Check(a, PermNodeWrite, Target{Kind: TargetNode, ID: 1})
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("err = %v, want ErrForbidden", err)
	}
}

func TestPermissionWithoutScopeIsDenied(t *testing.T) {
	// Has node:write, but only for node 1. Node 2 must be refused.
	a := actor("reseller", false, []Permission{PermNodeWrite}, 1)
	if err := Check(a, PermNodeWrite, Target{Kind: TargetNode, ID: 1}); err != nil {
		t.Errorf("in-scope node denied: %v", err)
	}
	if err := Check(a, PermNodeWrite, Target{Kind: TargetNode, ID: 2}); !errors.Is(err, ErrForbidden) {
		t.Errorf("out-of-scope node allowed: err = %v", err)
	}
}

func TestNonSuperWithEmptyScopeIsDeniedNotUnrestricted(t *testing.T) {
	// The dangerous default: an empty allow-list must mean "nothing",
	// never "everything".
	a := actor("reseller", false, []Permission{PermNodeRead})
	if err := Check(a, PermNodeRead, Target{Kind: TargetNode, ID: 1}); !errors.Is(err, ErrForbidden) {
		t.Fatal("empty scope granted access — an unscoped reseller must see nothing")
	}
}

func TestGlobalPermissionsUseTargetNone(t *testing.T) {
	a := actor("admin", false, []Permission{PermSettingsWrite})
	if err := Check(a, PermSettingsWrite, Target{Kind: TargetNone}); err != nil {
		t.Errorf("global permission denied: %v", err)
	}
}

func TestBuiltinRolesHaveExpectedShape(t *testing.T) {
	roles := BuiltinRoles()
	for _, name := range []string{"super_admin", "admin", "reseller", "readonly"} {
		if _, ok := roles[name]; !ok {
			t.Fatalf("built-in role %q missing", name)
		}
	}
	for _, p := range roles["readonly"] {
		if !p.IsRead() {
			t.Errorf("readonly role contains write permission %q", p)
		}
	}
	if len(roles["super_admin"]) != len(AllPermissions()) {
		t.Error("super_admin must hold every permission")
	}
}

func TestNilActorIsDenied(t *testing.T) {
	if err := Check(nil, PermNodeRead, Target{Kind: TargetNone}); !errors.Is(err, ErrForbidden) {
		t.Error("nil actor was not denied")
	}
}

func TestSuperAdminStillNeedsThePermission(t *testing.T) {
	// The super flag bypasses scope only, never the permission check. A
	// super admin assigned a custom role stripped of a permission must
	// still be denied it — otherwise Check's ordering is backwards.
	a := actor("super_admin", true, []Permission{PermNodeRead})

	if err := Check(a, PermNodeWrite, Target{Kind: TargetNone}); !errors.Is(err, ErrForbidden) {
		t.Errorf("super admin without permission was granted a global action: err = %v, want ErrForbidden", err)
	}

	if err := Check(a, PermNodeWrite, Target{Kind: TargetNode, ID: 1}); !errors.Is(err, ErrForbidden) {
		t.Errorf("super admin without permission was granted a scoped action: err = %v, want ErrForbidden", err)
	}
}

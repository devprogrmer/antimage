package rbac

import "testing"

func has(perms []Permission, want Permission) bool {
	for _, p := range perms {
		if p == want {
			return true
		}
	}
	return false
}

// Minting credit is the one operation that creates value out of nothing, so it
// must not travel with ordinary reseller administration.
//
// If reseller:write implied credit:grant, every operator who can rename a
// reseller could also pay themselves an unlimited balance and provision
// unlimited customers. Separating them means stealing credit requires a
// permission that an operator has to be deliberately given.
func TestCreditGrantIsPrivilegeSeparatedFromResellerWrite(t *testing.T) {
	roles := BuiltinRoles()

	admin := roles["admin"]
	if !has(admin, PermResellerWrite) {
		t.Error("admin cannot administer resellers, so the role cannot run the programme")
	}
	if has(admin, PermCreditGrant) {
		t.Error("admin can mint credit; renaming a reseller and paying yourself " +
			"must not be the same privilege level")
	}

	if !has(roles["super_admin"], PermCreditGrant) {
		t.Error("super_admin cannot grant credit, so nobody can")
	}
}

// A reseller is a tenant, not an administrator of tenancy. Holding any
// reseller:* permission would let one tenant enumerate or edit the others.
func TestResellerRoleHoldsNoTenancyPermissions(t *testing.T) {
	reseller := BuiltinRoles()["reseller"]

	for _, forbidden := range []Permission{
		PermResellerRead, PermResellerWrite, PermCreditGrant,
		PermAdminManage, PermRoleManage, PermSettingsWrite,
	} {
		if has(reseller, forbidden) {
			t.Errorf("the reseller role holds %q; a tenant could act on other tenants",
				forbidden)
		}
	}

	// It must still be able to do its actual job.
	for _, required := range []Permission{
		PermSubjectRead, PermSubjectWrite, PermCredReveal,
	} {
		if !has(reseller, required) {
			t.Errorf("the reseller role lacks %q and cannot manage its own customers",
				required)
		}
	}
}

// readonly must not acquire reseller reach through the new permissions.
func TestReadonlyGainsNoResellerReach(t *testing.T) {
	ro := BuiltinRoles()["readonly"]
	for _, forbidden := range []Permission{
		PermResellerRead, PermResellerWrite, PermCreditGrant,
	} {
		if has(ro, forbidden) {
			t.Errorf("readonly holds %q", forbidden)
		}
	}
}

// AllPermissions is what seeds super_admin. A permission missing from it is
// one that super_admin silently cannot exercise, which surfaces as an
// inexplicable 403 for the most privileged account.
func TestNewPermissionsAreRegisteredInAllPermissions(t *testing.T) {
	all := AllPermissions()
	for _, p := range []Permission{
		PermResellerRead, PermResellerWrite, PermCreditGrant,
	} {
		if !has(all, p) {
			t.Errorf("%q is not in AllPermissions, so super_admin will not be granted it", p)
		}
	}
}

// Write permissions must not be classified as reads, or a readonly guard that
// keys off IsRead would let them through.
func TestResellerWritePermissionsAreNotReads(t *testing.T) {
	if PermResellerWrite.IsRead() {
		t.Error("reseller:write classifies as a read")
	}
	if PermCreditGrant.IsRead() {
		t.Error("credit:grant classifies as a read; a read-only guard would admit it")
	}
	if !PermResellerRead.IsRead() {
		t.Error("reseller:read does not classify as a read")
	}
}

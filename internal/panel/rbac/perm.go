// Package rbac defines antimage's permission vocabulary and the single
// authorization chokepoint every handler passes through.
//
// This package is layer one of the two-layer enforcement in spec section 6.3.
// Layer two lives in the store, which filters rows by scope so a forgotten
// Check here still cannot leak another reseller's data.
package rbac

import "strings"

type Permission string

const (
	PermNodeRead     Permission = "node:read"
	PermNodeWrite    Permission = "node:write"
	PermNodeEnroll   Permission = "node:enroll"
	PermServiceRead  Permission = "service:read"
	PermServiceWrite Permission = "service:write"
	// Subjects are the people a node serves (SP2). Read is separate from write
	// so a reseller can see their own users without being able to mint
	// credentials, and credential:reveal is separate again because unsealing a
	// credential is a strictly more sensitive act than listing subjects.
	PermSubjectRead   Permission = "subject:read"
	PermSubjectWrite  Permission = "subject:write"
	PermCredReveal    Permission = "credential:reveal"
	PermAdminManage   Permission = "admin:manage"
	PermRoleManage    Permission = "role:manage"
	PermAuditRead     Permission = "audit:read"
	PermSettingsWrite Permission = "settings:write"
	PermAlertRead     Permission = "alert:read" // SP7: observability alerts

	// Reseller engine. Split three ways on purpose.
	//
	// PermResellerRead and PermResellerWrite govern the tenant record itself:
	// who exists, their ceilings, whether they are enabled. A reseller holds
	// neither -- they manage customers, not tenancy.
	//
	// PermCreditGrant is separate from PermResellerWrite because granting
	// credit is the one operation that creates value out of nothing. Editing a
	// display name and minting 10,000 credits must not be the same permission,
	// or every operator who can rename a reseller can also pay themselves.
	PermResellerRead  Permission = "reseller:read"
	PermResellerWrite Permission = "reseller:write"
	PermCreditGrant   Permission = "credit:grant"
)

// IsRead reports whether the permission grants only reads, which the
// read-only role and its defence-in-depth middleware rely on.
func (p Permission) IsRead() bool {
	return strings.HasSuffix(string(p), ":read")
}

func AllPermissions() []Permission {
	return []Permission{
		PermNodeRead, PermNodeWrite, PermNodeEnroll,
		PermServiceRead, PermServiceWrite,
		PermSubjectRead, PermSubjectWrite, PermCredReveal,
		PermAdminManage, PermRoleManage,
		PermAuditRead, PermSettingsWrite,
		PermAlertRead,
		PermResellerRead, PermResellerWrite, PermCreditGrant,
	}
}

// BuiltinRoles returns the four role templates. They are templates, not
// hardcoded behaviour: they seed roles.permissions, and a super admin may
// define further roles.
func BuiltinRoles() map[string][]Permission {
	return map[string][]Permission{
		"super_admin": AllPermissions(),
		"admin": {
			PermNodeRead, PermNodeWrite, PermNodeEnroll,
			PermServiceRead, PermServiceWrite,
			PermSubjectRead, PermSubjectWrite, PermCredReveal,
			PermAuditRead, PermAlertRead,
			// An admin may run the reseller programme but may NOT mint credit.
			// PermCreditGrant is deliberately withheld: it is the only
			// operation that creates value from nothing, so it stays with
			// super_admin until an operator explicitly delegates it.
			PermResellerRead, PermResellerWrite,
		},
		// A reseller manages their own users, which is the whole point of the
		// role, and may reveal a credential to hand it to that user.
		//
		// They hold NO reseller:* permission. A reseller reading their own
		// record is not an exercise of reseller:read -- it is the /me route,
		// scoped to themselves. Granting reseller:read here would let one
		// tenant enumerate the others.
		"reseller": {
			PermNodeRead, PermServiceRead, PermServiceWrite,
			PermSubjectRead, PermSubjectWrite, PermCredReveal,
			PermAlertRead,
		},
		"readonly": {
			PermNodeRead, PermServiceRead, PermSubjectRead,
			PermAlertRead,
		},
	}
}

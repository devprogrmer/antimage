package rbac

// Scope is the store-layer projection of an Actor. It carries exactly what
// SQL needs to filter rows and nothing else, so the store never imports
// handler or session types.
type Scope struct {
	AdminID int64
	IsSuper bool
}

func ScopeOf(a *Actor) Scope {
	if a == nil {
		return Scope{}
	}
	return Scope{AdminID: a.AdminID, IsSuper: a.IsSuper}
}

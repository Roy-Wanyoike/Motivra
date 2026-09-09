package jobs

import "github.com/google/uuid"

// ReadScope is the caller's principal-derived visibility filter applied by
// every jobs read query (ADR-0004 tenant isolation and resource-level
// authorization; ARCHITECTURE.md §5 "enforced server-side").
//
// The scope is always built from the validated access-token claims — never
// from any client-supplied body, query or header field. The zero value is
// deliberately inert: it matches no rows, so a caller that forgets to pass
// a scope fails closed instead of leaking data.
//
// The jobs tables carry no tenant_id column (tenancy for fleets is a later
// data-model milestone), so the enforceable principal scope today is
// resource-level: rows a principal owns (customer_id) or is assigned
// (technician_id). Platform-operator roles keep the cross-resource
// visibility their console requires; the list route stays RBAC-gated to
// them (OperatorRoles), giving the two-layer enforcement ADR-0004 mandates.
type ReadScope struct {
	// ViewerID is the authenticated subject (claims.UserID): the customer
	// who owns rows and the technician they are assigned to.
	ViewerID uuid.UUID
	// Operator marks a platform-operator principal (dispatcher, admin or
	// super-admin role) whose read surface spans all jobs and requests.
	Operator bool
}

// OperatorRoles are the roles whose read scope spans the whole platform.
// They mirror the dispatch-console RBAC gate on the jobs list route.
var OperatorRoles = []string{RoleDispatcher, RoleAdmin, RoleSuperAdmin}

// IsOperatorRole reports whether role belongs to the platform-operator set.
func IsOperatorRole(role string) bool {
	for _, r := range OperatorRoles {
		if r == role {
			return true
		}
	}
	return false
}

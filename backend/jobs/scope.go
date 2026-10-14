package jobs

import "github.com/google/uuid"

// ReadScope is the caller's principal-derived visibility filter applied by
// every jobs read query (ADR-0004 tenant isolation and resource-level
// authorization; ARCHITECTURE.md §5 "enforced server-side").
//
// The scope is always built from the validated access-token claims — never
// from any client-supplied body, query or header field. The zero value is
// deliberately inert: it matches no rows, so a caller that forgets to pass
// a scope fails closed instead of leaking data (the vehicles/inspections
// zero-scope "1 = 0" pattern).
//
// Exactly one boundary applies (mirroring the inspections domain):
//
//   - TenantID set: an organizational principal; only that tenant's rows
//     are visible. Isolation derives from the tenant_id columns added by
//     jobs migration 000006 (issue #88) — a tenant-mate's rows are within
//     the organization's boundary by design, and NO row of another tenant
//     ever matches.
//   - TenantID nil and ViewerID set: a personal principal; rows they own
//     (customer_id) or are assigned (technician_id). This remains the
//     enforceable scope for rows created before tenancy landed (their
//     tenant_id is NULL — the documented backfill posture of migration
//     000006) and for tenant-less principals.
//   - Operator: platform-operator roles (dispatcher, admin, super-admin)
//     keep the cross-resource visibility their console requires; the list
//     route stays RBAC-gated to them (OperatorRoles), giving the two-layer
//     enforcement ADR-0004 mandates.
//
// Write paths stamp tenant_id at the column level: creations take the
// actor's tenant claim, and child rows (job_assignments, job_transitions)
// denormalize the parent job's tenant inside the writing transaction.
type ReadScope struct {
	// ViewerID is the authenticated subject (claims.UserID): the customer
	// who owns rows and the technician they are assigned to. Used only
	// when TenantID is nil.
	ViewerID uuid.UUID
	// TenantID is the caller's tenant claim (claims.tenant_id); nil for
	// personal principals. When set it is the sole boundary.
	TenantID *uuid.UUID
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

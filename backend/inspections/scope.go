package inspections

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// RBAC vocabulary for the inspections domain (ADR-0004), mirroring the
// identity role vocabulary. Inspection work is performed by TECHNICIAN and
// GARAGE principals (with dispatch/ops roles coordinating); CUSTOMER reads
// their own inspections and reports through the customer_id linkage.
const (
	RoleCustomer   = "CUSTOMER"
	RoleTechnician = "TECHNICIAN"
	RoleGarage     = "GARAGE"
	RoleDispatcher = "DISPATCHER"
	RoleAdmin      = "ADMIN"
	RoleSuperAdmin = "SUPER_ADMIN"
)

// StaffRoles are the roles allowed to create sessions and perform
// inspection work (start, findings, evidence, submit, report issuance).
// Customers can read their own inspections but never author findings.
var StaffRoles = []string{RoleTechnician, RoleGarage, RoleDispatcher, RoleAdmin, RoleSuperAdmin}

// OperatorRoles are the platform-operator roles whose read scope spans all
// sessions (control-plane roles, ADR-0004).
var OperatorRoles = []string{RoleAdmin, RoleSuperAdmin}

// IsStaffRole reports whether role belongs to the inspection-staff set.
func IsStaffRole(role string) bool {
	for _, r := range StaffRoles {
		if r == role {
			return true
		}
	}
	return false
}

// IsOperatorRole reports whether role belongs to the platform-operator set.
func IsOperatorRole(role string) bool {
	for _, r := range OperatorRoles {
		if r == role {
			return true
		}
	}
	return false
}

// Scope is the data boundary applied to every inspections query. It is
// derived exclusively from the authenticated JWT claims (ADR-0004), never
// from client input, and is enforced inside the repository query.
//
// Exactly one of the following applies:
//
//   - TenantID set: an organizational principal; only that tenant's
//     sessions are visible.
//   - TenantID nil and PrincipalID set: a personal principal; only
//     sessions they inspected (inspector_id) or that concern them as the
//     vehicle's customer (customer_id) are visible.
//   - Explicitly constructed via UnrestrictedScope(): control-plane roles
//     (ADMIN, SUPER_ADMIN) read across tenants under the audit obligations
//     of ADR-0004. The HTTP layer produces it only for those roles (via
//     ScopeFromClaims).
//   - Zero value: matches NOTHING (fail closed, hardening issue #81). A
//     handler that forgets to derive its scope gets empty results rather
//     than a cross-tenant leak — the vehicles zero-scope "1 = 0" pattern.
type Scope struct {
	TenantID    *uuid.UUID
	PrincipalID *uuid.UUID

	explicitUnrestricted bool
}

// UnrestrictedScope is the only way to obtain a scope that exempts queries
// from tenant filtering. Restricting construction to an explicit call keeps
// the zero value fail-closed.
func UnrestrictedScope() Scope {
	return Scope{explicitUnrestricted: true}
}

// Unrestricted reports whether the scope exempts the query from tenant
// filtering (control-plane roles, ADR-0004). Only scopes built by
// UnrestrictedScope qualify; the zero value does not.
func (s Scope) Unrestricted() bool {
	return s.explicitUnrestricted && s.TenantID == nil && s.PrincipalID == nil
}

// scopePredicate renders the SQL condition constraining rows to scope.
// tenantCol, inspectorCol and customerCol name the boundary columns of the
// queried relation, qualified with a join alias when needed. nextParam is
// the placeholder number the condition starts at. It returns the condition
// ("" when the scope is unrestricted and no filtering applies) and the
// parameter values it consumes. The zero value constrains to no rows at
// all.
func scopePredicate(scope Scope, tenantCol, inspectorCol, customerCol string, nextParam int) (string, []any) {
	switch {
	case scope.Unrestricted():
		return "", nil
	case scope.TenantID != nil:
		return fmt.Sprintf("%s = $%d", tenantCol, nextParam), []any{*scope.TenantID}
	case scope.PrincipalID != nil:
		return fmt.Sprintf("(%s = $%d OR %s = $%d)", inspectorCol, nextParam, customerCol, nextParam),
			[]any{*scope.PrincipalID, *scope.PrincipalID}
	default:
		// Zero value (or malformed scope): fail closed.
		return "1 = 0", nil
	}
}

// ScopeFromClaims derives the inspections data boundary from the
// authenticated claims (never from client input, ADR-0004): a tenant claim
// scopes an organizational principal to its organization; a personal
// principal is scoped to the sessions it inspected or that concern it as
// the vehicle's customer; the control-plane roles (ADMIN, SUPER_ADMIN)
// read across tenants under audit obligation. A malformed tenant or
// subject claim is an authentication failure.
func ScopeFromClaims(claims platform.Claims) (Scope, error) {
	if IsOperatorRole(claims.Role) {
		return UnrestrictedScope(), nil
	}
	if claims.TenantID != "" {
		tenantID, err := uuid.Parse(claims.TenantID)
		if err != nil {
			return Scope{}, platform.ErrUnauthorized("authenticated tenant claim is not a tenant identifier")
		}
		return Scope{TenantID: &tenantID}, nil
	}
	principalID, err := uuid.Parse(claims.UserID)
	if err != nil {
		return Scope{}, platform.ErrUnauthorized("authenticated subject is not a user identifier")
	}
	return Scope{PrincipalID: &principalID}, nil
}

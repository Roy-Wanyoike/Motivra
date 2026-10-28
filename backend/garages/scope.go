package garages

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// RBAC vocabulary for the garages domain (ADR-0004), mirroring the identity
// role vocabulary (the payments domain restates the same set). The garage
// console's organizational principal is the GARAGE role; operators keep
// their cross-tenant read obligations.
const (
	RoleGarage     = "GARAGE"
	RoleDispatcher = "DISPATCHER"
	RoleAdmin      = "ADMIN"
	RoleSuperAdmin = "SUPER_ADMIN"
)

// OperatorRoles are the platform-operator roles whose read scope spans
// tenants (ADR-0004 control-plane roles). They mirror backend/jobs.
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

// Scope is the data boundary applied to every garages query. It is derived
// exclusively from the authenticated JWT claims (ADR-0004) — never from any
// client-supplied body, query or header field — and is enforced inside the
// repository query.
//
//   - TenantID set: an organizational principal (a GARAGE token carrying a
//     tenant_id claim); only that tenant's single garage record is visible.
//   - Operator: a platform-operator principal. The garages HTTP surface
//     never grants operators anything (the single-tenant endpoints have no
//     tenant selector and the scope never comes from client input); the
//     operator bit exists so in-process consumers (dispatch scoring, ops
//     oversight) can read capacity through the service API under the
//     ADR-0004 audit obligation without inventing a second tenancy
//     mechanism.
//   - Zero value: matches NOTHING (fail closed). Every store predicate
//     renders "1 = 0" for it, so a handler that forgets to derive a scope
//     leaks nothing — the vehicles/jobs zero-scope pattern.
type Scope struct {
	// TenantID is the caller's tenant claim (claims.tenant_id). When set it
	// is the sole boundary.
	TenantID *uuid.UUID
	// Operator marks a platform-operator principal (dispatcher, admin or
	// super-admin role).
	Operator bool
}

// readScope derives the caller's Scope from the validated access token in
// the request context. ok is false when no usable authenticated principal
// is present (missing claims or a malformed tenant claim) — the caller
// answers 401, failing closed.
func readScope(r *http.Request) (scope Scope, ok bool) {
	claims, present := platform.ClaimsFromContext(r.Context())
	if !present {
		return Scope{}, false
	}
	scope = Scope{Operator: IsOperatorRole(claims.Role)}
	if claims.TenantID != "" {
		tenantID, parseErr := uuid.Parse(claims.TenantID)
		if parseErr != nil {
			return Scope{}, false
		}
		scope.TenantID = &tenantID
	}
	return scope, true
}

// garageScope returns the scope for the endpoints that operate on the
// caller's own tenant's record (the single capacity row of one garage).
// The scope comes only from the validated token: a GARAGE principal must
// carry a tenant_id claim and answers 403 without one (fail closed, issue
// #170), a malformed tenant claim is an authentication failure (401), and
// every other role — including operators, for whom the endpoint has no
// meaningful tenant selector — is refused (403). It writes the error
// response itself and returns ok=false when the request must not proceed.
func garageScope(w http.ResponseWriter, r *http.Request) (Scope, bool) {
	scope, ok := readScope(r)
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return Scope{}, false
	}
	if scope.Operator || scope.TenantID == nil {
		platform.WriteError(w, platform.ErrForbidden(
			"garage endpoints operate on the caller's own tenant; a tenant_id claim is required"))
		return Scope{}, false
	}
	return scope, true
}

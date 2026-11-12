package identity

// Context authz matrix (ADR-0004: "permissions are enumerated per context,
// in each context's authz package: a role-to-permission matrix that is
// data, testable, and rendered into docs"). This file is the identity
// context's technician-verification slice, introduced by issue #166.

// TechnicianVerificationWriteRoles lists the roles allowed to record a
// technician skill-verification decision (approve or reject).
//
// Decision of record for #166: verification REUSES the existing fixed role
// set (ADR-0004 forbids ad-hoc role vocabularies; adding a dedicated
// VERIFIER role would be a schema + contract + token-claim change) and
// grants it to ADMIN and SUPER_ADMIN — the same operator roles that
// already administer accounts (GET /v1/admin/users) and read the audit
// trail. DISPATCHER and SUPPORT are staff but consume verification status,
// they do not grant it; a technician can never verify themselves because
// TECHNICIAN is not in this set (self-grant is impossible by role, and an
// actor whose highest claim is TECHNICIAN is denied before any lookup).
var TechnicianVerificationWriteRoles = []Role{RoleAdmin, RoleSuperAdmin}

// CanVerifyTechnicians reports whether role may record verification
// decisions (approve/reject). The matrix is data: the RBAC table test in
// authz_test.go enumerates every fixed role against it.
func CanVerifyTechnicians(role Role) bool {
	for _, allowed := range TechnicianVerificationWriteRoles {
		if role == allowed {
			return true
		}
	}
	return false
}

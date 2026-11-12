package identity

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTechnicianVerificationRBACMatrix pins the technician-verification
// slice of the identity authz matrix (ADR-0004: the matrix is data,
// enumerated and test-covered). The decision of record for issue #166:
// verification reuses the fixed role set and grants decision rights to
// ADMIN and SUPER_ADMIN only — no dedicated VERIFIER role. Every other
// fixed role is denied, TECHNICIAN included: self-grant is impossible by
// role, and the service additionally denies an actor deciding their own
// status (separation of duties, test-covered in
// TestServiceSelfVerificationDenied).
func TestTechnicianVerificationRBACMatrix(t *testing.T) {
	allRoles := []Role{
		RoleCustomer, RoleTechnician, RoleDispatcher, RoleGarage,
		RoleFleetAdmin, RoleSupport, RoleFinance, RoleAdmin, RoleSuperAdmin,
	}
	cases := []struct {
		role Role
		want bool
	}{
		{RoleCustomer, false},
		{RoleTechnician, false},
		{RoleDispatcher, false},
		{RoleGarage, false},
		{RoleFleetAdmin, false},
		{RoleSupport, false},
		{RoleFinance, false},
		{RoleAdmin, true},
		{RoleSuperAdmin, true},
	}
	require.Len(t, cases, len(allRoles), "the matrix must enumerate every fixed role")

	seen := make(map[Role]bool, len(cases))
	for _, tc := range cases {
		require.False(t, seen[tc.role], "role %s enumerated twice", tc.role)
		seen[tc.role] = true
		require.Equal(t, tc.want, CanVerifyTechnicians(tc.role),
			"role %s verification-write permission mismatch", tc.role)
	}

	// The matrix may only name roles from the fixed nine-role vocabulary.
	for _, role := range TechnicianVerificationWriteRoles {
		require.True(t, seen[role], "matrix names non-vocabulary role %s", role)
	}

	// IsValidVerificationStatus covers exactly the documented lifecycle,
	// which the contract of record publishes (issue #166).
	for _, status := range []string{
		VerificationStatusUnverified, VerificationStatusPending,
		VerificationStatusVerified, VerificationStatusRejected,
	} {
		require.True(t, IsValidVerificationStatus(status), "lifecycle value %s must be valid", status)
	}
	require.False(t, IsValidVerificationStatus("assessment_pending"),
		"the draft technician contract's spelling is not the identity enum")
	require.False(t, IsValidVerificationStatus(""))
	require.False(t, IsValidVerificationStatus("VERIFIED"), "enum values are lowercase")
}

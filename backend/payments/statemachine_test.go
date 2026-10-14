package payments

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// The exhaustive matrix tests mirror the jobs/inspections guard pattern:
// every legal pair is accepted, every illegal pair — including every
// self-transition and every unknown status — is rejected with ErrConflict.

func TestIntentStatusExhaustiveMatrix(t *testing.T) {
	// The documented legal edges.
	legal := map[IntentStatus][]IntentStatus{
		IntentCreated:    {IntentProcessing, IntentCanceled},
		IntentProcessing: {IntentSucceeded, IntentFailed},
	}

	statuses := AllIntentStatuses()
	require.Len(t, statuses, 5)

	for _, from := range statuses {
		for _, to := range statuses {
			err := CanTransitionIntentStatus(from, to)
			if isLegalTarget(legal, from, to) {
				assert.NoError(t, err, "expected %s -> %s to be legal", from, to)
			} else {
				require.Error(t, err, "expected %s -> %s to be illegal", from, to)
				assert.ErrorIs(t, err, ErrConflict)
			}
		}
	}

	// Unknown statuses conflict (they have no map entry at all).
	for _, from := range statuses {
		err := CanTransitionIntentStatus(from, IntentStatus("DESTROYED"))
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrConflict)
	}
	err := CanTransitionIntentStatus(IntentStatus("VOID"), IntentProcessing)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrConflict)

	// Terminal intents are pinned immutable.
	for _, terminal := range []IntentStatus{IntentSucceeded, IntentFailed, IntentCanceled} {
		assert.True(t, IsIntentTerminal(terminal), "%s must be terminal", terminal)
		for _, to := range statuses {
			require.Error(t, CanTransitionIntentStatus(terminal, to),
				"terminal %s must not move to %s", terminal, to)
		}
	}
	assert.False(t, IsIntentTerminal(IntentCreated))
	assert.False(t, IsIntentTerminal(IntentProcessing))
}

func TestTransactionStatusExhaustiveMatrix(t *testing.T) {
	legal := map[TransactionStatus][]TransactionStatus{
		TxPending: {TxSucceeded, TxFailed},
	}

	statuses := AllTransactionStatuses()
	require.Len(t, statuses, 3)

	for _, from := range statuses {
		for _, to := range statuses {
			err := CanTransitionTransactionStatus(from, to)
			if isLegalTarget(legal, from, to) {
				assert.NoError(t, err, "expected %s -> %s to be legal", from, to)
			} else {
				require.Error(t, err, "expected %s -> %s to be illegal", from, to)
				assert.ErrorIs(t, err, ErrConflict)
			}
		}
	}

	assert.True(t, IsTransactionTerminal(TxSucceeded))
	assert.True(t, IsTransactionTerminal(TxFailed))
	assert.False(t, IsTransactionTerminal(TxPending))
	assert.False(t, IsTransactionTerminal(TransactionStatus("REFUNDED")))
}

func TestRefundStatusExhaustiveMatrix(t *testing.T) {
	legal := map[RefundStatus][]RefundStatus{
		RefundRequested: {RefundCompleted, RefundFailed},
	}

	statuses := AllRefundStatuses()
	require.Len(t, statuses, 3)

	for _, from := range statuses {
		for _, to := range statuses {
			err := CanTransitionRefundStatus(from, to)
			if isLegalTarget(legal, from, to) {
				assert.NoError(t, err, "expected %s -> %s to be legal", from, to)
			} else {
				require.Error(t, err, "expected %s -> %s to be illegal", from, to)
				assert.ErrorIs(t, err, ErrConflict)
			}
		}
	}

	assert.True(t, IsRefundTerminal(RefundCompleted))
	assert.True(t, IsRefundTerminal(RefundFailed))
	assert.False(t, IsRefundTerminal(RefundRequested))
}

// isLegalTarget checks the documented map rather than the implementation
// map, so the test pins the contract, not the code.
func isLegalTarget[S ~string](legal map[S][]S, from, to S) bool {
	for _, target := range legal[from] {
		if target == to {
			return true
		}
	}
	return false
}

func TestScopeZeroValueFailsClosed(t *testing.T) {
	pred, args := scopePredicate(Scope{}, "tenant_id", "created_by", "customer_id", 1)
	assert.Equal(t, "1 = 0", pred)
	assert.Empty(t, args)
}

func TestScopePredicates(t *testing.T) {
	tenant := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	principal := uuid.MustParse("00000000-0000-0000-0000-000000000002")

	pred, args := scopePredicate(Scope{TenantID: &tenant}, "tenant_id", "created_by", "customer_id", 3)
	assert.Equal(t, "tenant_id = $3", pred)
	assert.Equal(t, []any{tenant}, args)

	pred, args = scopePredicate(Scope{PrincipalID: &principal}, "tenant_id", "created_by", "customer_id", 1)
	assert.Equal(t, "(created_by = $1 OR customer_id = $1)", pred)
	assert.Equal(t, []any{principal, principal}, args)

	pred, _ = scopePredicate(UnrestrictedScope(), "tenant_id", "created_by", "customer_id", 1)
	assert.Empty(t, pred)
}

func TestScopeUnrestrictedRequiresExplicitConstruction(t *testing.T) {
	assert.True(t, UnrestrictedScope().Unrestricted())
	// A zero value is never unrestricted even if it carries fields.
	assert.False(t, (Scope{TenantID: nil, PrincipalID: nil}).Unrestricted())
}

func TestScopeFromClaims(t *testing.T) {
	tenantID := uuid.New().String()
	userID := uuid.New().String()

	scope, err := ScopeFromClaims(platform.Claims{Role: RoleAdmin, UserID: userID})
	require.NoError(t, err)
	assert.True(t, scope.Unrestricted())

	scope, err = ScopeFromClaims(platform.Claims{Role: RoleGarage, UserID: userID, TenantID: tenantID})
	require.NoError(t, err)
	require.NotNil(t, scope.TenantID)
	assert.Equal(t, tenantID, scope.TenantID.String())

	scope, err = ScopeFromClaims(platform.Claims{Role: RoleCustomer, UserID: userID})
	require.NoError(t, err)
	require.NotNil(t, scope.PrincipalID)
	assert.Equal(t, userID, scope.PrincipalID.String())

	// Malformed claims are authentication failures, not empty scopes.
	_, err = ScopeFromClaims(platform.Claims{Role: RoleGarage, UserID: userID, TenantID: "not-a-uuid"})
	require.Error(t, err)
	_, err = ScopeFromClaims(platform.Claims{Role: RoleCustomer, UserID: "not-a-uuid"})
	require.Error(t, err)
}

func TestRoleSets(t *testing.T) {
	assert.True(t, IsPaymentRole(RoleGarage))
	assert.True(t, IsPaymentRole(RoleDispatcher))
	assert.False(t, IsPaymentRole(RoleTechnician))
	assert.False(t, IsPaymentRole(RoleCustomer))

	assert.True(t, IsOperatorRole(RoleAdmin))
	assert.True(t, IsOperatorRole(RoleSuperAdmin))
	assert.False(t, IsOperatorRole(RoleGarage))
}

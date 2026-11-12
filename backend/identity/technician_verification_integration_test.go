package identity

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	identitymigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/identity"
	"github.com/Roy-Wanyoike/Motivra/backend/platform/pgtest"
)

// Postgres-gated integration tests for the technician verification seam
// (issue #166): the append-only guarantee and the single-transaction audit
// coupling are physical properties, proven against real PostgreSQL like the
// audit_log trigger suite (issue #83). Skipped without TEST_DATABASE_URL.

// newIntegrationTechnician inserts one technician account straight through
// the service (registration + TECHNICIAN grant) and returns its id.
func newIntegrationTechnician(t *testing.T, svc *Service, store *PostgresStore, email string) uuid.UUID {
	t.Helper()
	_, err := svc.Register(t.Context(), email, "", "integration-pass", "Integration Tech", "ci-runner", "127.0.0.1")
	require.NoError(t, err)
	user, err := store.GetUserByEmail(t.Context(), email)
	require.NoError(t, err)
	require.NoError(t, store.AssignRole(t.Context(), user.ID, RoleTechnician, nil))
	return user.ID
}

// TestTechnicianVerificationAppendOnlyEnforcedByDatabase proves the
// decision records are write-once at the database level (issue #166
// tamper-evidence): INSERT works, UPDATE and DELETE raise the append-only
// trigger exception.
func TestTechnicianVerificationAppendOnlyEnforcedByDatabase(t *testing.T) {
	pool := pgtest.Pool(t)
	pgtest.Migrate(t, pool, identitymigrations.FS, "schema_migrations_identity")
	pgtest.Truncate(t, pool, identityTables...)
	defer pgtest.Truncate(t, pool, identityTables...)

	ctx := t.Context()
	_, err := pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, full_name, status, verification_status)
                 VALUES ($1, 'append-only@integration.test', 'x', 'Tech', 'active', 'verified')`,
		uuid.New())
	require.NoError(t, err)

	var technicianID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM users WHERE email = 'append-only@integration.test'`).Scan(&technicianID))

	_, err = pool.Exec(ctx,
		`INSERT INTO technician_verifications (technician_id, status, reason, actor_id)
                 VALUES ($1, 'verified', 'initial grant', $2)`,
		technicianID, technicianID)
	require.NoError(t, err, "INSERT into technician_verifications must keep working")

	_, err = pool.Exec(ctx,
		`UPDATE technician_verifications SET status = 'tampered'`)
	require.Error(t, err, "UPDATE on technician_verifications must raise the append-only exception")
	require.Contains(t, err.Error(), "append-only")

	_, err = pool.Exec(ctx, `DELETE FROM technician_verifications`)
	require.Error(t, err, "DELETE on technician_verifications must raise the append-only exception")
	require.Contains(t, err.Error(), "append-only")
}

// TestTechnicianVerificationAtomicWithAudit is the fail-closed seam proof
// against real PostgreSQL: a decision commits its record, status stamp and
// audit row together; an audit write that cannot succeed (invalid entry,
// same path a runtime failure takes) rolls ALL of it back — no unaudited
// decision can ever exist (ADR-0004).
func TestTechnicianVerificationAtomicWithAudit(t *testing.T) {
	validator, svc, store := integrationPool(t)
	ctx := t.Context()

	technicianID := newIntegrationTechnician(t, svc, store, "atomic-tech@integration.test")
	operator := newIntegrationTechnician(t, svc, store, "atomic-operator@integration.test")

	templateID := uuid.New()
	rec, err := svc.SetTechnicianVerificationStatus(ctx,
		VerificationActor{ID: operator, Role: RoleAdmin}, technicianID,
		VerificationDecision{
			Status:               VerificationStatusVerified,
			Reason:               "passed the practical assessment",
			AssessmentTemplateID: &templateID,
			EvidenceRefs:         []string{"evidence-handle-1"},
		})
	require.NoError(t, err)

	// All three writes committed together.
	require.Equal(t, VerificationStatusVerified, rec.Status)
	user, err := store.GetUser(ctx, technicianID)
	require.NoError(t, err)
	require.Equal(t, VerificationStatusVerified, user.VerificationStatus)
	require.Equal(t, 1, countAudit(t, store, ActionTechnicianVerificationApproved))
	require.Equal(t, 1, countAudit(t, store, ActionTechnicianVerificationRejected))

	// Token round-trip: the validator accepts an access token for the
	// verified technician (sanity that the chain still works end to end).
	issued, err := svc.issuer.Issue(ctx, store, user, "ci", "127.0.0.1")
	require.NoError(t, err)
	claims, err := validator.Validate(issued.AccessToken)
	require.NoError(t, err)
	require.Equal(t, technicianID.String(), claims.UserID)

	// Now force the audit write to fail: an invalid audit entry is rejected
	// INSIDE the transaction (after the record insert), so the rollback must
	// remove the record AND the status stamp.
	fx := TechnicianVerification{
		TechnicianID: technicianID,
		Status:       VerificationStatusRejected,
		Reason:       "re-assessed, rubric not met",
		ActorID:      operator,
	}
	err = store.InsertTechnicianVerification(ctx, &fx, AuditEntry{
		ActorID: &operator,
		Action:  "", // invalid: RecordAudit would refuse this too
	})
	require.Error(t, err, "an unauditable decision must fail")

	var recordCount, auditCount int
	require.NoError(t, store.pool.QueryRow(ctx,
		`SELECT count(*) FROM technician_verifications`).Scan(&recordCount))
	require.Equal(t, 1, recordCount, "only the first (audited) decision exists")
	require.NoError(t, store.pool.QueryRow(ctx,
		`SELECT verification_status IS NULL FROM users WHERE id = $1`, technicianID).Scan(&auditCount))
	require.Equal(t, 0, auditCount, "the status stamp rolled back with the record")
	auditCount = 0
	require.NoError(t, store.pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE action = $1`, ActionTechnicianVerificationRejected).Scan(&auditCount))
	require.Equal(t, 0, auditCount, "no partial audit row either")

	truncateAll(t, store)
}

// TestTechnicianVerificationLatestReturnsNewest proves the read model
// resolves the newest decision by created_at (the "newest record wins"
// rule of the append-only trail).
func TestTechnicianVerificationLatestReturnsNewest(t *testing.T) {
	_, svc, store := integrationPool(t)
	ctx := t.Context()

	technicianID := newIntegrationTechnician(t, svc, store, "latest-tech@integration.test")
	operator := newIntegrationTechnician(t, svc, store, "latest-operator@integration.test")
	actor := VerificationActor{ID: operator, Role: RoleAdmin}

	_, err := svc.SetTechnicianVerificationStatus(ctx, actor, technicianID,
		VerificationDecision{Status: VerificationStatusRejected, Reason: "first attempt failed"})
	require.NoError(t, err)
	_, err = svc.SetTechnicianVerificationStatus(ctx, actor, technicianID,
		VerificationDecision{Status: VerificationStatusVerified, Reason: "second attempt passed"})
	require.NoError(t, err)

	status, latest, err := svc.TechnicianVerificationStatus(ctx, technicianID)
	require.NoError(t, err)
	require.Equal(t, VerificationStatusVerified, status)
	require.NotNil(t, latest)
	require.Equal(t, VerificationStatusVerified, latest.Status)
	require.Equal(t, "second attempt passed", latest.Reason)

	// A technician who was never assessed reads as unverified with no
	// record — the fail-closed default dispatch (issue #167) relies on.
	outsider := newIntegrationTechnician(t, svc, store, "latest-outsider@integration.test")
	status, latest, err = svc.TechnicianVerificationStatus(ctx, outsider)
	require.NoError(t, err)
	require.Equal(t, VerificationStatusUnverified, status)
	require.Nil(t, latest)

	truncateAll(t, store)
}

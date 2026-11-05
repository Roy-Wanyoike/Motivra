package inspections

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	identitymigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/identity"
	inspectionsmigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/inspections"
	"github.com/Roy-Wanyoike/Motivra/backend/platform/pgtest"
)

// verificationIntegrationStore connects to the Postgres instance named by
// TEST_DATABASE_URL, applies the inspections chain AND the identity chain
// (the platform audit_log table the verification record commits with), and
// truncates the touched tables. Tests skip when the variable is unset.
func verificationIntegrationStore(t *testing.T) (*Service, *PostgresStore) {
	t.Helper()
	pool := pgtest.Pool(t)
	pgtest.Migrate(t, pool, inspectionsmigrations.FS, "schema_migrations_inspections")
	pgtest.Migrate(t, pool, identitymigrations.FS, "schema_migrations_identity")
	pgtest.Truncate(t, pool, append([]string{"audit_log"}, inspectionsTables...)...)

	store := NewPostgresStore(pool)
	return NewService(store, nil, nil), store
}

// integrationReportedSession drives a real-store session to REPORT_AVAILABLE
// with integrity intact (all required findings GREEN, one evidence record)
// and returns it with its issued report.
func integrationReportedSession(t *testing.T, svc *Service, inspector uuid.UUID, tenant uuid.UUID) (Session, Report) {
	t.Helper()
	ctx := context.Background()
	sess := &Session{
		TenantID:     &tenant,
		VehicleClass: ClassSedan,
		InspectorID:  inspector,
	}
	require.NoError(t, svc.CreateSession(ctx, sess))
	scope := tenantScope(tenant)

	started, err := svc.Start(ctx, sess.ID, inspector, scope)
	require.NoError(t, err)
	template, err := TemplateByID(started.TemplateID)
	require.NoError(t, err)
	for _, key := range template.RequiredItems() {
		_, err := svc.RecordFinding(ctx, sess.ID, &Finding{
			ItemKey: key, Result: ResultPass, Severity: SeverityGreen, Notes: "within spec",
		}, inspector, scope)
		require.NoError(t, err, "item %s", key)
	}
	_, err = svc.AttachEvidence(ctx, sess.ID, &Evidence{
		Kind: EvidenceKindPhoto, ContentHash: validHash("integration-evidence"),
		CapturedAt: time.Now().UTC().Add(-time.Minute), Uploaded: true,
	}, inspector, scope)
	require.NoError(t, err)
	_, err = svc.Submit(ctx, sess.ID, inspector, scope)
	require.NoError(t, err)
	report, err := svc.IssueReport(ctx, sess.ID, inspector, "", scope)
	require.NoError(t, err)
	return *sess, report
}

// TestReportVerificationIntegration drives a report to REPORT_AVAILABLE and
// proves, against real PostgreSQL, that the human verification trail is
// append-only, atomically audited and status-guarded (issue #185).
func TestReportVerificationIntegration(t *testing.T) {
	svc, store := verificationIntegrationStore(t)
	ctx := context.Background()

	inspector := uuid.New()
	reviewer := uuid.New()
	tenant := uuid.New()
	sess, report := integrationReportedSession(t, svc, inspector, tenant)

	rec, err := svc.RecordVerificationOutcome(ctx, sess.ID, reviewer, VerificationInput{
		Outcome: OutcomeVerified,
		Note:    "integration run",
	}, tenantScope(tenant))
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, rec.ID)
	require.False(t, rec.CreatedAt.IsZero())
	require.True(t, rec.IntegrityVerified)

	// The audit row committed with the record (same transaction): the trail
	// reads back through the scoped store path.
	records, err := svc.VerificationRecords(ctx, sess.ID, tenantScope(tenant))
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, rec.ID, records[0].ID)

	// A correction is a NEW record; the trail is append-only and ordered.
	time.Sleep(5 * time.Millisecond)
	second, err := svc.RecordVerificationOutcome(ctx, sess.ID, reviewer, VerificationInput{
		Outcome: OutcomeRejected,
		Note:    "reversed after review",
	}, tenantScope(tenant))
	require.NoError(t, err)
	records, err = svc.VerificationRecords(ctx, sess.ID, tenantScope(tenant))
	require.NoError(t, err)
	require.Len(t, records, 2)
	assert.Equal(t, rec.ID, records[0].ID, "oldest first")
	assert.Equal(t, second.ID, records[1].ID)

	// Scope enforcement at the SQL level: a stranger reads nothing.
	_, err = svc.VerificationRecords(ctx, sess.ID, staffScope(uuid.New()))
	require.True(t, errors.Is(err, ErrSessionNotFound), "got %v", err)

	// The insert trigger refuses rows for non-REPORT_AVAILABLE sessions.
	// Force the session to SUBMITTED and prove the guard fires at the
	// database level.
	_, err = store.pool.Exec(ctx, `UPDATE inspection_sessions SET status = 'SUBMITTED' WHERE id = $1`, sess.ID)
	require.NoError(t, err)
	_, err = store.pool.Exec(ctx, `
		INSERT INTO inspection_report_verifications
			(report_id, session_id, report_version, outcome, reviewer_id, integrity_verified)
		VALUES ($1, $2, 1, 'REJECTED', $3, false)`,
		report.ID, sess.ID, reviewer)
	require.Error(t, err, "verification inserts must be refused outside REPORT_AVAILABLE")
	require.ErrorContains(t, err, "REPORT_AVAILABLE")

	// Append-only at the SQL level.
	_, err = store.pool.Exec(ctx, `UPDATE inspection_report_verifications SET outcome = 'TAMPERED'`)
	require.Error(t, err, "verification UPDATE must raise the append-only exception")
	require.ErrorContains(t, err, "append-only")
	_, err = store.pool.Exec(ctx, `DELETE FROM inspection_report_verifications`)
	require.Error(t, err, "verification DELETE must raise the append-only exception")
	require.ErrorContains(t, err, "append-only")

	// The (report_id, session_id) composite FK refuses unpaired rows once the
	// status guard allows inserts again.
	_, err = store.pool.Exec(ctx, `UPDATE inspection_sessions SET status = 'REPORT_AVAILABLE' WHERE id = $1`, sess.ID)
	require.NoError(t, err)
	_, err = store.pool.Exec(ctx, `
		INSERT INTO inspection_report_verifications
			(report_id, session_id, report_version, outcome, reviewer_id, integrity_verified)
		VALUES ($1, $2, 1, 'VERIFIED', $3, true)`,
		uuid.New(), sess.ID, reviewer)
	require.Error(t, err, "an unpaired (report, session) insert must violate the composite FK")
}

// TestReportVerificationAuditAtomicityIntegration proves the atomicity
// contract: the record and its audit_log entry commit together — an audit
// failure (here: the audit_log table renamed away) rolls the record back.
func TestReportVerificationAuditAtomicityIntegration(t *testing.T) {
	svc, store := verificationIntegrationStore(t)
	ctx := context.Background()

	inspector := uuid.New()
	reviewer := uuid.New()
	tenant := uuid.New()
	sess, report := integrationReportedSession(t, svc, inspector, tenant)

	// Happy path: both rows exist.
	rec, err := svc.RecordVerificationOutcome(ctx, sess.ID, reviewer, VerificationInput{
		Outcome: OutcomeVerified,
	}, tenantScope(tenant))
	require.NoError(t, err)
	var auditCount int
	require.NoError(t, store.pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE action = $1 AND object_id = $2`,
		AuditActionReportVerified, rec.ReportID).Scan(&auditCount))
	require.Equal(t, 1, auditCount, "the audit row commits with the record")
	assert.Equal(t, report.ID, rec.ReportID)

	// Rename audit_log away: the audit insert fails, and the record insert
	// must roll back with it.
	_, err = store.pool.Exec(ctx, `ALTER TABLE audit_log RENAME TO audit_log_gone`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `ALTER TABLE IF EXISTS audit_log_gone RENAME TO audit_log`)
	})

	_, err = svc.RecordVerificationOutcome(ctx, sess.ID, reviewer, VerificationInput{
		Outcome: OutcomeRejected,
	}, tenantScope(tenant))
	require.Error(t, err, "an audit failure must fail the request")

	var trailCount int
	require.NoError(t, store.pool.QueryRow(ctx,
		`SELECT count(*) FROM inspection_report_verifications WHERE session_id = $1`, sess.ID).Scan(&trailCount))
	require.Equal(t, 1, trailCount, "no unaudited verification record may survive an audit failure")
}

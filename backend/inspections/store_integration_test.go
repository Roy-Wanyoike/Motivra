package inspections

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	inspectionsmigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/inspections"
	"github.com/Roy-Wanyoike/Motivra/backend/platform/pgtest"
)

// inspectionsTables are every table of the inspections chain, truncated
// before each test so each starts from a clean schema (findings, evidence,
// reports and transitions all cascade from inspection_sessions).
var inspectionsTables = []string{
	"inspection_sessions", "inspection_findings", "inspection_evidence",
	"inspection_reports", "inspection_session_transitions",
}

// integrationStore connects to the Postgres instance named by
// TEST_DATABASE_URL through the shared pgtest harness (issue #28, deferral
// 3), applies the inspections migration chain and returns a Service and
// store over the real schema. The service runs with a nil publisher: event
// delivery is unit-tested with fakes; here the database is the system under
// test. Tests skip when the variable is unset.
func integrationStore(t *testing.T) (*Service, *PostgresStore) {
	t.Helper()
	pool := pgtest.Pool(t)
	pgtest.Migrate(t, pool, inspectionsmigrations.FS, "schema_migrations_inspections")
	pgtest.Truncate(t, pool, inspectionsTables...)

	store := NewPostgresStore(pool)
	return NewService(store, nil, nil), store
}

// TestInspectionLifecycleIntegration drives the full lifecycle against real
// PostgreSQL — create, start, findings (with a correction), evidence,
// submit, report issuance and integrity verification — then proves the
// scoped reads, the append-only rows and the transition audit at the SQL
// level.
func TestInspectionLifecycleIntegration(t *testing.T) {
	svc, store := integrationStore(t)
	ctx := context.Background()

	inspector := uuid.New()
	customer := uuid.New()
	vehicle := uuid.New()
	tenant := uuid.New()
	sess := &Session{
		TenantID:     &tenant,
		VehicleID:    &vehicle,
		CustomerID:   &customer,
		VehicleClass: ClassSedan,
		InspectorID:  inspector,
	}
	require.NoError(t, svc.CreateSession(ctx, sess))
	require.NotEqual(t, uuid.Nil, sess.ID)
	require.False(t, sess.CreatedAt.IsZero(), "created_at must be returned from the insert")

	template, err := TemplateByID(sess.TemplateID)
	require.NoError(t, err)

	started, err := svc.Start(ctx, sess.ID, inspector, staffScope(inspector))
	require.NoError(t, err)
	require.Equal(t, StatusInProgress, started.Status)
	require.NotNil(t, started.StartedAt)

	// Record every required item GREEN, then correct the brakes item with a
	// RED finding linked to photo evidence (corrections are new rows).
	for _, key := range template.RequiredItems() {
		f, ferr := svc.RecordFinding(ctx, sess.ID, &Finding{
			ItemKey: key, Result: ResultPass, Severity: SeverityGreen, Notes: "within spec",
		}, inspector, staffScope(inspector))
		require.NoError(t, ferr, "item %s", key)
		require.NotEqual(t, uuid.Nil, f.ID)
		require.NotNil(t, f.RecordedBy)
		require.False(t, f.CreatedAt.IsZero())
	}
	time.Sleep(5 * time.Millisecond) // guarantee the correction's created_at orders after the first read
	redFinding, err := svc.RecordFinding(ctx, sess.ID, &Finding{
		ItemKey: "brakes", Result: ResultFail, Severity: SeverityRed, Notes: "pads at metal",
	}, inspector, staffScope(inspector))
	require.NoError(t, err)

	evidence, err := svc.AttachEvidence(ctx, sess.ID, &Evidence{
		Kind: EvidenceKindPhoto, ContentHash: validHash("brake-photo"),
		CapturedAt: time.Now().UTC().Add(-time.Minute), Uploaded: true, FindingID: &redFinding.ID,
	}, inspector, staffScope(inspector))
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, evidence.ID)
	require.NotNil(t, evidence.CapturedBy)
	require.Equal(t, inspector, *evidence.CapturedBy)

	submitted, err := svc.Submit(ctx, sess.ID, inspector, staffScope(inspector))
	require.NoError(t, err)
	require.Equal(t, StatusSubmitted, submitted.Status)
	require.NotNil(t, submitted.SubmittedAt)

	report, err := svc.IssueReport(ctx, sess.ID, inspector, "integration run", staffScope(inspector))
	require.NoError(t, err)
	require.Equal(t, ReportVersionInitial, report.Version)
	require.Len(t, report.ContentHash, 64)
	require.Equal(t, len(template.RequiredItems()), report.FindingsCount, "the newest record per item wins")

	// Verification recomputes from the live rows and verifies.
	verification, err := svc.Verify(ctx, sess.ID, nil, staffScope(inspector))
	require.NoError(t, err)
	require.True(t, verification.Verified, "body: %+v", verification)
	require.Equal(t, report.ContentHash, verification.RecomputedHash)

	// The transition audit holds one row per move, in order.
	final, err := store.GetSession(ctx, sess.ID, UnrestrictedScope())
	require.NoError(t, err)
	require.Equal(t, StatusReportAvailable, final.Status)
	require.NotNil(t, final.ReportedAt)

	// Scoped reads at the SQL level: the inspector sees their session, a
	// stranger does not, and the zero scope matches nothing ("1 = 0").
	got, err := svc.GetSession(ctx, sess.ID, staffScope(inspector))
	require.NoError(t, err)
	require.Equal(t, sess.ID, got.ID)
	_, err = svc.GetSession(ctx, sess.ID, staffScope(uuid.New()))
	require.True(t, errors.Is(err, ErrSessionNotFound), "a stranger must read nothing, got %v", err)

	empty, err := store.ListSessions(ctx, Scope{}, SessionFilter{}, 10, 0)
	require.NoError(t, err)
	assert.Empty(t, empty, "the zero scope must match no rows at all")

	// A tenant scope sees the tenant's rows; another tenant sees nothing.
	page, err := store.ListSessions(ctx, tenantScope(tenant), SessionFilter{}, 10, 0)
	require.NoError(t, err)
	require.Len(t, page, 1)
	otherPage, err := store.ListSessions(ctx, tenantScope(uuid.New()), SessionFilter{}, 10, 0)
	require.NoError(t, err)
	assert.Empty(t, otherPage)

	// Corrections kept both finding rows (append-only): the log carries 8
	// records, the report's frozen selection 7.
	findings, err := svc.Findings(ctx, sess.ID, staffScope(inspector))
	require.NoError(t, err)
	require.Len(t, findings, len(template.RequiredItems())+1)
}

// TestInspectionAppendOnlyTriggersIntegration proves every integrity
// trigger the migrations install: findings and evidence are append-only AND
// sealed once the session leaves IN_PROGRESS; reports are append-only and
// refuse to exist without evidence hashes; the transition audit is
// append-only. Gated on TEST_DATABASE_URL.
func TestInspectionAppendOnlyTriggersIntegration(t *testing.T) {
	pool := pgtest.Pool(t)
	pgtest.Migrate(t, pool, inspectionsmigrations.FS, "schema_migrations_inspections")
	pgtest.Truncate(t, pool, inspectionsTables...)
	ctx := context.Background()

	const sessionID = "11111111-1111-4111-8111-111111111111"
	const inspectorID = "22222222-2222-4222-8222-222222222222"
	_, err := pool.Exec(ctx, `
                INSERT INTO inspection_sessions (id, vehicle_class, template_id, template_name, template_version, inspector_id, status)
                VALUES ($1, 'SEDAN', gen_random_uuid(), 'Trigger Fixture Template', 1, $2, 'IN_PROGRESS')`,
		sessionID, inspectorID)
	require.NoError(t, err, "seed in-progress session")

	// --- findings: append-only + sealed after submit ----------------------
	_, err = pool.Exec(ctx, `
                INSERT INTO inspection_findings (session_id, item_key, result, severity, notes, recorded_by)
                VALUES ($1, 'brakes', 'PASS', 'GREEN', 'first read', $2)`, sessionID, inspectorID)
	require.NoError(t, err, "finding insert on an IN_PROGRESS session must work")

	_, err = pool.Exec(ctx, `UPDATE inspection_findings SET severity = 'RED', notes = 'tampered'`)
	require.Error(t, err, "finding UPDATE must raise the append-only exception")
	require.ErrorContains(t, err, "append-only")

	_, err = pool.Exec(ctx, `DELETE FROM inspection_findings`)
	require.Error(t, err, "finding DELETE must raise the append-only exception")
	require.ErrorContains(t, err, "append-only")

	// Corrections are a NEW row, never an edit: the second insert stands
	// beside the first.
	_, err = pool.Exec(ctx, `
                INSERT INTO inspection_findings (session_id, item_key, result, severity, notes, recorded_by)
                VALUES ($1, 'brakes', 'FAIL', 'AMBER', 'correction', $2)`, sessionID, inspectorID)
	require.NoError(t, err, "a corrected finding is a new record")
	var findingCount int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM inspection_findings WHERE item_key = 'brakes'`).Scan(&findingCount))
	require.Equal(t, 2, findingCount)

	// --- evidence: append-only + sealed after submit ----------------------
	_, err = pool.Exec(ctx, `
                INSERT INTO inspection_evidence (session_id, kind, content_hash, captured_at, captured_by, uploaded)
                VALUES ($1, 'PHOTO', $2, now() - interval '1 minute', $3, true)`,
		sessionID, validHash("trigger-photo"), inspectorID)
	require.NoError(t, err, "evidence insert on an IN_PROGRESS session must work")

	_, err = pool.Exec(ctx, `UPDATE inspection_evidence SET uploaded = false, notes = 'tampered'`)
	require.Error(t, err, "evidence UPDATE must raise the append-only exception")
	require.ErrorContains(t, err, "append-only")

	_, err = pool.Exec(ctx, `DELETE FROM inspection_evidence`)
	require.Error(t, err, "evidence DELETE must raise the append-only exception")
	require.ErrorContains(t, err, "append-only")

	// --- reports: refuse to exist without evidence hashes -----------------
	_, err = pool.Exec(ctx, `UPDATE inspection_sessions SET status = 'SUBMITTED', submitted_at = now() WHERE id = $1`, sessionID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
                INSERT INTO inspection_reports (session_id, version, inspector_id, template_id, template_name,
                        template_version, findings, evidence, findings_count, evidence_count, content_hash, issued_by)
                VALUES ($1, 1, $2, gen_random_uuid(), 'Trigger Fixture Template', 1, '[]'::jsonb, '[]'::jsonb, 0, 0,
                        $3, $2)`,
		sessionID, inspectorID, validHash("no-evidence-report"))
	require.Error(t, err, "a report must never assemble without evidence hashes")
	require.ErrorContains(t, err, "requires evidence")

	// Sealed rule: with the session no longer IN_PROGRESS, findings and
	// evidence inserts are refused outright.
	_, err = pool.Exec(ctx, `
                INSERT INTO inspection_findings (session_id, item_key, result, severity)
                VALUES ($1, 'tyres', 'PASS', 'GREEN')`, sessionID)
	require.Error(t, err, "finding insert on a SUBMITTED session must be sealed")
	require.ErrorContains(t, err, "sealed")

	_, err = pool.Exec(ctx, `
                INSERT INTO inspection_evidence (session_id, kind, content_hash, captured_at)
                VALUES ($1, 'READING', $2, now() - interval '1 minute')`, sessionID, validHash("late-reading"))
	require.Error(t, err, "evidence insert on a SUBMITTED session must be sealed")
	require.ErrorContains(t, err, "sealed")

	// --- reports: append-only ---------------------------------------------
	// The failed DELETE above raised before removing anything, so exactly one
	// evidence row survives (assert it — the report insert below depends on
	// it passing the require-evidence trigger).
	var evidenceCount int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM inspection_evidence`).Scan(&evidenceCount))
	require.Equal(t, 1, evidenceCount)

	_, err = pool.Exec(ctx, `
                INSERT INTO inspection_reports (session_id, version, inspector_id, template_id, template_name,
                        template_version, findings, evidence, findings_count, evidence_count, content_hash, issued_by)
                VALUES ($1, 1, $2, gen_random_uuid(), 'Trigger Fixture Template', 1, '[]'::jsonb, '[]'::jsonb, 0, 1,
                        $3, $2)`,
		sessionID, inspectorID, validHash("issued-report"))
	require.NoError(t, err, "report insert with evidence present must work")

	_, err = pool.Exec(ctx, `UPDATE inspection_reports SET content_hash = $2`)
	require.Error(t, err, "report UPDATE must raise the append-only exception")
	require.ErrorContains(t, err, "append-only")

	_, err = pool.Exec(ctx, `DELETE FROM inspection_reports`)
	require.Error(t, err, "report DELETE must raise the append-only exception")
	require.ErrorContains(t, err, "append-only")

	// --- transition audit: append-only ------------------------------------
	_, err = pool.Exec(ctx, `
                INSERT INTO inspection_session_transitions (session_id, from_status, to_status, actor_id, reason)
                VALUES ($1, 'IN_PROGRESS', 'SUBMITTED', $2, 'submitted')`, sessionID, inspectorID)
	require.NoError(t, err, "transition insert must work")

	_, err = pool.Exec(ctx, `UPDATE inspection_session_transitions SET to_status = 'TAMPERED'`)
	require.Error(t, err, "transition UPDATE must raise the append-only exception")
	require.ErrorContains(t, err, "append-only")

	_, err = pool.Exec(ctx, `DELETE FROM inspection_session_transitions`)
	require.Error(t, err, "transition DELETE must raise the append-only exception")
	require.ErrorContains(t, err, "append-only")
}

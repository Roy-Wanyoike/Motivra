package inspections

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reportedSession drives a fixture session all the way to REPORT_AVAILABLE
// with integrity intact and returns it together with its report.
func reportedSession(t *testing.T, svc *Service, store *fakeStore, inspectorID uuid.UUID, tenantID *uuid.UUID) (Session, Report) {
	t.Helper()
	sess := sessionInStoreFor(t, store, inspectorID, tenantID)
	_ = driveToSubmitted(t, svc, store, sess.ID, inspectorID)
	report, err := svc.IssueReport(context.Background(), sess.ID, inspectorID, "", staffScope(inspectorID))
	require.NoError(t, err)
	return sess, report
}

// --- service: RecordVerificationOutcome ---------------------------------

func TestRecordVerificationOutcomeVerified(t *testing.T) {
	t.Parallel()
	svc, store, pub, _ := newFixture()
	inspector := uuid.New()
	reviewer := uuid.New()
	tenant := uuid.New()
	sess, report := reportedSession(t, svc, store, inspector, &tenant)

	rec, err := svc.RecordVerificationOutcome(context.Background(), sess.ID, reviewer, VerificationInput{
		Outcome: OutcomeVerified,
		Note:    "checked against the vehicle",
	}, tenantScope(tenant))
	require.NoError(t, err)

	assert.NotEqual(t, uuid.Nil, rec.ID)
	assert.Equal(t, report.ID, rec.ReportID)
	assert.Equal(t, sess.ID, rec.SessionID)
	assert.Equal(t, ReportVersionInitial, rec.Version)
	assert.Equal(t, OutcomeVerified, rec.Outcome)
	assert.Equal(t, reviewer, rec.ReviewerID)
	assert.Equal(t, "checked against the vehicle", rec.Note)
	assert.True(t, rec.IntegrityVerified, "an intact report verifies")
	assert.False(t, rec.CreatedAt.IsZero())

	// The audit entry rides the same transaction: full attribution plus the
	// integrity snapshot.
	require.Len(t, store.verificationAudits, 1)
	audit := store.verificationAudits[0]
	assert.Equal(t, reviewer, audit.ActorID)
	assert.Equal(t, &tenant, audit.TenantID)
	assert.Equal(t, AuditActionReportVerified, audit.Action)
	assert.Equal(t, AuditObjectTypeReport, audit.ObjectType)
	assert.Equal(t, report.ID, audit.ObjectID)
	assert.Equal(t, sess.ID.String(), audit.Metadata["session_id"])
	assert.Equal(t, true, audit.Metadata["integrity_verified"])
	assert.Equal(t, report.ContentHash, audit.Metadata["stored_hash"])

	// Exactly one verified event, on the inspections domain.
	events := pub.ofType(t, EventReportVerified)
	require.Len(t, events, 1)
	assert.Equal(t, report.ID.String(), events[0].AggregateID)
	assert.Equal(t, reviewer.String(), events[0].ActorID)
	var payload ReportVerificationPayload
	require.NoError(t, json.Unmarshal(events[0].Payload, &payload))
	assert.Equal(t, string(OutcomeVerified), payload.Outcome)
	assert.True(t, payload.IntegrityVerified)
	assert.Zero(t, pub.ofTypeCount(t, EventReportRejected), "a verified decision publishes no rejected event")
}

func TestRecordVerificationOutcomeRejectedAlwaysAllowed(t *testing.T) {
	t.Parallel()
	svc, store, pub, _ := newFixture()
	inspector := uuid.New()
	tenant := uuid.New()
	reviewer := uuid.New()
	sess, report := reportedSession(t, svc, store, inspector, &tenant)

	// Even with integrity intact a reviewer may reject with a reason.
	rec, err := svc.RecordVerificationOutcome(context.Background(), sess.ID, reviewer, VerificationInput{
		Outcome: OutcomeRejected,
		Note:    "tyre tread below the operator threshold",
	}, tenantScope(tenant))
	require.NoError(t, err)
	assert.Equal(t, OutcomeRejected, rec.Outcome)
	assert.True(t, rec.IntegrityVerified, "the snapshot records what the recomputation answered")
	assert.Equal(t, report.ID, rec.ReportID)

	// A tampered report (drifted live rows) still records the rejection with
	// integrity_verified=false.
	store.mu.Lock()
	for id, f := range store.findings {
		if f.SessionID == sess.ID {
			f.ItemKey = "ghost-item"
			store.findings[id] = f
			break
		}
	}
	store.mu.Unlock()

	rec2, err := svc.RecordVerificationOutcome(context.Background(), sess.ID, reviewer, VerificationInput{
		Outcome: OutcomeRejected,
		Note:    "integrity drift",
	}, tenantScope(tenant))
	require.NoError(t, err)
	assert.False(t, rec2.IntegrityVerified)

	events := pub.ofType(t, EventReportRejected)
	require.Len(t, events, 2)
	assert.Equal(t, AuditActionReportRejected, store.verificationAudits[len(store.verificationAudits)-1].Action)
}

func TestRecordVerificationOutcomeIntegrityPrecondition(t *testing.T) {
	t.Parallel()
	svc, store, pub, _ := newFixture()
	inspector := uuid.New()
	tenant := uuid.New()
	reviewer := uuid.New()
	sess, _ := reportedSession(t, svc, store, inspector, &tenant)

	// Corrupt one live finding so the recomputation fails.
	store.mu.Lock()
	for id, f := range store.findings {
		if f.SessionID == sess.ID {
			f.ItemKey = "ghost-item"
			store.findings[id] = f
			break
		}
	}
	store.mu.Unlock()

	eventsBefore := pub.count()
	_, err := svc.RecordVerificationOutcome(context.Background(), sess.ID, reviewer, VerificationInput{
		Outcome: OutcomeVerified,
	}, tenantScope(tenant))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrConflict), "got %v", err)
	assert.Contains(t, err.Error(), "hash_match", "the refusal names the failed checks")
	assert.Contains(t, err.Error(), "findings_match")
	assert.Zero(t, len(store.verifications), "a refused VERIFIED records nothing")
	assert.Equal(t, eventsBefore, pub.count(), "a refused VERIFIED publishes nothing")

	// REJECTED remains allowed against the drifted report.
	rec, rerr := svc.RecordVerificationOutcome(context.Background(), sess.ID, reviewer, VerificationInput{
		Outcome: OutcomeRejected,
	}, tenantScope(tenant))
	require.NoError(t, rerr)
	assert.False(t, rec.IntegrityVerified)
}

func TestRecordVerificationOutcomeStatusGuard(t *testing.T) {
	t.Parallel()
	svc, store, pub, _ := newFixture()
	inspector := uuid.New()
	tenant := uuid.New()
	reviewer := uuid.New()
	sess := sessionInStoreFor(t, store, inspector, &tenant)

	// CREATED: nothing to verify yet.
	_, err := svc.RecordVerificationOutcome(context.Background(), sess.ID, reviewer, VerificationInput{
		Outcome: OutcomeVerified,
	}, tenantScope(tenant))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrConflict), "got %v", err)
	assert.Contains(t, err.Error(), string(StatusReportAvailable))

	// SUBMITTED (report not yet issued): same guard.
	_ = driveToSubmitted(t, svc, store, sess.ID, inspector)
	eventsBefore := pub.count()
	_, err = svc.RecordVerificationOutcome(context.Background(), sess.ID, reviewer, VerificationInput{
		Outcome: OutcomeRejected,
	}, tenantScope(tenant))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrConflict), "got %v", err)
	assert.Equal(t, eventsBefore, pub.count())
}

func TestRecordVerificationOutcomeSeparationOfDuties(t *testing.T) {
	t.Parallel()
	svc, store, _, _ := newFixture()
	inspector := uuid.New()
	tenant := uuid.New()
	sess, _ := reportedSession(t, svc, store, inspector, &tenant)

	// The capturing inspector may not verify their own work — the guard is
	// identity-based (defense in depth behind the role matrix).
	_, err := svc.RecordVerificationOutcome(context.Background(), sess.ID, inspector, VerificationInput{
		Outcome: OutcomeVerified,
	}, staffScope(inspector))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSelfVerification), "got %v", err)
}

func TestRecordVerificationOutcomeExpectedVersion(t *testing.T) {
	t.Parallel()
	svc, store, _, _ := newFixture()
	inspector := uuid.New()
	tenant := uuid.New()
	reviewer := uuid.New()
	sess, _ := reportedSession(t, svc, store, inspector, &tenant)

	stale := 99
	_, err := svc.RecordVerificationOutcome(context.Background(), sess.ID, reviewer, VerificationInput{
		Outcome:         OutcomeVerified,
		ExpectedVersion: &stale,
	}, tenantScope(tenant))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrConflict), "got %v", err)
	assert.Contains(t, err.Error(), "99")

	fresh := ReportVersionInitial
	rec, err := svc.RecordVerificationOutcome(context.Background(), sess.ID, reviewer, VerificationInput{
		Outcome:         OutcomeVerified,
		ExpectedVersion: &fresh,
	}, tenantScope(tenant))
	require.NoError(t, err)
	assert.Equal(t, ReportVersionInitial, rec.Version)
}

func TestRecordVerificationOutcomeInputValidation(t *testing.T) {
	t.Parallel()
	svc, store, _, _ := newFixture()
	inspector := uuid.New()
	tenant := uuid.New()
	reviewer := uuid.New()
	sess, _ := reportedSession(t, svc, store, inspector, &tenant)

	cases := []struct {
		name string
		in   VerificationInput
	}{
		{"unknown outcome", VerificationInput{Outcome: "MAYBE"}},
		{"empty outcome", VerificationInput{}},
		{"note too long", VerificationInput{Outcome: OutcomeVerified, Note: strings.Repeat("x", MaxNotesLength+1)}},
		{"bad expected version", VerificationInput{Outcome: OutcomeVerified, ExpectedVersion: &[]int{0}[0]}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := svc.RecordVerificationOutcome(context.Background(), sess.ID, reviewer, tc.in, tenantScope(tenant))
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrInvalidInput), "got %v", err)
		})
	}
	assert.Zero(t, len(store.verifications), "refused inputs record nothing")
}

func TestRecordVerificationOutcomeScope(t *testing.T) {
	t.Parallel()
	svc, store, _, _ := newFixture()
	inspector := uuid.New()
	reviewer := uuid.New()
	tenant := uuid.New()
	sess, _ := reportedSession(t, svc, store, inspector, &tenant)

	// A stranger reads nothing (fail-closed 404 semantics).
	_, err := svc.RecordVerificationOutcome(context.Background(), sess.ID, reviewer, VerificationInput{
		Outcome: OutcomeVerified,
	}, staffScope(uuid.New()))
	require.True(t, errors.Is(err, ErrSessionNotFound), "got %v", err)

	// A tenant-mate reviewer sees and verifies it.
	rec, err := svc.RecordVerificationOutcome(context.Background(), sess.ID, reviewer, VerificationInput{
		Outcome: OutcomeVerified,
	}, tenantScope(tenant))
	require.NoError(t, err)
	assert.Equal(t, reviewer, rec.ReviewerID)

	// The zero scope matches nothing.
	_, err = svc.RecordVerificationOutcome(context.Background(), sess.ID, reviewer, VerificationInput{
		Outcome: OutcomeVerified,
	}, Scope{})
	require.True(t, errors.Is(err, ErrSessionNotFound), "got %v", err)
}

func TestVerificationRecordsAppendOnlyAndOrdered(t *testing.T) {
	t.Parallel()
	svc, store, _, _ := newFixture()
	inspector := uuid.New()
	tenant := uuid.New()
	reviewer := uuid.New()
	sess, _ := reportedSession(t, svc, store, inspector, &tenant)

	first, err := svc.RecordVerificationOutcome(context.Background(), sess.ID, reviewer, VerificationInput{
		Outcome: OutcomeRejected, Note: "first pass",
	}, tenantScope(tenant))
	require.NoError(t, err)
	time.Sleep(2 * time.Millisecond)
	second, err := svc.RecordVerificationOutcome(context.Background(), sess.ID, reviewer, VerificationInput{
		Outcome: OutcomeVerified, Note: "correction",
	}, tenantScope(tenant))
	require.NoError(t, err)

	records, err := svc.VerificationRecords(context.Background(), sess.ID, tenantScope(tenant))
	require.NoError(t, err)
	require.Len(t, records, 2)
	assert.Equal(t, first.ID, records[0].ID, "oldest first")
	assert.Equal(t, second.ID, records[1].ID)

	// Records are readable by the customer's scope too (same read path as
	// the report), and invisible to strangers.
	_, err = svc.VerificationRecords(context.Background(), sess.ID, staffScope(uuid.New()))
	require.True(t, errors.Is(err, ErrSessionNotFound), "got %v", err)
}

// --- HTTP: routes, RBAC matrix, wire shapes ------------------------------

func TestVerificationOutcomeRoleMatrixOverHTTP(t *testing.T) {
	t.Parallel()
	h, store, _, svc := newTestAPI(t)
	inspector := uuid.New()
	tenant := uuid.New()
	sess, _ := reportedSession(t, svc, store, inspector, &tenant)
	path := "/v1/inspections/sessions/" + sess.ID.String() + "/report/verification/outcome"

	// GARAGE reviewers are tenant principals: their tenant-scoped token must
	// reach the fixture session.
	allowedReviewer := uuid.New()
	adminActor := uuid.New()
	superAdminActor := uuid.New()
	cases := []struct {
		name  string
		token string
		actor uuid.UUID
		want  int
	}{
		{"garage tenant reviewer", mintTenantToken(t, allowedReviewer.String(), RoleGarage, tenant.String()), allowedReviewer, http.StatusCreated},
		{"admin operator", mintToken(t, adminActor.String(), RoleAdmin), adminActor, http.StatusCreated},
		{"super admin operator", mintToken(t, superAdminActor.String(), RoleSuperAdmin), superAdminActor, http.StatusCreated},
		{"technician denied", mintToken(t, uuid.New().String(), RoleTechnician), uuid.New(), http.StatusForbidden},
		{"dispatcher denied", mintToken(t, uuid.New().String(), RoleDispatcher), uuid.New(), http.StatusForbidden},
		{"customer denied", mintToken(t, uuid.New().String(), RoleCustomer), uuid.New(), http.StatusForbidden},
		{"garage other tenant 404", mintTenantToken(t, uuid.New().String(), RoleGarage, uuid.New().String()), uuid.New(), http.StatusNotFound},
		{"unauthenticated 401", "", uuid.Nil, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := do(t, h, http.MethodPost, path, tc.token, []byte(`{"outcome":"VERIFIED","note":"reviewed"}`))
			require.Equal(t, tc.want, w.Code, "body: %s", w.Body.String())
			switch tc.want {
			case http.StatusCreated:
				got := decodeBody(t, w)
				assert.Equal(t, "VERIFIED", got["outcome"])
				assert.Equal(t, tc.actor.String(), got["reviewer_id"], "reviewer comes from the token")
				assert.Equal(t, sess.ID.String(), got["session_id"])
				assert.Equal(t, float64(ReportVersionInitial), got["report_version"])
				assert.Equal(t, true, got["integrity_verified"])
			case http.StatusForbidden:
				assert.Equal(t, "forbidden", decodeBody(t, w)["code"])
			case http.StatusNotFound:
				assert.Equal(t, "not_found", decodeBody(t, w)["code"])
			case http.StatusUnauthorized:
				assert.Equal(t, "unauthorized", decodeBody(t, w)["code"])
			}
		})
	}

	// The capturing inspector is refused even in an allowed role
	// (separation of duties over HTTP).
	inspectorToken := mintTenantToken(t, inspector.String(), RoleGarage, tenant.String())
	w := do(t, h, http.MethodPost, path, inspectorToken, []byte(`{"outcome":"VERIFIED"}`))
	require.Equal(t, http.StatusForbidden, w.Code)
}

func TestVerificationOutcomeValidationOverHTTP(t *testing.T) {
	t.Parallel()
	h, store, _, svc := newTestAPI(t)
	inspector := uuid.New()
	tenant := uuid.New()
	sess, _ := reportedSession(t, svc, store, inspector, &tenant)
	token := mintTenantToken(t, uuid.New().String(), RoleGarage, tenant.String())
	path := "/v1/inspections/sessions/" + sess.ID.String() + "/report/verification/outcome"

	cases := []struct {
		name string
		body string
	}{
		{"unknown outcome", `{"outcome":"MAYBE"}`},
		{"malformed json", `{"outcome":`},
		{"note too long", `{"outcome":"VERIFIED","note":"` + strings.Repeat("x", MaxNotesLength+1) + `"}`},
		{"bad expected version", `{"outcome":"VERIFIED","expected_report_version":0}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := do(t, h, http.MethodPost, path, token, []byte(tc.body))
			require.Equal(t, http.StatusUnprocessableEntity, w.Code, "body: %s", w.Body.String())
			assert.Equal(t, "validation_failed", decodeBody(t, w)["code"])
		})
	}

	// Unknown session is 404 (no existence leak).
	w := do(t, h, http.MethodPost, "/v1/inspections/sessions/"+uuid.New().String()+"/report/verification/outcome", token,
		[]byte(`{"outcome":"VERIFIED"}`))
	require.Equal(t, http.StatusNotFound, w.Code)

	// A stale expected_report_version is 409.
	w = do(t, h, http.MethodPost, path, token, []byte(`{"outcome":"VERIFIED","expected_report_version":42}`))
	require.Equal(t, http.StatusConflict, w.Code)

	// The happy path returns the record.
	w = do(t, h, http.MethodPost, path, token, []byte(`{"outcome":"VERIFIED","note":"ok"}`))
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, "VERIFIED", decodeBody(t, w)["outcome"])
}

func TestVerificationRecordsOverHTTP(t *testing.T) {
	t.Parallel()
	h, store, _, svc := newTestAPI(t)
	inspector := uuid.New()
	tenant := uuid.New()
	sess, _ := reportedSession(t, svc, store, inspector, &tenant)
	token := mintTenantToken(t, uuid.New().String(), RoleGarage, tenant.String())
	path := "/v1/inspections/sessions/" + sess.ID.String() + "/report/verification/records"

	// Empty trail before any decision.
	w := do(t, h, http.MethodGet, path, token, nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "[]", strings.TrimSpace(w.Body.String()))

	_, err := svc.RecordVerificationOutcome(context.Background(), sess.ID, uuid.New(), VerificationInput{
		Outcome: OutcomeRejected,
	}, opScope())
	require.NoError(t, err)

	w = do(t, h, http.MethodGet, path, token, nil)
	require.Equal(t, http.StatusOK, w.Code)
	var records []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &records))
	require.Len(t, records, 1)
	assert.Equal(t, "REJECTED", records[0]["outcome"])

	// A stranger reads nothing (404), an operator sees the trail.
	strangerToken := mintTenantToken(t, uuid.New().String(), RoleGarage, uuid.New().String())
	w = do(t, h, http.MethodGet, path, strangerToken, nil)
	require.Equal(t, http.StatusNotFound, w.Code)
	operatorToken, _ := tokenFor(t, RoleAdmin)
	w = do(t, h, http.MethodGet, path, operatorToken, nil)
	require.Equal(t, http.StatusOK, w.Code)

	// Unauthenticated is 401.
	w = do(t, h, http.MethodGet, path, "", nil)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestVerificationEventFailureFailsRequest(t *testing.T) {
	t.Parallel()
	svc, store, pub, _ := newFixture()
	inspector := uuid.New()
	tenant := uuid.New()
	reviewer := uuid.New()
	sess, _ := reportedSession(t, svc, store, inspector, &tenant)
	pub.fail = errors.New("jetstream down")

	_, err := svc.RecordVerificationOutcome(context.Background(), sess.ID, reviewer, VerificationInput{
		Outcome: OutcomeVerified,
	}, tenantScope(tenant))
	require.Error(t, err, "events are part of the domain contract, not best-effort logging")
}

package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// Garage-scoped job list (issue #165): the GARAGE role is admitted to
// GET /v1/jobs with a tenant-bounded ReadScope derived exclusively from
// the token's tenant_id claim — the garage's tenant id (the apps/garage
// console job board, the #124 surface). The two-garage isolation cases
// below are the #124 merge gate: garage A's page never contains garage B's
// rows, the boundary is enforced in the store predicate (jobScopePredicate
// tenant branch), and every failure mode fails closed (403 without a
// tenant claim, 401 unauthenticated or malformed claim). Operators keep
// their unrestricted listing, now with the ADR-0004 privileged-read audit
// record; customers remain outside the list route entirely.

// stubAuditRecorder records ListAuditRecords (and can be told to fail) for
// the service-level and HTTP-level audit assertions.
type stubAuditRecorder struct {
	mu      sync.Mutex
	records []ListAuditRecord
	fail    bool
}

func (s *stubAuditRecorder) RecordJobsListed(_ context.Context, r ListAuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("audit_log unavailable")
	}
	s.records = append(s.records, r)
	return nil
}

func (s *stubAuditRecorder) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

// newAuditedTestAPI is newTestAPI with a jobs.AuditRecorder wired,
// mirroring the cmd/jobs and cmd/gateway production wiring.
func newAuditedTestAPI(t *testing.T) (http.Handler, *fakeStore, *capturePublisher, *Service, *stubAuditRecorder) {
	t.Helper()
	store := newFakeStore()
	pub := &capturePublisher{}
	audit := &stubAuditRecorder{}
	svc := NewServiceWithAudit(store, pub, audit)
	validator := platform.NewJWTValidator(httpTestSecret, "motivra-identity", "motivra")
	router := chi.NewRouter()
	router.Use(platform.AuthMiddleware(validator))
	Routes(router, svc, platform.RequireAuthenticated)
	return router, store, pub, svc, audit
}

// decodeList decodes a JSON array response body (the jobs-list wire form).
func decodeList(t *testing.T, w *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rows), "body: %s", w.Body.String())
	return rows
}

// TestGarageListsOnlyOwnGarageJobs proves the issue #165 admission and the
// #124 merge gate: two garages, each listing only its own tenant's rows —
// the boundary is the token's tenant_id claim, never client input.
func TestGarageListsOnlyOwnGarageJobs(t *testing.T) {
	t.Parallel()
	h, _, _, _ := newTestAPI(t)

	tenantA, tenantB := uuid.New(), uuid.New()
	custTokenA := mintTenantToken(t, uuid.New().String(), RoleCustomer, tenantA.String())
	custTokenB := mintTenantToken(t, uuid.New().String(), RoleCustomer, tenantB.String())

	// Two jobs in garage A's tenant, one in garage B's.
	reqA1 := createRequestOverHTTP(t, h, custTokenA)
	jobA1 := createJobOverHTTP(t, h, custTokenA, reqA1["id"].(string))["id"].(string)
	reqA2 := createRequestOverHTTP(t, h, custTokenA)
	jobA2 := createJobOverHTTP(t, h, custTokenA, reqA2["id"].(string))["id"].(string)
	reqB := createRequestOverHTTP(t, h, custTokenB)
	jobB := createJobOverHTTP(t, h, custTokenB, reqB["id"].(string))["id"].(string)

	garageA := mintTenantToken(t, uuid.New().String(), RoleGarage, tenantA.String())
	garageB := mintTenantToken(t, uuid.New().String(), RoleGarage, tenantB.String())

	// Garage A lists exactly its own two jobs, never garage B's row.
	w := do(t, h, http.MethodGet, "/v1/jobs", garageA, nil)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	rows := decodeList(t, w)
	require.Len(t, rows, 2)
	ids := map[string]bool{}
	for _, row := range rows {
		ids[row["id"].(string)] = true
	}
	assert.True(t, ids[jobA1] && ids[jobA2], "garage A must see its own jobs")
	assert.False(t, ids[jobB], "garage A must never see garage B's jobs")

	// Garage B symmetrically sees only its own row.
	w = do(t, h, http.MethodGet, "/v1/jobs", garageB, nil)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	rows = decodeList(t, w)
	require.Len(t, rows, 1)
	assert.Equal(t, jobB, rows[0]["id"].(string))

	// A tenant-mate of garage A (different subject, same tenant claim) sees
	// the same organization's page: the boundary is the tenant.
	garageA2 := mintTenantToken(t, uuid.New().String(), RoleGarage, tenantA.String())
	w = do(t, h, http.MethodGet, "/v1/jobs", garageA2, nil)
	require.Equal(t, http.StatusOK, w.Code)
	rows = decodeList(t, w)
	require.Len(t, rows, 2, "a garage tenant-mate shares the organization's boundary")

	// Cross-garage single-resource reads keep the documented 404 (no
	// existence leak); the list route answers the foreign rows with absence.
	w = do(t, h, http.MethodGet, "/v1/jobs/"+jobB, garageA, nil)
	require.Equal(t, http.StatusNotFound, w.Code, "cross-garage get must be not-found")
	assert.Equal(t, "not_found", decodeBody(t, w)["code"])
	w = do(t, h, http.MethodGet, "/v1/jobs/"+jobA1, garageB, nil)
	require.Equal(t, http.StatusNotFound, w.Code)

	// The status filter composes INSIDE the tenant boundary: garage A's
	// narrow can only ever select garage A's rows.
	w = do(t, h, http.MethodGet, "/v1/jobs?status=CREATED", garageA, nil)
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, decodeList(t, w), 2)
}

// TestGarageFiltersStayTenantBounded drives a garage listing through the
// same filter matrix operators get and asserts the page narrows within the
// garage's own tenant only.
func TestGarageFiltersStayTenantBounded(t *testing.T) {
	t.Parallel()
	h, _, _, svc := newTestAPI(t)

	tenantA, tenantB := uuid.New(), uuid.New()
	custTokenA := mintTenantToken(t, uuid.New().String(), RoleCustomer, tenantA.String())
	custTokenB := mintTenantToken(t, uuid.New().String(), RoleCustomer, tenantB.String())

	jobA := createJobOverHTTP(t, h, custTokenA, createRequestOverHTTP(t, h, custTokenA)["id"].(string))["id"].(string)
	jobB := createJobOverHTTP(t, h, custTokenB, createRequestOverHTTP(t, h, custTokenB)["id"].(string))["id"].(string)

	// Move garage A's job through dispatch and assign a technician
	// (operator flow, the legal CREATED -> TRIAGING -> DISPATCHING path).
	ctx := context.Background()
	jobAID := mustUUID(t, jobA)
	require.NoError(t, svc.Transition(ctx, jobAID, StatusTriaging, nil, "triage", opScope()))
	require.NoError(t, svc.Transition(ctx, jobAID, StatusDispatching, nil, "dispatch", opScope()))
	tech := uuid.New()
	require.NoError(t, svc.AssignTechnician(ctx, jobAID, tech, nil, opScope()))

	garageA := mintTenantToken(t, uuid.New().String(), RoleGarage, tenantA.String())
	garageB := mintTenantToken(t, uuid.New().String(), RoleGarage, tenantB.String())

	// Status narrow: garage A sees its own ASSIGNED job.
	w := do(t, h, http.MethodGet, "/v1/jobs?status=ASSIGNED", garageA, nil)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	rows := decodeList(t, w)
	require.Len(t, rows, 1)
	assert.Equal(t, jobA, rows[0]["id"].(string))

	// Technician narrow: garage A's page narrows to its own assignment.
	w = do(t, h, http.MethodGet, "/v1/jobs?technician_id="+tech.String(), garageA, nil)
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, decodeList(t, w), 1)

	// Garage B's technician narrow yields nothing: its tenant holds no
	// assignment for that technician — a foreign row never matches.
	w = do(t, h, http.MethodGet, "/v1/jobs?technician_id="+tech.String(), garageB, nil)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	rows = decodeList(t, w)
	require.NotNil(t, rows)
	assert.Empty(t, rows, "garage B must not see garage A's assignment through the filter")

	// The unfiltered pages still disagree: each garage sees only its own.
	w = do(t, h, http.MethodGet, "/v1/jobs", garageB, nil)
	require.Equal(t, http.StatusOK, w.Code)
	rows = decodeList(t, w)
	require.Len(t, rows, 1)
	assert.Equal(t, jobB, rows[0]["id"].(string))
}

// TestGarageWithoutTenantClaimIsForbidden proves the fail-closed posture:
// a GARAGE token without a tenant_id claim is refused with 403 (the
// garages-domain convention, issue #170) — it can never fall through to
// the personal customer/technician scope and read rows that way.
func TestGarageWithoutTenantClaimIsForbidden(t *testing.T) {
	t.Parallel()
	h, store, _, _ := newTestAPI(t)

	// Seed a personal (tenant-less) job: exactly what a tenant-less GARAGE
	// scope would match if the scope ever fell through to the personal
	// customer/technician branch.
	jobInStore(t, store, StatusCreated)

	garageless := mintToken(t, uuid.New().String(), RoleGarage)
	w := do(t, h, http.MethodGet, "/v1/jobs", garageless, nil)
	require.Equal(t, http.StatusForbidden, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, "forbidden", decodeBody(t, w)["code"])

	// The failure is not a scopeless read: the same token stays unable to
	// list anything (no rows returned, nothing leaked), with or without
	// pagination parameters.
	w = do(t, h, http.MethodGet, "/v1/jobs?limit=200", garageless, nil)
	require.Equal(t, http.StatusForbidden, w.Code)

	// Customers keep the operator-only list route (unchanged behavior).
	customer, _ := tokenFor(t, RoleCustomer)
	w = do(t, h, http.MethodGet, "/v1/jobs", customer, nil)
	require.Equal(t, http.StatusForbidden, w.Code, "customers keep the operator-only list route")
}

// TestGarageListAuthFailuresClosed covers the authentication failure modes
// of the garage-scoped list: no token is 401 and a malformed tenant claim
// is an authentication failure, never a scopeless or platform-wide read.
func TestGarageListAuthFailuresClosed(t *testing.T) {
	t.Parallel()
	h, _, _, _ := newTestAPI(t)

	// Unauthenticated probes are rejected before scoping applies.
	w := do(t, h, http.MethodGet, "/v1/jobs", "", nil)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "unauthorized", decodeBody(t, w)["code"])

	// A malformed tenant claim fails closed as 401 (the readScope posture).
	bad := mintTenantToken(t, uuid.New().String(), RoleGarage, "not-a-uuid")
	w = do(t, h, http.MethodGet, "/v1/jobs", bad, nil)
	require.Equal(t, http.StatusUnauthorized, w.Code, "malformed tenant claims fail closed")
	assert.Equal(t, "unauthorized", decodeBody(t, w)["code"])
}

// TestStaffListingAudited proves the ADR-0004 privileged-read audit of the
// operator board (issue #165: "staff keep unrestricted listing with
// audit"): every operator page appends one record with the acting
// principal and the page shape; GARAGE and customer traffic records
// nothing; and a failed audit record fails the request so the read signal
// can never be lost silently.
func TestStaffListingAudited(t *testing.T) {
	t.Parallel()
	h, _, _, _, audit := newAuditedTestAPI(t)

	tenantA := uuid.New()
	custTokenA := mintTenantToken(t, uuid.New().String(), RoleCustomer, tenantA.String())
	createJobOverHTTP(t, h, custTokenA, createRequestOverHTTP(t, h, custTokenA)["id"].(string))

	dispatcher, dispatcherID := tokenFor(t, RoleDispatcher)
	customer, _ := tokenFor(t, RoleCustomer)
	garageA := mintTenantToken(t, uuid.New().String(), RoleGarage, tenantA.String())

	// The operator page is audited: one record, attributed to the
	// dispatcher, carrying the exact page shape.
	w := do(t, h, http.MethodGet, "/v1/jobs?status=CREATED&limit=25", dispatcher, nil)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	rows := decodeList(t, w)
	require.Len(t, rows, 1)
	require.Equal(t, 1, audit.count(), "an operator listing must append one audit record")
	require.Len(t, audit.records, 1)
	record := audit.records[0]
	assert.Equal(t, dispatcherID, record.ActorID, "the record attributes the acting operator")
	assert.Nil(t, record.TenantID, "a tenant-less operator reads the platform-wide board")
	require.NotNil(t, record.Filter.Status)
	assert.Equal(t, StatusCreated, *record.Filter.Status)
	assert.Equal(t, 25, record.Limit)
	assert.Equal(t, 0, record.Offset)
	assert.Equal(t, len(rows), record.Returned)

	// A second operator page appends a second record.
	w = do(t, h, http.MethodGet, "/v1/jobs", dispatcher, nil)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 2, audit.count(), "every operator page is audited")
	assert.Nil(t, audit.records[1].Filter.Status, "the unfiltered board records no status narrow")
	assert.Equal(t, 1, audit.records[1].Returned, "the record carries the returned count")

	// GARAGE traffic is tenant-bounded, not privileged: no audit record.
	w = do(t, h, http.MethodGet, "/v1/jobs", garageA, nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 2, audit.count(), "garage listings are not audited")

	// Customer denial records nothing either.
	w = do(t, h, http.MethodGet, "/v1/jobs", customer, nil)
	require.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, 2, audit.count(), "denied requests append nothing")

	// A failed audit record fails the request (ADR-0004: the read signal
	// can never be lost silently) — the listing answers 500, not 200.
	audit.mu.Lock()
	audit.fail = true
	audit.mu.Unlock()
	w = do(t, h, http.MethodGet, "/v1/jobs", dispatcher, nil)
	require.Equal(t, http.StatusInternalServerError, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, "internal_error", decodeBody(t, w)["code"])
	audit.mu.Lock()
	audit.fail = false
	audit.mu.Unlock()
}

// TestJobsOperatorAuditAtServiceLayer covers the service seam directly:
// the record is written per operator call (including the JobsByStatus
// compatibility wrapper), never for tenant or personal scopes, and the
// actor is the scope's viewer.
func TestJobsOperatorAuditAtServiceLayer(t *testing.T) {
	t.Parallel()
	svc, store, _ := newFixture()
	audit := &stubAuditRecorder{}
	svc.audit = audit
	ctx := context.Background()

	job := jobInStore(t, store, StatusCreated)
	tenant := uuid.New()
	seedTenantRequestAndJob(t, svc, uuid.New(), &tenant)

	// Operator page via the compatibility wrapper: audited with the applied
	// status narrow.
	created := StatusCreated
	_, err := svc.JobsByStatus(ctx, created, 10, 3, opScope())
	require.NoError(t, err)
	require.Len(t, audit.records, 1)
	assert.Equal(t, 10, audit.records[0].Limit)
	assert.Equal(t, 3, audit.records[0].Offset)
	require.NotNil(t, audit.records[0].Filter.Status)
	assert.Equal(t, StatusCreated, *audit.records[0].Filter.Status)

	// Tenant scope: a bounded read, not audited.
	_, err = svc.Jobs(ctx, JobFilter{}, 10, 0, tenantScope(tenant))
	require.NoError(t, err)
	assert.Len(t, audit.records, 1, "tenant-bounded pages are not audited")

	// Personal scope: same.
	_, err = svc.Jobs(ctx, JobFilter{}, 10, 0, viewScope(job.CustomerID))
	require.NoError(t, err)
	assert.Len(t, audit.records, 1, "personal pages are not audited")
}

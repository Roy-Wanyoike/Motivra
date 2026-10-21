package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Cross-tenant read isolation (ADR-0004: "Integration tests prove
// cross-tenant denial"). Two principals' fixtures are seeded; acting as
// tenant A, every read path must return zero rows from tenant B — and a
// denial must be indistinguishable from a missing resource (404, no
// existence leak). Isolation derives from the tenant_id columns added by
// jobs migration 000006 (issue #88): an organizational principal (a token
// carrying a tenant claim) is bound to its tenant's rows; personal
// principals keep the resource-level scope (customer ownership
// customer_id, technician assignment technician_id); operators remain the
// documented cross-resource surface; and the zero scope fails closed.

// seedTenantJob drives one principal through the API: create a request,
// convert it to a job, and returns both IDs.
func seedTenantJob(t *testing.T, h http.Handler, token string) (jobID, requestID string) {
	t.Helper()
	req := createRequestOverHTTP(t, h, token)
	job := createJobOverHTTP(t, h, token, req["id"].(string))
	return job["id"].(string), req["id"].(string)
}

func TestCrossTenantJobReadsDenied(t *testing.T) {
	t.Parallel()
	h, _, pub, _ := newTestAPI(t)

	tokenA, _ := tokenFor(t, RoleCustomer)
	tokenB, _ := tokenFor(t, RoleCustomer)
	jobB, requestB := seedTenantJob(t, h, tokenB)

	// Tenant A probes every read path of tenant B's fixtures.
	w := do(t, h, http.MethodGet, "/v1/jobs/"+jobB, tokenA, nil)
	require.Equal(t, http.StatusNotFound, w.Code, "cross-tenant job get must be denied")
	assert.Equal(t, "not_found", decodeBody(t, w)["code"])

	w = do(t, h, http.MethodGet, "/v1/jobs/"+jobB+"/transitions", tokenA, nil)
	require.Equal(t, http.StatusNotFound, w.Code, "cross-tenant transitions read must be denied")

	// Conversion of a foreign request is denied the same way and must not
	// consume it. The publisher already carries the events from seeding
	// tenant B's fixtures, so the assertion is that the denied call adds
	// none.
	eventsBefore := pub.count()
	w = do(t, h, http.MethodPost, "/v1/requests/"+requestB+"/job", tokenA, nil)
	require.Equal(t, http.StatusNotFound, w.Code, "cross-tenant conversion must be denied")
	assert.Equal(t, "not_found", decodeBody(t, w)["code"])
	assert.Equal(t, eventsBefore, pub.count(), "denied conversion must publish nothing")

	// The list endpoint is operator-only: no cross-tenant listing for
	// customers at all.
	w = do(t, h, http.MethodGet, "/v1/jobs?status=CREATED", tokenA, nil)
	require.Equal(t, http.StatusForbidden, w.Code)

	// Unauthenticated probes are rejected before scoping applies.
	w = do(t, h, http.MethodGet, "/v1/jobs/"+jobB, "", nil)
	require.Equal(t, http.StatusUnauthorized, w.Code)

	// B still sees their own job: isolation, not breakage.
	w = do(t, h, http.MethodGet, "/v1/jobs/"+jobB, tokenB, nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, jobB, decodeBody(t, w)["id"])
}

func TestCrossTenantStoreReadsDenied(t *testing.T) {
	t.Parallel()
	svc, store, _ := newFixture()
	ctx := context.Background()

	// Seed one job per tenant directly (any customer/technician pairing).
	jobA := jobInStore(t, store, StatusCreated)
	jobB := jobInStore(t, store, StatusCreated)

	// Zero scope fails closed: no viewer, no rows.
	_, err := svc.GetJob(ctx, jobB.ID, ReadScope{})
	require.True(t, errors.Is(err, ErrJobNotFound), "zero scope must match nothing, got %v", err)
	empty, err := listJobsByStatus(store, ctx, StatusCreated, 10, 0, ReadScope{})
	require.NoError(t, err)
	assert.Empty(t, empty, "zero scope must return an empty page")
	assert.Empty(t, mustTransitions(t, store, jobB.ID, ReadScope{}), "zero scope must see no transitions")

	// The owner sees their own job, never the other tenant's.
	_, err = svc.GetJob(ctx, jobB.ID, viewScope(jobB.CustomerID))
	require.NoError(t, err)
	_, err = svc.GetJob(ctx, jobA.ID, viewScope(jobB.CustomerID))
	require.True(t, errors.Is(err, ErrJobNotFound), "customer A must not read customer B's job, got %v", err)

	page, err := svc.JobsByStatus(ctx, StatusCreated, 10, 0, viewScope(jobA.CustomerID))
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, jobA.ID, page[0].ID, "scoped listing must only contain the viewer's jobs")

	// An assigned technician sees their job; another technician does not.
	// Assignment is an operator flow and needs the legal CREATED ->
	// TRIAGING -> DISPATCHING approach first.
	tech := uuid.New()
	driveDispatching(t, svc, jobB.ID)
	require.NoError(t, svc.AssignTechnician(ctx, jobB.ID, tech, nil, opScope()))
	_, err = svc.GetJob(ctx, jobB.ID, viewScope(tech))
	require.NoError(t, err, "the assigned technician must see the job")
	_, err = svc.GetJob(ctx, jobB.ID, viewScope(uuid.New()))
	require.True(t, errors.Is(err, ErrJobNotFound), "an unassigned technician must not see the job, got %v", err)

	// Operators are the documented cross-resource surface.
	for _, role := range OperatorRoles {
		op := ReadScope{ViewerID: uuid.New(), Operator: true}
		_, err := svc.GetJob(ctx, jobB.ID, op)
		require.NoError(t, err, "operator role %s must see the job", role)
	}
}

func TestOperatorSeesAcrossTenantsByDesign(t *testing.T) {
	t.Parallel()
	h, _, _, _ := newTestAPI(t)

	tokenA, _ := tokenFor(t, RoleCustomer)
	tokenB, _ := tokenFor(t, RoleCustomer)
	jobA, _ := seedTenantJob(t, h, tokenA)
	jobB, _ := seedTenantJob(t, h, tokenB)

	dispatcher, _ := tokenFor(t, RoleDispatcher)
	w := do(t, h, http.MethodGet, "/v1/jobs?status=CREATED", dispatcher, nil)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rows))
	ids := map[string]bool{}
	for _, row := range rows {
		ids[row["id"].(string)] = true
	}
	assert.True(t, ids[jobA], "operator listing spans tenant A")
	assert.True(t, ids[jobB], "operator listing spans tenant B")

	w = do(t, h, http.MethodGet, "/v1/jobs/"+jobB, dispatcher, nil)
	require.Equal(t, http.StatusOK, w.Code, "operators read any job by id")

	// The transitions audit trail follows the same operator visibility.
	w = do(t, h, http.MethodGet, "/v1/jobs/"+jobA+"/transitions", dispatcher, nil)
	require.Equal(t, http.StatusOK, w.Code)
}

// mustTransitions reads a job's trail through the store, failing the test
// on transport errors.
func mustTransitions(t *testing.T, store *fakeStore, jobID uuid.UUID, scope ReadScope) []JobTransition {
	t.Helper()
	transitions, err := store.ListTransitions(context.Background(), jobID, scope)
	require.NoError(t, err)
	return transitions
}

// mintTenantToken issues a valid HS256 access token carrying a tenant_id
// claim (an organizational principal). An empty tenant mints a personal
// token; the malformedTenant sentinel produces a non-UUID tenant claim.
func mintTenantToken(t *testing.T, sub, role, tenant string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub":  sub,
		"role": role,
		"iss":  "motivra-identity",
		"aud":  []string{"motivra"},
		"exp":  time.Now().Add(15 * time.Minute).Unix(),
		"iat":  time.Now().Add(-time.Minute).Unix(),
	}
	if tenant != "" {
		claims["tenant_id"] = tenant
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(httpTestSecret))
	require.NoError(t, err)
	return signed
}

// TestTenantScopedReadsDeriveFromTenantID (issue #88 AC: "Jobs tenancy:
// schema + scope tests document/derive isolation from tenant_id") proves
// the ReadScope semantics against the fake store: a tenant claim is the
// sole boundary for organizational principals; personal principals keep
// resource-level scope; NULL-tenant (pre-migration and personal) rows stay
// visible only through the resource scope; the zero scope still fails
// closed.
func TestTenantScopedReadsDeriveFromTenantID(t *testing.T) {
	t.Parallel()
	svc, store, _ := newFixture()
	ctx := context.Background()

	// One job per tenant, each created through the real service so the
	// tenant stamping (request -> job inheritance) is itself exercised.
	tenantA, tenantB := uuid.New(), uuid.New()
	customerA, customerB := uuid.New(), uuid.New()
	jobA := seedTenantRequestAndJob(t, svc, customerA, &tenantA)
	jobB := seedTenantRequestAndJob(t, svc, customerB, &tenantB)
	require.Equal(t, tenantA, *jobA.TenantID, "the job inherits the request's tenant")
	require.Equal(t, tenantB, *jobB.TenantID)

	// A tenant A principal reads tenant A's job and only tenant A's jobs.
	got, err := svc.GetJob(ctx, jobA.ID, tenantScope(tenantA))
	require.NoError(t, err)
	require.Equal(t, jobA.ID, got.ID)

	page, err := svc.JobsByStatus(ctx, StatusCreated, 50, 0, tenantScope(tenantA))
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, jobA.ID, page[0].ID, "a tenant page contains only that tenant's rows")

	// Tenant B cannot read tenant A's job or its trail: cross-tenant denial.
	_, err = svc.GetJob(ctx, jobA.ID, tenantScope(tenantB))
	require.True(t, errors.Is(err, ErrJobNotFound), "cross-tenant get must be not-found, got %v", err)
	assert.Empty(t, mustTransitions(t, store, jobA.ID, tenantScope(tenantB)), "cross-tenant trail must read as empty")

	// A tenant-mate (different subject, same tenant claim) CAN read it:
	// isolation derives from tenant_id, not from the subject id.
	mate := ReadScope{ViewerID: uuid.New(), TenantID: &tenantA}
	_, err = svc.GetJob(ctx, jobA.ID, mate)
	require.NoError(t, err, "a tenant-mate shares the organization's boundary")

	// A NULL-tenant row (pre-migration backfill posture / personal row) is
	// invisible to every tenant scope but remains visible to its customer
	// through the resource scope.
	personal := jobInStore(t, store, StatusCreated)
	_, err = svc.GetJob(ctx, personal.ID, tenantScope(tenantA))
	require.True(t, errors.Is(err, ErrJobNotFound), "tenant scopes never see NULL-tenant rows, got %v", err)
	_, err = svc.GetJob(ctx, personal.ID, viewScope(personal.CustomerID))
	require.NoError(t, err, "personal rows keep the resource-level scope (documented backfill posture)")

	// The zero scope still fails closed with the tenancy columns present.
	_, err = svc.GetJob(ctx, jobA.ID, ReadScope{})
	require.True(t, errors.Is(err, ErrJobNotFound), "zero scope must match nothing, got %v", err)
	empty, err := listJobsByStatus(store, ctx, StatusCreated, 10, 0, ReadScope{})
	require.NoError(t, err)
	assert.Empty(t, empty, "zero scope must return an empty page")
}

// seedTenantRequestAndJob runs one customer (with the given tenant, which
// may be nil for a personal principal) through request -> CREATED job.
func seedTenantRequestAndJob(t *testing.T, svc *Service, customerID uuid.UUID, tenantID *uuid.UUID) Job {
	t.Helper()
	req := &ServiceRequest{
		TenantID:    tenantID,
		CustomerID:  customerID,
		Description: "Fleet vehicle will not start",
	}
	require.NoError(t, svc.CreateRequest(context.Background(), req))
	job, err := svc.CreateJobFromRequest(context.Background(), req.ID, opScope())
	require.NoError(t, err)
	return job
}

// TestTenantClaimScopesHTTPReads proves the HTTP surface: a tenant claim
// in the validated token binds the principal to the tenant's rows (a
// tenant-mate reads the job, another tenant does not, with the identical
// 404 as an unknown job), and a malformed tenant claim fails closed as
// 401 — the scope is derived from claims, never widened by client input.
func TestTenantClaimScopesHTTPReads(t *testing.T) {
	t.Parallel()
	h, _, pub, _ := newTestAPI(t)

	tenantA, tenantB := uuid.New(), uuid.New()
	tokenA1 := mintTenantToken(t, uuid.New().String(), RoleCustomer, tenantA.String())
	tokenA2 := mintTenantToken(t, uuid.New().String(), RoleCustomer, tenantA.String())
	tokenB := mintTenantToken(t, uuid.New().String(), RoleCustomer, tenantB.String())

	req := createRequestOverHTTP(t, h, tokenA1)
	job := createJobOverHTTP(t, h, tokenA1, req["id"].(string))
	jobID := job["id"].(string)
	require.NotEmpty(t, pub.ofType(t, EventJobCreated), "the creation flow publishes its event")

	// The creating principal reads their job.
	w := do(t, h, http.MethodGet, "/v1/jobs/"+jobID, tokenA1, nil)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	// A same-tenant principal reads it too: the boundary is the tenant.
	w = do(t, h, http.MethodGet, "/v1/jobs/"+jobID, tokenA2, nil)
	require.Equal(t, http.StatusOK, w.Code, "a tenant-mate reads the organization's job")

	// Another tenant gets the identical 404 as an unknown job.
	w = do(t, h, http.MethodGet, "/v1/jobs/"+jobID, tokenB, nil)
	require.Equal(t, http.StatusNotFound, w.Code, "cross-tenant reads are denied without an existence leak")
	assert.Equal(t, "not_found", decodeBody(t, w)["code"])

	// A malformed tenant claim is an authentication failure, not a 500 or a
	// scopeless read.
	bad := mintTenantToken(t, uuid.New().String(), RoleCustomer, "not-a-uuid")
	w = do(t, h, http.MethodGet, "/v1/jobs/"+jobID, bad, nil)
	require.Equal(t, http.StatusUnauthorized, w.Code, "malformed tenant claims fail closed")
	w = do(t, h, http.MethodPost, "/v1/requests", bad, []byte(`{"description":"x"}`))
	require.Equal(t, http.StatusUnauthorized, w.Code, "malformed tenant claims cannot create rows")
}

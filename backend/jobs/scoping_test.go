package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Cross-tenant read isolation (ADR-0004: "Integration tests prove
// cross-tenant denial"). Two principals' fixtures are seeded; acting as
// tenant A, every read path must return zero rows from tenant B — and a
// denial must be indistinguishable from a missing resource (404, no
// existence leak). The jobs tables carry no tenant_id column yet, so the
// enforceable principal scope is resource-level: customer ownership
// (customer_id) and technician assignment (technician_id), with operators
// as the documented cross-resource surface.

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
	empty, err := store.ListJobsByStatus(ctx, StatusCreated, 10, 0, ReadScope{})
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

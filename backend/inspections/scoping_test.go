package inspections

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

// Cross-principal read isolation (ADR-0004: "Integration tests prove
// cross-tenant denial"). Sessions are tenant-scoped (tenant_id) or
// principal-scoped (inspector_id / customer_id); every read path must be
// deny-by-default, a denial must be indistinguishable from a missing
// resource, and the zero scope must match nothing at all.

func TestZeroScopeFailsClosedOnEveryRead(t *testing.T) {
	t.Parallel()
	svc, store, _, _ := newFixture()
	ctx := context.Background()

	inspector := uuid.New()
	sess := sessionInStoreFor(t, store, inspector, nil)
	_ = driveToSubmitted(t, svc, store, sess.ID, inspector)
	_, err := svc.IssueReport(ctx, sess.ID, inspector, "", staffScope(inspector))
	require.NoError(t, err)

	// Zero scope matches nothing — every read path, no exceptions.
	_, err = svc.GetSession(ctx, sess.ID, Scope{})
	require.True(t, errors.Is(err, ErrSessionNotFound), "zero scope must not see the session, got %v", err)

	empty, err := svc.Sessions(ctx, Scope{}, SessionFilter{}, 10, 0)
	require.NoError(t, err)
	assert.Empty(t, empty, "zero scope must return an empty page")

	_, err = svc.Findings(ctx, sess.ID, Scope{})
	require.True(t, errors.Is(err, ErrSessionNotFound), "got %v", err)
	_, err = svc.Evidence(ctx, sess.ID, Scope{})
	require.True(t, errors.Is(err, ErrSessionNotFound), "got %v", err)
	// The report path fails closed at the session check first.
	_, err = svc.GetReport(ctx, sess.ID, nil, Scope{})
	require.True(t, errors.Is(err, ErrSessionNotFound), "got %v", err)
	_, err = svc.Verify(ctx, sess.ID, nil, Scope{})
	require.True(t, errors.Is(err, ErrSessionNotFound), "got %v", err)
}

func TestPrincipalIsolationBetweenInspectors(t *testing.T) {
	t.Parallel()
	svc, store, _, _ := newFixture()
	ctx := context.Background()

	inspectorA := uuid.New()
	inspectorB := uuid.New()
	sessA := sessionInStoreFor(t, store, inspectorA, nil)
	sessB := sessionInStoreFor(t, store, inspectorB, nil)
	_ = driveToSubmitted(t, svc, store, sessA.ID, inspectorA)
	_ = driveToSubmitted(t, svc, store, sessB.ID, inspectorB)

	// A sees theirs, never B's.
	got, err := svc.GetSession(ctx, sessA.ID, staffScope(inspectorA))
	require.NoError(t, err)
	assert.Equal(t, sessA.ID, got.ID)
	_, err = svc.GetSession(ctx, sessB.ID, staffScope(inspectorA))
	require.True(t, errors.Is(err, ErrSessionNotFound), "inspector A must not read inspector B's session, got %v", err)

	// The page only contains the viewer's sessions.
	page, err := svc.Sessions(ctx, staffScope(inspectorA), SessionFilter{}, 10, 0)
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, sessA.ID, page[0].ID)

	// Children inherit the session boundary.
	findingsA, err := svc.Findings(ctx, sessA.ID, staffScope(inspectorA))
	require.NoError(t, err)
	assert.NotEmpty(t, findingsA)
	_, err = svc.Findings(ctx, sessB.ID, staffScope(inspectorA))
	require.True(t, errors.Is(err, ErrSessionNotFound), "got %v", err)

	_, err = svc.Evidence(ctx, sessB.ID, staffScope(inspectorA))
	require.True(t, errors.Is(err, ErrSessionNotFound), "got %v", err)
	_, err = svc.GetReport(ctx, sessB.ID, nil, staffScope(inspectorA))
	// The report read fails closed at the session check first, so the
	// sentinel is ErrSessionNotFound — the same shape the zero-scope test
	// pins. Both sentinels map onto 404 over HTTP.
	require.True(t, errors.Is(err, ErrSessionNotFound), "got %v", err)
}

func TestTenantScopeIsolation(t *testing.T) {
	t.Parallel()
	svc, store, _, _ := newFixture()
	ctx := context.Background()

	tenantA := uuid.New()
	tenantB := uuid.New()
	sessA := sessionInStoreFor(t, store, uuid.New(), &tenantA)
	sessB := sessionInStoreFor(t, store, uuid.New(), &tenantB)

	// Tenant A sees its own sessions only — even though the members are
	// different people (inspector ids differ from the tenant principals).
	got, err := svc.GetSession(ctx, sessA.ID, tenantScope(tenantA))
	require.NoError(t, err)
	assert.Equal(t, sessA.ID, got.ID)
	_, err = svc.GetSession(ctx, sessB.ID, tenantScope(tenantA))
	require.True(t, errors.Is(err, ErrSessionNotFound), "tenant A must not read tenant B's session, got %v", err)

	page, err := svc.Sessions(ctx, tenantScope(tenantA), SessionFilter{}, 10, 0)
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, sessA.ID, page[0].ID)
}

func TestCustomerReadsOwnInspectionOnly(t *testing.T) {
	t.Parallel()
	svc, store, _, _ := newFixture()
	ctx := context.Background()

	inspector := uuid.New()
	customer := uuid.New()
	other := uuid.New()
	sess := sessionInStoreFor(t, store, inspector, nil)
	// Stamp the customer linkage (as the HTTP layer would via the body).
	stored := sess
	stored.CustomerID = &customer
	store.mu.Lock()
	store.sessions[sess.ID] = stored
	store.mu.Unlock()

	got, err := svc.GetSession(ctx, sess.ID, staffScope(customer))
	require.NoError(t, err, "the vehicle's customer reads their inspection")
	assert.Equal(t, sess.ID, got.ID)

	_, err = svc.GetSession(ctx, sess.ID, staffScope(other))
	require.True(t, errors.Is(err, ErrSessionNotFound), "an unrelated customer reads nothing, got %v", err)
}

func TestOperatorsReadAcrossTenantsByDesign(t *testing.T) {
	t.Parallel()
	svc, store, _, _ := newFixture()
	ctx := context.Background()

	sessA := sessionInStoreFor(t, store, uuid.New(), &[]uuid.UUID{uuid.New()}[0])
	sessB := sessionInStoreFor(t, store, uuid.New(), &[]uuid.UUID{uuid.New()}[0])

	for _, id := range []uuid.UUID{sessA.ID, sessB.ID} {
		_, err := svc.GetSession(ctx, id, opScope())
		require.NoError(t, err, "operators read any session by id")
	}
	page, err := svc.Sessions(ctx, opScope(), SessionFilter{}, 10, 0)
	require.NoError(t, err)
	assert.Len(t, page, 2, "operator listing spans tenants")
}

// --- HTTP-level isolation --------------------------------------------------

func TestCrossInspectorReadsDeniedOverHTTP(t *testing.T) {
	t.Parallel()
	h, store, pub, svc := newTestAPI(t)

	tokenA, idA := tokenFor(t, RoleTechnician)
	tokenB, _ := tokenFor(t, RoleTechnician)
	sessA := sessionInStoreFor(t, store, idA, nil)
	_ = driveToSubmitted(t, svc, store, sessA.ID, idA)
	_, err := svc.IssueReport(context.Background(), sessA.ID, idA, "", staffScope(idA))
	require.NoError(t, err)
	require.Equal(t, 3, pub.count(), "fixture lifecycle publishes exactly started+submitted+issued")

	// Inspector B probes every read path of inspector A's session.
	for _, path := range []string{
		"/v1/inspections/sessions/" + sessA.ID.String(),
		"/v1/inspections/sessions/" + sessA.ID.String() + "/findings",
		"/v1/inspections/sessions/" + sessA.ID.String() + "/evidence",
		"/v1/inspections/sessions/" + sessA.ID.String() + "/report",
		"/v1/inspections/sessions/" + sessA.ID.String() + "/report/verification",
	} {
		w := do(t, h, http.MethodGet, path, tokenB, nil)
		require.Equal(t, http.StatusNotFound, w.Code, "cross-inspector %s must be denied", path)
		assert.Equal(t, "not_found", decodeBody(t, w)["code"])
	}

	// A still reads their own session: isolation, not breakage.
	w := do(t, h, http.MethodGet, "/v1/inspections/sessions/"+sessA.ID.String(), tokenA, nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, sessA.ID.String(), decodeBody(t, w)["id"])
}

func TestTenantClaimScopesSessionListOverHTTP(t *testing.T) {
	t.Parallel()
	h, store, _, _ := newTestAPI(t)

	tenantA, tenantB := uuid.New(), uuid.New()
	sessA := sessionInStoreFor(t, store, uuid.New(), &tenantA)
	sessB := sessionInStoreFor(t, store, uuid.New(), &tenantB)

	tokenA := mintTenantToken(t, uuid.New().String(), RoleTechnician, tenantA.String())
	w := do(t, h, http.MethodGet, "/v1/inspections/sessions", tokenA, nil)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rows))
	require.Len(t, rows, 1, "a tenant principal sees only its organization's sessions")
	assert.Equal(t, sessA.ID.String(), rows[0]["id"])
	assert.NotEqual(t, sessB.ID.String(), rows[0]["id"])

	// A tenant token for an org with no sessions sees nothing.
	tokenEmpty := mintTenantToken(t, uuid.New().String(), RoleTechnician, uuid.New().String())
	w = do(t, h, http.MethodGet, "/v1/inspections/sessions", tokenEmpty, nil)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rows))
	assert.Empty(t, rows)
}

func TestMalformedTenantClaimIsUnauthorized(t *testing.T) {
	t.Parallel()
	h, _, _, _ := newTestAPI(t)
	token := mintTenantToken(t, uuid.New().String(), RoleTechnician, "not-a-uuid")
	w := do(t, h, http.MethodGet, "/v1/inspections/sessions", token, nil)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "unauthorized", decodeBody(t, w)["code"])
}

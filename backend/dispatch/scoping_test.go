package dispatch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// Cross-tenant read isolation (ADR-0004: "Integration tests prove
// cross-tenant denial").
//
// The dispatch engine is a pure function: it owns no storage, so the
// package contains no SELECT and no query-level scoping is applicable
// (documented N/A, not code-changed). Technician profiles, availability
// and scoring inputs are supplied entirely by the caller — the dispatch
// console — which makes the enforcement surface the RBAC gate plus
// statelessness:
//
//  1. Only platform operators (DISPATCHER, ADMIN, SUPER_ADMIN — the same
//     operator set as the jobs domain) may reach either route, so a
//     customer or technician of any tenant can never use the engine to
//     probe scoring data at all.
//  2. A response is a pure function of the request body: no principal,
//     tenant or previous request can influence it, so one tenant's scores
//     can never carry another tenant's data.
//
// These tests pin both properties through the real platform middleware
// chain, exactly as cmd/dispatch mounts it.

// scopingTestSecret signs test tokens for the real-middleware tests below.
const scopingTestSecret = "dispatch-scoping-test-secret-1234"

// mintToken issues a valid HS256 access token for the given identity,
// mirroring the token shape the identity service issues.
func mintToken(t *testing.T, sub, role string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub":  sub,
		"role": role,
		"iss":  "motivra-identity",
		"aud":  []string{"motivra"},
		"exp":  time.Now().Add(15 * time.Minute).Unix(),
		"iat":  time.Now().Add(-time.Minute).Unix(),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(scopingTestSecret))
	require.NoError(t, err)
	return signed
}

// newScopedTestAPI wires Routes through a platform.Server carrying the
// real auth middleware and the production RequireRole gate, exactly as
// cmd/dispatch boots them.
func newScopedTestAPI() http.Handler {
	validator := platform.NewJWTValidator(scopingTestSecret, "motivra-identity", "motivra")
	cfg := platform.LoadForTest()
	srv := platform.NewServer(cfg, platform.NewLogger(cfg.App), platform.WithMiddleware(platform.AuthMiddleware(validator)))
	Routes(srv.Router, func(h http.HandlerFunc) http.HandlerFunc {
		return platform.RequireRole(DispatcherRoles, h)
	})
	return srv
}

// doBearer sends a request with a Bearer access token.
func doBearer(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// tenantBody builds a score request for one tenant's candidate pair. The
// technician ids are derived deterministically from the tenant label so the
// tests can prove that no foreign id ever appears in another tenant's
// response.
func tenantBody(t *testing.T, tenant string) (body string, techIDs []string) {
	t.Helper()
	cands := make([]Candidate, 2)
	ids := make([]string, 2)
	for i := range cands {
		id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("%s:candidate:%d", tenant, i)))
		ids[i] = id.String()
		cands[i] = Candidate{
			TechnicianID:      id,
			ETAMinutes:        10 + i,
			Skills:            []string{"engine"},
			Available:         true,
			HasEquipment:      true,
			HasRequiredParts:  true,
			AcceptanceRate:    0.9,
			CompletionRate:    0.9,
			AvgCustomerRating: 4.5,
		}
	}
	raw, err := json.Marshal(scoreRequest{
		JobContext: JobContext{RequiredSkills: []string{"engine"}, VehicleMake: "Toyota", RequiredParts: []string{"oil filter"}},
		Candidates: cands,
	})
	require.NoError(t, err)
	return string(raw), ids
}

func TestDispatchReadsAreOperatorOnly(t *testing.T) {
	t.Parallel()
	h := newScopedTestAPI()
	probe, _ := tenantBody(t, "gate")

	// Non-operator roles of any tenant are rejected before any scoring
	// work happens.
	for _, role := range []string{"CUSTOMER", "TECHNICIAN", "GARAGE", "FLEET_ADMIN"} {
		token := mintToken(t, uuid.NewString(), role)
		rec := doBearer(t, h, http.MethodPost, "/v1/dispatch/score", token, probe)
		require.Equal(t, http.StatusForbidden, rec.Code, "role %s must not score", role)
		var problem map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem), "role %s", role)
		assert.Equal(t, "forbidden", problem["code"], "role %s", role)

		rec = doBearer(t, h, http.MethodGet, "/v1/dispatch/factors", token, "")
		require.Equal(t, http.StatusForbidden, rec.Code, "role %s must not read the catalogue", role)
	}

	// Unauthenticated requests are rejected before the role gate.
	rec := doBearer(t, h, http.MethodPost, "/v1/dispatch/score", "", probe)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	rec = doBearer(t, h, http.MethodGet, "/v1/dispatch/factors", "", "")
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	// Every operator role reaches both routes.
	for _, role := range DispatcherRoles {
		token := mintToken(t, uuid.NewString(), role)
		rec := doBearer(t, h, http.MethodPost, "/v1/dispatch/score", token, probe)
		require.Equal(t, http.StatusOK, rec.Code, "role %s must score; body: %s", role, rec.Body.String())

		rec = doBearer(t, h, http.MethodGet, "/v1/dispatch/factors", token, "")
		require.Equal(t, http.StatusOK, rec.Code, "role %s must read the catalogue", role)
	}
}

func TestStatelessScoringLeaksNothingAcrossPrincipals(t *testing.T) {
	t.Parallel()
	h := newScopedTestAPI()

	// Two operators (different subjects, different operator roles) score
	// two disjoint tenants' candidate sets, interleaved.
	bodyA, techsA := tenantBody(t, "tenant-a")
	bodyB, techsB := tenantBody(t, "tenant-b")
	require.NotEqual(t, bodyA, bodyB, "the two tenants' fixtures must differ")

	operatorA := mintToken(t, uuid.NewString(), "DISPATCHER")
	operatorB := mintToken(t, uuid.NewString(), "ADMIN")

	firstA := doBearer(t, h, http.MethodPost, "/v1/dispatch/score", operatorA, bodyA)
	require.Equal(t, http.StatusOK, firstA.Code, "body: %s", firstA.Body.String())
	interloper := doBearer(t, h, http.MethodPost, "/v1/dispatch/score", operatorB, bodyB)
	require.Equal(t, http.StatusOK, interloper.Code, "body: %s", interloper.Body.String())
	secondA := doBearer(t, h, http.MethodPost, "/v1/dispatch/score", operatorA, bodyA)
	require.Equal(t, http.StatusOK, secondA.Code)

	// Same body, same operator, after a foreign tenant scored in between:
	// byte-identical output. The engine holds no per-principal or
	// per-tenant state a previous request could poison.
	require.Equal(t, firstA.Body.String(), secondA.Body.String(),
		"identical input must score identically regardless of interleaving")

	// Neither response carries the other tenant's technician ids.
	for _, id := range techsB {
		assert.NotContains(t, firstA.Body.String(), id, "tenant A's scores must not contain tenant B's technicians")
		assert.NotContains(t, secondA.Body.String(), id, "tenant A's re-score must not contain tenant B's technicians")
	}
	for _, id := range techsA {
		assert.NotContains(t, interloper.Body.String(), id, "tenant B's scores must not contain tenant A's technicians")
	}

	// The same body scored by a different operator principal produces the
	// identical bytes: the authenticated principal cannot reshape another
	// tenant's scoring surface.
	otherA := doBearer(t, h, http.MethodPost, "/v1/dispatch/score", operatorB, bodyA)
	require.Equal(t, http.StatusOK, otherA.Code)
	require.Equal(t, firstA.Body.String(), otherA.Body.String(),
		"the response must be a pure function of the body, independent of the caller")
}

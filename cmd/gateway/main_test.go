package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Roy-Wanyoike/Motivra/backend/dispatch"
	"github.com/Roy-Wanyoike/Motivra/backend/garages"
	"github.com/Roy-Wanyoike/Motivra/backend/identity"
	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// gatewayTestSecret signs the test tokens for the RBAC probes; the same
// HS256 fixture pattern the domain suites use (iss/aud match the identity
// defaults the gateway validator enforces).
const gatewayTestSecret = "gateway-http-test-secret-gateway-1"

// mintGatewayToken issues a valid HS256 access token with the given
// identity claims (sub, role and an optional tenant_id).
func mintGatewayToken(t *testing.T, sub, role, tenantID string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub":  sub,
		"role": role,
		"iss":  "motivra-identity",
		"aud":  []string{"motivra"},
		"exp":  time.Now().Add(15 * time.Minute).Unix(),
		"iat":  time.Now().Add(-time.Minute).Unix(),
	}
	if tenantID != "" {
		claims["tenant_id"] = tenantID
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(gatewayTestSecret))
	require.NoError(t, err)
	return signed
}

// newGatewayUnderTest builds the gateway server through the production
// constructor chain (newGatewayServer: the ADR-0008 boot gate plus the
// shared token-verification middleware) WITHOUT running the wiring loop —
// the exact state the process is in between bind and wire.
func newGatewayUnderTest(t *testing.T) (*platform.Server, *platform.Boot) {
	t.Helper()
	cfg := platform.LoadForTest()
	logger := platform.NewLogger(cfg.App)
	boot := platform.NewBoot(cfg, logger)
	var validator atomic.Pointer[platform.JWTValidator]
	validator.Store(platform.NewJWTValidator(gatewayTestSecret, "motivra-identity", "motivra"))
	return newGatewayServer(cfg, logger, boot, &validator, nil), boot
}

// wireGateway opens the ADR-0008 gate the way the routes boot step does:
// the composed domain router is swapped in atomically and the wiring loop
// (one green step, the routes-shaped shape of gatewayBootSteps' last step)
// flips readiness. Everything after this helper runs against the same
// serveDomainGate path production traffic takes.
func wireGateway(t *testing.T, boot *platform.Boot, routes domainRoutes) {
	t.Helper()
	boot.SetDomainHandler(buildDomainRouter(routes))
	boot.Wire(context.Background(), platform.BootStep{Name: "routes", Run: func(context.Context) error { return nil }})
	require.Eventually(t, boot.Ready, 5*time.Second, 5*time.Millisecond,
		"the wiring loop must flip readiness after the routes step lands")
}

// doGateway performs a request against the handler and returns the recorder.
func doGateway(t *testing.T, h http.Handler, method, path, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func decodeGatewayBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &m), "body: %s", w.Body.String())
	return m
}

// TestHealthzAnswersBeforeDependencies pins the ADR-0008 liveness contract
// for the gateway: /healthz is dependency-free and answers 200 while the
// process is still un-wired (no database, no validator wiring, nothing).
func TestHealthzAnswersBeforeDependencies(t *testing.T) {
	t.Parallel()
	srv, boot := newGatewayUnderTest(t)
	require.False(t, boot.Ready(), "precondition: the boot gate is still closed")

	w := doGateway(t, srv, http.MethodGet, "/healthz", "", nil)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.Equal(t, "ok", decodeGatewayBody(t, w)["status"])
}

// TestReadyzReportsBlockerNamesBeforeWiring pins the readiness contract: a
// latched blocker turns /readyz into 503 problem+json carrying the check
// NAMES only — the full reason (which leaks DSN fragments) never reaches
// the unauthenticated body (hardening #81, ADR-0008).
func TestReadyzReportsBlockerNamesBeforeWiring(t *testing.T) {
	t.Parallel()
	srv, boot := newGatewayUnderTest(t)
	boot.Block("database", "postgres://motivra:hunter2@db.internal:5432/motivra — unreachable")

	w := doGateway(t, srv, http.MethodGet, "/readyz", "", nil)
	require.Equal(t, http.StatusServiceUnavailable, w.Code, "body: %s", w.Body.String())
	require.Equal(t, "application/problem+json", w.Header().Get("Content-Type"))

	body := decodeGatewayBody(t, w)
	assert.Equal(t, "not_ready", body["code"])
	assert.Equal(t, []any{"database"}, body["failed"], "machine-readable blocker NAMES")
	assert.NotContains(t, w.Body.String(), "hunter2", "the full reason must not leak to anonymous callers")
	assert.NotContains(t, w.Body.String(), "db.internal")
	assert.NotContains(t, w.Body.String(), "reasons", "full reasons are operator-only")
}

// TestDomainRoutesGateBeforeWiring pins the degraded-boot domain gate:
// until wiring completes, a /v1 domain path answers 503 problem+json
// (code not_ready), never a route hit into an unwired service.
func TestDomainRoutesGateBeforeWiring(t *testing.T) {
	t.Parallel()
	srv, boot := newGatewayUnderTest(t)
	boot.Block("database", "MOTIVRA_DATABASE_URL is required for the gateway")

	w := doGateway(t, srv, http.MethodGet, "/v1/vehicles", "", nil)
	require.Equal(t, http.StatusServiceUnavailable, w.Code, "body: %s", w.Body.String())
	require.Equal(t, "application/problem+json", w.Header().Get("Content-Type"))
	assert.Equal(t, "not_ready", decodeGatewayBody(t, w)["code"])
}

// TestUnknownV1PrefixProblem404 pins the gateway 404 contract (issue #202):
// an unknown /v1 prefix reaches the gateway's own problem+json 404 once the
// gate is open — the platform's bare-text chi 404 would break the API's
// error contract.
func TestUnknownV1PrefixProblem404(t *testing.T) {
	t.Parallel()
	srv, boot := newGatewayUnderTest(t)
	wireGateway(t, boot, domainRoutes{})

	w := doGateway(t, srv, http.MethodGet, "/v1/definitely-not-a-domain", "", nil)
	require.Equal(t, http.StatusNotFound, w.Code, "body: %s", w.Body.String())
	require.Equal(t, "application/problem+json", w.Header().Get("Content-Type"))
	body := decodeGatewayBody(t, w)
	assert.Equal(t, "not_found", body["code"])
	assert.Contains(t, body["detail"], "/v1/definitely-not-a-domain")
}

// TestMethodNotAllowedProblemJSON pins the gateway 405 contract: a known
// route hit with an unsupported method renders the same problem+json shape
// the platform Server's method gate writes before readiness.
func TestMethodNotAllowedProblemJSON(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	writeDomainMethodNotAllowed(w, httptest.NewRequest(http.MethodDelete, "/v1/vehicles", nil))
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
	require.Equal(t, "application/problem+json", w.Header().Get("Content-Type"))
	assert.Equal(t, "method_not_allowed", decodeGatewayBody(t, w)["code"])
}

// TestAllDomainPrefixesMountedThroughProductionConstructors pins the
// gateway's core promise (issue #202): every domain's REAL router
// constructor — productionMounts, the exact function the routes boot step
// runs — mounts its /v1 prefix on ONE chi router with no conflicts (chi
// panics at registration on conflicting patterns). The route table is the
// subject here; request-level proofs live in the dispatch/garages tests
// above and in the pgtest-gated full-boot integration test.
func TestAllDomainPrefixesMountedThroughProductionConstructors(t *testing.T) {
	t.Parallel()
	cfg := platform.LoadForTest()
	logger := platform.NewLogger(cfg.App)
	w := newGatewayWiring(cfg, logger, nil)
	breacher, err := identity.NewBreacher(w.breachCfg)
	require.NoError(t, err)
	w.breacher = breacher

	domain := buildDomainRouter(productionMounts(w))

	seen := map[string]bool{}
	require.NoError(t, chi.Walk(domain, func(_ string, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		seen[strings.TrimSuffix(route, "/")] = true
		return nil
	}))

	for _, route := range []string{
		// identity: auth flows, sessions, JWKS, profile, admin surfaces.
		"/v1/auth/register", "/v1/auth/login", "/v1/auth/refresh", "/v1/auth/logout",
		"/v1/auth/verify", "/v1/auth/verification/resend", "/v1/auth/password",
		"/v1/auth/sessions", "/.well-known/jwks.json", "/v1/users/me",
		"/v1/admin/users", "/v1/admin/audit",
		// vehicles
		"/v1/vehicles",
		// jobs
		"/v1/requests", "/v1/jobs",
		// dispatch
		"/v1/dispatch/score", "/v1/dispatch/factors",
		// payments
		"/v1/payments/intents", "/v1/payments/transactions/{transactionID}",
		"/v1/payments/refunds/{refundID}", "/v1/payments/webhooks/{provider}",
		"/v1/payments/reconcile",
		// inspections
		"/v1/inspections/sessions", "/v1/inspections/templates",
		// garages (rides the jobs process, issue #170)
		"/v1/garage/capacity",
	} {
		assert.True(t, seen[route], "route %q must be registered on the gateway router (registered: %v)", route, seen)
	}
}

// TestHealthzAndReadyzAfterWiring pins the post-wiring health posture:
// healthz stays 200 and readyz flips to an honest 200 once no blocker is
// latched.
func TestHealthzAndReadyzAfterWiring(t *testing.T) {
	t.Parallel()
	srv, boot := newGatewayUnderTest(t)
	wireGateway(t, boot, domainRoutes{})

	w := doGateway(t, srv, http.MethodGet, "/healthz", "", nil)
	require.Equal(t, http.StatusOK, w.Code)

	w = doGateway(t, srv, http.MethodGet, "/readyz", "", nil)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, "ready", decodeGatewayBody(t, w)["status"])
}

// TestDispatchRoutingUnderGateway proves per-prefix routing reaches a REAL
// domain handler through the production mount path: the composed router is
// installed via SetDomainHandler exactly as the routes boot step does, the
// dispatch constructor registers /v1/dispatch verbatim, and the scoring
// engine answers through the shared auth middleware + its RBAC gate
// (DispatcherRoles). No database: dispatch scores statelessly.
func TestDispatchRoutingUnderGateway(t *testing.T) {
	t.Parallel()
	srv, boot := newGatewayUnderTest(t)
	wireGateway(t, boot, domainRoutes{
		Dispatch: func(r chi.Router) {
			dispatch.Routes(r, func(h http.HandlerFunc) http.HandlerFunc {
				return platform.RequireRole(dispatch.DispatcherRoles, h)
			}, nil)
		},
	})

	body, err := json.Marshal(map[string]any{
		"job_context": dispatch.JobContext{RequiredSkills: []string{"engine"}},
		"candidates": []dispatch.Candidate{{
			TechnicianID:      uuid.MustParse("00000000-0000-4000-8000-000000000001"),
			ETAMinutes:        10,
			Skills:            []string{"engine"},
			Available:         true,
			HasEquipment:      true,
			HasRequiredParts:  true,
			AcceptanceRate:    0.9,
			CompletionRate:    0.9,
			AvgCustomerRating: 4.5,
		}},
	})
	require.NoError(t, err)

	// A dispatcher reaches the real scoring handler.
	w := doGateway(t, srv, http.MethodPost, "/v1/dispatch/score",
		mintGatewayToken(t, uuid.NewString(), "DISPATCHER", ""), body)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	resp := decodeGatewayBody(t, w)
	require.Contains(t, resp, "scores")
	assert.Len(t, resp["scores"], 1)

	// A customer is refused by the domain's own RBAC gate (403, not 404 —
	// the route exists and the gateway did not weaken it).
	w = doGateway(t, srv, http.MethodPost, "/v1/dispatch/score",
		mintGatewayToken(t, uuid.NewString(), "CUSTOMER", ""), body)
	require.Equal(t, http.StatusForbidden, w.Code, "body: %s", w.Body.String())

	// An anonymous request is refused (401) — the gateway middleware chain
	// populates claims; RequireRole enforces them.
	w = doGateway(t, srv, http.MethodPost, "/v1/dispatch/score", "", body)
	require.Equal(t, http.StatusUnauthorized, w.Code, "body: %s", w.Body.String())
}

// capacityFakeStore is the minimal in-memory garages.Store (two methods —
// the whole interface) behind the REAL garages service in the routing test.
type capacityFakeStore struct {
	mu   sync.Mutex
	rows map[uuid.UUID]garages.Capacity
}

func newCapacityFakeStore() *capacityFakeStore {
	return &capacityFakeStore{rows: map[uuid.UUID]garages.Capacity{}}
}

func (f *capacityFakeStore) Capacity(_ context.Context, tenantID uuid.UUID) (garages.Capacity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.rows[tenantID]
	if !ok {
		return garages.Capacity{}, garages.ErrCapacityNotFound
	}
	return c, nil
}

func (f *capacityFakeStore) UpsertCapacity(_ context.Context, c *garages.Capacity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.TenantID == uuid.Nil {
		return errors.New("garages: upsert capacity: tenant_id is required")
	}
	c.CreatedAt, c.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	f.rows[c.TenantID] = *c
	return nil
}

// TestGaragesCapacityRouteUnderGateway proves the garages capacity surface
// (which rides the jobs service's process, cmd/jobs precedent) is reachable
// under the gateway at its documented /v1/garage/capacity paths, through
// the REAL garages.Routes constructor, service and RBAC gate (GARAGE role
// with a tenant_id claim; everything else fails closed).
func TestGaragesCapacityRouteUnderGateway(t *testing.T) {
	t.Parallel()
	srv, boot := newGatewayUnderTest(t)
	store := newCapacityFakeStore()
	wireGateway(t, boot, domainRoutes{
		Garages: func(r chi.Router) {
			garages.Routes(r, garages.NewService(store, nil), platform.RequireAuthenticated)
		},
	})

	tenant := uuid.New().String()
	garageToken := mintGatewayToken(t, uuid.NewString(), "GARAGE", tenant)
	putBody := []byte(`{"bays":4,"opening_time":"08:00","closing_time":"17:00","technician_slots":6}`)

	// The GARAGE principal writes its capacity through the gateway.
	w := doGateway(t, srv, http.MethodPut, "/v1/garage/capacity", garageToken, putBody)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, float64(4), decodeGatewayBody(t, w)["bays"])

	// ...and reads it back on the documented GET path.
	w = doGateway(t, srv, http.MethodGet, "/v1/garage/capacity", garageToken, nil)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	got := decodeGatewayBody(t, w)
	assert.Equal(t, float64(4), got["bays"])
	assert.Equal(t, float64(6), got["technician_slots"])

	// A non-garage role is refused by the domain's own gate (403).
	w = doGateway(t, srv, http.MethodGet, "/v1/garage/capacity",
		mintGatewayToken(t, uuid.NewString(), "CUSTOMER", tenant), nil)
	require.Equal(t, http.StatusForbidden, w.Code, "body: %s", w.Body.String())

	// Anonymous is refused (401).
	w = doGateway(t, srv, http.MethodGet, "/v1/garage/capacity", "", nil)
	require.Equal(t, http.StatusUnauthorized, w.Code, "body: %s", w.Body.String())
}

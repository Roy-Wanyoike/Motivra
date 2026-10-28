package garages

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// httpTestSecret signs test tokens for the HTTP handlers.
const httpTestSecret = "garages-http-test-secret-garages-1"

// fakeStore is an in-memory Store mirroring the PostgresStore's
// single-row-per-tenant semantics closely enough to exercise the service
// discipline.
type fakeStore struct {
	mu       sync.Mutex
	capacity map[uuid.UUID]Capacity
	now      time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		capacity: make(map[uuid.UUID]Capacity),
		now:      time.Now().UTC(),
	}
}

func (f *fakeStore) stamp() time.Time {
	f.now = f.now.Add(time.Millisecond)
	return f.now
}

func (f *fakeStore) Capacity(_ context.Context, tenantID uuid.UUID) (Capacity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.capacity[tenantID]
	if !ok {
		return Capacity{}, ErrCapacityNotFound
	}
	return c, nil
}

func (f *fakeStore) UpsertCapacity(_ context.Context, c *Capacity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.TenantID == uuid.Nil {
		return errors.New("garages: upsert capacity: tenant_id is required")
	}
	created := f.stamp()
	if prev, ok := f.capacity[c.TenantID]; ok {
		created = prev.CreatedAt
	}
	c.CreatedAt, c.UpdatedAt = created, f.stamp()
	f.capacity[c.TenantID] = *c
	return nil
}

// capturePublisher records every published event (and the domain it was
// published under). Mirrors the jobs test publisher.
type capturePublisher struct {
	mu      sync.Mutex
	events  []platform.Event
	domains []string
	fail    bool
}

func (p *capturePublisher) Publish(_ context.Context, domain string, e platform.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail {
		return errors.New("jetstream unavailable")
	}
	p.domains = append(p.domains, domain)
	p.events = append(p.events, e)
	return nil
}

func (p *capturePublisher) ofType(t *testing.T, eventType string) []platform.Event {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []platform.Event
	for _, e := range p.events {
		if e.EventType == eventType {
			out = append(out, e)
		}
	}
	return out
}

func (p *capturePublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.events)
}

// newTestAPI builds a chi router wired exactly like cmd/jobs: auth
// middleware, the garages routes and platform.RequireAuthenticated as the
// auth gate, over the fake store and capturing publisher.
func newTestAPI(t *testing.T) (http.Handler, *fakeStore, *capturePublisher) {
	t.Helper()
	store := newFakeStore()
	pub := &capturePublisher{}
	svc := NewService(store, pub)
	validator := platform.NewJWTValidator(httpTestSecret, "motivra-identity", "motivra")
	router := chi.NewRouter()
	router.Use(platform.AuthMiddleware(validator))
	Routes(router, svc, platform.RequireAuthenticated)
	return router, store, pub
}

// mintGarageToken issues a valid HS256 access token for the given identity.
// An empty tenant mints a personal token; the malformedTenant sentinel
// produces a non-UUID tenant claim.
func mintGarageToken(t *testing.T, sub, role, tenant string) string {
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

// garageToken returns a GARAGE token bound to a fresh tenant plus the tenant id.
func garageToken(t *testing.T) (string, uuid.UUID) {
	t.Helper()
	tenant := uuid.New()
	return mintGarageToken(t, uuid.New().String(), RoleGarage, tenant.String()), tenant
}

// do performs a request against the test API and returns the recorder.
func do(t *testing.T, h http.Handler, method, path, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &m), "body: %s", w.Body.String())
	return m
}

func TestCapacityDefaultsWhenNeverConfigured(t *testing.T) {
	t.Parallel()
	h, _, pub := newTestAPI(t)
	token, _ := garageToken(t)

	w := do(t, h, http.MethodGet, "/v1/garage/capacity", token, nil)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	got := decodeBody(t, w)
	assert.Equal(t, float64(0), got["bays"], "an unconfigured garage declares zero bays")
	assert.Equal(t, float64(0), got["technician_slots"])
	assert.Equal(t, DefaultOpeningTime, got["opening_time"])
	assert.Equal(t, DefaultClosingTime, got["closing_time"])
	assert.NotContains(t, got, "updated_at", "unwritten configuration carries no update stamps")
	assert.NotContains(t, got, "updated_by")
	assert.Zero(t, pub.count(), "reads publish nothing")
}

func TestCapacityPutThenGetRoundTrip(t *testing.T) {
	t.Parallel()
	h, store, pub := newTestAPI(t)
	token, tenant := garageToken(t)

	body := `{"bays":3,"opening_time":"08:00","closing_time":"18:00","technician_slots":6}`
	w := do(t, h, http.MethodPut, "/v1/garage/capacity", token, []byte(body))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	got := decodeBody(t, w)
	assert.Equal(t, float64(3), got["bays"])
	assert.Equal(t, "08:00", got["opening_time"])
	assert.Equal(t, "18:00", got["closing_time"])
	assert.Equal(t, float64(6), got["technician_slots"])
	assert.NotEmpty(t, got["updated_at"])
	assert.NotEmpty(t, got["updated_by"])

	// GET returns the stored record.
	w = do(t, h, http.MethodGet, "/v1/garage/capacity", token, nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, float64(3), decodeBody(t, w)["bays"])

	// Exactly one row per tenant, replaced on re-write.
	w = do(t, h, http.MethodPut, "/v1/garage/capacity", token, []byte(`{"bays":4,"opening_time":"07:30","closing_time":"19:00","technician_slots":8}`))
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, store.capacity, 1)
	stored := store.capacity[tenant]
	assert.Equal(t, 4, stored.Bays)
	assert.Equal(t, "07:30", stored.OpeningTime)
	assert.Equal(t, 8, stored.TechnicianSlots)

	events := pub.ofType(t, EventCapacityUpdated)
	require.Len(t, events, 2, "every successful write publishes capacity.updated.v1")
}

func TestCapacityValidationFailures(t *testing.T) {
	t.Parallel()
	h, _, pub := newTestAPI(t)
	token, _ := garageToken(t)

	cases := []struct {
		name string
		body string
	}{
		{name: "negative bays", body: `{"bays":-1,"opening_time":"08:00","closing_time":"18:00","technician_slots":2}`},
		{name: "negative technician slots", body: `{"bays":2,"opening_time":"08:00","closing_time":"18:00","technician_slots":-3}`},
		{name: "opening equals closing", body: `{"bays":2,"opening_time":"08:00","closing_time":"08:00","technician_slots":2}`},
		{name: "opening after closing", body: `{"bays":2,"opening_time":"19:00","closing_time":"08:00","technician_slots":2}`},
		{name: "malformed opening_time", body: `{"bays":2,"opening_time":"8am","closing_time":"18:00","technician_slots":2}`},
		{name: "malformed closing_time", body: `{"bays":2,"opening_time":"08:00","closing_time":"25:99","technician_slots":2}`},
		{name: "missing bays", body: `{"opening_time":"08:00","closing_time":"18:00","technician_slots":2}`},
		{name: "missing technician_slots", body: `{"bays":2,"opening_time":"08:00","closing_time":"18:00"}`},
		{name: "missing opening_time", body: `{"bays":2,"closing_time":"18:00","technician_slots":2}`},
		{name: "missing closing_time", body: `{"bays":2,"opening_time":"08:00","technician_slots":2}`},
		{name: "malformed json", body: `{"bays":`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := do(t, h, http.MethodPut, "/v1/garage/capacity", token, []byte(tc.body))
			require.Equal(t, http.StatusUnprocessableEntity, w.Code, "body: %s", w.Body.String())
			assert.Equal(t, "validation_failed", decodeBody(t, w)["code"])
		})
	}
	assert.Zero(t, pub.count(), "failed writes must publish nothing")
}

func TestCapacityRequiresGarageRole(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestAPI(t)
	body := []byte(`{"bays":2,"opening_time":"08:00","closing_time":"18:00","technician_slots":2}`)

	for _, role := range []string{RoleDispatcher, RoleAdmin, RoleSuperAdmin, "CUSTOMER", "TECHNICIAN"} {
		role := role
		t.Run(role, func(t *testing.T) {
			t.Parallel()
			token := mintGarageToken(t, uuid.New().String(), role, uuid.New().String())
			w := do(t, h, http.MethodGet, "/v1/garage/capacity", token, nil)
			require.Equal(t, http.StatusForbidden, w.Code, "role %s must not read capacity", role)
			assert.Equal(t, "forbidden", decodeBody(t, w)["code"])
			w = do(t, h, http.MethodPut, "/v1/garage/capacity", token, body)
			require.Equal(t, http.StatusForbidden, w.Code, "role %s must not write capacity", role)
		})
	}

	// Operators are refused on the HTTP surface by design: the endpoint has
	// no tenant selector and the scope never comes from client input.
	// Operator consumption goes through the service API and events.
	w := do(t, h, http.MethodGet, "/v1/garage/capacity", mintGarageToken(t, uuid.New().String(), RoleDispatcher, uuid.New().String()), nil)
	require.Equal(t, http.StatusForbidden, w.Code)

	// Unauthenticated requests are rejected before scoping applies.
	w = do(t, h, http.MethodGet, "/v1/garage/capacity", "", nil)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	w = do(t, h, http.MethodPut, "/v1/garage/capacity", "", body)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestCapacityFailsClosedWithoutTenantClaim(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestAPI(t)
	body := []byte(`{"bays":2,"opening_time":"08:00","closing_time":"18:00","technician_slots":2}`)

	// GARAGE without a tenant_id claim: fail closed (403), never a
	// scopeless read or write.
	personal := mintGarageToken(t, uuid.New().String(), RoleGarage, "")
	w := do(t, h, http.MethodGet, "/v1/garage/capacity", personal, nil)
	require.Equal(t, http.StatusForbidden, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, "forbidden", decodeBody(t, w)["code"])
	w = do(t, h, http.MethodPut, "/v1/garage/capacity", personal, body)
	require.Equal(t, http.StatusForbidden, w.Code)

	// A malformed tenant claim is an authentication failure, not a 500 or a
	// scopeless write.
	bad := mintGarageToken(t, uuid.New().String(), RoleGarage, "not-a-uuid")
	w = do(t, h, http.MethodGet, "/v1/garage/capacity", bad, nil)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	w = do(t, h, http.MethodPut, "/v1/garage/capacity", bad, body)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

// TestCapacityTenantIsolation is the #124 merge gate: garage A never reads
// or writes garage B's capacity. Each principal's boundary is its own
// token's tenant claim — there is no tenant parameter to abuse.
func TestCapacityTenantIsolation(t *testing.T) {
	t.Parallel()
	h, store, _ := newTestAPI(t)
	tokenA, tenantA := garageToken(t)
	tokenB, _ := garageToken(t)

	// A configures its garage.
	w := do(t, h, http.MethodPut, "/v1/garage/capacity", tokenA,
		[]byte(`{"bays":5,"opening_time":"06:00","closing_time":"20:00","technician_slots":9}`))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	// B reads: B sees the documented defaults, never A's row.
	w = do(t, h, http.MethodGet, "/v1/garage/capacity", tokenB, nil)
	require.Equal(t, http.StatusOK, w.Code)
	got := decodeBody(t, w)
	assert.Equal(t, float64(0), got["bays"], "garage B must not see garage A's capacity")
	assert.Equal(t, DefaultOpeningTime, got["opening_time"])

	// B writes its own row; A's row is untouched.
	w = do(t, h, http.MethodPut, "/v1/garage/capacity", tokenB,
		[]byte(`{"bays":1,"opening_time":"09:00","closing_time":"17:00","technician_slots":1}`))
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, store.capacity, 2)
	assert.Equal(t, 5, store.capacity[tenantA].Bays, "B's write must not touch A's row")

	// A still reads its own values back: isolation, not breakage.
	w = do(t, h, http.MethodGet, "/v1/garage/capacity", tokenA, nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, float64(5), decodeBody(t, w)["bays"])
}

func TestCapacityEventEnvelope(t *testing.T) {
	t.Parallel()
	h, _, pub := newTestAPI(t)
	token, tenant := garageToken(t)

	w := do(t, h, http.MethodPut, "/v1/garage/capacity", token,
		[]byte(`{"bays":2,"opening_time":"08:00","closing_time":"18:00","technician_slots":3}`))
	require.Equal(t, http.StatusOK, w.Code)

	events := pub.ofType(t, EventCapacityUpdated)
	require.Len(t, events, 1)
	assert.Equal(t, EventDomain, pub.domains[0], "publish domain must be garages")
	assert.Equal(t, tenant.String(), events[0].TenantID, "the envelope carries the owning tenant")
	assert.NotEmpty(t, events[0].EventID)
}

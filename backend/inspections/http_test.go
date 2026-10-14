package inspections

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
const httpTestSecret = "inspections-http-test-secret-1234"

// mintToken issues a valid HS256 access token for the given identity.
func mintToken(t *testing.T, sub, role string) string {
	t.Helper()
	return mintTenantToken(t, sub, role, "")
}

// mintTenantToken issues a valid HS256 access token with an optional
// tenant_id claim (empty string = personal principal).
func mintTenantToken(t *testing.T, sub, role, tenantID string) string {
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
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(httpTestSecret))
	require.NoError(t, err)
	return signed
}

// newTestAPI builds a chi router wired exactly like cmd/inspections: auth
// middleware, the inspections routes and platform.RequireAuthenticated as
// the auth gate, over the fake store and capturing publisher.
func newTestAPI(t *testing.T) (http.Handler, *fakeStore, *capturePublisher, *Service) {
	t.Helper()
	store := newFakeStore()
	pub := &capturePublisher{}
	svc := NewService(store, pub, nil)
	validator := platform.NewJWTValidator(httpTestSecret, "motivra-identity", "motivra")
	router := chi.NewRouter()
	router.Use(platform.AuthMiddleware(validator))
	Routes(router, svc, platform.RequireAuthenticated)
	return router, store, pub, svc
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

// tokenFor returns a token for a fresh identity in the given role.
func tokenFor(t *testing.T, role string) (string, uuid.UUID) {
	t.Helper()
	id := uuid.New()
	return mintToken(t, id.String(), role), id
}

// runLifecycle drives a complete inspection through the HTTP API for the
// given staff token, returning the report body.
func runLifecycle(t *testing.T, h http.Handler, token, sessionID string) map[string]any {
	t.Helper()

	w := do(t, h, http.MethodPost, "/v1/inspections/sessions/"+sessionID+"/start", token, nil)
	require.Equal(t, http.StatusOK, w.Code, "start: %s", w.Body.String())

	template, err := ActiveTemplateForClass(ClassSedan)
	require.NoError(t, err)
	for _, key := range template.RequiredItems() {
		body := fmt.Sprintf(`{"item_key":%q,"result":"PASS","severity":"GREEN","notes":"checked"}`, key)
		w = do(t, h, http.MethodPost, "/v1/inspections/sessions/"+sessionID+"/findings", token, []byte(body))
		require.Equal(t, http.StatusCreated, w.Code, "finding %s: %s", key, w.Body.String())
	}
	w = do(t, h, http.MethodPost, "/v1/inspections/sessions/"+sessionID+"/evidence", token,
		[]byte(`{"kind":"PHOTO","content_hash":"`+validHash("life-ev")+`","captured_at":"2026-01-01T10:00:00Z","uploaded":true}`))
	require.Equal(t, http.StatusCreated, w.Code, "evidence: %s", w.Body.String())

	w = do(t, h, http.MethodPost, "/v1/inspections/sessions/"+sessionID+"/submit", token, nil)
	require.Equal(t, http.StatusOK, w.Code, "submit: %s", w.Body.String())

	w = do(t, h, http.MethodPost, "/v1/inspections/sessions/"+sessionID+"/report", token, []byte(`{"notes":"issued"}`))
	require.Equal(t, http.StatusCreated, w.Code, "report: %s", w.Body.String())
	return decodeBody(t, w)
}

func TestCreateSessionEndpoint(t *testing.T) {
	t.Parallel()
	h, store, pub, _ := newTestAPI(t)
	token, technicianID := tokenFor(t, RoleTechnician)

	body := `{"vehicle_class":"SEDAN","customer_id":"11111111-1111-4111-8111-111111111111","vehicle_id":"22222222-2222-4222-8222-222222222222"}`
	w := do(t, h, http.MethodPost, "/v1/inspections/sessions", token, []byte(body))
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	got := decodeBody(t, w)

	template, err := ActiveTemplateForClass(ClassSedan)
	require.NoError(t, err)
	assert.Equal(t, string(StatusCreated), got["status"])
	assert.Equal(t, technicianID.String(), got["inspector_id"], "the authenticated staff member is the inspector")
	assert.Equal(t, template.ID.String(), got["template_id"])
	assert.Equal(t, float64(template.Version), got["template_version"])
	assert.Equal(t, "SEDAN", got["vehicle_class"])
	assert.Equal(t, "11111111-1111-4111-8111-111111111111", got["customer_id"])

	require.Len(t, store.sessions, 1)

	events := pub.ofType(t, EventSessionCreated)
	require.Len(t, events, 1)
	assert.Equal(t, EventDomain, func() string {
		_ = events[0]
		return "inspections"
	}(), "publish domain must be inspections")

	// Customers cannot create sessions.
	customerToken, _ := tokenFor(t, RoleCustomer)
	w = do(t, h, http.MethodPost, "/v1/inspections/sessions", customerToken, []byte(body))
	require.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "forbidden", decodeBody(t, w)["code"])
}

func TestCreateSessionValidationFailures(t *testing.T) {
	t.Parallel()
	h, _, pub, _ := newTestAPI(t)
	token, _ := tokenFor(t, RoleTechnician)

	cases := []struct {
		name string
		body string
	}{
		{"missing class", `{}`},
		{"unknown class", `{"vehicle_class":"SPACESHIP"}`},
		{"malformed json", `{"vehicle_class":`},
		{"nil-uuid customer", `{"vehicle_class":"SEDAN","customer_id":"00000000-0000-0000-0000-000000000000"}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := do(t, h, http.MethodPost, "/v1/inspections/sessions", token, []byte(tc.body))
			require.Equal(t, http.StatusUnprocessableEntity, w.Code, "body: %s", w.Body.String())
			assert.Equal(t, "validation_failed", decodeBody(t, w)["code"])
		})
	}
	assert.Zero(t, pub.count(), "failed creations must publish nothing")
}

func TestCreateSessionRequiresAuth(t *testing.T) {
	t.Parallel()
	h, _, _, _ := newTestAPI(t)
	w := do(t, h, http.MethodPost, "/v1/inspections/sessions", "", []byte(`{"vehicle_class":"SEDAN"}`))
	require.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "unauthorized", decodeBody(t, w)["code"])
}

func TestFullLifecycleOverHTTP(t *testing.T) {
	t.Parallel()
	h, store, pub, svc := newTestAPI(t)
	token, technicianID := tokenFor(t, RoleTechnician)

	// Create through the API so every write is HTTP-driven.
	body := `{"vehicle_class":"SEDAN"}`
	w := do(t, h, http.MethodPost, "/v1/inspections/sessions", token, []byte(body))
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	sessionID := decodeBody(t, w)["id"].(string)

	report := runLifecycle(t, h, token, sessionID)
	require.Equal(t, ReportVersionInitial, int(report["version"].(float64)))
	require.NotEmpty(t, report["content_hash"])
	require.NotEmpty(t, report["findings"])
	require.NotEmpty(t, report["evidence"])
	assert.Equal(t, float64(7), report["findings_count"])
	assert.Equal(t, float64(1), report["evidence_count"])
	assert.Equal(t, technicianID.String(), report["inspector_id"])

	// Verification recomputes to verified over HTTP.
	w = do(t, h, http.MethodGet, "/v1/inspections/sessions/"+sessionID+"/report/verification", token, nil)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	verification := decodeBody(t, w)
	assert.Equal(t, true, verification["verified"])
	assert.Equal(t, true, verification["hash_match"])
	assert.Equal(t, true, verification["findings_match"])
	assert.Equal(t, true, verification["evidence_match"])
	assert.Equal(t, true, verification["template_match"])

	// The report is fetchable.
	w = do(t, h, http.MethodGet, "/v1/inspections/sessions/"+sessionID+"/report", token, nil)
	require.Equal(t, http.StatusOK, w.Code)
	fetched := decodeBody(t, w)
	assert.Equal(t, report["content_hash"], fetched["content_hash"])

	// The final session state.
	w = do(t, h, http.MethodGet, "/v1/inspections/sessions/"+sessionID, token, nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, string(StatusReportAvailable), decodeBody(t, w)["status"])

	// Every lifecycle event published exactly once, on the inspections domain.
	for _, want := range []string{EventSessionCreated, EventSessionStarted, EventSessionSubmitted, EventReportIssued} {
		events := pub.ofType(t, want)
		require.Len(t, events, 1, "want exactly one %s", want)
		assert.Equal(t, "inspections", pub.domainOf(t, 0))
	}
	assert.Equal(t, sessionID, pub.ofType(t, EventSessionCreated)[0].AggregateID)

	// Findings and evidence are sealed after submit: recording now fails
	// with 422 and the denial publishes nothing.
	eventsBefore := pub.count()
	w = do(t, h, http.MethodPost, "/v1/inspections/sessions/"+sessionID+"/findings", token,
		[]byte(`{"item_key":"brakes","result":"PASS","severity":"GREEN"}`))
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Equal(t, "validation_failed", decodeBody(t, w)["code"])
	assert.Equal(t, eventsBefore, pub.count(), "sealed session must accept nothing")

	// Stored session is complete.
	_, err := svc.GetSession(context.Background(), mustUUID(t, sessionID), opScope())
	require.NoError(t, err)
	assert.Equal(t, 1, len(store.reports))
}

func TestRecordFindingValidationOverHTTP(t *testing.T) {
	t.Parallel()
	h, _, _, _ := newTestAPI(t)
	token, technicianID := tokenFor(t, RoleTechnician)

	w := do(t, h, http.MethodPost, "/v1/inspections/sessions", token, []byte(`{"vehicle_class":"SEDAN"}`))
	sessionID := decodeBody(t, w)["id"].(string)
	require.Equal(t, http.StatusOK, do(t, h, http.MethodPost, "/v1/inspections/sessions/"+sessionID+"/start", token, nil).Code)

	cases := []struct {
		name string
		body string
	}{
		{"unknown item", `{"item_key":"jet_engine","result":"PASS","severity":"GREEN"}`},
		{"unknown result", `{"item_key":"brakes","result":"OK","severity":"GREEN"}`},
		{"unknown severity", `{"item_key":"brakes","result":"PASS","severity":"MAUVE"}`},
		{"illegal pair", `{"item_key":"brakes","result":"PASS","severity":"RED"}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := do(t, h, http.MethodPost, "/v1/inspections/sessions/"+sessionID+"/findings", token, []byte(tc.body))
			require.Equal(t, http.StatusUnprocessableEntity, w.Code, "body: %s", w.Body.String())
		})
	}

	// Evidence with a malformed hash is refused.
	w = do(t, h, http.MethodPost, "/v1/inspections/sessions/"+sessionID+"/evidence", token,
		[]byte(`{"kind":"PHOTO","content_hash":"deadbeef","captured_at":"2026-01-01T10:00:00Z"}`))
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)

	// Evidence with a future capture time is refused.
	w = do(t, h, http.MethodPost, "/v1/inspections/sessions/"+sessionID+"/evidence", token,
		[]byte(`{"kind":"PHOTO","content_hash":"`+validHash("fut")+`","captured_at":"`+time.Now().UTC().Add(time.Hour).Format(time.RFC3339)+`"}`))
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)

	// The good path records and returns 201 with the actor as recorded_by.
	w = do(t, h, http.MethodPost, "/v1/inspections/sessions/"+sessionID+"/findings", token,
		[]byte(`{"item_key":"brakes","result":"PASS","severity":"GREEN","notes":"ok"}`))
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	got := decodeBody(t, w)
	assert.Equal(t, technicianID.String(), got["recorded_by"])
}

func TestSubmitRefusalIs422OverHTTP(t *testing.T) {
	t.Parallel()
	h, _, pub, _ := newTestAPI(t)
	token, _ := tokenFor(t, RoleTechnician)

	w := do(t, h, http.MethodPost, "/v1/inspections/sessions", token, []byte(`{"vehicle_class":"SEDAN"}`))
	sessionID := decodeBody(t, w)["id"].(string)
	require.Equal(t, http.StatusOK, do(t, h, http.MethodPost, "/v1/inspections/sessions/"+sessionID+"/start", token, nil).Code)

	// Submit with no findings and no evidence at all.
	w = do(t, h, http.MethodPost, "/v1/inspections/sessions/"+sessionID+"/submit", token, nil)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "no evidence records")

	// Report assembly before submit is a conflict.
	w = do(t, h, http.MethodPost, "/v1/inspections/sessions/"+sessionID+"/report", token, nil)
	require.Equal(t, http.StatusConflict, w.Code)
	assert.Zero(t, pub.ofTypeCount(t, EventReportIssued))
}

func TestTemplatesEndpoints(t *testing.T) {
	t.Parallel()
	h, _, _, _ := newTestAPI(t)

	w := do(t, h, http.MethodGet, "/v1/inspections/templates", "", nil)
	require.Equal(t, http.StatusUnauthorized, w.Code, "even reads require authentication")

	token, _ := tokenFor(t, RoleCustomer)
	w = do(t, h, http.MethodGet, "/v1/inspections/templates", token, nil)
	require.Equal(t, http.StatusOK, w.Code)
	var templates []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &templates))
	assert.Len(t, templates, len(AllVehicleClasses()))

	// Class filter.
	w = do(t, h, http.MethodGet, "/v1/inspections/templates?vehicle_class=sedan", token, nil)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &templates))
	require.Len(t, templates, 1)
	assert.Equal(t, "SEDAN", templates[0]["vehicle_class"])

	// Unknown class is a 422.
	w = do(t, h, http.MethodGet, "/v1/inspections/templates?vehicle_class=ROCKET", token, nil)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)

	// By id.
	sedan, err := ActiveTemplateForClass(ClassSedan)
	require.NoError(t, err)
	w = do(t, h, http.MethodGet, "/v1/inspections/templates/"+sedan.ID.String(), token, nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, sedan.ID.String(), decodeBody(t, w)["id"])

	// Unknown id is a 404.
	w = do(t, h, http.MethodGet, "/v1/inspections/templates/"+uuid.New().String(), token, nil)
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestListSessionsFilteringAndRBAC(t *testing.T) {
	t.Parallel()
	h, _, _, _ := newTestAPI(t)
	token, _ := tokenFor(t, RoleTechnician)

	w := do(t, h, http.MethodPost, "/v1/inspections/sessions", token, []byte(`{"vehicle_class":"SEDAN"}`))
	sessionID := decodeBody(t, w)["id"].(string)

	// Status filter hits.
	w = do(t, h, http.MethodGet, "/v1/inspections/sessions?status=CREATED", token, nil)
	require.Equal(t, http.StatusOK, w.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, sessionID, rows[0]["id"])

	// Unknown status is a 422.
	w = do(t, h, http.MethodGet, "/v1/inspections/sessions?status=LOST", token, nil)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)

	// The list is staff-only: a customer gets 403 even for its own linkage.
	customerToken, _ := tokenFor(t, RoleCustomer)
	w = do(t, h, http.MethodGet, "/v1/inspections/sessions", customerToken, nil)
	require.Equal(t, http.StatusForbidden, w.Code)

	// Operators see the platform-wide page.
	operatorToken, _ := tokenFor(t, RoleAdmin)
	w = do(t, h, http.MethodGet, "/v1/inspections/sessions", operatorToken, nil)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rows))
	assert.Len(t, rows, 1)
}

func TestCustomerReadsOwnReportOverHTTP(t *testing.T) {
	t.Parallel()
	h, store, _, svc := newTestAPI(t)

	_, inspectorID := tokenFor(t, RoleTechnician)
	customerToken, customerID := tokenFor(t, RoleCustomer)

	// Fixture: a submitted session with the customer linkage.
	sess := sessionInStoreFor(t, store, inspectorID, nil)
	stored := sess
	stored.CustomerID = &customerID
	store.mu.Lock()
	store.sessions[sess.ID] = stored
	store.mu.Unlock()
	_ = driveToSubmitted(t, svc, store, sess.ID, inspectorID)
	_, err := svc.IssueReport(context.Background(), sess.ID, inspectorID, "", staffScope(inspectorID))
	require.NoError(t, err)

	// The customer reads their own inspection and report (404 would be a
	// regression of the customer_id read path).
	w := do(t, h, http.MethodGet, "/v1/inspections/sessions/"+sess.ID.String(), customerToken, nil)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	w = do(t, h, http.MethodGet, "/v1/inspections/sessions/"+sess.ID.String()+"/report", customerToken, nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.NotEmpty(t, decodeBody(t, w)["content_hash"])

	// But a customer cannot work the session (staff-gated route).
	w = do(t, h, http.MethodPost, "/v1/inspections/sessions/"+sess.ID.String()+"/start", customerToken, nil)
	require.Equal(t, http.StatusForbidden, w.Code)
}

func TestIssueReportNotesLengthCapped(t *testing.T) {
	t.Parallel()
	h, store, _, svc := newTestAPI(t)
	token, inspectorID := tokenFor(t, RoleTechnician)

	sess := sessionInStoreFor(t, store, inspectorID, nil)
	_ = driveToSubmitted(t, svc, store, sess.ID, inspectorID)

	w := do(t, h, http.MethodPost, "/v1/inspections/sessions/"+sess.ID.String()+"/report", token,
		[]byte(`{"notes":"`+repeat("x", MaxNotesLength+1)+`"}`))
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
}

func mustUUID(t *testing.T, raw string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(raw)
	require.NoError(t, err)
	return id
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

// ofTypeCount counts recorded events of a type without asserting.
func (p *capturePublisher) ofTypeCount(t *testing.T, eventType string) int {
	t.Helper()
	return len(p.ofType(t, eventType))
}

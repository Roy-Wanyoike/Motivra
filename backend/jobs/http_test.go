package jobs

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
const httpTestSecret = "jobs-http-test-secret-jobs-1234"

// mintToken issues a valid HS256 access token for the given identity.
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
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(httpTestSecret))
	require.NoError(t, err)
	return signed
}

// newTestAPI builds a chi router wired exactly like cmd/jobs: auth
// middleware, the jobs routes and platform.RequireAuthenticated as the
// auth gate, over the part-1 fake store and capturing publisher. The
// Service is returned so tests can seed job states directly.
func newTestAPI(t *testing.T) (http.Handler, *fakeStore, *capturePublisher, *Service) {
	t.Helper()
	store := newFakeStore()
	pub := &capturePublisher{}
	svc := NewService(store, pub)
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

// createRequestOverHTTP creates a valid service request through the API
// and returns the decoded response.
func createRequestOverHTTP(t *testing.T, h http.Handler, token string) map[string]any {
	t.Helper()
	body := `{"description":"Engine overheats on the highway","location_lat":-1.286389,"location_lng":36.817223,"address_text":"Nairobi CBD"}`
	w := do(t, h, http.MethodPost, "/v1/requests", token, []byte(body))
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	return decodeBody(t, w)
}

// createJobOverHTTP converts a request into a job through the API.
func createJobOverHTTP(t *testing.T, h http.Handler, token, requestID string) map[string]any {
	t.Helper()
	w := do(t, h, http.MethodPost, "/v1/requests/"+requestID+"/job", token, nil)
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	return decodeBody(t, w)
}

func TestCreateRequestEndpoint(t *testing.T) {
	t.Parallel()
	h, store, pub, _ := newTestAPI(t)
	token, customerID := tokenFor(t, RoleCustomer)

	got := createRequestOverHTTP(t, h, token)
	assert.NotEmpty(t, got["id"])
	assert.Equal(t, customerID.String(), got["customer_id"])
	assert.Equal(t, RequestStatusReceived, got["status"])
	assert.Equal(t, "Engine overheats on the highway", got["description"])
	assert.Equal(t, float64(-1.286389), got["location_lat"])
	assert.Equal(t, "Nairobi CBD", got["address_text"])
	assert.NotEmpty(t, got["created_at"])

	require.Len(t, store.requests, 1)

	events := pub.ofType(t, EventRequestReceived)
	require.Len(t, events, 1)
	assert.Equal(t, EventDomain, pub.domainOf(t, 0), "publish domain must be jobs")
	assert.Equal(t, "", events[0].CorrelationID, "non-HTTP callers publish an empty correlation_id")
}

func TestCreateRequestValidationFailures(t *testing.T) {
	t.Parallel()
	h, _, pub, _ := newTestAPI(t)
	token, _ := tokenFor(t, RoleCustomer)

	cases := []struct {
		name string
		body string
	}{
		{name: "blank description", body: `{"description":"   "}`},
		{name: "missing description", body: `{}`},
		{name: "lat without lng", body: `{"description":"x","location_lat":1.0}`},
		{name: "lat out of bounds", body: `{"description":"x","location_lat":91.5,"location_lng":0}`},
		{name: "malformed json", body: `{"description":`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := do(t, h, http.MethodPost, "/v1/requests", token, []byte(tc.body))
			require.Equal(t, http.StatusUnprocessableEntity, w.Code, "body: %s", w.Body.String())
			assert.Equal(t, "validation_failed", decodeBody(t, w)["code"])
		})
	}
	assert.Zero(t, pub.count(), "failed creations must publish nothing")
}

func TestCreateRequestRequiresAuth(t *testing.T) {
	t.Parallel()
	h, _, _, _ := newTestAPI(t)

	w := do(t, h, http.MethodPost, "/v1/requests", "", []byte(`{"description":"x"}`))
	require.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "unauthorized", decodeBody(t, w)["code"])
}

func TestCreateJobFromRequestEndpoint(t *testing.T) {
	t.Parallel()
	h, store, _, _ := newTestAPI(t)
	token, _ := tokenFor(t, RoleCustomer)

	req := createRequestOverHTTP(t, h, token)
	requestID := req["id"].(string)

	job := createJobOverHTTP(t, h, token, requestID)
	assert.Equal(t, string(StatusCreated), job["status"])
	assert.Equal(t, requestID, job["service_request_id"])
	assert.Equal(t, req["description"], job["problem_summary"])

	stored, err := store.GetRequest(context.Background(), mustUUID(t, requestID), opScope())
	require.NoError(t, err)
	assert.Equal(t, RequestStatusConverted, stored.Status)

	// Second conversion conflicts.
	w := do(t, h, http.MethodPost, "/v1/requests/"+requestID+"/job", token, nil)
	require.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, "conflict", decodeBody(t, w)["code"])
}

func TestTransitionByDispatcher(t *testing.T) {
	t.Parallel()
	h, store, _, svc := newTestAPI(t)
	dispatcher, _ := tokenFor(t, RoleDispatcher)

	job := jobInStore(t, store, StatusCreated)

	body := `{"to":"TRIAGING","reason":"customer request triaged"}`
	w := do(t, h, http.MethodPost, "/v1/jobs/"+job.ID.String()+"/transitions", dispatcher, []byte(body))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	got := decodeBody(t, w)
	assert.Equal(t, string(StatusTriaging), got["status"])
	assert.Equal(t, job.ID.String(), got["id"])

	stored, err := svc.GetJob(context.Background(), job.ID, opScope())
	require.NoError(t, err)
	assert.Equal(t, StatusTriaging, stored.Status)
}

func TestIllegalTransitionConflicts(t *testing.T) {
	t.Parallel()
	h, store, pub, _ := newTestAPI(t)
	dispatcher, _ := tokenFor(t, RoleDispatcher)

	job := jobInStore(t, store, StatusCreated)

	w := do(t, h, http.MethodPost, "/v1/jobs/"+job.ID.String()+"/transitions", dispatcher, []byte(`{"to":"COMPLETED","reason":"shortcut"}`))
	require.Equal(t, http.StatusConflict, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, "conflict", decodeBody(t, w)["code"])
	assert.Zero(t, pub.count(), "rejected transitions must publish nothing")

	stored, err := store.GetJob(context.Background(), job.ID, opScope())
	require.NoError(t, err)
	assert.Equal(t, StatusCreated, stored.Status, "rejected transition must not mutate")
}

func TestTransitionForbiddenForCustomer(t *testing.T) {
	t.Parallel()
	h, store, _, _ := newTestAPI(t)
	customer, _ := tokenFor(t, RoleCustomer)

	job := jobInStore(t, store, StatusCreated)

	w := do(t, h, http.MethodPost, "/v1/jobs/"+job.ID.String()+"/transitions", customer, []byte(`{"to":"TRIAGING","reason":"nope"}`))
	require.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "forbidden", decodeBody(t, w)["code"])
}

func TestTransitionRequiresKnownStatus(t *testing.T) {
	t.Parallel()
	h, store, _, _ := newTestAPI(t)
	dispatcher, _ := tokenFor(t, RoleDispatcher)

	job := jobInStore(t, store, StatusCreated)

	w := do(t, h, http.MethodPost, "/v1/jobs/"+job.ID.String()+"/transitions", dispatcher, []byte(`{"to":"WARP_SPEED","reason":"x"}`))
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Equal(t, "validation_failed", decodeBody(t, w)["code"])
}

func TestTransitionRequiresAuth(t *testing.T) {
	t.Parallel()
	h, store, _, _ := newTestAPI(t)

	job := jobInStore(t, store, StatusCreated)

	w := do(t, h, http.MethodPost, "/v1/jobs/"+job.ID.String()+"/transitions", "", []byte(`{"to":"TRIAGING"}`))
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAssignAndAcceptFlow(t *testing.T) {
	t.Parallel()
	h, store, pub, svc := newTestAPI(t)
	dispatcher, _ := tokenFor(t, RoleDispatcher)

	job := jobInStore(t, store, StatusCreated)
	driveDispatching(t, svc, job.ID)
	tech := uuid.New()

	assignBody := fmt.Sprintf(`{"technician_id":%q}`, tech.String())
	w := do(t, h, http.MethodPost, "/v1/jobs/"+job.ID.String()+"/assignments", dispatcher, []byte(assignBody))
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	got := decodeBody(t, w)
	assert.Equal(t, string(StatusAssigned), got["status"])
	assert.Equal(t, tech.String(), got["technician_id"])

	// Only TECHNICIAN role may accept; the token subject is the technician.
	techToken := mintToken(t, tech.String(), RoleTechnician)
	w = do(t, h, http.MethodPost, "/v1/jobs/"+job.ID.String()+"/accept", techToken, nil)
	require.Equal(t, http.StatusNoContent, w.Code)
	assert.Empty(t, w.Body.String())

	stored, err := svc.GetJob(context.Background(), job.ID, opScope())
	require.NoError(t, err)
	assert.Equal(t, StatusAccepted, stored.Status)

	require.Len(t, pub.ofType(t, EventJobAssigned), 1)
	var acceptedEvent bool
	for _, e := range pub.ofType(t, EventJobStatusChanged) {
		var p JobStatusChangedPayload
		require.NoError(t, json.Unmarshal(e.Payload, &p))
		if p.From == StatusAssigned && p.To == StatusAccepted {
			acceptedEvent = true
		}
	}
	assert.True(t, acceptedEvent, "acceptance must publish status.changed")

	// A different technician cannot accept; the scoped read makes a foreign
	// job indistinguishable from a missing one (404, no existence leak).
	w = do(t, h, http.MethodPost, "/v1/jobs/"+job.ID.String()+"/accept", mintToken(t, uuid.New().String(), RoleTechnician), nil)
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestAssignForbiddenForTechnician(t *testing.T) {
	t.Parallel()
	h, store, _, svc := newTestAPI(t)
	technician, _ := tokenFor(t, RoleTechnician)

	job := jobInStore(t, store, StatusCreated)
	driveDispatching(t, svc, job.ID)

	w := do(t, h, http.MethodPost, "/v1/jobs/"+job.ID.String()+"/assignments", technician, []byte(fmt.Sprintf(`{"technician_id":%q}`, uuid.New().String())))
	require.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "forbidden", decodeBody(t, w)["code"])
}

func TestAcceptRequiresTechnicianRole(t *testing.T) {
	t.Parallel()
	h, store, _, svc := newTestAPI(t)
	dispatcher, _ := tokenFor(t, RoleDispatcher)

	job := jobInStore(t, store, StatusCreated)
	driveDispatching(t, svc, job.ID)
	tech := uuid.New()
	require.NoError(t, svc.AssignTechnician(context.Background(), job.ID, tech, nil, opScope()))

	// A dispatcher cannot accept on behalf of the technician.
	w := do(t, h, http.MethodPost, "/v1/jobs/"+job.ID.String()+"/accept", dispatcher, nil)
	require.Equal(t, http.StatusForbidden, w.Code)
}

func TestTransitionAuditTrail(t *testing.T) {
	t.Parallel()
	h, store, _, svc := newTestAPI(t)
	dispatcher, dispatcherID := tokenFor(t, RoleDispatcher)

	job := jobInStore(t, store, StatusCreated)
	require.NoError(t, svc.Transition(context.Background(), job.ID, StatusTriaging, &dispatcherID, "triaged", opScope()))

	w := do(t, h, http.MethodGet, "/v1/jobs/"+job.ID.String()+"/transitions", dispatcher, nil)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var rows []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, string(StatusCreated), rows[0]["from_status"])
	assert.Equal(t, string(StatusTriaging), rows[0]["to_status"])
	assert.Equal(t, "triaged", rows[0]["reason"])
	assert.Equal(t, dispatcherID.String(), rows[0]["actor_id"])
}

func TestGetJobEndpoint(t *testing.T) {
	t.Parallel()
	h, store, _, _ := newTestAPI(t)

	job := jobInStore(t, store, StatusCreated)
	// The customer token's subject is the job's owner: reads are scoped to
	// the authenticated principal, so owners see their own jobs.
	token := mintToken(t, job.CustomerID.String(), RoleCustomer)

	w := do(t, h, http.MethodGet, "/v1/jobs/"+job.ID.String(), token, nil)
	require.Equal(t, http.StatusOK, w.Code)
	got := decodeBody(t, w)
	assert.Equal(t, job.ID.String(), got["id"])
	assert.Equal(t, string(StatusCreated), got["status"])

	w = do(t, h, http.MethodGet, "/v1/jobs/"+uuid.New().String(), token, nil)
	require.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, "not_found", decodeBody(t, w)["code"])
}

func TestListJobsByStatusEndpoint(t *testing.T) {
	t.Parallel()
	h, store, _, _ := newTestAPI(t)
	dispatcher, _ := tokenFor(t, RoleDispatcher)
	customer, _ := tokenFor(t, RoleCustomer)

	jobInStore(t, store, StatusCreated)
	jobInStore(t, store, StatusCreated)
	jobInStore(t, store, StatusDispatching)

	w := do(t, h, http.MethodGet, "/v1/jobs?status=CREATED", dispatcher, nil)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rows))
	assert.Len(t, rows, 2)

	w = do(t, h, http.MethodGet, "/v1/jobs?status=NOT_A_STATUS", dispatcher, nil)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Equal(t, "validation_failed", decodeBody(t, w)["code"])

	w = do(t, h, http.MethodGet, "/v1/jobs?status=", dispatcher, nil)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)

	w = do(t, h, http.MethodGet, "/v1/jobs?status=CREATED&limit=0&offset=-3", dispatcher, nil)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)

	w = do(t, h, http.MethodGet, "/v1/jobs?status=CREATED&limit=1", dispatcher, nil)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rows))
	assert.Len(t, rows, 1, "limit must constrain the page")

	// The list endpoint is dispatcher/admin only.
	w = do(t, h, http.MethodGet, "/v1/jobs?status=CREATED", customer, nil)
	require.Equal(t, http.StatusForbidden, w.Code)
}

// mustUUID parses raw as a UUID, failing the test when malformed.
func mustUUID(t *testing.T, raw string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(raw)
	require.NoError(t, err)
	return id
}

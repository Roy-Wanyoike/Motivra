package vehicles

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
const httpTestSecret = "vehicles-http-test-secret-vehicles-1234"

// mintToken issues a valid HS256 access token for the given identity.
func mintToken(t *testing.T, sub, tenant, role string) string {
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
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(httpTestSecret))
	require.NoError(t, err)
	return signed
}

// newTestAPI builds a chi router wired exactly like cmd/vehicles: auth
// middleware, platform.RequireAuthenticated and the vehicles routes, over a
// fake store and a capturing publisher. The Service is returned so tests
// can seed history directly.
func newTestAPI(t *testing.T) (http.Handler, *fakeStore, *fakePublisher, *Service) {
	t.Helper()
	store := newFakeStore()
	pub := &fakePublisher{}
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
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &m))
	return m
}

// ownerToken returns a token for a fresh owner identity.
func ownerToken(t *testing.T) (string, uuid.UUID) {
	t.Helper()
	id := uuid.New()
	return mintToken(t, id.String(), "", "CUSTOMER"), id
}

// registerVINOverHTTP registers the given valid VIN through the API.
func registerVINOverHTTP(t *testing.T, h http.Handler, token, vin string) map[string]any {
	t.Helper()
	body := fmt.Sprintf(`{"vin":%q,"make":"Honda","model":"Accord","year_of_manufacture":2020,"plate":"KDA 123A","color":"Silver"}`, vin)
	w := do(t, h, http.MethodPost, "/v1/vehicles", token, []byte(body))
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	return decodeBody(t, w)
}

// registerVehicleOverHTTP registers the canonical valid test VIN.
func registerVehicleOverHTTP(t *testing.T, h http.Handler, token string) map[string]any {
	t.Helper()
	return registerVINOverHTTP(t, h, token, "1HGCM82633A004352")
}

func TestCreateVehicleEndpoint(t *testing.T) {
	t.Parallel()
	h, store, pub, _ := newTestAPI(t)
	token, ownerID := ownerToken(t)

	got := registerVehicleOverHTTP(t, h, token)
	assert.Equal(t, "1HGCM82633A004352", got["vin"], "response must carry the normalized VIN")
	assert.Equal(t, ownerID.String(), got["owner_user_id"])
	assert.Equal(t, "Honda", got["make"])
	assert.Equal(t, float64(2020), got["year_of_manufacture"])
	assert.NotEmpty(t, got["id"])
	assert.NotEmpty(t, got["created_at"])

	require.Len(t, store.vehicles, 1)

	_, events := pub.published()
	require.Len(t, events, 1)
	assert.Equal(t, EventVehicleCreated, events[0].EventType)
}

func TestCreateVehicleWithTenantClaim(t *testing.T) {
	t.Parallel()
	h, _, _, _ := newTestAPI(t)
	tenant := uuid.New()
	token := mintToken(t, uuid.New().String(), tenant.String(), "CUSTOMER")

	got := registerVehicleOverHTTP(t, h, token)
	require.NotNil(t, got["tenant_id"])
	assert.Equal(t, tenant.String(), got["tenant_id"])
}

func TestCreateVehicleRequiresAuth(t *testing.T) {
	t.Parallel()
	h, _, _, _ := newTestAPI(t)

	w := do(t, h, http.MethodPost, "/v1/vehicles", "", []byte(`{"vin":"1HGCM82633A004352","make":"H","model":"A","year_of_manufacture":2020}`))
	require.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "unauthorized", decodeBody(t, w)["code"])
}

func TestCreateVehicleValidationFailures(t *testing.T) {
	t.Parallel()
	h, _, _, _ := newTestAPI(t)
	token, _ := ownerToken(t)

	cases := []struct {
		name string
		body string
	}{
		{name: "bad checksum", body: `{"vin":"1HGCM82633A004353","make":"Honda","model":"Accord","year_of_manufacture":2020}`},
		{name: "missing make", body: `{"vin":"1HGCM82633A004352","model":"Accord","year_of_manufacture":2020}`},
		{name: "year out of range", body: `{"vin":"1HGCM82633A004352","make":"Honda","model":"Accord","year_of_manufacture":1900}`},
		{name: "malformed json", body: `{"vin":`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := do(t, h, http.MethodPost, "/v1/vehicles", token, []byte(tc.body))
			require.Equal(t, http.StatusUnprocessableEntity, w.Code)
			assert.Equal(t, "validation_failed", decodeBody(t, w)["code"])
		})
	}
}

func TestCreateVehicleDuplicateVINConflict(t *testing.T) {
	t.Parallel()
	h, _, _, _ := newTestAPI(t)
	first, _ := ownerToken(t)
	registerVehicleOverHTTP(t, h, first)

	// A different owner registers the same VIN (raw form) -> 409.
	second := mintToken(t, uuid.New().String(), "", "CUSTOMER")
	body := []byte(`{"vin":" 1HGCM82633A004352 ","make":"Honda","model":"Civic","year_of_manufacture":2021}`)
	w := do(t, h, http.MethodPost, "/v1/vehicles", second, body)
	require.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, "conflict", decodeBody(t, w)["code"])
}

func TestGetVehicleEndpoint(t *testing.T) {
	t.Parallel()
	h, _, _, _ := newTestAPI(t)
	token, _ := ownerToken(t)
	created := registerVehicleOverHTTP(t, h, token)
	id := created["id"].(string)

	w := do(t, h, http.MethodGet, "/v1/vehicles/"+id, token, nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, id, decodeBody(t, w)["id"])

	t.Run("unknown vehicle is 404", func(t *testing.T) {
		w := do(t, h, http.MethodGet, "/v1/vehicles/"+uuid.New().String(), token, nil)
		require.Equal(t, http.StatusNotFound, w.Code)
		assert.Equal(t, "not_found", decodeBody(t, w)["code"])
	})

	t.Run("malformed id is 422", func(t *testing.T) {
		w := do(t, h, http.MethodGet, "/v1/vehicles/not-a-uuid", token, nil)
		require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	})
}

func TestGetVehiclePassportEndpoint(t *testing.T) {
	t.Parallel()
	h, _, _, svc := newTestAPI(t)
	token, _ := ownerToken(t)
	created := registerVehicleOverHTTP(t, h, token)
	id := created["id"].(string)

	// One extra history event so the passport count is meaningful.
	require.NoError(t, svc.RecordEvent(context.Background(), &HistoryEvent{
		VehicleID:  uuid.MustParse(id),
		EventType:  HistoryEventTypeRepair,
		Summary:    "Replaced water pump",
		OccurredAt: time.Now().UTC(),
	}))

	w := do(t, h, http.MethodGet, "/v1/vehicles/"+id+"/passport", token, nil)
	require.Equal(t, http.StatusOK, w.Code)
	got := decodeBody(t, w)
	assert.Equal(t, float64(2), got["service_event_count"], "note plus repair")
	assert.Equal(t, id, got["id"])
	assert.Equal(t, "1HGCM82633A004352", got["vin"])
}

func TestGetVehicleHistoryEndpoint(t *testing.T) {
	t.Parallel()
	h, _, _, svc := newTestAPI(t)
	token, _ := ownerToken(t)
	created := registerVehicleOverHTTP(t, h, token)
	id := created["id"].(string)

	base := time.Now().UTC()
	for i, summary := range []string{"first", "second", "third"} {
		require.NoError(t, svc.RecordEvent(context.Background(), &HistoryEvent{
			VehicleID:  uuid.MustParse(id),
			EventType:  HistoryEventTypeNote,
			Summary:    summary,
			OccurredAt: base.Add(time.Duration(i+1) * time.Minute),
		}))
	}

	t.Run("newest first", func(t *testing.T) {
		w := do(t, h, http.MethodGet, "/v1/vehicles/"+id+"/history", token, nil)
		require.Equal(t, http.StatusOK, w.Code)
		var events []map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &events))
		require.Len(t, events, 4, "note plus three recorded events")
		assert.Equal(t, "third", events[0]["summary"])
		assert.Equal(t, "second", events[1]["summary"])
		assert.Equal(t, "first", events[2]["summary"])
		assert.Equal(t, "Vehicle registered on Motivra", events[3]["summary"])
	})

	t.Run("limit and offset paginate", func(t *testing.T) {
		w := do(t, h, http.MethodGet, "/v1/vehicles/"+id+"/history?limit=2&offset=1", token, nil)
		require.Equal(t, http.StatusOK, w.Code)
		var events []map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &events))
		require.Len(t, events, 2)
		assert.Equal(t, "second", events[0]["summary"])
	})

	t.Run("invalid limit is 422", func(t *testing.T) {
		w := do(t, h, http.MethodGet, "/v1/vehicles/"+id+"/history?limit=zero", token, nil)
		require.Equal(t, http.StatusUnprocessableEntity, w.Code)
		assert.Equal(t, "validation_failed", decodeBody(t, w)["code"])
	})

	t.Run("registration note is listed for a fresh vehicle", func(t *testing.T) {
		other, _ := ownerToken(t)
		otherCreated := registerVINOverHTTP(t, h, other, "1M8GDM9AXKP042788")
		w := do(t, h, http.MethodGet, "/v1/vehicles/"+otherCreated["id"].(string)+"/history", other, nil)
		require.Equal(t, http.StatusOK, w.Code)
		var events []map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &events))
		require.Len(t, events, 1, "registration note")
	})
}

func TestRecordMileageEndpointAsOwner(t *testing.T) {
	t.Parallel()
	h, store, _, _ := newTestAPI(t)
	token, _ := ownerToken(t)
	created := registerVehicleOverHTTP(t, h, token)
	id := created["id"].(string)

	w := do(t, h, http.MethodPost, "/v1/vehicles/"+id+"/mileage", token, []byte(`{"odometer_km":152300}`))
	require.Equal(t, http.StatusNoContent, w.Code)
	require.Empty(t, w.Body.String())

	got := store.vehicles[uuid.MustParse(id)]
	assert.Equal(t, int64(152300), got.MileageLatestKm)
}

func TestRecordMileageForbiddenForOtherCustomer(t *testing.T) {
	t.Parallel()
	h, _, _, _ := newTestAPI(t)
	token, _ := ownerToken(t)
	created := registerVehicleOverHTTP(t, h, token)

	other := mintToken(t, uuid.New().String(), "", "CUSTOMER")
	w := do(t, h, http.MethodPost, "/v1/vehicles/"+created["id"].(string)+"/mileage", other, []byte(`{"odometer_km":100}`))
	require.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "forbidden", decodeBody(t, w)["code"])
}

func TestRecordMileageAllowedForAdmin(t *testing.T) {
	t.Parallel()
	h, store, _, _ := newTestAPI(t)
	token, _ := ownerToken(t)
	created := registerVehicleOverHTTP(t, h, token)
	id := created["id"].(string)

	admin := mintToken(t, uuid.New().String(), "", RoleAdmin)
	w := do(t, h, http.MethodPost, "/v1/vehicles/"+id+"/mileage", admin, []byte(`{"odometer_km":900}`))
	require.Equal(t, http.StatusNoContent, w.Code)

	got := store.vehicles[uuid.MustParse(id)]
	assert.Equal(t, int64(900), got.MileageLatestKm)
}

func TestRecordMileageFailurePaths(t *testing.T) {
	t.Parallel()
	h, _, _, svc := newTestAPI(t)
	token, _ := ownerToken(t)
	created := registerVehicleOverHTTP(t, h, token)
	id := created["id"].(string)

	t.Run("negative km is 422", func(t *testing.T) {
		w := do(t, h, http.MethodPost, "/v1/vehicles/"+id+"/mileage", token, []byte(`{"odometer_km":-5}`))
		require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	})

	t.Run("missing odometer_km is 422", func(t *testing.T) {
		w := do(t, h, http.MethodPost, "/v1/vehicles/"+id+"/mileage", token, []byte(`{}`))
		require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	})

	t.Run("unknown vehicle is 404", func(t *testing.T) {
		w := do(t, h, http.MethodPost, "/v1/vehicles/"+uuid.New().String()+"/mileage", token, []byte(`{"odometer_km":10}`))
		require.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("malformed id is 422", func(t *testing.T) {
		w := do(t, h, http.MethodPost, "/v1/vehicles/nope/mileage", token, []byte(`{"odometer_km":10}`))
		require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	})

	t.Run("decrease is 409", func(t *testing.T) {
		require.NoError(t, svc.RecordMileage(context.Background(), uuid.MustParse(id), 2000, nil))
		w := do(t, h, http.MethodPost, "/v1/vehicles/"+id+"/mileage", token, []byte(`{"odometer_km":1999}`))
		require.Equal(t, http.StatusConflict, w.Code)
		assert.Equal(t, "conflict", decodeBody(t, w)["code"])
	})
}

func TestCanMutate(t *testing.T) {
	t.Parallel()
	owner := uuid.New()
	v := Vehicle{OwnerUserID: owner}

	assert.True(t, canMutate(platform.Claims{UserID: owner.String(), Role: "CUSTOMER"}, v), "owner may mutate")
	assert.True(t, canMutate(platform.Claims{UserID: uuid.New().String(), Role: RoleAdmin}, v), "admin may mutate")
	assert.True(t, canMutate(platform.Claims{UserID: uuid.New().String(), Role: RoleSuperAdmin}, v), "super admin may mutate")
	assert.False(t, canMutate(platform.Claims{UserID: uuid.New().String(), Role: "CUSTOMER"}, v), "another customer may not")
	assert.False(t, canMutate(platform.Claims{UserID: "not-a-uuid", Role: "CUSTOMER"}, v), "unparseable subject may not")
}

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
	tenant := uuid.New()
	ownerWithTenant := mintToken(t, uuid.New().String(), tenant.String(), "CUSTOMER")
	created := registerVINOverHTTP(t, h, ownerWithTenant, "1HGCM82633A004352")
	id := created["id"].(string)

	// A tenant colleague sees the vehicle through the tenant scope but is
	// neither the owner nor an administrator: 403.
	colleague := mintToken(t, uuid.New().String(), tenant.String(), "CUSTOMER")
	w := do(t, h, http.MethodPost, "/v1/vehicles/"+id+"/mileage", colleague, []byte(`{"odometer_km":100}`))
	require.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "forbidden", decodeBody(t, w)["code"])

	// A personal customer outside the tenant cannot even see the vehicle:
	// scoped reads answer 404, never the row (ADR-0004).
	other := mintToken(t, uuid.New().String(), "", "CUSTOMER")
	w = do(t, h, http.MethodPost, "/v1/vehicles/"+id+"/mileage", other, []byte(`{"odometer_km":100}`))
	require.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, "not_found", decodeBody(t, w)["code"])
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
		require.NoError(t, svc.RecordMileage(context.Background(), uuid.MustParse(id), 2000, nil, VehicleScope{}))
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

// seedVehicle inserts a vehicle directly into the fake store, bypassing
// registration so listing tests can control created_at and IDs
// deterministically. VINs are structurally valid and unique per test.
func seedVehicle(t *testing.T, store *fakeStore, tenant *uuid.UUID, owner uuid.UUID, createdAt time.Time) Vehicle {
	t.Helper()
	v := Vehicle{
		ID:              uuid.New(),
		TenantID:        tenant,
		OwnerUserID:     owner,
		VIN:             randomVIN(t),
		Make:            "Honda",
		Model:           "Accord",
		Year:            2020,
		MileageLatestKm: 10,
		CreatedAt:       createdAt,
		UpdatedAt:       createdAt,
	}
	store.mu.Lock()
	store.vehicles[v.ID] = v
	store.mu.Unlock()
	return v
}

// decodeList decodes a listing page response body.
func decodeList(t *testing.T, w *httptest.ResponseRecorder) (ids []string, nextCursor *string) {
	t.Helper()
	var got struct {
		Vehicles   []map[string]any `json:"vehicles"`
		NextCursor *string          `json:"next_cursor"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	for _, v := range got.Vehicles {
		ids = append(ids, v["id"].(string))
	}
	return ids, got.NextCursor
}

func TestListVehiclesEndpoint(t *testing.T) {
	t.Parallel()
	h, store, _, _ := newTestAPI(t)
	tenant := uuid.New()
	token := mintToken(t, uuid.New().String(), tenant.String(), "CUSTOMER")

	base := time.Now().UTC().Add(-time.Hour)
	first := seedVehicle(t, store, &tenant, uuid.New(), base)
	second := seedVehicle(t, store, &tenant, uuid.New(), base.Add(time.Minute))

	w := do(t, h, http.MethodGet, "/v1/vehicles", token, nil)
	require.Equal(t, http.StatusOK, w.Code)
	ids, next := decodeList(t, w)
	require.Len(t, ids, 2)
	assert.Equal(t, second.ID.String(), ids[0], "newest first")
	assert.Equal(t, first.ID.String(), ids[1])
	assert.Nil(t, next, "a complete page has no next cursor")

	// Passport deep-link integrity: every row carries the registry id the
	// web app needs for /dashboard/vehicles/{vehicleId}.
	for _, v := range store.vehicles {
		if v.TenantID != nil && *v.TenantID == tenant {
			row := w.Body.String()
			assert.Contains(t, row, v.ID.String())
		}
	}
}

func TestListVehiclesPersonalScope(t *testing.T) {
	t.Parallel()
	h, store, _, _ := newTestAPI(t)
	owner := uuid.New()
	token := mintToken(t, owner.String(), "", "CUSTOMER")

	base := time.Now().UTC()
	older := seedVehicle(t, store, nil, owner, base.Add(-2*time.Hour))
	newer := seedVehicle(t, store, nil, owner, base)
	// Own vehicle registered under a tenant: visible to tenant tokens, not
	// to the caller's personal token.
	tenanted := uuid.New()
	seedVehicle(t, store, &tenanted, owner, base.Add(-time.Minute))
	// Another user's personal vehicle: never in this listing.
	seedVehicle(t, store, nil, uuid.New(), base.Add(-time.Hour))

	w := do(t, h, http.MethodGet, "/v1/vehicles", token, nil)
	require.Equal(t, http.StatusOK, w.Code)
	ids, next := decodeList(t, w)
	require.Len(t, ids, 2, "only the caller's own personal vehicles")
	assert.Equal(t, []string{newer.ID.String(), older.ID.String()}, ids)
	assert.Nil(t, next)
}

func TestListVehiclesTenantIsolation(t *testing.T) {
	t.Parallel()
	h, store, _, _ := newTestAPI(t)
	tenantA, tenantB := uuid.New(), uuid.New()
	base := time.Now().UTC()
	a1 := seedVehicle(t, store, &tenantA, uuid.New(), base)
	a2 := seedVehicle(t, store, &tenantA, uuid.New(), base.Add(2*time.Minute))
	seedVehicle(t, store, &tenantB, uuid.New(), base.Add(time.Minute))
	seedVehicle(t, store, nil, uuid.New(), base.Add(3*time.Minute))

	tokenA := mintToken(t, uuid.New().String(), tenantA.String(), "CUSTOMER")
	w := do(t, h, http.MethodGet, "/v1/vehicles", tokenA, nil)
	require.Equal(t, http.StatusOK, w.Code)
	ids, _ := decodeList(t, w)
	require.Len(t, ids, 2, "tenant A sees only tenant A rows, never tenant B or personal rows")
	assert.ElementsMatch(t, []string{a1.ID.String(), a2.ID.String()}, ids)

	tokenB := mintToken(t, uuid.New().String(), tenantB.String(), "CUSTOMER")
	w = do(t, h, http.MethodGet, "/v1/vehicles", tokenB, nil)
	require.Equal(t, http.StatusOK, w.Code)
	ids, _ = decodeList(t, w)
	require.Len(t, ids, 1, "tenant B sees none of tenant A's rows")

	// Control-plane roles read across tenants under the ADR-0004 audit
	// obligation: the zero-value scope is unrestricted for them only.
	admin := mintToken(t, uuid.New().String(), "", RoleAdmin)
	w = do(t, h, http.MethodGet, "/v1/vehicles", admin, nil)
	require.Equal(t, http.StatusOK, w.Code)
	ids, _ = decodeList(t, w)
	assert.Len(t, ids, 4)
}

func TestListVehiclesCursorPagination(t *testing.T) {
	t.Parallel()
	h, store, _, _ := newTestAPI(t)
	tenant := uuid.New()
	token := mintToken(t, uuid.New().String(), tenant.String(), "CUSTOMER")

	base := time.Now().UTC().Add(-time.Hour)
	// v0 oldest ... v4 newest, distinct keyset positions.
	ordered := make([]Vehicle, 0, 5)
	for i := 0; i < 5; i++ {
		ordered = append(ordered, seedVehicle(t, store, &tenant, uuid.New(), base.Add(time.Duration(i)*time.Minute)))
	}

	// Page 1: newest two, plus an exact next cursor.
	w := do(t, h, http.MethodGet, "/v1/vehicles?limit=2", token, nil)
	require.Equal(t, http.StatusOK, w.Code)
	ids, cursor := decodeList(t, w)
	require.Equal(t, []string{ordered[4].ID.String(), ordered[3].ID.String()}, ids)
	require.NotNil(t, cursor, "more pages exist")

	// A registration landing between pages must not shift the next page
	// (keyset stability) — it merely joins the head of the listing.
	seedVehicle(t, store, &tenant, uuid.New(), base.Add(10*time.Minute))

	// Page 2 follows the cursor exactly.
	w = do(t, h, http.MethodGet, "/v1/vehicles?limit=2&cursor="+*cursor, token, nil)
	require.Equal(t, http.StatusOK, w.Code)
	ids, cursor = decodeList(t, w)
	require.Equal(t, []string{ordered[2].ID.String(), ordered[1].ID.String()}, ids)
	require.NotNil(t, cursor)

	// Page 3 is the last: no next cursor, no repeats, no gaps.
	w = do(t, h, http.MethodGet, "/v1/vehicles?limit=2&cursor="+*cursor, token, nil)
	require.Equal(t, http.StatusOK, w.Code)
	ids, cursor = decodeList(t, w)
	require.Equal(t, []string{ordered[0].ID.String()}, ids)
	assert.Nil(t, cursor, "the final page has no next cursor")

	// A cursor at the exact tail keeps returning the empty final page.
	tailCursor, err := EncodeVehicleCursor(VehicleCursor{CreatedAt: ordered[0].CreatedAt, ID: ordered[0].ID})
	require.NoError(t, err)
	w = do(t, h, http.MethodGet, "/v1/vehicles?limit=2&cursor="+tailCursor, token, nil)
	require.Equal(t, http.StatusOK, w.Code)
	ids, next := decodeList(t, w)
	assert.Empty(t, ids)
	assert.Nil(t, next)
}

func TestListVehiclesRequiresAuth(t *testing.T) {
	t.Parallel()
	h, _, _, _ := newTestAPI(t)

	w := do(t, h, http.MethodGet, "/v1/vehicles", "", nil)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "unauthorized", decodeBody(t, w)["code"])
}

func TestListVehiclesInvalidParams(t *testing.T) {
	t.Parallel()
	h, store, _, _ := newTestAPI(t)
	tenant := uuid.New()
	token := mintToken(t, uuid.New().String(), tenant.String(), "CUSTOMER")
	seedVehicle(t, store, &tenant, uuid.New(), time.Now().UTC())

	cases := []struct {
		name  string
		query string
	}{
		{name: "non-integer limit", query: "?limit=soon"},
		{name: "zero limit", query: "?limit=0"},
		{name: "negative limit", query: "?limit=-3"},
		{name: "malformed cursor", query: "?cursor=not-a-cursor"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := do(t, h, http.MethodGet, "/v1/vehicles"+tc.query, token, nil)
			require.Equal(t, http.StatusUnprocessableEntity, w.Code)
			assert.Equal(t, "validation_failed", decodeBody(t, w)["code"])
		})
	}

	t.Run("empty cursor is treated as absent, like every optional param", func(t *testing.T) {
		// House convention (mirrored from the jobs domain): an empty
		// optional query value means "not provided", so ?cursor= starts
		// a fresh listing instead of failing.
		w := do(t, h, http.MethodGet, "/v1/vehicles?cursor=", token, nil)
		require.Equal(t, http.StatusOK, w.Code)
		ids, next := decodeList(t, w)
		require.Len(t, ids, 1)
		assert.Nil(t, next)
	})

	t.Run("limit above 100 is capped, not rejected", func(t *testing.T) {
		w := do(t, h, http.MethodGet, "/v1/vehicles?limit=1000", token, nil)
		require.Equal(t, http.StatusOK, w.Code)
		ids, next := decodeList(t, w)
		require.Len(t, ids, 1)
		assert.Nil(t, next)
	})
}

func TestScopeFromClaims(t *testing.T) {
	t.Parallel()
	tenant := uuid.New()

	tenantScope, err := scopeFromClaims(platform.Claims{UserID: uuid.New().String(), TenantID: tenant.String(), Role: "CUSTOMER"})
	require.NoError(t, err)
	require.NotNil(t, tenantScope.TenantID)
	assert.Equal(t, tenant, *tenantScope.TenantID)
	assert.Nil(t, tenantScope.OwnerID)

	owner := uuid.New()
	personalScope, err := scopeFromClaims(platform.Claims{UserID: owner.String(), Role: "CUSTOMER"})
	require.NoError(t, err)
	assert.Nil(t, personalScope.TenantID)
	require.NotNil(t, personalScope.OwnerID)
	assert.Equal(t, owner, *personalScope.OwnerID)

	adminScope, err := scopeFromClaims(platform.Claims{UserID: uuid.New().String(), Role: RoleSuperAdmin})
	require.NoError(t, err)
	assert.True(t, adminScope.Unrestricted(), "control-plane roles are unrestricted under audit")

	_, err = scopeFromClaims(platform.Claims{UserID: uuid.New().String(), TenantID: "not-a-uuid", Role: "CUSTOMER"})
	require.Error(t, err, "malformed tenant claim is an authentication failure")
	_, err = scopeFromClaims(platform.Claims{UserID: "not-a-uuid", Role: "CUSTOMER"})
	require.Error(t, err, "malformed subject claim is an authentication failure")
}

func TestVehicleCursorRoundTrip(t *testing.T) {
	t.Parallel()
	c := VehicleCursor{CreatedAt: time.Date(2026, 9, 9, 10, 11, 12, 123456789, time.UTC), ID: uuid.New()}
	encoded, err := EncodeVehicleCursor(c)
	require.NoError(t, err)
	assert.NotContains(t, encoded, "/", "cursor must be URL-safe")

	decoded, err := ParseVehicleCursor(encoded)
	require.NoError(t, err)
	assert.True(t, c.CreatedAt.Equal(decoded.CreatedAt), "nanosecond precision must survive")
	assert.Equal(t, c.ID, decoded.ID)

	for _, raw := range []string{"", "not-a-cursor", "###", "v1." + encoded} {
		_, err := ParseVehicleCursor(raw)
		require.Error(t, err, "cursor %q must be rejected", raw)
	}

	missingID, err := EncodeVehicleCursor(VehicleCursor{CreatedAt: time.Now().UTC()})
	require.Error(t, err, "a cursor without an id must not encode")
	assert.Empty(t, missingID)
}

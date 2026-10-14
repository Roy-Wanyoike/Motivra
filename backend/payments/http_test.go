package payments

import (
	"bytes"
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

// The HTTP tests pin the route contract end-to-end over the fake store:
// RBAC gating (money-mover vs operator roles), tenant scoping derived from
// the token claims, the signature-verified webhook receiver (no JWT) and
// the operator reconciliation trigger.

const httpTestSecret = "payments-http-test-secret-1234"

// mintToken issues a valid HS256 access token for the given identity.
func mintToken(t *testing.T, sub, role, tenantID string) string {
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

// newTestAPI wires the router exactly like cmd/payments: NO global auth
// middleware (the webhook receiver must stay JWT-free), per-route JWT +
// role gates through the requireAuth closure, sandbox provider, capturing
// publisher and a reconciliation over the same fake store.
func newTestAPI(t *testing.T) (http.Handler, *fakeStore, *capturePublisher, *SandboxProvider) {
	t.Helper()
	store := newFakeStore()
	pub := &capturePublisher{}
	sandbox := NewSandboxProvider([]byte(testWebhookSecret))
	svc := NewService(store, map[string]Provider{sandbox.Name(): sandbox}, pub, nil)
	rec := NewReconciler(store, map[string]Provider{sandbox.Name(): sandbox}, pub, nil, DefaultStaleAfter)

	validator := platform.NewJWTValidator(httpTestSecret, "motivra-identity", "motivra")
	router := chi.NewRouter()
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			platform.AuthMiddleware(validator)(platform.RequireAuthenticated(h)).ServeHTTP(w, r)
		}
	}
	Routes(router, svc, rec, authed)
	return router, store, pub, sandbox
}

func do(t *testing.T, h http.Handler, method, path, token string, body []byte, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for k, vs := range header {
		for _, v := range vs {
			r.Header.Add(k, v)
		}
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

// initiateOverHTTP drives an initiation through the API and returns the
// decoded response body.
func initiateOverHTTP(t *testing.T, h http.Handler, token string, tenantID string, amountMinor int64) map[string]any {
	t.Helper()
	body := []byte(fmt.Sprintf(`{"amount_minor":%d,"currency":"KES","description":"http fixture"}`, amountMinor))
	w := do(t, h, http.MethodPost, "/v1/payments/intents", token, body, nil)
	require.Equal(t, http.StatusCreated, w.Code, "initiate: %s", w.Body.String())
	return decodeBody(t, w)
}

// settleSandboxOverHTTP settles a sandbox transaction through the webhook
// route (the exact local-dev flow the README documents).
func settleSandboxOverHTTP(t *testing.T, h http.Handler, sandbox *SandboxProvider, providerRef string, amountMinor int64) map[string]any {
	t.Helper()
	now := time.Now()
	body, err := SandboxWebhookBody(WebhookEventTransactionSucceeded, providerRef, amountMinor, "", now)
	require.NoError(t, err)
	hdr := http.Header{}
	hdr.Set(WebhookSignatureHeader, SignWebhookPayload(sandbox.WebhookSecret, body, now))
	w := do(t, h, http.MethodPost, "/v1/payments/webhooks/sandbox", "", body, hdr)
	require.Equal(t, http.StatusOK, w.Code, "webhook: %s", w.Body.String())
	return decodeBody(t, w)
}

func TestInitiateEndpointRequiresMoneyMoverRole(t *testing.T) {
	h, _, _, _ := newTestAPI(t)

	for _, role := range []string{RoleGarage, RoleDispatcher, RoleAdmin, RoleSuperAdmin} {
		t.Run(role, func(t *testing.T) {
			token := mintToken(t, uuid.New().String(), role, "")
			body := []byte(`{"amount_minor":25000,"currency":"KES"}`)
			w := do(t, h, http.MethodPost, "/v1/payments/intents", token, body, nil)
			require.Equal(t, http.StatusCreated, w.Code, "role %s: %s", role, w.Body.String())
		})
	}

	for _, role := range []string{RoleCustomer, RoleTechnician} {
		t.Run(role, func(t *testing.T) {
			token := mintToken(t, uuid.New().String(), role, "")
			w := do(t, h, http.MethodPost, "/v1/payments/intents", token, []byte(`{"amount_minor":25000,"currency":"KES"}`), nil)
			require.Equal(t, http.StatusForbidden, w.Code, "role %s must never move money", role)
		})
	}

	// No token at all is a 401.
	w := do(t, h, http.MethodPost, "/v1/payments/intents", "", []byte(`{"amount_minor":25000,"currency":"KES"}`), nil)
	require.Equal(t, http.StatusUnauthorized, w.Code)

	// A tampered token is a 401, not a pass-through.
	w = do(t, h, http.MethodPost, "/v1/payments/intents", "not-a-jwt", []byte(`{"amount_minor":25000,"currency":"KES"}`), nil)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestInitiateEndpointTenantFromClaims(t *testing.T) {
	h, _, _, _ := newTestAPI(t)
	tenant := uuid.New().String()
	token := mintToken(t, uuid.New().String(), RoleGarage, tenant)

	body := decodeBody(t, do(t, h, http.MethodPost, "/v1/payments/intents", token,
		[]byte(`{"amount_minor":25000,"currency":"KES"}`), nil))
	intent := body["intent"].(map[string]any)
	assert.Equal(t, "PROCESSING", intent["status"])
	assert.Equal(t, float64(25000), intent["amount_minor"])
	assert.Equal(t, tenant, intent["tenant_id"], "the tenant comes from the claims, never the body")
	// A body trying to smuggle a tenant changes nothing (unknown fields are
	// ignored; tenant is not an accepted input).
	require.NotNil(t, intent["created_by"])

	// The transaction arrived PENDING.
	tx := body["transaction"].(map[string]any)
	assert.Equal(t, "PENDING", tx["status"])
}

func TestIntentIsolationOverHTTP(t *testing.T) {
	h, _, _, _ := newTestAPI(t)
	tenantA := uuid.New().String()
	tenantB := uuid.New().String()
	garageA := uuid.New()

	tokenA := mintToken(t, garageA.String(), RoleGarage, tenantA)
	created := initiateOverHTTP(t, h, tokenA, tenantA, 12_000)
	intentID := created["intent"].(map[string]any)["id"].(string)
	txID := created["transaction"].(map[string]any)["id"].(string)

	// The owner sees it; another tenant does not (404 = no existence leak).
	w := do(t, h, http.MethodGet, "/v1/payments/intents/"+intentID, tokenA, nil, nil)
	require.Equal(t, http.StatusOK, w.Code)

	tokenB := mintToken(t, uuid.New().String(), RoleGarage, tenantB)
	w = do(t, h, http.MethodGet, "/v1/payments/intents/"+intentID, tokenB, nil, nil)
	require.Equal(t, http.StatusNotFound, w.Code, "another tenant must read nothing")
	w = do(t, h, http.MethodGet, "/v1/payments/transactions/"+txID, tokenB, nil, nil)
	require.Equal(t, http.StatusNotFound, w.Code)
	w = do(t, h, http.MethodGet, "/v1/payments/intents/"+intentID+"/transactions", tokenB, nil, nil)
	require.Equal(t, http.StatusNotFound, w.Code)

	// The tenant-scoped list only carries the tenant's own intents.
	w = do(t, h, http.MethodGet, "/v1/payments/intents", tokenB, nil, nil)
	require.Equal(t, http.StatusOK, w.Code)
	var list []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	assert.Empty(t, list, "tenant B sees zero intents")

	w = do(t, h, http.MethodGet, "/v1/payments/intents", tokenA, nil, nil)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	require.Len(t, list, 1)

	// Operators (control-plane roles) read across tenants.
	tokenOp := mintToken(t, uuid.New().String(), RoleSuperAdmin, "")
	w = do(t, h, http.MethodGet, "/v1/payments/intents", tokenOp, nil, nil)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	assert.Len(t, list, 1, "the operator sees the tenant's intent")
}

func TestRefundEndpointRoleAndStateGating(t *testing.T) {
	h, _, _, sandbox := newTestAPI(t)
	tenant := uuid.New().String()
	creator := uuid.New()
	token := mintToken(t, creator.String(), RoleGarage, tenant)

	created := initiateOverHTTP(t, h, token, tenant, 9_000)
	intentID := created["intent"].(map[string]any)["id"].(string)
	providerRef := created["intent"].(map[string]any)["provider_ref"].(string)

	// Refunding a PROCESSING intent conflicts.
	w := do(t, h, http.MethodPost, "/v1/payments/intents/"+intentID+"/refund", token, []byte(`{"reason":"early"}`), nil)
	require.Equal(t, http.StatusConflict, w.Code)

	// Settle it through the webhook route, then refund.
	settleSandboxOverHTTP(t, h, sandbox, providerRef, 9_000)

	// A CUSTOMER of the same tenant can read but never refund.
	customerToken := mintToken(t, uuid.New().String(), RoleCustomer, tenant)
	w = do(t, h, http.MethodGet, "/v1/payments/intents/"+intentID, customerToken, nil, nil)
	require.Equal(t, http.StatusOK, w.Code, "the payer reads their own tenant's payments")
	w = do(t, h, http.MethodPost, "/v1/payments/intents/"+intentID+"/refund", customerToken, nil, nil)
	require.Equal(t, http.StatusForbidden, w.Code)

	// The money-mover refunds in full (optional body: bare POST works).
	w = do(t, h, http.MethodPost, "/v1/payments/intents/"+intentID+"/refund", token, nil, nil)
	require.Equal(t, http.StatusCreated, w.Code, "refund: %s", w.Body.String())
	refund := decodeBody(t, w)
	assert.Equal(t, "COMPLETED", refund["status"])
	assert.Equal(t, float64(9000), refund["amount_minor"], "full refund")

	// A second refund conflicts.
	w = do(t, h, http.MethodPost, "/v1/payments/intents/"+intentID+"/refund", token, nil, nil)
	require.Equal(t, http.StatusConflict, w.Code)

	// The refund is fetchable through the tenant scope.
	refundID := refund["id"].(string)
	w = do(t, h, http.MethodGet, "/v1/payments/refunds/"+refundID, token, nil, nil)
	require.Equal(t, http.StatusOK, w.Code)
	w = do(t, h, http.MethodGet, "/v1/payments/refunds/"+refundID, mintToken(t, uuid.New().String(), RoleGarage, uuid.New().String()), nil, nil)
	require.Equal(t, http.StatusNotFound, w.Code, "another tenant reads nothing")
}

func TestWebhookEndpointTrustBoundaryAndReplay(t *testing.T) {
	h, _, _, sandbox := newTestAPI(t)
	tenant := uuid.New().String()
	token := mintToken(t, uuid.New().String(), RoleGarage, tenant)
	created := initiateOverHTTP(t, h, token, tenant, 7_700)
	intentID := created["intent"].(map[string]any)["id"].(string)
	providerRef := created["intent"].(map[string]any)["provider_ref"].(string)

	now := time.Now()
	body, err := SandboxWebhookBody(WebhookEventTransactionSucceeded, providerRef, 7_700, "", now)
	require.NoError(t, err)
	sig := http.Header{}
	sig.Set(WebhookSignatureHeader, SignWebhookPayload(sandbox.WebhookSecret, body, now))

	// Unsigned: 401 — the trust boundary is the route's front door.
	w := do(t, h, http.MethodPost, "/v1/payments/webhooks/sandbox", "", body, nil)
	require.Equal(t, http.StatusUnauthorized, w.Code)

	// Wrong secret: 401.
	attacker := http.Header{}
	attacker.Set(WebhookSignatureHeader, SignWebhookPayload([]byte("wrong"), body, now))
	w = do(t, h, http.MethodPost, "/v1/payments/webhooks/sandbox", "", body, attacker)
	require.Equal(t, http.StatusUnauthorized, w.Code)

	// Unknown provider path: 404.
	w = do(t, h, http.MethodPost, "/v1/payments/webhooks/unknown", "", body, sig)
	require.Equal(t, http.StatusNotFound, w.Code)

	// First delivery applies; the N replays are absorbed as duplicates.
	first := decodeBody(t, do(t, h, http.MethodPost, "/v1/payments/webhooks/sandbox", "", body, sig))
	assert.Equal(t, "applied", first["status"])
	assert.Equal(t, intentID, first["intent_id"])
	for i := 0; i < 4; i++ {
		replay := decodeBody(t, do(t, h, http.MethodPost, "/v1/payments/webhooks/sandbox", "", body, sig))
		assert.Equal(t, "duplicate", replay["status"], "replay %d", i)
		assert.Equal(t, true, replay["duplicate"])
	}
}

func TestWebhookEndpointRejectsAmountMismatch(t *testing.T) {
	h, _, _, sandbox := newTestAPI(t)
	tenant := uuid.New().String()
	token := mintToken(t, uuid.New().String(), RoleGarage, tenant)
	created := initiateOverHTTP(t, h, token, tenant, 7_700)
	providerRef := created["intent"].(map[string]any)["provider_ref"].(string)

	now := time.Now()
	body, err := SandboxWebhookBody(WebhookEventTransactionSucceeded, providerRef, 999_999, "", now)
	require.NoError(t, err)
	sig := http.Header{}
	sig.Set(WebhookSignatureHeader, SignWebhookPayload(sandbox.WebhookSecret, body, now))
	w := do(t, h, http.MethodPost, "/v1/payments/webhooks/sandbox", "", body, sig)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, "attested amount must equal the attempted amount")
}

func TestReconcileEndpointOperatorOnly(t *testing.T) {
	h, store, _, sandbox := newTestAPI(t)
	tenant := uuid.New().String()
	token := mintToken(t, uuid.New().String(), RoleGarage, tenant)

	// A stale pending payment: the missed-webhook world.
	created := initiateOverHTTP(t, h, token, tenant, 5_500)
	intentID := created["intent"].(map[string]any)["id"].(string)
	txID := created["transaction"].(map[string]any)["id"].(string)
	store.BackdateTransaction(mustParseUUID(t, txID), 10*DefaultStaleAfter)
	sandbox.SetOutcome(mustParseUUID(t, txID), TxSucceeded)

	// GARAGE is refused; ADMIN runs the batch and repairs the drift.
	w := do(t, h, http.MethodPost, "/v1/payments/reconcile", token, nil, nil)
	require.Equal(t, http.StatusForbidden, w.Code)

	admin := mintToken(t, uuid.New().String(), RoleAdmin, "")
	w = do(t, h, http.MethodPost, "/v1/payments/reconcile", admin, nil, nil)
	require.Equal(t, http.StatusOK, w.Code, "reconcile: %s", w.Body.String())
	result := decodeBody(t, w)
	assert.Equal(t, float64(1), result["scanned"])
	assert.Equal(t, float64(1), result["repaired"])

	// Convergence: the intent is settled.
	w = do(t, h, http.MethodGet, "/v1/payments/intents/"+intentID, token, nil, nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "SUCCEEDED", decodeBody(t, w)["status"])
}

func TestListEndpointFiltersAndPagination(t *testing.T) {
	h, _, _, _ := newTestAPI(t)
	tenant := uuid.New().String()
	token := mintToken(t, uuid.New().String(), RoleGarage, tenant)
	for i := 0; i < 3; i++ {
		initiateOverHTTP(t, h, token, tenant, int64(1_000+i))
	}

	w := do(t, h, http.MethodGet, "/v1/payments/intents?limit=2", token, nil, nil)
	require.Equal(t, http.StatusOK, w.Code)
	var page []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
	assert.Len(t, page, 2)

	w = do(t, h, http.MethodGet, "/v1/payments/intents?limit=2&offset=2", token, nil, nil)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
	assert.Len(t, page, 1)

	w = do(t, h, http.MethodGet, "/v1/payments/intents?status=PROCESSING", token, nil, nil)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
	assert.Len(t, page, 3)

	w = do(t, h, http.MethodGet, "/v1/payments/intents?status=EXPLODED", token, nil, nil)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)

	w = do(t, h, http.MethodGet, "/v1/payments/intents?limit=0", token, nil, nil)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
}

func mustParseUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	require.NoError(t, err)
	return id
}

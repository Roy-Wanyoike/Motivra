package identity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// newTestAPI wires Routes through a platform.Server carrying the real
// auth middleware chain (as cmd/identity boots it), backed by the
// in-memory store. Returns the handler, the service, the store and the
// validator.
func newTestAPI() (http.Handler, *Service, *fakeStore, *platform.JWTValidator) {
	svc, store := newTestService()
	validator := platform.NewJWTValidator(testSecret, testIssuer, testAudience)
	cfg := platform.LoadForTest()
	srv := platform.NewServer(cfg, platform.NewLogger(cfg.App), platform.WithMiddleware(platform.AuthMiddleware(validator)))
	Routes(srv.Router, svc)
	return srv, svc, store, validator
}

// postJSON sends a JSON body to the handler and returns the recorder.
func postJSON(t *testing.T, handler http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// getAuthed sends a GET with a Bearer access token.
func getAuthed(t *testing.T, handler http.Handler, path, accessToken string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestRegisterEndpoint(t *testing.T) {
	router, _, store, _ := newTestAPI()

	rec := postJSON(t, router, "/v1/auth/register",
		`{"email":"Web.User@Example.com","phone":"+254700000003","password":"strong-passphrase","full_name":"Web User","device_name":"pixel-8"}`)
	require.Equal(t, http.StatusCreated, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var resp AccessResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.AccessToken)
	require.NotEmpty(t, resp.RefreshToken)
	require.True(t, resp.ExpiresAt.After(time.Now()))

	_, err := store.GetUserByEmail(t.Context(), "web.user@example.com")
	require.NoError(t, err, "email stored lowercased")

	// The client IP lands in the registration audit metadata.
	entries := store.auditsByAction(ActionUserRegistered)
	require.Len(t, entries, 1)
	require.Equal(t, "203.0.113.7", entries[0].Metadata["ip_address"])
}

func TestRegisterEndpointValidationAndConflict(t *testing.T) {
	router, _, _, _ := newTestAPI()

	rec := postJSON(t, router, "/v1/auth/register", `{"email":"broken","password":"strong-passphrase","full_name":"X"}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))

	rec = postJSON(t, router, "/v1/auth/register", `{"email":"a@b.co","password":"tiny","full_name":"X"}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	rec = postJSON(t, router, "/v1/auth/register", `{"email":"a@b.co","password":"strong-passphrase","full_name":"First"}`)
	require.Equal(t, http.StatusCreated, rec.Code)
	rec = postJSON(t, router, "/v1/auth/register", `{"email":"A@B.CO","password":"other-passphrase-1","full_name":"Second"}`)
	require.Equal(t, http.StatusConflict, rec.Code, "duplicate email is case-insensitive")
}

func TestLoginEndpointIdenticalFailures(t *testing.T) {
	router, _, _, _ := newTestAPI()
	postJSON(t, router, "/v1/auth/register", `{"email":"login@api.test","password":"strong-passphrase","full_name":"Login"}`)

	rec := postJSON(t, router, "/v1/auth/login", `{"email":"login@api.test","password":"strong-passphrase","device_name":"web"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	var resp AccessResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.AccessToken)

	recUnknown := postJSON(t, router, "/v1/auth/login", `{"email":"ghost@api.test","password":"strong-passphrase"}`)
	recWrong := postJSON(t, router, "/v1/auth/login", `{"email":"login@api.test","password":"wrong-passphrase"}`)
	require.Equal(t, http.StatusUnauthorized, recUnknown.Code)
	require.Equal(t, http.StatusUnauthorized, recWrong.Code)
	require.Equal(t, recUnknown.Body.String(), recWrong.Body.String(), "identical 401 bodies")
}

func TestRefreshAndLogoutEndpointFlow(t *testing.T) {
	router, svc, _, _ := newTestAPI()
	rec := postJSON(t, router, "/v1/auth/register", `{"email":"flow@api.test","password":"strong-passphrase","full_name":"Flow"}`)
	var registered AccessResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &registered))

	rec = postJSON(t, router, "/v1/auth/refresh", `{"refresh_token":"`+registered.RefreshToken+`"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	var refreshed AccessResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &refreshed))
	require.NotEqual(t, registered.RefreshToken, refreshed.RefreshToken)

	// Reusing the rotated token is rejected.
	rec = postJSON(t, router, "/v1/auth/refresh", `{"refresh_token":"`+registered.RefreshToken+`"}`)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	// Logout revokes the live session; the token can no longer refresh.
	rec = postJSON(t, router, "/v1/auth/logout", `{"refresh_token":"`+refreshed.RefreshToken+`"}`)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Empty(t, rec.Body.Bytes())

	rec = postJSON(t, router, "/v1/auth/refresh", `{"refresh_token":"`+refreshed.RefreshToken+`"}`)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	// Logout of an unknown token is 401, not 404 (no state disclosure).
	rec = postJSON(t, router, "/v1/auth/logout", `{"refresh_token":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	// Logout without a token body is a validation error.
	rec = postJSON(t, router, "/v1/auth/logout", `{"refresh_token":""}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	require.Len(t, svc.store.(*fakeStore).auditsByAction(ActionSessionRevoked), 1)
}

func TestProfileEndpoint(t *testing.T) {
	router, _, _, validator := newTestAPI()

	rec := getAuthed(t, router, "/v1/users/me", "")
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	rec = postJSON(t, router, "/v1/auth/register", `{"email":"me@api.test","password":"strong-passphrase","full_name":"Me","phone":"+254700000004"}`)
	var registered AccessResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &registered))

	rec = getAuthed(t, router, "/v1/users/me", registered.AccessToken)
	require.Equal(t, http.StatusOK, rec.Code)
	var profile map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &profile))
	require.Equal(t, "me@api.test", profile["email"])
	require.Equal(t, []any{"CUSTOMER"}, profile["roles"])
	require.NotContains(t, profile, "password_hash")
	require.NotContains(t, profile, "PasswordHash")
	_, err := validator.Validate(registered.AccessToken)
	require.NoError(t, err)
}

func TestAdminListUsersEndpointRBAC(t *testing.T) {
	router, svc, store, _ := newTestAPI()
	for i := 0; i < 3; i++ {
		rec := postJSON(t, router, "/v1/auth/register", `{"email":"user`+string(rune('a'+i))+`@api.test","password":"strong-passphrase","full_name":"U"}`)
		require.Equal(t, http.StatusCreated, rec.Code)
	}

	// A plain CUSTOMER token cannot list users.
	rec := postJSON(t, router, "/v1/auth/register", `{"email":"plain@api.test","password":"strong-passphrase","full_name":"P"}`)
	require.Equal(t, http.StatusCreated, rec.Code)
	var customer AccessResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &customer))
	rec = getAuthed(t, router, "/v1/admin/users", customer.AccessToken)
	require.Equal(t, http.StatusForbidden, rec.Code)

	// Grant ADMIN to one account and list with its token.
	admin, err := store.GetUserByEmail(t.Context(), "usera@api.test")
	require.NoError(t, err)
	require.NoError(t, store.AssignRole(t.Context(), admin.ID, RoleAdmin, nil))
	issuer := newTestIssuer()
	adminTokens, err := issuer.Issue(t.Context(), svc.store, admin, "console", "127.0.0.1")
	require.NoError(t, err)

	rec = getAuthed(t, router, "/v1/admin/users", adminTokens.AccessToken)
	require.Equal(t, http.StatusOK, rec.Code)
	var page userListResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.Len(t, page.Users, 4)
	require.Equal(t, defaultListUsersLimit, page.Limit)
	for _, u := range page.Users {
		require.NotEmpty(t, u.ID)
		require.NotEmpty(t, u.Email)
		require.NotNil(t, u.Roles, "roles render as an array, never null")
	}

	rec = getAuthed(t, router, "/v1/admin/users?limit=2&offset=2", adminTokens.AccessToken)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.Len(t, page.Users, 2)
	require.Equal(t, 2, page.Limit)
	require.Equal(t, 2, page.Offset)

	rec = getAuthed(t, router, "/v1/admin/users?limit=abc", adminTokens.AccessToken)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	// Unauthenticated listing is rejected.
	rec = getAuthed(t, router, "/v1/admin/users", "")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestClientIPPrefersForwardedFor(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login", nil)
	req.RemoteAddr = "10.0.0.1:5000"
	req.Header.Set("X-Forwarded-For", "198.51.100.5, 10.0.0.2")
	require.Equal(t, "198.51.100.5", clientIP(req))

	req.Header.Del("X-Forwarded-For")
	require.Equal(t, "10.0.0.1:5000", clientIP(req))
}

package platform

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

const testSecret = "unit-test-secret-unit-test-secret-1234"

func makeToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(testSecret))
	require.NoError(t, err)
	return signed
}

func validClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"sub":       "user-1",
		"tenant_id": "org-1",
		"role":      "CUSTOMER",
		"sid":       "sess-1",
		"iss":       "motivra-identity",
		"aud":       []string{"motivra"},
		"exp":       time.Now().Add(15 * time.Minute).Unix(),
		"iat":       time.Now().Add(-time.Minute).Unix(),
	}
}

func TestValidateSuccess(t *testing.T) {
	v := NewJWTValidator(testSecret, "motivra-identity", "motivra")
	claims, err := v.Validate(makeToken(t, validClaims()))
	require.NoError(t, err)
	require.Equal(t, "user-1", claims.UserID)
	require.Equal(t, "org-1", claims.TenantID)
	require.Equal(t, "CUSTOMER", claims.Role)
	require.Equal(t, "sess-1", claims.SessionID)
	require.False(t, claims.ExpiresAt.IsZero())
}

func TestValidateFailures(t *testing.T) {
	v := NewJWTValidator(testSecret, "motivra-identity", "motivra")

	t.Run("empty token", func(t *testing.T) {
		_, err := v.Validate("")
		require.Error(t, err)
	})

	t.Run("wrong signature", func(t *testing.T) {
		other := NewJWTValidator("another-secret-another-secret-5678", "motivra-identity", "motivra")
		_, err := other.Validate(makeToken(t, validClaims()))
		require.Error(t, err)
	})

	t.Run("expired", func(t *testing.T) {
		c := validClaims()
		c["exp"] = time.Now().Add(-time.Hour).Unix()
		_, err := v.Validate(makeToken(t, c))
		require.Error(t, err)
	})

	t.Run("wrong issuer", func(t *testing.T) {
		c := validClaims()
		c["iss"] = "evil-issuer"
		_, err := v.Validate(makeToken(t, c))
		require.Error(t, err)
	})

	t.Run("wrong audience", func(t *testing.T) {
		c := validClaims()
		c["aud"] = []string{"other-service"}
		_, err := v.Validate(makeToken(t, c))
		require.Error(t, err)
	})

	t.Run("missing subject", func(t *testing.T) {
		c := validClaims()
		delete(c, "sub")
		_, err := v.Validate(makeToken(t, c))
		require.Error(t, err)
	})

	t.Run("rejects non-HS256 algorithms", func(t *testing.T) {
		_, err := v.Validate("eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.sig")
		require.Error(t, err)
	})
}
func TestRequireRoleMiddleware(t *testing.T) {
	validator := NewJWTValidator(testSecret, "motivra-identity", "motivra")
	inner := RequireRole([]string{"ADMIN", "SUPER_ADMIN"}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := AuthMiddleware(validator)(http.Handler(inner))

	t.Run("no auth gets 401", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/admin", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		require.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("wrong role gets 403", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/admin", nil)
		r.Header.Set("Authorization", "Bearer "+makeToken(t, validClaims()))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		require.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("admin passes", func(t *testing.T) {
		c := validClaims()
		c["role"] = "ADMIN"
		r := httptest.NewRequest("GET", "/admin", nil)
		r.Header.Set("Authorization", "Bearer "+makeToken(t, c))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		require.Equal(t, http.StatusOK, w.Code)
	})
}

func TestAuthMiddlewareStoresClaims(t *testing.T) {
	v := NewJWTValidator(testSecret, "motivra-identity", "motivra")
	mw := AuthMiddleware(v)
	var got Claims
	var ok bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = ClaimsFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	r := httptest.NewRequest("GET", "/v1/ping", nil)
	r.Header.Set("Authorization", "Bearer "+makeToken(t, validClaims()))
	mw(inner).ServeHTTP(httptest.NewRecorder(), r)

	require.True(t, ok)
	require.Equal(t, "user-1", got.UserID)
}

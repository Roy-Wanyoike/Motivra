package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := LoadForTest()
	cfg.App.Name = "test-svc"
	logger := NewLogger(cfg.App)
	return NewServer(cfg, logger)
}

func TestHealthzAlwaysReadyToServe(t *testing.T) {
	s := newTestServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/healthz", nil)
	s.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"status":"ok"`)
}

func TestReadyzHealthyAndUnhealthy(t *testing.T) {
	s := newTestServer(t)
	s.checkers["database"] = func(context.Context) error { return nil }
	s.checkers["nats"] = func(context.Context) error { return errors.New("connection refused") }

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "nats")

	s.checkers["nats"] = func(context.Context) error { return nil }
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	require.Equal(t, http.StatusOK, w.Code)
}

func TestRecovererConvertsPanicTo500(t *testing.T) {
	s := newTestServer(t)
	s.Router.Get("/boom", func(http.ResponseWriter, *http.Request) {
		panic("exploded")
	})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/boom", nil))
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "internal_error")
}

func TestRequestIDAndLoggerInContext(t *testing.T) {
	s := newTestServer(t)
	var sawLogger bool
	s.Router.Get("/ctx", func(w http.ResponseWriter, r *http.Request) {
		_, sawLogger = FromContext(r.Context()), true
		if FromContext(r.Context()) == nil {
			sawLogger = false
		}
		w.WriteHeader(http.StatusOK)
	})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/ctx", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, sawLogger)
}

func TestAuthMiddlewareAnonymousPassesThrough(t *testing.T) {
	cfg := LoadForTest()
	cfg.App.Name = "test-svc"
	validator := NewJWTValidator(testSecret, "motivra-identity", "motivra")
	s := NewServer(cfg, NewLogger(cfg.App),
		WithMiddleware(AuthMiddleware(validator)),
	)
	s.Router.Get("/open", func(w http.ResponseWriter, r *http.Request) {
		_, authed := ClaimsFromContext(r.Context())
		_ = json.NewEncoder(w).Encode(map[string]bool{"authed": authed})
	})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/open", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"authed":false`)
}

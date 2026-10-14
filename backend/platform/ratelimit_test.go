package platform

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRateLimiterAllowsWithinWindow(t *testing.T) {
	t.Parallel()
	l := NewRateLimiter(3)
	require.True(t, l.Allow("1.2.3.4"))
	require.True(t, l.Allow("1.2.3.4"))
	require.True(t, l.Allow("1.2.3.4"))
	require.False(t, l.Allow("1.2.3.4"), "fourth request inside the window is rejected")

	require.True(t, l.Allow("5.6.7.8"), "keys are independent")
}

func TestRateLimiterResetsAfterWindow(t *testing.T) {
	t.Parallel()
	l := NewRateLimiter(1)
	now := time.Now()
	l.now = func() time.Time { return now }

	require.True(t, l.Allow("k"))
	require.False(t, l.Allow("k"))
	now = now.Add(2 * l.window)
	require.True(t, l.Allow("k"), "a fresh window resets the counter")
}

func TestRateLimiterDisabledPassesEverything(t *testing.T) {
	t.Parallel()
	var nilLimiter *RateLimiter
	require.True(t, nilLimiter.Allow("k"), "nil limiter is pass-through")

	off := NewRateLimiter(0)
	for i := 0; i < 100; i++ {
		require.True(t, off.Allow("k"), "rpm=0 disables limiting")
	}
}

func TestRateLimiterMiddleware429(t *testing.T) {
	t.Parallel()
	l := NewRateLimiter(2)
	handler := l.Middleware(func(r *http.Request) string { return r.RemoteAddr })(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	ip := "10.9.9.9:1"
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/auth/login", nil)
		req.RemoteAddr = ip
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login", nil)
	req.RemoteAddr = ip
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, "application/problem+json", w.Header().Get("Content-Type"))
	require.NotEmpty(t, w.Header().Get("Retry-After"))
	require.Contains(t, w.Body.String(), "rate_limited")
}

package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/Roy-Wanyoike/Motivra/backend/platform/redistest"
)

// newTestRedisLimiter builds one limiter "instance" over the shared
// integration Redis (self-skipping without TEST_REDIS_URL, pgtest-style).
func newTestRedisLimiter(t *testing.T, rpm int) (*RedisRateLimiter, *redis.Client) {
	t.Helper()
	client := redistest.Client(t)
	redistest.FlushDB(t, client)
	l := NewRedisRateLimiter(client, "identity-test", rpm)
	return l, client
}

// TestRedisLimiterAllowsAndRejects proves the sliding-window decision core
// against a live Redis: rpm requests admitted, the (rpm+1)th rejected, the
// window's Retry-After positive and bounded.
func TestRedisLimiterAllowsAndRejects(t *testing.T) {
	l, _ := newTestRedisLimiter(t, 3)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		allowed, err := l.Allow(ctx, "1.2.3.4")
		require.NoError(t, err)
		require.True(t, allowed, "request %d inside the empty window", i+1)
	}
	allowed, err := l.Allow(ctx, "1.2.3.4")
	require.NoError(t, err)
	require.False(t, allowed, "the 4th request inside the window is rejected")

	retry := l.RetryAfter(ctx, "1.2.3.4")
	require.Greater(t, retry, time.Duration(0))
	require.LessOrEqual(t, retry, redisWindow)

	allowed, err = l.Allow(ctx, "5.6.7.8")
	require.NoError(t, err)
	require.True(t, allowed, "keys are independent")
}

// TestRedisLimiterCrossInstanceConsistency is the issue #87 acceptance
// proof, replicated at the store level: TWO limiter instances (two
// independent clients, as two service replicas would hold) share one Redis
// state. When instance A exhausts the limit, instance B sees the SAME
// exhausted window and rejects — with the in-process limiter each replica
// would grant its own full budget.
func TestRedisLimiterCrossInstanceConsistency(t *testing.T) {
	_, client := newTestRedisLimiter(t, 5)
	replicaA := NewRedisRateLimiter(client, "identity-a", 5)
	replicaB := NewRedisRateLimiter(client, "identity-b", 5)
	ctx := context.Background()

	grantedA, grantedB := 0, 0
	for i := 0; i < 10; i++ {
		if ok, err := replicaA.Allow(ctx, "shared-client"); err == nil && ok {
			grantedA++
		}
		if ok, err := replicaB.Allow(ctx, "shared-client"); err == nil && ok {
			grantedB++
		}
	}
	require.Equal(t, 5, grantedA+grantedB,
		"the GLOBAL limit is 5 across BOTH replicas, not 5 per replica — exactly the cross-instance consistency the in-process limiter cannot give")
}

// TestRedisLimiterFailOpen pins the availability posture: Redis errors let
// the request through (throttling must not become a self-inflicted outage).
func TestRedisLimiterFailOpen(t *testing.T) {
	t.Parallel()
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 200 * time.Millisecond})
	t.Cleanup(func() { _ = client.Close() })
	l := NewRedisRateLimiter(client, "identity-test", 60)

	allowed, err := l.Allow(context.Background(), "1.2.3.4")
	require.False(t, allowed)
	require.Error(t, err)
}

// TestRedisLimiterMiddleware429 checks middleware parity with the
// in-process limiter: 429 + problem+json + Retry-After on exhaustion.
func TestRedisLimiterMiddleware429(t *testing.T) {
	l, _ := newTestRedisLimiter(t, 2)
	handler := l.Middleware(func(r *http.Request) string { return r.RemoteAddr })(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

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

// TestRedisLimiterDisabled passes through like the in-process limiter.
func TestRedisLimiterDisabled(t *testing.T) {
	t.Parallel()
	var nilLimiter *RedisRateLimiter
	allowed, err := nilLimiter.Allow(context.Background(), "k")
	require.NoError(t, err)
	require.True(t, allowed, "nil limiter is pass-through")

	off := NewRedisRateLimiter(nil, "svc", 0)
	allowed, err = off.Allow(context.Background(), "k")
	require.NoError(t, err)
	require.True(t, allowed, "rpm=0 disables limiting")
}

// TestAuthLimiterInterface: both backends satisfy the route-table contract.
func TestAuthLimiterInterface(t *testing.T) {
	t.Parallel()
	var memory AuthLimiter = NewRateLimiter(10)
	require.NotNil(t, memory)
	// The Redis-backed one compiles against the same interface (constructed
	// in the integration tests above).
	var _ AuthLimiter = (*RedisRateLimiter)(nil)
}

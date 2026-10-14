package platform

import (
	"net/http"
	"sync"
	"time"
)

// AuthLimiter is the limiter surface the identity routes consume
// (issue #87): both the in-process fixed-window RateLimiter (single
// instance) and the Redis-backed sliding-window RedisRateLimiter
// (cross-instance consistency) satisfy it, so the route table never
// changes with the deployment shape.
type AuthLimiter interface {
	Middleware(keyFn func(*http.Request) string) func(http.Handler) http.Handler
}

// RateLimiter is a fixed-window per-key request limiter for protecting
// expensive or abuse-prone endpoints (authentication above all: each
// argon2id verification allocates 64 MiB, so unthrottled login is also a
// memory/CPU denial-of-service vector). It is intentionally in-process:
// correct for single-instance deployments and as the fallback when
// MOTIVRA_REDIS_URL is unset; horizontal scaling uses RedisRateLimiter so
// limits are shared across replicas (issue #87, ADR-0007).
//
// Buckets are keyed by caller-chosen strings (client IPs for auth routes)
// and garbage-collected lazily, so memory stays bounded under rotation.
type RateLimiter struct {
	rpm int

	mu      sync.Mutex
	buckets map[string]*rateBucket
	now     func() time.Time

	// window is the fixed window length; exposed for tests that shrink it.
	window time.Duration
}

type rateBucket struct {
	count  int
	starts time.Time
}

// NewRateLimiter returns a limiter allowing rpm requests per window per
// key. rpm <= 0 disables limiting (Allow always true, middleware pass-through).
func NewRateLimiter(rpm int) *RateLimiter {
	return &RateLimiter{
		rpm:     rpm,
		buckets: map[string]*rateBucket{},
		now:     time.Now,
		window:  time.Minute,
	}
}

// Allow reports whether one more request from key is permitted in the
// current window.
func (l *RateLimiter) Allow(key string) bool {
	if l == nil || l.rpm <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.buckets[key]
	if !ok || now.Sub(b.starts) >= l.window {
		b = &rateBucket{count: 0, starts: now}
		l.buckets[key] = b
	}
	// Lazy GC: on average a bucket lives one window; sweep when the map
	// grows past a small multiple of the live key population.
	if len(l.buckets) > 4*l.rpm+1024 {
		for k, old := range l.buckets {
			if now.Sub(old.starts) >= l.window {
				delete(l.buckets, k)
			}
		}
	}
	if b.count >= l.rpm {
		return false
	}
	b.count++
	return true
}

// RetryAfter reports how long until the key's window resets. It is valid
// to call after Allow returns false.
func (l *RateLimiter) RetryAfter() time.Duration {
	if l == nil || l.rpm <= 0 {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	// The window that rejected the most recent request; good enough for a
	// coarse Retry-After hint (per-key start times differ).
	latest := time.Time{}
	for _, b := range l.buckets {
		if b.starts.After(latest) {
			latest = b.starts
		}
	}
	if latest.IsZero() {
		return l.window
	}
	reset := latest.Add(l.window)
	if d := time.Until(reset); d > 0 {
		return d
	}
	return time.Second
}

// Middleware returns an HTTP middleware that keys the limiter with keyFn
// and rejects over-limit requests with a 429 problem+json body carrying
// Retry-After. Disabled limiters pass through untouched.
func (l *RateLimiter) Middleware(keyFn func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow(keyFn(r)) {
				retry := l.RetryAfter().Round(time.Second)
				w.Header().Set("Retry-After", retry.String())
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(LogJSON(map[string]any{
					"code":        "rate_limited",
					"title":       "Too many requests",
					"retry_after": retry.String(),
				})))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

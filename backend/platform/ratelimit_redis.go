package platform

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
)

// redisKeyPrefix namespaces every rate-limit bucket so a shared Redis
// never collides with other keys.
const redisKeyPrefix = "motivra:rl:"

// redisWindow is the sliding-window length; it matches the in-process
// limiter's fixed window so the rpm semantics (requests per minute) are
// identical across backends.
const redisWindow = time.Minute

// RedisRateLimiter is a distributed sliding-window limiter (issue #87,
// ADR-0007): the window state lives in Redis, so N replicas of a service
// share ONE limit per key instead of N independent in-process buckets —
// the cross-instance 429 consistency the in-process limiter cannot give.
//
// The decision is one atomic Lua script (ZREMRANGEBYSCORE + ZCARD + ZADD +
// PEXPIRE), so concurrent replicas cannot race past the limit.
//
// Failure posture: Redis unavailability FAILS OPEN (request allowed, a
// warning is logged and the error outcome counted). Throttling is
// abuse protection; a Redis outage must not lock every user out of
// authentication. /readyz carries the redis checker so operators see the
// degraded state.
type RedisRateLimiter struct {
	client  *redis.Client
	rpm     int
	service string
	now     func() time.Time

	script *redis.Script

	decisions *prometheus.CounterVec
	logger    *slog.Logger
}

// LimiterOption customises a RedisRateLimiter.
type LimiterOption func(*RedisRateLimiter)

// WithLimiterMetrics registers the decision counter on reg (pass the
// service Metrics.Registry()). Without it the counter still records but
// is not exposed.
func WithLimiterMetrics(reg *prometheus.Registry) LimiterOption {
	return func(l *RedisRateLimiter) {
		l.decisions = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rate_limit_decisions_total",
				Help: "Rate-limiter decisions by backend (redis), outcome (allowed|rejected|error) and service.",
			},
			[]string{"backend", "outcome", "service"},
		)
		if reg != nil {
			reg.MustRegister(l.decisions)
		}
	}
}

// WithLimiterLogger wires structured logging for fail-open events.
func WithLimiterLogger(logger *slog.Logger) LimiterOption {
	return func(l *RedisRateLimiter) { l.logger = logger }
}

// NewRedisRateLimiter dials no connection itself — client is shared with
// the caller (closed by the binary's cleanup chain). rpm <= 0 disables
// limiting exactly like the in-process limiter.
func NewRedisRateLimiter(client *redis.Client, service string, rpm int, opts ...LimiterOption) *RedisRateLimiter {
	l := &RedisRateLimiter{
		client:  client,
		rpm:     rpm,
		service: service,
		now:     time.Now,
		script:  redis.NewScript(redisSlidingWindowLua),
	}
	for _, opt := range opts {
		opt(l)
	}
	if l.decisions == nil {
		// Unregistered instrument: keeps Allow/Middleware total in tests
		// and offline tooling.
		l.decisions = prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "rate_limit_decisions_total", Help: "Rate-limiter decisions."},
			[]string{"backend", "outcome", "service"},
		)
	}
	if l.logger == nil {
		l.logger = slog.Default()
	}
	return l
}

// count records one decision.
func (l *RedisRateLimiter) count(outcome string) {
	if l.decisions != nil {
		l.decisions.WithLabelValues("redis", outcome, l.service).Inc()
	}
}

// Allow reports whether one more request from key is permitted inside the
// sliding window shared through Redis. Errors fail OPEN: the request is
// allowed and the error returned for diagnostics/logging.
func (l *RedisRateLimiter) Allow(ctx context.Context, key string) (bool, error) {
	if l == nil || l.rpm <= 0 {
		return true, nil
	}
	return l.allow(ctx, key)
}

// allow is the decision core (split so tests can pin behavior without the
// disabled short-circuit).
func (l *RedisRateLimiter) allow(ctx context.Context, key string) (bool, error) {
	now := l.now()
	nowMS := now.UnixMilli()
	member := fmt.Sprintf("%d-%s", now.UnixNano(), randomSuffix())
	res, err := l.script.Run(ctx, l.client, []string{redisKeyPrefix + key},
		nowMS, redisWindow.Milliseconds(), l.rpm, member).Slice()
	if err != nil {
		return false, fmt.Errorf("redis sliding window: %w", err)
	}
	if len(res) != 2 {
		return false, fmt.Errorf("redis sliding window: unexpected script result shape %d", len(res))
	}
	allowed, _ := res[0].(int64)
	if allowed == 1 {
		return true, nil
	}
	return false, nil
}

// RetryAfter returns how long until the key's window admits a request
// again. Valid to call after Allow returns false; on error it reports the
// full window (the conservative hint).
func (l *RedisRateLimiter) RetryAfter(ctx context.Context, key string) time.Duration {
	if l == nil || l.rpm <= 0 {
		return 0
	}
	res, err := l.script.Run(ctx, l.client, []string{redisKeyPrefix + key},
		l.now().UnixMilli(), redisWindow.Milliseconds(), l.rpm, "probe").Slice()
	if err != nil || len(res) != 2 {
		return redisWindow
	}
	ms, _ := res[1].(int64)
	if ms <= 0 {
		return time.Second
	}
	return time.Duration(ms) * time.Millisecond
}

// Middleware implements AuthLimiter: keys with keyFn, rejects over-limit
// requests with a 429 problem+json body carrying Retry-After, fails open
// on Redis errors. A disabled limiter (nil or rpm<=0) passes through.
func (l *RedisRateLimiter) Middleware(keyFn func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			allowed, err := l.Allow(r.Context(), keyFn(r))
			switch {
			case err != nil:
				l.count("error")
				l.logger.Warn("rate limiter degraded; failing open",
					"error", err, "path", r.URL.Path)
			case allowed:
				l.count("allowed")
			default:
				l.count("rejected")
				retry := l.RetryAfter(r.Context(), keyFn(r)).Round(time.Second)
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

// randomSuffix returns 8 random bytes, hex — the per-request ZSET member
// uniqueness that keeps concurrent same-millisecond requests distinct.
func randomSuffix() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is catastrophic but must not panic a request
		// path: the nanosecond timestamp keeps members practically unique.
		return fmt.Sprintf("%x", binary.LittleEndian.Uint64([]byte(time.Now().Format("060102150405.000000000"))))
	}
	return fmt.Sprintf("%x", b)
}

// redisSlidingWindowLua is the atomic sliding-window decision:
//
//	KEYS[1] bucket key
//	ARGV[1] now (ms), ARGV[2] window (ms), ARGV[3] limit, ARGV[4] member
//
// Returns {allowed(0|1), retry_after_ms}. Prune-then-count-then-add runs
// as one script, so two replicas racing the same key cannot both admit a
// limit-exceeding request.
var redisSlidingWindowLua = `
local key      = KEYS[1]
local now      = tonumber(ARGV[1])
local window   = tonumber(ARGV[2])
local limit    = tonumber(ARGV[3])
local member   = ARGV[4]

redis.call('ZREMRANGEBYSCORE', key, '-inf', now - window)
local count = redis.call('ZCARD', key)
if count < limit then
    redis.call('ZADD', key, now, member)
    redis.call('PEXPIRE', key, window)
    return {1, 0}
end

local retry = window
local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
if #oldest >= 2 then
    retry = tonumber(oldest[2]) + window - now
    if retry < 1 then retry = 1 end
end
return {0, retry}
`

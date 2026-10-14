// Package redistest is the Redis twin of pgtest (issue #87): a shared
// harness for Redis-gated integration tests. Suites call Client to connect
// via TEST_REDIS_URL and FlushDB to reset state between tests.
//
// Every helper skips the calling test — with the runbook instructions in
// the skip message — when TEST_REDIS_URL is unset, so `go test ./...`
// compiles and passes cleanly on machines and CI runners without a Redis.
// Locally: `docker compose -f docker-compose.dev.yml up -d redis` exposes
// redis on localhost:6379 (compose dev stack); see docs/RUNBOOK.md.
package redistest

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// EnvRedisURL is the environment variable that opts a test binary into the
// live-Redis integration suite.
const EnvRedisURL = "TEST_REDIS_URL"

// RedisURL returns the configured integration Redis URL ("" when unset).
func RedisURL() string {
	return os.Getenv(EnvRedisURL)
}

// Client connects to TEST_REDIS_URL, pings it and registers an automatic
// Close on test cleanup; it skips the calling test with a clear reason
// when the variable is unset.
func Client(t *testing.T) *redis.Client {
	t.Helper()
	url := RedisURL()
	if url == "" {
		t.Skipf("%s is not set; skipping Redis integration test — start the dev stack's redis (`docker compose -f docker-compose.dev.yml up -d redis`), then export %s='redis://localhost:6379/0' (see docs/RUNBOOK.md)", EnvRedisURL, EnvRedisURL)
	}
	opt, err := redis.ParseURL(url)
	require.NoError(t, err)
	client := redis.NewClient(opt)
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Ping(context.Background()).Err(), "TEST_REDIS_URL points at an unreachable Redis")
	return client
}

// FlushDB wipes the shared test database between cases. TEST_REDIS_URL is
// an explicit operator action (like TEST_DATABASE_URL) — point it at a
// disposable database, never a shared cache.
func FlushDB(t *testing.T, client *redis.Client) {
	t.Helper()
	require.NoError(t, client.FlushDB(context.Background()).Err())
}

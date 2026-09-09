// Package pgtest is the shared harness for Motivra's Postgres-gated
// integration tests (issue #28, deferral 3). Domain suites call Pool to
// connect via TEST_DATABASE_URL, Migrate to apply their domain's embedded
// migration chain programmatically (golang-migrate, per the ADR-0003
// per-domain chains) and Truncate to reset the schema between tests.
//
// Every helper skips the calling test — with the runbook instructions in
// the skip message — when TEST_DATABASE_URL is unset, so `go test ./...`
// compiles and passes cleanly on machines and CI runners without a
// database. CI will execute this suite once the GitHub Actions billing
// lock (issue #10) is lifted; until then local runs against
// `make dev` (docker-compose.dev.yml) are the merge gate. See
// docs/RUNBOOK.md for the full local path.
package pgtest

import (
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// EnvDatabaseURL is the environment variable that opts a test binary into
// the real-Postgres integration suite.
const EnvDatabaseURL = "TEST_DATABASE_URL"

// DatabaseURL returns the configured integration database URL ("" when
// unset, in which case Pool skips).
func DatabaseURL() string {
	return os.Getenv(EnvDatabaseURL)
}

// Pool returns a pgx pool connected to TEST_DATABASE_URL, or skips the
// calling test with a clear reason when the variable is unset. The pool is
// closed automatically when the test and its cleanup phase finish.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := DatabaseURL()
	if url == "" {
		t.Skipf("%s is not set; skipping Postgres integration test — start the dev stack with `make dev`, then export %s='postgres://motivra:motivra@localhost:5432/motivra?sslmode=disable' (see docs/RUNBOOK.md)", EnvDatabaseURL, EnvDatabaseURL)
	}
	pool, err := platform.NewPostgres(t.Context(), url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// Migrate applies the domain's embedded migration chain (migrationsFS is an
// embed.FS rooted at the domain's migrations directory) using the given
// schema_migrations table name. It is idempotent: an up-to-date database is
// a no-op.
func Migrate(t *testing.T, pool *pgxpool.Pool, migrationsFS fs.FS, table string) {
	t.Helper()
	require.NoError(t, platform.MigrateUp(t.Context(), pool, migrationsFS, table))
}

// Truncate empties the named tables and everything referencing them via
// foreign keys (CASCADE), giving each test a clean schema. Table names are
// test-controlled literals, matching the domain's migration DDL.
func Truncate(t *testing.T, pool *pgxpool.Pool, tables ...string) {
	t.Helper()
	require.NotEmpty(t, tables, "pgtest.Truncate needs at least one table")
	stmt := fmt.Sprintf(`TRUNCATE %s CASCADE`, strings.Join(tables, ", "))
	_, err := pool.Exec(t.Context(), stmt)
	require.NoError(t, err)
}

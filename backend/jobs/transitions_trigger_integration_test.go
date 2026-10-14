package jobs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	jobsmigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/jobs"
	"github.com/Roy-Wanyoike/Motivra/backend/platform/pgtest"
)

// TestJobTransitionsAppendOnlyEnforcedByDatabase proves the job state
// history is physically immutable (integrity issue #83): UPDATE and DELETE
// raise trigger exceptions while INSERT keeps working. Gated on
// TEST_DATABASE_URL like the other integration suites.
func TestJobTransitionsAppendOnlyEnforcedByDatabase(t *testing.T) {
	pool := pgtest.Pool(t)
	pgtest.Migrate(t, pool, jobsmigrations.FS, "schema_migrations_jobs")
	pgtest.Truncate(t, pool, jobsTables...)
	defer pgtest.Truncate(t, pool, jobsTables...)

	ctx := context.Background()
	// job_transitions.job_id references jobs(id); seed the minimal valid
	// chain: service_requests -> jobs (columns per migrations 000001/000002).
	_, err := pool.Exec(ctx,
		`INSERT INTO service_requests (id, customer_id, description)
		 VALUES ('22222222-2222-4222-8222-222222222222', '33333333-3333-4333-8333-333333333333', 'trigger test')`)
	require.NoError(t, err, "seed service request")
	_, err = pool.Exec(ctx,
		`INSERT INTO jobs (id, service_request_id, customer_id, status, problem_summary)
		 VALUES ('11111111-1111-4111-8111-111111111111', '22222222-2222-4222-8222-222222222222', '33333333-3333-4333-8333-333333333333', 'CREATED', 'trigger test')`)
	require.NoError(t, err, "seed job row for the transition")

	_, err = pool.Exec(ctx,
		`INSERT INTO job_transitions (job_id, from_status, to_status) VALUES ('11111111-1111-4111-8111-111111111111', 'CREATED', 'TRIAGING')`)
	require.NoError(t, err, "INSERT into job_transitions must keep working")

	_, err = pool.Exec(ctx, `UPDATE job_transitions SET to_status = 'TAMPERED'`)
	require.Error(t, err, "UPDATE on job_transitions must raise the append-only exception")
	require.Contains(t, err.Error(), "append-only")

	_, err = pool.Exec(ctx, `DELETE FROM job_transitions`)
	require.Error(t, err, "DELETE on job_transitions must raise the append-only exception")
	require.Contains(t, err.Error(), "append-only")
}

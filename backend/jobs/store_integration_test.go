package jobs

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	jobsnextmigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/jobs"
	"github.com/Roy-Wanyoike/Motivra/backend/platform/pgtest"
)

// jobsTables are every table of the jobs chain, truncated between tests so
// each starts from a clean schema (job_assignments and job_transitions
// cascade from jobs).
var jobsTables = []string{"service_requests", "jobs", "job_assignments", "job_transitions"}

// integrationStore connects to the Postgres instance named by
// TEST_DATABASE_URL through the shared pgtest harness (issue #28, deferral
// 3), applies the jobs migration chain and returns a Service and store over
// the real schema. The service runs with a nil publisher: event delivery is
// unit-tested with fakes and end-to-end in platform under TEST_NATS_URL;
// here the database is the system under test. Tests skip when the variable
// is unset.
func integrationStore(t *testing.T) (*Service, *PostgresStore) {
	t.Helper()
	pool := pgtest.Pool(t)
	pgtest.Migrate(t, pool, jobsnextmigrations.FS, "schema_migrations_jobs")
	pgtest.Truncate(t, pool, jobsTables...)

	store := NewPostgresStore(pool)
	return NewService(store, nil), store
}

// floatOf returns a pointer to v (request coordinates).
func floatOf(v float64) *float64 { return &v }

// TestJobLifecycleIntegration drives request -> job -> transitions ->
// assignment -> acceptance against real PostgreSQL, then inspects the
// append-only transition audit trail and the scoped reads at the SQL level.
func TestJobLifecycleIntegration(t *testing.T) {
	svc, store := integrationStore(t)
	ctx := t.Context()

	// Intake: a validated request persists in status 'received'.
	req := &ServiceRequest{
		CustomerID:  uuid.New(),
		Description: "Brakes squeal and pedal pulses under light braking",
		LocationLat: floatOf(-1.292066),
		LocationLng: floatOf(36.821945),
		AddressText: "Moi Ave, Nairobi CBD",
	}
	require.NoError(t, svc.CreateRequest(ctx, req))
	require.NotEqual(t, uuid.Nil, req.ID)
	require.Equal(t, RequestStatusReceived, req.Status)
	require.False(t, req.CreatedAt.IsZero())

	stored, err := store.GetRequest(ctx, req.ID, opScope())
	require.NoError(t, err)
	require.Equal(t, req.Description, stored.Description)
	require.Equal(t, RequestStatusReceived, stored.Status)

	// Conversion: the request is atomically claimed into a CREATED job.
	op := opScope()
	job, err := svc.CreateJobFromRequest(ctx, req.ID, op)
	require.NoError(t, err)
	require.Equal(t, StatusCreated, job.Status)
	require.NotNil(t, job.ServiceRequestID)
	require.Equal(t, req.ID, *job.ServiceRequestID)

	converted, err := store.GetRequest(ctx, req.ID, op)
	require.NoError(t, err)
	require.Equal(t, RequestStatusConverted, converted.Status)

	// A second conversion fails and consumes nothing.
	_, err = svc.CreateJobFromRequest(ctx, req.ID, op)
	require.True(t, errors.Is(err, ErrAlreadyConverted), "double conversion must fail, got %v", err)

	// State machine: CREATED -> TRIAGING -> DISPATCHING, each move recorded.
	actor := uuid.New()
	require.NoError(t, svc.Transition(ctx, job.ID, StatusTriaging, &actor, "intake triage", op))
	require.NoError(t, svc.Transition(ctx, job.ID, StatusDispatching, &actor, "routed to dispatch", op))

	// Illegal move is rejected before it touches the store.
	err = svc.Transition(ctx, job.ID, StatusRepairing, &actor, "illegal", op)
	require.True(t, errors.Is(err, ErrConflict), "illegal move must conflict, got %v", err)

	// Assignment: the store appends the job_assignments row and the
	// DISPATCHING -> ASSIGNED transition in one transaction.
	technician := uuid.New()
	require.NoError(t, svc.AssignTechnician(ctx, job.ID, technician, &actor, op))

	assigned, err := store.GetJob(ctx, job.ID, op)
	require.NoError(t, err)
	require.Equal(t, StatusAssigned, assigned.Status)
	require.NotNil(t, assigned.TechnicianID)
	require.Equal(t, technician, *assigned.TechnicianID)

	// Acceptance: ASSIGNED -> ACCEPTED stamps the assignment row.
	require.NoError(t, svc.Accept(ctx, job.ID, technician, viewScope(technician)))

	accepted, err := store.GetJob(ctx, job.ID, viewScope(technician))
	require.NoError(t, err)
	require.Equal(t, StatusAccepted, accepted.Status)

	// The append-only transition trail reads back in chronological order.
	transitions, err := store.ListTransitions(ctx, job.ID, op)
	require.NoError(t, err)
	require.Len(t, transitions, 4)
	wantPairs := [][2]Status{
		{StatusCreated, StatusTriaging},
		{StatusTriaging, StatusDispatching},
		{StatusDispatching, StatusAssigned},
		{StatusAssigned, StatusAccepted},
	}
	for i, pair := range wantPairs {
		require.Equal(t, pair[0], transitions[i].FromStatus, "transition %d from", i)
		require.Equal(t, pair[1], transitions[i].ToStatus, "transition %d to", i)
	}
	require.Equal(t, "intake triage", transitions[0].Reason)
	require.NotNil(t, transitions[0].ActorID)
	require.Equal(t, actor, *transitions[0].ActorID)
	require.Equal(t, "technician assigned", transitions[2].Reason)
	require.Equal(t, "technician accepted assignment", transitions[3].Reason)
	for i, tr := range transitions {
		require.False(t, tr.CreatedAt.IsZero(), "transition %d must carry its row timestamp", i)
	}

	// Scoped listing at the SQL level: the customer sees the job, the
	// assigned technician sees it, the operator sees it, nobody else does.
	customerPage, err := store.ListJobsByStatus(ctx, StatusAccepted, 50, 0, viewScope(accepted.CustomerID))
	require.NoError(t, err)
	require.Len(t, customerPage, 1)
	require.Equal(t, accepted.ID, customerPage[0].ID)

	techPage, err := store.ListJobsByStatus(ctx, StatusAccepted, 50, 0, viewScope(technician))
	require.NoError(t, err)
	require.Len(t, techPage, 1)

	opPage, err := store.ListJobsByStatus(ctx, StatusAccepted, 50, 0, op)
	require.NoError(t, err)
	require.Len(t, opPage, 1)

	createdPage, err := store.ListJobsByStatus(ctx, StatusCreated, 50, 0, op)
	require.NoError(t, err)
	require.Empty(t, createdPage, "the job left CREATED: no row in that status bucket")

	pgtest.Truncate(t, store.pool, jobsTables...)
}

// TestJobStoreScopeIsolationIntegration proves at the SQL level what
// ADR-0004 mandates and the fake-store tests approximate: principal-scoped
// reads never return another principal's rows, and denial is
// indistinguishable from absence.
func TestJobStoreScopeIsolationIntegration(t *testing.T) {
	svc, store := integrationStore(t)
	ctx := t.Context()

	// Seed two jobs for two different customers through the real service
	// surface (request -> job), so the fixtures are themselves realistic.
	customerA, customerB := uuid.New(), uuid.New()
	jobA := seedRequestAndJob(t, svc, customerA)
	jobB := seedRequestAndJob(t, svc, customerB)

	// Customer B cannot read customer A's job or its trail...
	_, err := store.GetJob(ctx, jobA.ID, viewScope(customerB))
	require.True(t, errors.Is(err, ErrJobNotFound), "foreign job must be not-found, got %v", err)
	assert.Empty(t, storeTransitions(t, store, jobA.ID, viewScope(customerB)), "foreign trail must read as empty")

	// ...and the zero scope fails closed (matches no rows at all).
	_, err = store.GetJob(ctx, jobB.ID, ReadScope{})
	require.True(t, errors.Is(err, ErrJobNotFound), "zero scope must match nothing, got %v", err)
	empty, err := store.ListJobsByStatus(ctx, StatusCreated, 50, 0, ReadScope{})
	require.NoError(t, err)
	assert.Empty(t, empty, "zero scope must return an empty page")

	// Customer A still sees exactly their own rows: isolation, not breakage.
	got, err := svc.GetJob(ctx, jobA.ID, viewScope(customerA))
	require.NoError(t, err)
	require.Equal(t, jobA.ID, got.ID)
	page, err := svc.JobsByStatus(ctx, StatusCreated, 50, 0, viewScope(customerA))
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, jobA.ID, page[0].ID)

	// Unknown ids behave exactly like invisible ones (no existence leak).
	_, err = store.GetJob(ctx, uuid.New(), viewScope(customerA))
	require.True(t, errors.Is(err, ErrJobNotFound), "unknown job must be not-found, got %v", err)

	pgtest.Truncate(t, store.pool, jobsTables...)
}

// storeTransitions reads a job's trail through the Postgres store, failing
// the test on transport errors. (scoping_test.go's mustTransitions covers
// the fake store; this one is the SQL-side counterpart.)
func storeTransitions(t *testing.T, store *PostgresStore, jobID uuid.UUID, scope ReadScope) []JobTransition {
	t.Helper()
	transitions, err := store.ListTransitions(t.Context(), jobID, scope)
	require.NoError(t, err)
	return transitions
}

// seedRequestAndJob runs one customer through request -> CREATED job using
// the real service against the real store.
func seedRequestAndJob(t *testing.T, svc *Service, customerID uuid.UUID) Job {
	t.Helper()
	req := &ServiceRequest{
		CustomerID:  customerID,
		Description: "Engine warning light, rough idle",
		LocationLat: floatOf(-1.303202),
		LocationLng: floatOf(36.791611),
		AddressText: "Karen Rd, Nairobi",
	}
	require.NoError(t, svc.CreateRequest(t.Context(), req))
	job, err := svc.CreateJobFromRequest(t.Context(), req.ID, opScope())
	require.NoError(t, err)
	return job
}

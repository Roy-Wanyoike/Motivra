package garages

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	garagesmigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/garages"
	"github.com/Roy-Wanyoike/Motivra/backend/platform/pgtest"
)

// garagesTables are every table of the garages chain, truncated between
// tests so each starts from a clean schema.
var garagesTables = []string{"garage_capacity"}

// integrationStore connects to the Postgres instance named by
// TEST_DATABASE_URL through the shared pgtest harness (issue #28, deferral
// 3), applies the garages migration chain and returns a Service and store
// over the real schema. The service runs with a nil publisher: event
// delivery is unit-tested with fakes and end-to-end in platform under
// TEST_NATS_URL; here the database is the system under test. Tests skip
// when the variable is unset.
func integrationStore(t *testing.T) (*Service, *PostgresStore) {
	t.Helper()
	pool := pgtest.Pool(t)
	pgtest.Migrate(t, pool, garagesmigrations.FS, "schema_migrations_garages")
	pgtest.Truncate(t, pool, garagesTables...)

	store := NewPostgresStore(pool)
	return NewService(store, nil), store
}

// TestCapacityLifecycleIntegration drives write -> read -> rewrite against
// real PostgreSQL and proves the single-row-per-tenant constraint, the
// CHECK constraints and cross-tenant isolation at the SQL level.
func TestCapacityLifecycleIntegration(t *testing.T) {
	svc, store := integrationStore(t)
	ctx := t.Context()
	tenantA, tenantB := uuid.New(), uuid.New()
	actorA := uuid.New()

	// A tenant that never wrote a row reads the documented defaults.
	defaults, err := svc.Capacity(ctx, tenantA)
	require.NoError(t, err)
	assert.Equal(t, 0, defaults.Bays)
	assert.Equal(t, DefaultOpeningTime, defaults.OpeningTime)
	assert.Equal(t, DefaultClosingTime, defaults.ClosingTime)

	// Write: the row persists with its stamps and the actor.
	written, err := svc.UpdateCapacity(ctx, Scope{TenantID: &tenantA}, &actorA, 3, "08:00", "18:00", 6)
	require.NoError(t, err)
	require.False(t, written.CreatedAt.IsZero())
	require.False(t, written.UpdatedAt.IsZero())
	require.NotNil(t, written.UpdatedBy)
	assert.Equal(t, actorA, *written.UpdatedBy)

	stored, err := store.Capacity(ctx, tenantA)
	require.NoError(t, err)
	assert.Equal(t, 3, stored.Bays)
	assert.Equal(t, "08:00", stored.OpeningTime)
	assert.Equal(t, "18:00", stored.ClosingTime)
	assert.Equal(t, 6, stored.TechnicianSlots)

	// Rewrite: still exactly one row per tenant (the upsert path).
	_, err = svc.UpdateCapacity(ctx, Scope{TenantID: &tenantA}, &actorA, 4, "07:30", "19:30", 8)
	require.NoError(t, err)
	stored, err = store.Capacity(ctx, tenantA)
	require.NoError(t, err)
	assert.Equal(t, 4, stored.Bays)
	assert.Equal(t, "07:30", stored.OpeningTime)

	// Isolation at the SQL level: B's row is independent, and B's reads
	// never surface A's values.
	_, err = svc.UpdateCapacity(ctx, Scope{TenantID: &tenantB}, nil, 1, "09:00", "17:00", 2)
	require.NoError(t, err)
	b, err := store.Capacity(ctx, tenantB)
	require.NoError(t, err)
	assert.Equal(t, 1, b.Bays)
	a, err := store.Capacity(ctx, tenantA)
	require.NoError(t, err)
	assert.Equal(t, 4, a.Bays, "B's write must not touch A's row")
	assert.Nil(t, b.UpdatedBy, "a system-actor write stores no updated_by")
}

// TestCapacityConstraintsEnforcedByDatabase proves the migration's CHECK
// constraints reject what the API validation exists to catch anyway
// (defense in depth), and that the operating-hours CHECK holds.
func TestCapacityConstraintsEnforcedByDatabase(t *testing.T) {
	_, store := integrationStore(t)
	ctx := t.Context()
	tenant := uuid.New()

	// Negative bays violate the CHECK constraint.
	err := store.UpsertCapacity(ctx, &Capacity{
		TenantID: tenant, Bays: -1, OpeningTime: "08:00", ClosingTime: "18:00",
	})
	require.Error(t, err, "negative bays must be rejected by the database")

	// opening_time == closing_time violates the operating-hours CHECK.
	err = store.UpsertCapacity(ctx, &Capacity{
		TenantID: tenant, Bays: 2, OpeningTime: "08:00", ClosingTime: "08:00",
	})
	require.Error(t, err, "an empty operating window must be rejected by the database")

	// A tenant-less row violates the single-row-per-tenant key's NOT NULL.
	err = store.UpsertCapacity(ctx, &Capacity{
		TenantID: uuid.Nil, Bays: 2, OpeningTime: "08:00", ClosingTime: "18:00",
	})
	require.Error(t, err, "a tenant-less capacity row must be rejected by the database")

	// The valid write still lands after the rejections.
	err = store.UpsertCapacity(ctx, &Capacity{
		TenantID: tenant, Bays: 2, OpeningTime: "08:00", ClosingTime: "18:00", TechnicianSlots: 3,
	})
	require.NoError(t, err)
}

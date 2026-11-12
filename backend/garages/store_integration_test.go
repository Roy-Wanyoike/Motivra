package garages

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	garagesmigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/garages"
	identitymigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/identity"
	"github.com/Roy-Wanyoike/Motivra/backend/platform/pgtest"
)

// garagesTables are every table of the garages chain, truncated between
// tests so each starts from a clean schema.
var garagesTables = []string{"garage_capacity", "garage_appointments"}

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
	// The appointment lifecycle commits its audit rows into the shared
	// audit_log (the identity chain's table), so the identity chain is
	// applied too — the same posture as the inspections verification suite.
	pgtest.Migrate(t, pool, identitymigrations.FS, "schema_migrations_identity")
	pgtest.Truncate(t, pool, append([]string{"audit_log"}, garagesTables...)...)

	store := NewPostgresStore(pool)
	return NewService(store, nil), store
}

// TestAppointmentLifecycleIntegration drives the appointment calendar
// against real PostgreSQL (issue #169): slot validation against the
// declared capacity, the double-booking exclusion constraint, the
// explicit transition state machine, the same-transaction audit seam and
// cross-tenant isolation at the SQL level.
func TestAppointmentLifecycleIntegration(t *testing.T) {
	svc, store := integrationStore(t)
	ctx := t.Context()
	tenant := uuid.New()
	actor := uuid.New()
	jobID := uuid.New()

	// Declare capacity: without it the garage cannot take booked work.
	_, err := svc.UpdateCapacity(ctx, Scope{TenantID: &tenant}, &actor, 2, "08:00", "18:00", 2)
	require.NoError(t, err)

	slot := futureSlot()
	created, err := svc.CreateAppointment(ctx, actor, tenant, jobID, slot, nil, nil)
	require.NoError(t, err)
	require.Equal(t, AppointmentRequested, created.Status)
	require.False(t, created.CreatedAt.IsZero())

	// Double-booking: the same window is refused, both by the
	// transactional check and by the EXCLUDE constraint underneath it.
	_, err = svc.CreateAppointment(ctx, actor, tenant, uuid.New(), slot.Add(30*time.Minute), nil, nil)
	require.ErrorIs(t, err, ErrSlotConflict)
	_, err = svc.CreateAppointment(ctx, actor, tenant, uuid.New(), slot, nil, nil)
	require.ErrorIs(t, err, ErrSlotConflict)
	// Adjacent and other-bay bookings fit.
	bay := "bay-1"
	_, err = svc.CreateAppointment(ctx, actor, tenant, uuid.New(), slot.Add(time.Hour), &bay, nil)
	require.NoError(t, err)

	// Explicit transitions only; the audit row commits with the move.
	confirmed, err := svc.TransitionAppointment(ctx, Scope{TenantID: &tenant}, actor, RoleGarage, created.ID, AppointmentConfirmed)
	require.NoError(t, err)
	assert.Equal(t, AppointmentConfirmed, confirmed.Status)
	_, err = svc.TransitionAppointment(ctx, Scope{TenantID: &tenant}, actor, RoleGarage, created.ID, AppointmentCompleted)
	require.NoError(t, err)
	_, err = svc.TransitionAppointment(ctx, Scope{TenantID: &tenant}, actor, RoleGarage, created.ID, AppointmentCancelled)
	require.ErrorIs(t, err, ErrInvalidTransition, "terminal states are immutable")

	rows, err := store.pool.Query(ctx,
		`SELECT action FROM audit_log WHERE object_type = $1 AND object_id = $2 ORDER BY created_at, id`, AppointmentObjectType, created.ID)
	require.NoError(t, err)
	var actions []string
	for rows.Next() {
		var action string
		require.NoError(t, rows.Scan(&action))
		actions = append(actions, action)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{ActionAppointmentCreated, ActionAppointmentConfirmed, ActionAppointmentCompleted}, actions,
		"one append-only audit row per lifecycle change, in order")

	// A foreign scope reads the row as missing (cross-tenant probing
	// cannot confirm existence) and cannot transition it.
	_, err = store.GetAppointment(ctx, created.ID, Scope{TenantID: &tenant})
	require.NoError(t, err)
	_, err = store.GetAppointment(ctx, created.ID, Scope{})
	require.ErrorIs(t, err, ErrAppointmentNotFound, "a zero scope matches nothing")
	_, err = store.TransitionAppointment(ctx, created.ID, Scope{}, []string{AppointmentConfirmed}, AppointmentCancelled,
		AppointmentAuditRecord{Action: ActionAppointmentCancelled})
	require.ErrorIs(t, err, ErrAppointmentNotFound)

	// The audit seam is same-transaction: renaming audit_log away fails
	// the transition and rolls the status change back.
	_, err = store.pool.Exec(ctx, `ALTER TABLE audit_log RENAME TO audit_log_gone`)
	require.NoError(t, err)
	_, err = svc.TransitionAppointment(ctx, Scope{TenantID: &tenant}, actor, RoleGarage, created.ID, AppointmentCancelled)
	require.Error(t, err, "the transition must fail without its audit row")
	_, err = store.pool.Exec(ctx, `ALTER TABLE IF EXISTS audit_log_gone RENAME TO audit_log`)
	require.NoError(t, err)
	completed, err := store.GetAppointment(ctx, created.ID, Scope{TenantID: &tenant})
	require.NoError(t, err)
	assert.Equal(t, AppointmentCompleted, completed.Status, "the failed transition rolled back")
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

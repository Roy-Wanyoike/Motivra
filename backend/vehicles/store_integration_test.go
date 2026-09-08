package vehicles

import (
	"context"
	"math/rand/v2"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	vehiclesmigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/vehicles"
	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// integrationPool skips the test unless TEST_DATABASE_URL is set, then opens
// a pool and applies the vehicles migration chain with the domain-specific
// schema_migrations table.
func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres integration test")
	}
	ctx := context.Background()
	pool, err := platform.NewPostgres(ctx, url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, platform.MigrateUp(ctx, pool, vehiclesmigrations.FS, "schema_migrations_vehicles"))
	return pool
}

// randomVIN builds a structurally valid VIN: a random 17-character body with
// a computed ISO 3779 check digit at position 9, so integration runs never
// collide on the UNIQUE vin_normalized constraint.
func randomVIN(t *testing.T) string {
	t.Helper()
	const alphabet = "ABCDEFGHJKLMNPRSTUVWXYZ0123456789"
	body := make([]byte, vinLength)
	for i := range body {
		body[i] = alphabet[rand.IntN(len(alphabet))]
	}
	vin := string(body)
	check, err := vinCheckDigit(vin)
	require.NoError(t, err)
	return vin[:8] + string(check) + vin[9:]
}

// TestVehiclePassportLifecycleIntegration exercises the registry end to end
// against a real PostgreSQL: registration, duplicate-VIN conflict, mileage
// updates, the passport view and the append-only trigger.
func TestVehiclePassportLifecycleIntegration(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	store := NewPostgresStore(pool)
	svc := NewService(store, nil)

	v := &Vehicle{
		OwnerUserID:     uuid.New(),
		VIN:             randomVIN(t),
		Make:            "Honda",
		Model:           "Accord",
		Year:            2020,
		Plate:           "KDA 123A",
		Color:           "Silver",
		MileageLatestKm: 1000,
	}
	require.NoError(t, svc.RegisterVehicle(ctx, v, v.OwnerUserID))
	require.NotEqual(t, uuid.Nil, v.ID)
	require.False(t, v.CreatedAt.IsZero(), "created_at must be returned from the insert")
	require.False(t, v.UpdatedAt.IsZero(), "updated_at must be returned from the insert")

	// Registering the same VIN again (different owner) must conflict.
	duplicate := &Vehicle{
		OwnerUserID: uuid.New(),
		VIN:         v.VIN,
		Make:        "Honda",
		Model:       "Civic",
		Year:        2021,
	}
	err := svc.RegisterVehicle(ctx, duplicate, duplicate.OwnerUserID)
	requireStatus(t, err, 409)

	// Round-trip through GetVehicle.
	got, err := svc.GetVehicle(ctx, v.ID)
	require.NoError(t, err)
	require.Equal(t, v.VIN, got.VIN)
	require.Equal(t, v.Make, got.Make)
	require.Equal(t, v.Year, got.Year)
	require.Equal(t, int64(1000), got.MileageLatestKm)

	// Registration seeds exactly one note event.
	events, err := store.ListHistory(ctx, v.ID, 10, 0)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, HistoryEventTypeNote, events[0].EventType)
	require.Equal(t, initialHistorySummary, events[0].Summary)

	// The components table exists and cascades from the vehicle.
	_, err = pool.Exec(ctx,
		`INSERT INTO vehicle_components (vehicle_id, name, category, notes) VALUES ($1, $2, $3, $4)`,
		v.ID, "Engine", "powertrain", "2.0 petrol")
	require.NoError(t, err)

	// A later mileage reading must land after the note event.
	time.Sleep(20 * time.Millisecond)
	require.NoError(t, svc.RecordMileage(ctx, v.ID, 1500, &v.OwnerUserID))

	passport, err := svc.Passport(ctx, v.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1500), passport.MileageLatestKm)
	require.Equal(t, int64(2), passport.ServiceEventCount)
	require.NotNil(t, passport.LastServiceAt)

	// The odometer must never decrease.
	err = svc.RecordMileage(ctx, v.ID, 1400, &v.OwnerUserID)
	requireStatus(t, err, 409)

	// History is ordered occurred_at DESC: the mileage event comes first.
	events, err = svc.History(ctx, v.ID, 10, 0)
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, HistoryEventTypeMileage, events[0].EventType)
	require.NotNil(t, events[0].OdometerKm)
	require.Equal(t, int64(1500), *events[0].OdometerKm)
	require.False(t, events[0].OccurredAt.Before(events[1].OccurredAt))

	// Append-only enforcement: raw UPDATE and DELETE must both fail.
	_, err = pool.Exec(ctx,
		`UPDATE vehicle_service_history SET summary = 'tampered' WHERE vehicle_id = $1`, v.ID)
	require.Error(t, err, "UPDATE on vehicle_service_history must be rejected")
	require.ErrorContains(t, err, "vehicle_service_history is append-only")

	_, err = pool.Exec(ctx,
		`DELETE FROM vehicle_service_history WHERE vehicle_id = $1`, v.ID)
	require.Error(t, err, "DELETE on vehicle_service_history must be rejected")
	require.ErrorContains(t, err, "vehicle_service_history is append-only")

	// Service-level validation still guards the store.
	err = svc.RecordEvent(ctx, &HistoryEvent{
		VehicleID:  v.ID,
		EventType:  "oil-change",
		Summary:    "invalid type must be rejected",
		OccurredAt: time.Now().UTC(),
	})
	requireStatus(t, err, 422)
}

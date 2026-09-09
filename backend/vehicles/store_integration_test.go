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

	// Round-trip through GetVehicle. The registering owner is a personal
	// principal, so their owner-derived scope must see the row (ADR-0004:
	// scope comes from the authenticated identity, exercised here end to end).
	ownerScope := VehicleScope{OwnerID: &v.OwnerUserID}
	got, err := svc.GetVehicle(ctx, v.ID, ownerScope)
	require.NoError(t, err)
	require.Equal(t, v.VIN, got.VIN)
	require.Equal(t, v.Make, got.Make)
	require.Equal(t, v.Year, got.Year)
	require.Equal(t, int64(1000), got.MileageLatestKm)

	// Registration seeds exactly one note event.
	events, err := store.ListHistory(ctx, v.ID, ownerScope, 10, 0)
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
	require.NoError(t, svc.RecordMileage(ctx, v.ID, 1500, &v.OwnerUserID, ownerScope))

	passport, err := svc.Passport(ctx, v.ID, ownerScope)
	require.NoError(t, err)
	require.Equal(t, int64(1500), passport.MileageLatestKm)
	require.Equal(t, int64(2), passport.ServiceEventCount)
	require.NotNil(t, passport.LastServiceAt)

	// The odometer must never decrease.
	err = svc.RecordMileage(ctx, v.ID, 1400, &v.OwnerUserID, ownerScope)
	requireStatus(t, err, 409)

	// History is ordered occurred_at DESC: the mileage event comes first.
	events, err = svc.History(ctx, v.ID, ownerScope, 10, 0)
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

// TestListVehiclesKeysetIntegration verifies against real PostgreSQL that
// the registry listing enforces the tenant boundary inside the SQL query and
// that keyset pagination walks with no repeats and no gaps.
func TestListVehiclesKeysetIntegration(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	store := NewPostgresStore(pool)

	tenantA, tenantB := uuid.New(), uuid.New()

	// Five tenant-A vehicles with strictly increasing created_at
	// (ordered[0] oldest).
	ordered := make([]Vehicle, 0, 5)
	for i := 0; i < 5; i++ {
		v := &Vehicle{
			TenantID:        &tenantA,
			OwnerUserID:     uuid.New(),
			VIN:             randomVIN(t),
			Make:            "Honda",
			Model:           "Accord",
			Year:            2020,
			MileageLatestKm: int64(1000 + i),
		}
		require.NoError(t, store.CreateVehicle(ctx, v))
		time.Sleep(5 * time.Millisecond) // guarantee distinct created_at
		ordered = append(ordered, *v)
	}
	// A tenant-B row and a personal row that must never leak into tenant A.
	require.NoError(t, store.CreateVehicle(ctx, &Vehicle{
		TenantID: &tenantB, OwnerUserID: uuid.New(),
		VIN: randomVIN(t), Make: "Toyota", Model: "Hilux", Year: 2021,
	}))
	require.NoError(t, store.CreateVehicle(ctx, &Vehicle{
		OwnerUserID: uuid.New(),
		VIN:         randomVIN(t), Make: "Nissan", Model: "X-Trail", Year: 2022,
	}))

	scopeA := VehicleScope{TenantID: &tenantA}

	// Isolation: tenant A sees exactly its five rows, newest first.
	page, _, err := store.ListVehicles(ctx, scopeA, nil, 50)
	require.NoError(t, err)
	require.Len(t, page, 5)
	for i := range page {
		require.Equal(t, ordered[len(ordered)-1-i].ID, page[i].ID, "newest first")
	}

	// Walk keyset pages of two and confirm no repeats and no gaps.
	seen := map[uuid.UUID]bool{}
	var cursor *VehicleCursor
	pages := 0
	for {
		page, next, err := store.ListVehicles(ctx, scopeA, cursor, 2)
		require.NoError(t, err)
		for _, v := range page {
			require.False(t, seen[v.ID], "keyset pages must not repeat rows")
			seen[v.ID] = true
		}
		pages++
		if next == nil {
			break
		}
		cursor = next
	}
	require.Equal(t, 3, pages, "five rows at page size two walk across three pages")
	require.Len(t, seen, 5)
}

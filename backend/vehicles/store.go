package vehicles

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrVehicleNotFound is returned by Store reads when the vehicle does not
// exist or is outside the caller's scope. Services translate it into a
// platform not-found error.
var ErrVehicleNotFound = errors.New("vehicles: vehicle not found")

// History pagination defaults applied by ListHistory when limit is not
// provided or exceeds the cap.
const (
	defaultHistoryLimit = 50
	maxHistoryLimit     = 200
)

// Registry listing pagination defaults applied by ListVehicles when limit is
// not provided or exceeds the cap.
const (
	defaultListLimit = 50
	maxListLimit     = 100
)

// VehicleScope is the data boundary applied to every vehicle query. It is
// derived exclusively from the authenticated JWT claims (ADR-0004), never
// from client input, and is enforced inside the repository query.
//
// Exactly one of the following applies:
//
//   - TenantID set: an organizational principal; only that tenant's vehicles
//     are visible.
//   - TenantID nil and OwnerID set: a personal principal; only vehicles
//     registered outside any tenant and owned by the principal are visible.
//   - Zero value (both nil): unrestricted — reserved for control-plane roles
//     (ADMIN, SUPER_ADMIN) which read across tenants under the audit
//     obligations of ADR-0004. It is never produced for CUSTOMER-style
//     claims; the HTTP layer constructs scopes via scopeFromClaims.
type VehicleScope struct {
	TenantID *uuid.UUID
	OwnerID  *uuid.UUID
}

// Unrestricted reports whether the scope exempts the query from tenant
// filtering (control-plane roles, ADR-0004).
func (s VehicleScope) Unrestricted() bool { return s.TenantID == nil && s.OwnerID == nil }

// vehicleScopePredicate renders the SQL condition constraining rows to
// scope. tenantCol and ownerCol name the boundary columns of the queried
// relation, qualified with a join alias when needed. nextParam is the
// placeholder number the condition starts at; it consumes one parameter
// ("" and nil when the scope is unrestricted).
func vehicleScopePredicate(scope VehicleScope, tenantCol, ownerCol string, nextParam int) (string, []any) {
	switch {
	case scope.Unrestricted():
		return "", nil
	case scope.TenantID != nil:
		return fmt.Sprintf("%s = $%d", tenantCol, nextParam), []any{*scope.TenantID}
	default:
		return fmt.Sprintf("%s IS NULL AND %s = $%d", tenantCol, ownerCol, nextParam), []any{*scope.OwnerID}
	}
}

// Store is the persistence contract for the vehicles domain. Implementations
// must keep vehicle_service_history append-only.
type Store interface {
	// CreateVehicle inserts v and its initial history event ("note":
	// "Vehicle registered on Motivra") in a single transaction. It assigns
	// v.ID when unset and populates v.CreatedAt and v.UpdatedAt.
	CreateVehicle(ctx context.Context, v *Vehicle) error

	// GetVehicle returns the vehicle with the given id within the caller's
	// scope, or an error wrapping ErrVehicleNotFound when it does not exist
	// or is invisible to the scope.
	GetVehicle(ctx context.Context, vehicleID uuid.UUID, scope VehicleScope) (Vehicle, error)

	// ListVehicles returns up to limit vehicles visible to scope, newest
	// first (created_at DESC, id DESC). cursor positions the page after the
	// previously returned row (nil starts a fresh listing); limit <= 0
	// selects defaultListLimit and limit is capped at maxListLimit. When
	// another page exists, the returned cursor carries the last row's keyset
	// position; it is nil on the final page.
	ListVehicles(ctx context.Context, scope VehicleScope, cursor *VehicleCursor, limit int) ([]Vehicle, *VehicleCursor, error)

	// RecordHistoryEvent appends e to the vehicle's service history. It
	// fails with an error wrapping ErrVehicleNotFound when the vehicle does
	// not exist.
	RecordHistoryEvent(ctx context.Context, e HistoryEvent) error

	// ListHistory returns the vehicle's history ordered by occurred_at
	// descending, for a vehicle visible to scope (the boundary is joined
	// onto the registry so an out-of-scope vehicle yields
	// ErrVehicleNotFound). limit <= 0 selects defaultHistoryLimit and limit
	// is capped at maxHistoryLimit; offset < 0 is treated as 0.
	ListHistory(ctx context.Context, vehicleID uuid.UUID, scope VehicleScope, limit, offset int) ([]HistoryEvent, error)

	// GetPassport returns the read-only passport assembled from the
	// vehicle_passports view for a vehicle within scope, or an error
	// wrapping ErrVehicleNotFound.
	GetPassport(ctx context.Context, vehicleID uuid.UUID, scope VehicleScope) (Passport, error)

	// UpdateMileage sets the vehicle's latest odometer reading and appends a
	// mileage history event in a single transaction. It fails with an error
	// wrapping ErrVehicleNotFound when the vehicle does not exist.
	UpdateMileage(ctx context.Context, vehicleID uuid.UUID, km int64) error
}

// PostgresStore implements Store on PostgreSQL through pgx.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// Compile-time assertion that PostgresStore satisfies Store.
var _ Store = (*PostgresStore)(nil)

// NewPostgresStore returns a Store backed by pool. The pool must already
// have the vehicles migrations applied (platform.MigrateUp with
// vehiclesmigrations.FS).
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

// initialHistorySummary is the summary of the history event recorded when a
// vehicle is registered.
const initialHistorySummary = "Vehicle registered on Motivra"

const vehicleColumns = `id, tenant_id, owner_user_id, vin_normalized, make, model, year_of_manufacture, plate, color, mileage_latest_km, created_at, updated_at`

// CreateVehicle implements Store.
func (s *PostgresStore) CreateVehicle(ctx context.Context, v *Vehicle) error {
	if v == nil {
		return fmt.Errorf("vehicles: create vehicle: vehicle is required")
	}
	if v.ID == uuid.Nil {
		v.ID = uuid.New()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("vehicles: begin create vehicle: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	err = tx.QueryRow(ctx, `
                INSERT INTO vehicles
                        (id, tenant_id, owner_user_id, vin_normalized, make, model, year_of_manufacture, plate, color, mileage_latest_km)
                VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
                RETURNING created_at, updated_at`,
		v.ID, v.TenantID, v.OwnerUserID, v.VIN, v.Make, v.Model, v.Year, v.Plate, v.Color, v.MileageLatestKm,
	).Scan(&v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return fmt.Errorf("vehicles: insert vehicle: %w", err)
	}

	_, err = tx.Exec(ctx, `
                INSERT INTO vehicle_service_history (vehicle_id, event_type, occurred_at, summary, recorded_by)
                VALUES ($1, $2, $3, $4, $5)`,
		v.ID, HistoryEventTypeNote, time.Now().UTC(), initialHistorySummary, v.OwnerUserID,
	)
	if err != nil {
		return fmt.Errorf("vehicles: insert initial history event: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("vehicles: commit create vehicle: %w", err)
	}
	return nil
}

// GetVehicle implements Store. The scope predicate runs inside the query
// (ADR-0004): an out-of-scope vehicle is indistinguishable from a missing
// one.
func (s *PostgresStore) GetVehicle(ctx context.Context, vehicleID uuid.UUID, scope VehicleScope) (Vehicle, error) {
	pred, predArgs := vehicleScopePredicate(scope, "tenant_id", "owner_user_id", 2)
	where := "id = $1"
	args := []any{vehicleID}
	if pred != "" {
		where += " AND " + pred
		args = append(args, predArgs...)
	}
	row := s.pool.QueryRow(ctx, `
                SELECT `+vehicleColumns+`
                FROM vehicles
                WHERE `+where, args...)
	v, err := scanVehicle(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Vehicle{}, fmt.Errorf("vehicles: get vehicle %s: %w", vehicleID, ErrVehicleNotFound)
		}
		return Vehicle{}, fmt.Errorf("vehicles: get vehicle: %w", err)
	}
	return v, nil
}

// ListVehicles implements Store: one keyset page of the registry, scoped to
// the caller. The query probes limit+1 rows so the returned cursor is exact
// without a second count query.
func (s *PostgresStore) ListVehicles(ctx context.Context, scope VehicleScope, cursor *VehicleCursor, limit int) ([]Vehicle, *VehicleCursor, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}

	pred, predArgs := vehicleScopePredicate(scope, "tenant_id", "owner_user_id", 1)
	where := "TRUE"
	args := []any{}
	if pred != "" {
		where = pred
		args = append(args, predArgs...)
	}
	if cursor != nil {
		args = append(args, cursor.CreatedAt.UTC(), cursor.ID)
		where += fmt.Sprintf(" AND (created_at, id) < ($%d, $%d)", len(args)-1, len(args))
	}
	args = append(args, limit+1)
	query := fmt.Sprintf(`
                SELECT `+vehicleColumns+`
                FROM vehicles
                WHERE %s
                ORDER BY created_at DESC, id DESC
                LIMIT $%d`, where, len(args))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("vehicles: list vehicles: %w", err)
	}
	defer rows.Close()

	out := make([]Vehicle, 0, limit+1)
	for rows.Next() {
		v, err := scanVehicle(rows)
		if err != nil {
			return nil, nil, fmt.Errorf("vehicles: scan vehicle: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("vehicles: list vehicles: %w", err)
	}

	var next *VehicleCursor
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = &VehicleCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return out, next, nil
}

// RecordHistoryEvent implements Store.
func (s *PostgresStore) RecordHistoryEvent(ctx context.Context, e HistoryEvent) error {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	_, err := s.pool.Exec(ctx, `
                INSERT INTO vehicle_service_history
                        (id, vehicle_id, event_type, occurred_at, odometer_km, summary, evidence_ref, recorded_by)
                VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		e.ID, e.VehicleID, e.EventType, e.OccurredAt, e.OdometerKm, e.Summary, e.EvidenceRef, e.RecordedBy,
	)
	if err != nil {
		if isForeignKeyViolation(err) {
			return fmt.Errorf("vehicles: record history event: %w", ErrVehicleNotFound)
		}
		return fmt.Errorf("vehicles: record history event: %w", err)
	}
	return nil
}

// ListHistory implements Store. The scope boundary is joined onto the
// registry so history for an out-of-scope vehicle is also denied at the
// query level.
func (s *PostgresStore) ListHistory(ctx context.Context, vehicleID uuid.UUID, scope VehicleScope, limit, offset int) ([]HistoryEvent, error) {
	if limit <= 0 {
		limit = defaultHistoryLimit
	}
	if limit > maxHistoryLimit {
		limit = maxHistoryLimit
	}
	if offset < 0 {
		offset = 0
	}
	pred, predArgs := vehicleScopePredicate(scope, "v.tenant_id", "v.owner_user_id", 2)
	where := "h.vehicle_id = $1"
	args := []any{vehicleID}
	if pred != "" {
		where += " AND " + pred
		args = append(args, predArgs...)
	}
	args = append(args, limit, offset)
	query := fmt.Sprintf(`
                SELECT h.id, h.vehicle_id, h.event_type, h.occurred_at, h.odometer_km, h.summary, h.evidence_ref, h.recorded_by, h.created_at
                FROM vehicle_service_history h
                JOIN vehicles v ON v.id = h.vehicle_id
                WHERE %s
                ORDER BY h.occurred_at DESC
                LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("vehicles: list history: %w", err)
	}
	defer rows.Close()

	out := make([]HistoryEvent, 0, limit)
	for rows.Next() {
		var e HistoryEvent
		if err := rows.Scan(&e.ID, &e.VehicleID, &e.EventType, &e.OccurredAt, &e.OdometerKm, &e.Summary, &e.EvidenceRef, &e.RecordedBy, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("vehicles: scan history event: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("vehicles: list history: %w", err)
	}
	return out, nil
}

// GetPassport implements Store.
func (s *PostgresStore) GetPassport(ctx context.Context, vehicleID uuid.UUID, scope VehicleScope) (Passport, error) {
	pred, predArgs := vehicleScopePredicate(scope, "tenant_id", "owner_user_id", 2)
	where := "vehicle_id = $1"
	args := []any{vehicleID}
	if pred != "" {
		where += " AND " + pred
		args = append(args, predArgs...)
	}
	row := s.pool.QueryRow(ctx, `
                SELECT vehicle_id, tenant_id, owner_user_id, vin_normalized, make, model, year_of_manufacture,
                       plate, color, mileage_latest_km, service_event_count, last_service_at, created_at
                FROM vehicle_passports
                WHERE `+where, args...)
	var p Passport
	err := row.Scan(&p.ID, &p.TenantID, &p.OwnerUserID, &p.VIN, &p.Make, &p.Model, &p.Year,
		&p.Plate, &p.Color, &p.MileageLatestKm, &p.ServiceEventCount, &p.LastServiceAt, &p.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Passport{}, fmt.Errorf("vehicles: get passport %s: %w", vehicleID, ErrVehicleNotFound)
		}
		return Passport{}, fmt.Errorf("vehicles: get passport: %w", err)
	}
	return p, nil
}

// UpdateMileage implements Store.
func (s *PostgresStore) UpdateMileage(ctx context.Context, vehicleID uuid.UUID, km int64) error {
	if km < 0 {
		return fmt.Errorf("vehicles: update mileage: km must be non-negative, got %d", km)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("vehicles: begin update mileage: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
                UPDATE vehicles
                SET mileage_latest_km = $2, updated_at = now()
                WHERE id = $1`, vehicleID, km)
	if err != nil {
		return fmt.Errorf("vehicles: update mileage: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("vehicles: update mileage %s: %w", vehicleID, ErrVehicleNotFound)
	}

	summary := fmt.Sprintf("Odometer reading recorded: %d km", km)
	_, err = tx.Exec(ctx, `
                INSERT INTO vehicle_service_history (vehicle_id, event_type, occurred_at, odometer_km, summary)
                VALUES ($1, $2, $3, $4, $5)`,
		vehicleID, HistoryEventTypeMileage, time.Now().UTC(), km, summary,
	)
	if err != nil {
		return fmt.Errorf("vehicles: insert mileage history event: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("vehicles: commit update mileage: %w", err)
	}
	return nil
}

// scanVehicle scans one row of vehicleColumns into a Vehicle.
func scanVehicle(row pgx.Row) (Vehicle, error) {
	var v Vehicle
	err := row.Scan(&v.ID, &v.TenantID, &v.OwnerUserID, &v.VIN, &v.Make, &v.Model, &v.Year,
		&v.Plate, &v.Color, &v.MileageLatestKm, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return Vehicle{}, err
	}
	return v, nil
}

// isForeignKeyViolation reports whether err is a PostgreSQL foreign-key
// violation (SQLSTATE 23503), which in this domain means the referenced
// vehicle does not exist.
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23503"
	}
	return false
}

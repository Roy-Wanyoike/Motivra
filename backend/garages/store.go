package garages

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the persistence contract for the garages domain. Implementations
// must keep one capacity row per tenant (single-row-per-tenant constraint)
// and must perform read-modify-write flows inside single transactions.
//
// Every read method takes a Scope derived from the authenticated principal
// (ADR-0004: the repository layer enforces scope; there is no unscoped
// variant). Rows the scope does not satisfy — including under the zero
// value — yield not-found or an empty page exactly like a missing row, so
// cross-principal probing cannot confirm existence.
type Store interface {
	// Capacity returns the capacity row of the tenant, or an error wrapping
	// ErrCapacityNotFound when the tenant has never written one. Operators
	// carry no tenant boundary; the caller must pass the concrete tenant.
	Capacity(ctx context.Context, tenantID uuid.UUID) (Capacity, error)

	// UpsertCapacity inserts or updates the tenant's single capacity row,
	// populating c.CreatedAt/c.UpdatedAt from the database.
	UpsertCapacity(ctx context.Context, c *Capacity) error
}

// PostgresStore implements Store on PostgreSQL through pgx.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// Compile-time assertion that PostgresStore satisfies Store.
var _ Store = (*PostgresStore)(nil)

// NewPostgresStore returns a Store backed by pool. The pool must already
// have the garages migrations applied (platform.MigrateUp with
// garagesmigrations.FS).
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

const capacityColumns = `tenant_id, bays, opening_time, closing_time, technician_slots, updated_by, created_at, updated_at`

// Capacity implements Store. The lookup is tenant-exact: a scope never
// widens it (the service only calls this with scope.TenantID), so there is
// no cross-tenant read surface at all.
func (s *PostgresStore) Capacity(ctx context.Context, tenantID uuid.UUID) (Capacity, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+capacityColumns+`
		FROM garage_capacity
		WHERE tenant_id = $1`, tenantID)
	c, err := scanCapacity(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Capacity{}, fmt.Errorf("garages: get capacity %s: %w", tenantID, ErrCapacityNotFound)
		}
		return Capacity{}, fmt.Errorf("garages: get capacity: %w", err)
	}
	return c, nil
}

// UpsertCapacity implements Store. The unique constraint on tenant_id
// (migration 000001, "single row per tenant") makes the write idempotent
// under concurrency: two racing writes for one tenant converge on one row.
func (s *PostgresStore) UpsertCapacity(ctx context.Context, c *Capacity) error {
	if c == nil {
		return fmt.Errorf("garages: upsert capacity: capacity is required")
	}
	if c.TenantID == uuid.Nil {
		return fmt.Errorf("garages: upsert capacity: tenant_id is required")
	}
	opening, err := parseWallClock(c.OpeningTime)
	if err != nil {
		return fmt.Errorf("garages: upsert capacity: %w", err)
	}
	closing, err := parseWallClock(c.ClosingTime)
	if err != nil {
		return fmt.Errorf("garages: upsert capacity: %w", err)
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO garage_capacity
			(tenant_id, bays, opening_time, closing_time, technician_slots, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tenant_id) DO UPDATE SET
			bays             = EXCLUDED.bays,
			opening_time     = EXCLUDED.opening_time,
			closing_time     = EXCLUDED.closing_time,
			technician_slots = EXCLUDED.technician_slots,
			updated_by       = EXCLUDED.updated_by,
			updated_at       = now()
		RETURNING created_at, updated_at`,
		c.TenantID, c.Bays, opening, closing, c.TechnicianSlots, c.UpdatedBy,
	).Scan(&c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return fmt.Errorf("garages: upsert capacity: %w", err)
	}
	return nil
}

func scanCapacity(row pgx.Row) (Capacity, error) {
	var (
		c                Capacity
		opening, closing time.Time
	)
	err := row.Scan(&c.TenantID, &c.Bays, &opening, &closing, &c.TechnicianSlots, &c.UpdatedBy, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return Capacity{}, err
	}
	c.OpeningTime = opening.Format(TimeLayout)
	c.ClosingTime = closing.Format(TimeLayout)
	return c, nil
}

// parseWallClock parses a declared HH:MM operating time into the zero-date
// wall-clock time the time columns store.
func parseWallClock(raw string) (time.Time, error) {
	return time.Parse(TimeLayout, raw)
}

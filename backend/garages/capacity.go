package garages

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// TimeLayout is the wall-clock layout of opening_time/closing_time
// ("08:00"). Operating hours carry no timezone: they are the garage's local
// business hours as declared by the garage itself, and consumers that
// compare them to instants must supply the garage's timezone (tracked
// follow-up; the appointments conflict logic of issue #169 deliberately
// does not guess one).
const TimeLayout = "15:04"

// Defaults reported while a tenant has never written a capacity row: zero
// declared capacity (no bays, no technician slots) and the conventional
// 08:00–18:00 service day. Zero bays means "the garage cannot take booked
// work yet" — the honest default for the console.
const (
	DefaultOpeningTime = "08:00"
	DefaultClosingTime = "18:00"
)

// Capacity is one partner garage's declared service capacity (issue #170):
// service lanes (bays), operating hours and parallel technician slots.
// Exactly one row exists per tenant (single-row-per-tenant constraint in
// migration 000001) and the record is derived exclusively from the
// authenticated GARAGE principal's tenant claim — never from client input.
type Capacity struct {
	// TenantID is the owning organization (the GARAGE token's tenant claim).
	TenantID uuid.UUID
	// Bays is the number of service lanes available for booked work. >= 0.
	Bays int
	// OpeningTime and ClosingTime are the operating hours in TimeLayout
	// ("08:00"). OpeningTime < ClosingTime is validated server-side and
	// enforced by a CHECK constraint.
	OpeningTime string
	ClosingTime string
	// TechnicianSlots is the parallel technician capacity. >= 0.
	TechnicianSlots int
	// UpdatedBy is the authenticated principal that last wrote the record
	// (nil for the system actor).
	UpdatedBy *uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time
}

// validate enforces the server-side rules of a capacity write: bays and
// technician slots are non-negative, both times parse as HH:MM and opening
// precedes closing. It never trusts the client: the HTTP handler applies it
// before the store (which re-checks via CHECK constraints) is touched.
func (c *Capacity) validate() error {
	if c.Bays < 0 {
		return fmt.Errorf("garages: capacity: bays must be >= 0")
	}
	if c.TechnicianSlots < 0 {
		return fmt.Errorf("garages: capacity: technician_slots must be >= 0")
	}
	opening, err := time.Parse(TimeLayout, c.OpeningTime)
	if err != nil {
		return fmt.Errorf("garages: capacity: opening_time must be HH:MM")
	}
	closing, err := time.Parse(TimeLayout, c.ClosingTime)
	if err != nil {
		return fmt.Errorf("garages: capacity: closing_time must be HH:MM")
	}
	if !opening.Before(closing) {
		return fmt.Errorf("garages: capacity: opening_time must be before closing_time")
	}
	return nil
}

// defaultCapacity is the response for a tenant that has never configured
// capacity: zero declared capacity inside the conventional service day.
func defaultCapacity(tenantID uuid.UUID) Capacity {
	return Capacity{
		TenantID:    tenantID,
		Bays:        0,
		OpeningTime: DefaultOpeningTime,
		ClosingTime: DefaultClosingTime,
	}
}

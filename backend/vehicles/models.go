package vehicles

import (
	"time"

	"github.com/google/uuid"
)

// Vehicle is the canonical registry record for one vehicle. VIN always holds
// the normalized ISO 3779 form (the vin_normalized column in PostgreSQL);
// raw user input is never persisted. TenantID is nil for consumer-owned
// vehicles and set for fleet/dealer-owned ones.
type Vehicle struct {
	ID              uuid.UUID
	TenantID        *uuid.UUID
	OwnerUserID     uuid.UUID
	VIN             string
	Make            string
	Model           string
	Year            int
	Plate           string
	Color           string
	MileageLatestKm int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// HistoryEvent is one append-only record in the vehicle's service history
// (service, repair, inspection, mileage, incident, modification or note).
// Existing events are never mutated or deleted; corrections are new events.
type HistoryEvent struct {
	ID          uuid.UUID
	VehicleID   uuid.UUID
	EventType   string
	Summary     string
	EvidenceRef string
	OccurredAt  time.Time
	OdometerKm  *int64
	RecordedBy  *uuid.UUID
	CreatedAt   time.Time
}

// Passport is the read-only, customer-facing summary of a vehicle assembled
// from the registry and its history (the vehicle_passports view). It is
// never edited directly.
type Passport struct {
	ID                uuid.UUID
	TenantID          *uuid.UUID
	OwnerUserID       uuid.UUID
	VIN               string
	Make              string
	Model             string
	Year              int
	Plate             string
	Color             string
	MileageLatestKm   int64
	ServiceEventCount int64
	LastServiceAt     *time.Time
	CreatedAt         time.Time
}

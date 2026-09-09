package vehicles

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// EventDomain is the JetStream domain prefix used when publishing: events go
// out on motivra.vehicles.<event_type> (ADR-0002).
const EventDomain = "vehicles"

// Published event types, following "<aggregate>.<event>.v<n>".
const (
	EventVehicleCreated         = "vehicle.created.v1"
	EventVehicleHistoryUpdated  = "vehicle.history.updated.v1"
	EventVehicleMileageRecorded = "vehicle.mileage.recorded.v1"
)

// History event types, mirroring the vehicle_service_history CHECK
// constraint.
const (
	HistoryEventTypeService      = "service"
	HistoryEventTypeRepair       = "repair"
	HistoryEventTypeInspection   = "inspection"
	HistoryEventTypeMileage      = "mileage"
	HistoryEventTypeIncident     = "incident"
	HistoryEventTypeModification = "modification"
	HistoryEventTypeNote         = "note"
)

// Bounds on the year of manufacture, mirroring the vehicles CHECK
// constraint. Exported so the HTTP layer can validate with the same limits.
const (
	MinYearOfManufacture = 1950
	MaxYearOfManufacture = 2100
)

// historyEventTypes is the set of valid history event types.
var historyEventTypes = map[string]struct{}{
	HistoryEventTypeService:      {},
	HistoryEventTypeRepair:       {},
	HistoryEventTypeInspection:   {},
	HistoryEventTypeMileage:      {},
	HistoryEventTypeIncident:     {},
	HistoryEventTypeModification: {},
	HistoryEventTypeNote:         {},
}

// ValidHistoryEventType reports whether t is an allowed history event type.
func ValidHistoryEventType(t string) bool {
	_, ok := historyEventTypes[t]
	return ok
}

// Service orchestrates the vehicles domain on top of a Store, publishing a
// domain event for every state change. publisher may be nil, in which case
// publishing is skipped (useful in tests and offline tooling).
type Service struct {
	store     Store
	publisher platform.Publisher
}

// NewService returns a Service over store. publisher may be nil.
func NewService(store Store, publisher platform.Publisher) *Service {
	return &Service{store: store, publisher: publisher}
}

// RegisterVehicle validates and normalizes v, persists it together with its
// initial history event and publishes vehicle.created.v1. v.VIN may be raw
// user input; it is replaced with the canonical normalized VIN. Registering
// a VIN that already exists yields a platform conflict error. Validation
// failures yield platform validation errors.
func (s *Service) RegisterVehicle(ctx context.Context, v *Vehicle, actorID uuid.UUID) error {
	if v == nil {
		return platform.ErrValidation("vehicle is required")
	}
	vin, err := ValidateVIN(v.VIN)
	if err != nil {
		return platform.ErrValidation("vin is not valid", platform.FieldError{Field: "vin", Issue: err.Error()})
	}
	v.VIN = vin
	if v.OwnerUserID == uuid.Nil {
		return platform.ErrValidation("owner_user_id is required",
			platform.FieldError{Field: "owner_user_id", Issue: "is required"})
	}
	if v.Year < MinYearOfManufacture || v.Year > MaxYearOfManufacture {
		return platform.ErrValidation(
			fmt.Sprintf("year_of_manufacture must be between %d and %d", MinYearOfManufacture, MaxYearOfManufacture),
			platform.FieldError{Field: "year_of_manufacture", Issue: "out of range"})
	}
	v.Make = strings.TrimSpace(v.Make)
	v.Model = strings.TrimSpace(v.Model)
	v.Plate = strings.TrimSpace(v.Plate)
	v.Color = strings.TrimSpace(v.Color)
	if v.Make == "" {
		return platform.ErrValidation("make is required",
			platform.FieldError{Field: "make", Issue: "is required"})
	}
	if v.Model == "" {
		return platform.ErrValidation("model is required",
			platform.FieldError{Field: "model", Issue: "is required"})
	}
	if v.MileageLatestKm < 0 {
		return platform.ErrValidation("mileage must be non-negative",
			platform.FieldError{Field: "mileage_latest_km", Issue: "must be non-negative"})
	}
	if v.ID == uuid.Nil {
		v.ID = uuid.New()
	}

	if err := s.store.CreateVehicle(ctx, v); err != nil {
		if platform.IsUniqueViolation(err) {
			return platform.ErrConflict("a vehicle with this VIN is already registered")
		}
		return fmt.Errorf("vehicles: register vehicle: %w", err)
	}

	return s.publish(ctx, EventVehicleCreated, v.ID, v.TenantID, &actorID, map[string]any{
		"vehicle_id":          v.ID.String(),
		"vin":                 v.VIN,
		"make":                v.Make,
		"model":               v.Model,
		"year_of_manufacture": v.Year,
		"plate":               v.Plate,
		"color":               v.Color,
		"mileage_latest_km":   v.MileageLatestKm,
		"owner_user_id":       v.OwnerUserID.String(),
	})
}

// GetVehicle returns the vehicle with the given id within the caller's
// scope, or a platform not-found error when it does not exist or is
// invisible to the scope.
func (s *Service) GetVehicle(ctx context.Context, vehicleID uuid.UUID, scope VehicleScope) (Vehicle, error) {
	v, err := s.store.GetVehicle(ctx, vehicleID, scope)
	if err != nil {
		if errors.Is(err, ErrVehicleNotFound) {
			return Vehicle{}, platform.ErrNotFound("vehicle not found")
		}
		return Vehicle{}, fmt.Errorf("vehicles: get vehicle: %w", err)
	}
	return v, nil
}

// RecordEvent appends one history event to the vehicle's append-only
// history and publishes vehicle.history.updated.v1. event_type must be one
// of the allowed HistoryEventType values; summary is required; a zero
// OccurredAt defaults to now. The envelope actor is the event's RecordedBy.
func (s *Service) RecordEvent(ctx context.Context, e *HistoryEvent) error {
	if e == nil {
		return platform.ErrValidation("history event is required")
	}
	if e.VehicleID == uuid.Nil {
		return platform.ErrValidation("vehicle_id is required",
			platform.FieldError{Field: "vehicle_id", Issue: "is required"})
	}
	if !ValidHistoryEventType(e.EventType) {
		return platform.ErrValidation("event_type is not supported",
			platform.FieldError{Field: "event_type", Issue: "must be one of service, repair, inspection, mileage, incident, modification, note"})
	}
	e.Summary = strings.TrimSpace(e.Summary)
	if e.Summary == "" {
		return platform.ErrValidation("summary is required",
			platform.FieldError{Field: "summary", Issue: "is required"})
	}
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}

	if err := s.store.RecordHistoryEvent(ctx, *e); err != nil {
		if errors.Is(err, ErrVehicleNotFound) {
			return platform.ErrNotFound("vehicle not found")
		}
		return fmt.Errorf("vehicles: record history event: %w", err)
	}

	payload := map[string]any{
		"vehicle_id":       e.VehicleID.String(),
		"history_event_id": e.ID.String(),
		"event_type":       e.EventType,
		"summary":          e.Summary,
		"occurred_at":      e.OccurredAt.UTC().Format(time.RFC3339Nano),
		"evidence_ref":     e.EvidenceRef,
	}
	if e.OdometerKm != nil {
		payload["odometer_km"] = *e.OdometerKm
	}
	return s.publish(ctx, EventVehicleHistoryUpdated, e.VehicleID, nil, e.RecordedBy, payload)
}

// ListVehicles returns one keyset page of the registry visible to scope,
// newest first. cursor positions the page (nil starts the listing); limit
// bounds the page size (defaults and the cap mirror Store.ListVehicles).
// The returned cursor is nil on the last page.
func (s *Service) ListVehicles(ctx context.Context, scope VehicleScope, cursor *VehicleCursor, limit int) ([]Vehicle, *VehicleCursor, error) {
	vehicles, next, err := s.store.ListVehicles(ctx, scope, cursor, limit)
	if err != nil {
		return nil, nil, fmt.Errorf("vehicles: list vehicles: %w", err)
	}
	return vehicles, next, nil
}

// History returns the vehicle's history ordered by occurred_at descending,
// newest first, for a vehicle visible to scope. Pagination semantics are
// defined by Store.ListHistory.
func (s *Service) History(ctx context.Context, vehicleID uuid.UUID, scope VehicleScope, limit, offset int) ([]HistoryEvent, error) {
	events, err := s.store.ListHistory(ctx, vehicleID, scope, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("vehicles: list history: %w", err)
	}
	return events, nil
}

// Passport returns the read-only Vehicle Passport assembled from the
// vehicle_passports view for a vehicle within scope, or a platform
// not-found error.
func (s *Service) Passport(ctx context.Context, vehicleID uuid.UUID, scope VehicleScope) (Passport, error) {
	p, err := s.store.GetPassport(ctx, vehicleID, scope)
	if err != nil {
		if errors.Is(err, ErrVehicleNotFound) {
			return Passport{}, platform.ErrNotFound("vehicle not found")
		}
		return Passport{}, fmt.Errorf("vehicles: get passport: %w", err)
	}
	return p, nil
}

// RecordMileage records a new odometer reading: it rejects negative values,
// refuses to let the odometer decrease (platform conflict error), persists
// the new reading together with a mileage history event and publishes
// vehicle.mileage.recorded.v1 carrying the old and new readings. Re-recording
// the current reading is allowed (old and new are equal).
func (s *Service) RecordMileage(ctx context.Context, vehicleID uuid.UUID, km int64, recordedBy *uuid.UUID, scope VehicleScope) error {
	if km < 0 {
		return platform.ErrValidation("mileage must be non-negative",
			platform.FieldError{Field: "odometer_km", Issue: "must be non-negative"})
	}
	current, err := s.store.GetVehicle(ctx, vehicleID, scope)
	if err != nil {
		if errors.Is(err, ErrVehicleNotFound) {
			return platform.ErrNotFound("vehicle not found")
		}
		return fmt.Errorf("vehicles: load vehicle for mileage: %w", err)
	}
	if km < current.MileageLatestKm {
		return platform.ErrConflict(fmt.Sprintf("odometer cannot decrease from %d km to %d km", current.MileageLatestKm, km))
	}
	if err := s.store.UpdateMileage(ctx, vehicleID, km); err != nil {
		if errors.Is(err, ErrVehicleNotFound) {
			return platform.ErrNotFound("vehicle not found")
		}
		return fmt.Errorf("vehicles: update mileage: %w", err)
	}

	return s.publish(ctx, EventVehicleMileageRecorded, vehicleID, current.TenantID, recordedBy, map[string]any{
		"vehicle_id": vehicleID.String(),
		"old_km":     current.MileageLatestKm,
		"new_km":     km,
	})
}

// publish builds the envelope with platform.NewEvent and hands it to the
// publisher. It is a no-op when no publisher is configured. The event
// envelope carries tenantID and actorID (empty strings when unset).
func (s *Service) publish(ctx context.Context, eventType string, aggregateID uuid.UUID, tenantID, actorID *uuid.UUID, payload map[string]any) error {
	if s.publisher == nil {
		return nil
	}
	tenant := ""
	if tenantID != nil {
		tenant = tenantID.String()
	}
	actor := ""
	if actorID != nil && *actorID != uuid.Nil {
		actor = actorID.String()
	}
	e, err := platform.NewEvent(eventType, aggregateID.String(), tenant, actor, "", payload)
	if err != nil {
		return fmt.Errorf("vehicles: build %s event: %w", eventType, err)
	}
	if err := s.publisher.Publish(ctx, EventDomain, e); err != nil {
		return fmt.Errorf("vehicles: publish %s: %w", eventType, err)
	}
	return nil
}

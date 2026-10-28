package garages

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// EventDomain is the JetStream domain prefix used when publishing: events
// go out on motivra.garages.<event_type> (ADR-0002; the bounded context is
// documented as "Garages" in ARCHITECTURE.md §1).
const EventDomain = "garages"

// Published event types, following "<aggregate>.<event>.v<n>".
const (
	// EventCapacityUpdated is published when a garage writes its capacity
	// configuration (issue #170). Downstream consumers (appointments
	// conflict logic, dispatch scoring) read the authoritative row through
	// this domain's API — the event is the change notification.
	EventCapacityUpdated = "capacity.updated.v1"
)

// ErrCapacityNotFound is returned by capacity reads when the tenant has
// never written a capacity row. The service layer surfaces the documented
// defaults instead of an error for the GET path.
var ErrCapacityNotFound = errors.New("garages: capacity not configured")

// CapacityUpdatedPayload is the payload of capacity.updated.v1.
type CapacityUpdatedPayload struct {
	TenantID        uuid.UUID  `json:"tenant_id"`
	Bays            int        `json:"bays"`
	OpeningTime     string     `json:"opening_time"`
	ClosingTime     string     `json:"closing_time"`
	TechnicianSlots int        `json:"technician_slots"`
	UpdatedBy       *uuid.UUID `json:"updated_by,omitempty"`
}

// Service is the garages domain service. It owns the capacity aggregate and
// (from issue #169) the appointment lifecycle. publisher may be nil (tests,
// offline tooling); events are simply skipped then.
type Service struct {
	store     Store
	publisher platform.Publisher
}

// NewService returns a Service over store. publisher may be nil.
func NewService(store Store, publisher platform.Publisher) *Service {
	return &Service{store: store, publisher: publisher}
}

// Capacity returns the tenant's declared capacity. A tenant that has never
// written a row gets the documented defaults (zero bays, 08:00–18:00): the
// console reads before the first write, and "not configured yet" is a
// truthful answer, not a 404.
func (s *Service) Capacity(ctx context.Context, tenantID uuid.UUID) (Capacity, error) {
	capacity, err := s.store.Capacity(ctx, tenantID)
	if err != nil {
		if errors.Is(err, ErrCapacityNotFound) {
			return defaultCapacity(tenantID), nil
		}
		return Capacity{}, fmt.Errorf("garages: capacity: %w", err)
	}
	return capacity, nil
}

// UpdateCapacity validates and persists the tenant's capacity record
// (single row per tenant, upsert) and publishes capacity.updated.v1. The
// record's tenant comes from the authenticated scope — callers pass
// scope.TenantID, never a client-supplied tenant.
func (s *Service) UpdateCapacity(ctx context.Context, scope Scope, actorID *uuid.UUID, bays int, openingTime, closingTime string, technicianSlots int) (Capacity, error) {
	if scope.TenantID == nil {
		return Capacity{}, fmt.Errorf("garages: update capacity: a tenant scope is required")
	}
	capacity := Capacity{
		TenantID:        *scope.TenantID,
		Bays:            bays,
		OpeningTime:     openingTime,
		ClosingTime:     closingTime,
		TechnicianSlots: technicianSlots,
		UpdatedBy:       actorID,
	}
	if err := capacity.validate(); err != nil {
		return Capacity{}, err
	}
	if err := s.store.UpsertCapacity(ctx, &capacity); err != nil {
		return Capacity{}, fmt.Errorf("garages: update capacity: %w", err)
	}

	if err := s.publish(ctx, EventCapacityUpdated, capacity.TenantID, capacity.TenantID, actorID, CapacityUpdatedPayload{
		TenantID:        capacity.TenantID,
		Bays:            capacity.Bays,
		OpeningTime:     capacity.OpeningTime,
		ClosingTime:     capacity.ClosingTime,
		TechnicianSlots: capacity.TechnicianSlots,
		UpdatedBy:       capacity.UpdatedBy,
	}); err != nil {
		return Capacity{}, err
	}
	return capacity, nil
}

// publish sends one domain event on the garages domain. It is a no-op when
// no publisher is configured. Publishing failures surface to the caller:
// events are part of the domain contract, not best-effort logging (mirrors
// backend/jobs). tenantID is the owning organization rendered into the
// event envelope (empty for tenant-less aggregates).
func (s *Service) publish(ctx context.Context, eventType string, aggregateID, tenantID uuid.UUID, actorID *uuid.UUID, payload any) error {
	if s.publisher == nil {
		return nil
	}
	actor := ""
	if actorID != nil && *actorID != uuid.Nil {
		actor = actorID.String()
	}
	e, err := platform.NewEvent(eventType, aggregateID.String(), tenantID.String(), actor, correlationFromContext(ctx), payload)
	if err != nil {
		return fmt.Errorf("garages: build %s event: %w", eventType, err)
	}
	if err := s.publisher.Publish(ctx, EventDomain, e); err != nil {
		return fmt.Errorf("garages: publish %s: %w", eventType, err)
	}
	return nil
}

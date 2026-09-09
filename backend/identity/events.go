package identity

import (
	"context"
	"fmt"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// EventDomain is the JetStream domain prefix used when publishing: events
// go out on motivra.identity.<event_type> (ADR-0002).
const EventDomain = "identity"

// Published event types, following "<aggregate>.<event>.v<n>" (ADR-0002).
const (
	// EventUserCreated fires after a registration fully succeeds: the user
	// row, the first session and the audit entry are all written.
	EventUserCreated = "user.created.v1"
	// EventRoleGranted fires after a role grant is persisted and audited.
	EventRoleGranted = "role.granted.v1"
)

// UserCreatedPayload is the payload of user.created.v1. It carries IDs and
// state names only — never credential material.
type UserCreatedPayload struct {
	UserID uuid.UUID `json:"user_id"`
	Status string    `json:"status"`
	Roles  []Role    `json:"roles"`
}

// RoleGrantedPayload is the payload of role.granted.v1. TenantID is omitted
// for personal (tenant-less) grants.
type RoleGrantedPayload struct {
	UserID   uuid.UUID  `json:"user_id"`
	Role     Role       `json:"role"`
	TenantID *uuid.UUID `json:"tenant_id,omitempty"`
}

// correlationFromContext sources the envelope correlation_id from the
// platform server's request-ID middleware (ADR-0002: correlation is
// "propagated from the originating request"). It is empty for non-HTTP
// callers. When Temporal workflows start driving multi-request flows, the
// workflow id becomes the stable correlation source and this helper is
// where the swap lands.
func correlationFromContext(ctx context.Context) string {
	return middleware.GetReqID(ctx)
}

// publish builds the envelope with platform.NewEvent and hands it to the
// publisher. It is a no-op when no publisher is configured (tests, offline
// tooling, or a deployment without MOTIVRA_NATS_URL). Publishing failures
// surface to the caller: events are part of the domain contract, not
// best-effort logging — the same discipline the jobs and vehicles services
// apply. causation_id stays null for these request-initiated events
// (ADR-0002: "null for initiators"); once services consume events, the
// causation chain begins at the consumed event_id.
func (s *Service) publish(ctx context.Context, eventType, aggregateID, tenantID, actorID string, payload any) error {
	if s.publisher == nil {
		return nil
	}
	e, err := platform.NewEvent(eventType, aggregateID, tenantID, actorID, correlationFromContext(ctx), payload)
	if err != nil {
		return fmt.Errorf("identity: build %s event: %w", eventType, err)
	}
	if err := s.publisher.Publish(ctx, EventDomain, e); err != nil {
		return fmt.Errorf("identity: publish %s: %w", eventType, err)
	}
	return nil
}

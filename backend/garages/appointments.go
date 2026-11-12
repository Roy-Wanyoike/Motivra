package garages

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Appointment lifecycle (issue #169): requested → confirmed → completed,
// with cancelled reachable from requested/confirmed and no_show from
// confirmed. Terminal states are immutable; every other move is an
// explicit transition that appends an audit_log row in the same
// transaction (the dispatch DecisionRecorder seam, ADR-0004).
const (
	AppointmentRequested = "requested"
	AppointmentConfirmed = "confirmed"
	AppointmentCompleted = "completed"
	AppointmentCancelled = "cancelled"
	AppointmentNoShow    = "no_show"
)

// appointmentTerminalStatuses are the immutable end states.
var appointmentTerminalStatuses = map[string]bool{
	AppointmentCompleted: true,
	AppointmentCancelled: true,
	AppointmentNoShow:    true,
}

// appointmentTransitionSources maps each TARGET status to the set of
// source statuses the move is allowed from. Anything not listed is a 409.
var appointmentTransitionSources = map[string][]string{
	AppointmentConfirmed: {AppointmentRequested},
	AppointmentCompleted: {AppointmentConfirmed},
	AppointmentCancelled: {AppointmentRequested, AppointmentConfirmed},
	AppointmentNoShow:    {AppointmentConfirmed},
}

// canTransition reports whether the from → to move is an explicit edge of
// the appointment state machine.
func canTransition(from, to string) bool {
	for _, src := range appointmentTransitionSources[to] {
		if src == from {
			return true
		}
	}
	return false
}

// appointmentSlotDuration is the calendar window one appointment holds.
// The #169 contract carries only scheduled_for (no end time); the slot
// duration below is what the overlap semantics — both the transactional
// check and the database EXCLUDE constraint — quantize to.
const appointmentSlotDuration = time.Hour

// Appointment audit actions and object type (the audit_log vocabulary of
// the dispatch decision recorder, namespaced for this domain).
const (
	AppointmentObjectType         = "garage_appointment"
	ActionAppointmentCreated      = "garages.appointment.created"
	ActionAppointmentConfirmed    = "garages.appointment.confirmed"
	ActionAppointmentCompleted    = "garages.appointment.completed"
	ActionAppointmentCancelled    = "garages.appointment.cancelled"
	ActionAppointmentNoShow       = "garages.appointment.no_show"
	EventAppointmentCreated       = "appointment.created.v1"
	EventAppointmentStatusChanged = "appointment.status.changed.v1"
)

// Domain errors of the appointments aggregate. The HTTP layer maps them:
// not-found → 404, invalid transition and slot conflict → 409, slot
// validation → 422, anything else → 500 without leaking internals.
var (
	ErrAppointmentNotFound   = errors.New("garages: appointment not found")
	ErrInvalidTransition     = errors.New("garages: appointment status transition not allowed")
	ErrSlotConflict          = errors.New("garages: appointment slot conflict")
	ErrOutsideOperatingHours = errors.New("garages: appointment outside declared operating hours")
	ErrCapacityNotDeclared   = errors.New("garages: garage has not declared capacity for booked work")
	ErrPastSlot              = errors.New("garages: appointment scheduled_for is in the past")
	ErrTransitionForbidden   = errors.New("garages: appointment transitions are performed by the owning garage; ADMIN/SUPER_ADMIN may override")
)

// Appointment is one calendar slot a garage holds for a service
// request/job. The job is referenced BY ID only: no jobs-table reads or
// writes happen in this domain (the jobs aggregate stays authoritative).
type Appointment struct {
	// ID is the appointment identifier.
	ID uuid.UUID
	// TenantID is the owning garage organization (the token's tenant claim
	// for GARAGE principals; the addressed garage for CUSTOMER requests).
	TenantID uuid.UUID
	// JobID is the service request/job this appointment serves — an opaque
	// UUID reference, never dereferenced across domains.
	JobID uuid.UUID
	// ScheduledFor is the slot start (RFC 3339 instant).
	ScheduledFor time.Time
	// Bay is the optional service-lane label ("bay-2"); nil means the
	// garage's general pool.
	Bay *string
	// Notes are optional free-text handling notes (≤1024 chars).
	Notes *string
	// Status is the lifecycle state (the constants above).
	Status string
	// CreatedBy is the authenticated principal that opened the appointment.
	CreatedBy uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time
}

// active reports whether the appointment still occupies its slot (only
// requested/confirmed appointments participate in double-booking checks;
// terminal ones release the calendar).
func (a Appointment) active() bool {
	return a.Status == AppointmentRequested || a.Status == AppointmentConfirmed
}

// effectiveBay normalizes the optional bay for overlap comparisons: a
// missing bay is the garage's general pool (”).
func effectiveBay(bay *string) string {
	if bay == nil {
		return ""
	}
	return *bay
}

// AppointmentAuditRecord is one append-only audit_log row written in the
// same transaction as the lifecycle change it describes (create or
// transition). A failure to write it fails the whole request — the audit
// signal can never be lost silently (ADR-0004).
type AppointmentAuditRecord struct {
	Action   string
	ObjectID uuid.UUID
	ActorID  uuid.UUID
	TenantID uuid.UUID
	// From/To capture the status edge (From empty for creation).
	From string
	To   string
	// JobID ties the audit row back to the referenced service request.
	JobID uuid.UUID
	// Override marks an ADMIN/SUPER_ADMIN cross-tenant intervention.
	Override bool
}

// CreateSlot carries the slot facts the store needs to run the
// transactional double-booking check alongside the INSERT: the window the
// appointment claims and the tenant's declared parallel technician
// capacity (from the #170 capacity model).
type CreateSlot struct {
	Start           time.Time
	End             time.Time
	Bay             *string
	TechnicianSlots int
}

// AppointmentStore is the persistence contract for the appointments
// aggregate. Reads take a Scope derived from the authenticated principal
// (ADR-0004); rows the scope does not satisfy yield not-found exactly like
// a missing row, so cross-tenant probing cannot confirm existence. Every
// write that changes state performs its audit INSERT inside the same
// transaction.
type AppointmentStore interface {
	// CreateAppointment inserts the appointment (status requested) and the
	// creation audit row in one transaction, running the slot-conflict
	// check inside it. It returns ErrSlotConflict when the slot is taken.
	CreateAppointment(ctx context.Context, a *Appointment, slot CreateSlot, audit AppointmentAuditRecord) error

	// GetAppointment returns the appointment visible to scope, or an error
	// wrapping ErrAppointmentNotFound.
	GetAppointment(ctx context.Context, id uuid.UUID, scope Scope) (Appointment, error)

	// ListAppointments returns the tenant's appointments ordered by
	// scheduled_for (newest first), filtered and paged.
	ListAppointments(ctx context.Context, tenantID uuid.UUID, filter AppointmentFilter, limit, offset int) ([]Appointment, error)

	// TransitionAppointment moves the appointment through one explicit
	// status edge inside a single transaction (row lock, status guard,
	// UPDATE, audit INSERT) and returns the updated row.
	TransitionAppointment(ctx context.Context, id uuid.UUID, scope Scope, allowedFrom []string, to string, audit AppointmentAuditRecord) (Appointment, error)
}

// AppointmentFilter is the list query of the calendar window read.
type AppointmentFilter struct {
	Status *string
	From   *time.Time
	To     *time.Time
}

// appointmentEventPayload is the payload of the appointment events.
type appointmentEventPayload struct {
	AppointmentID  uuid.UUID `json:"appointment_id"`
	TenantID       uuid.UUID `json:"tenant_id"`
	JobID          uuid.UUID `json:"job_id"`
	ScheduledFor   time.Time `json:"scheduled_for"`
	Bay            *string   `json:"bay,omitempty"`
	Status         string    `json:"status"`
	PreviousStatus string    `json:"previous_status,omitempty"`
}

// CreateAppointment validates the requested slot against the garage's
// declared capacity model (#170) and opens the appointment in status
// requested. The tenant is the caller-supplied target garage — the route
// address a CUSTOMER books against or the GARAGE principal's own tenant
// claim — and actorID always comes from the verified token.
//
// Slot rules (server-side, never trusted from the client):
//   - the garage must have declared capacity (bays ≥ 1 and technician
//     slots ≥ 1; zero is the documented "cannot take booked work yet"),
//   - scheduled_for must be in the future and its wall clock — the
//     timezone the client declared in the RFC 3339 instant, never a
//     guessed one (see TimeLayout) — must fit inside the operating hours
//     with the full one-hour slot,
//   - the bay must be free (transactional check + DB exclusion
//     constraint) and parallel technician capacity must not be exceeded.
func (s *Service) CreateAppointment(ctx context.Context, actorID uuid.UUID, tenantID, jobID uuid.UUID, scheduledFor time.Time, bay, notes *string) (Appointment, error) {
	if s.appointments == nil {
		return Appointment{}, fmt.Errorf("garages: create appointment: store does not support appointments")
	}
	capacity, err := s.Capacity(ctx, tenantID)
	if err != nil {
		return Appointment{}, fmt.Errorf("garages: create appointment: %w", err)
	}
	if capacity.Bays <= 0 {
		return Appointment{}, fmt.Errorf("garages: create appointment: %w (bays)", ErrCapacityNotDeclared)
	}
	if capacity.TechnicianSlots <= 0 {
		return Appointment{}, fmt.Errorf("garages: create appointment: %w (technician_slots)", ErrCapacityNotDeclared)
	}
	if err := validateSlot(scheduledFor, capacity); err != nil {
		return Appointment{}, err
	}
	a := Appointment{
		TenantID:     tenantID,
		JobID:        jobID,
		ScheduledFor: scheduledFor.UTC(),
		Bay:          bay,
		Notes:        notes,
		Status:       AppointmentRequested,
		CreatedBy:    actorID,
	}
	audit := AppointmentAuditRecord{
		Action:   ActionAppointmentCreated,
		ActorID:  actorID,
		TenantID: tenantID,
		To:       AppointmentRequested,
		JobID:    jobID,
	}
	slot := CreateSlot{Start: a.ScheduledFor, End: a.ScheduledFor.Add(appointmentSlotDuration), Bay: bay, TechnicianSlots: capacity.TechnicianSlots}
	if err := s.appointments.CreateAppointment(ctx, &a, slot, audit); err != nil {
		return Appointment{}, fmt.Errorf("garages: create appointment: %w", err)
	}
	if err := s.publish(ctx, EventAppointmentCreated, a.ID, a.TenantID, &actorID, appointmentEventPayload{
		AppointmentID: a.ID,
		TenantID:      a.TenantID,
		JobID:         a.JobID,
		ScheduledFor:  a.ScheduledFor,
		Bay:           a.Bay,
		Status:        a.Status,
	}); err != nil {
		return Appointment{}, err
	}
	return a, nil
}

// validateSlot enforces the operating-hours fit of a requested slot. The
// wall clock is taken from the RFC 3339 instant's own offset — the
// garage's local business clock as the client declared it — because the
// capacity model deliberately carries no timezone (see TimeLayout).
func validateSlot(scheduledFor time.Time, capacity Capacity) error {
	if !scheduledFor.After(time.Now()) {
		return fmt.Errorf("garages: create appointment: %w", ErrPastSlot)
	}
	opening, err := time.Parse(TimeLayout, capacity.OpeningTime)
	if err != nil {
		return fmt.Errorf("garages: create appointment: capacity %w", err)
	}
	closing, err := time.Parse(TimeLayout, capacity.ClosingTime)
	if err != nil {
		return fmt.Errorf("garages: create appointment: capacity %w", err)
	}
	hour, min, _ := scheduledFor.Clock()
	start := time.Date(0, 1, 1, hour, min, 0, 0, time.UTC)
	end := start.Add(appointmentSlotDuration)
	if start.Before(opening) || end.After(closing) {
		return fmt.Errorf("garages: create appointment: %w (%s–%s)", ErrOutsideOperatingHours, capacity.OpeningTime, capacity.ClosingTime)
	}
	return nil
}

// Appointment returns one appointment visible to the scope: the GARAGE
// principal's own tenant or any tenant for platform operators. Rows the
// scope does not satisfy are indistinguishable from missing ones.
func (s *Service) Appointment(ctx context.Context, scope Scope, id uuid.UUID) (Appointment, error) {
	if s.appointments == nil {
		return Appointment{}, fmt.Errorf("garages: get appointment: store does not support appointments")
	}
	a, err := s.appointments.GetAppointment(ctx, id, scope)
	if err != nil {
		return Appointment{}, fmt.Errorf("garages: get appointment: %w", err)
	}
	return a, nil
}

// Appointments lists one tenant's appointments (the calendar window read).
// The tenant is resolved by the caller: the GARAGE principal's own claim
// or the addressed garage of an operator read. Operators read across
// tenants under ADR-0004; the collection endpoint has no unscoped mode.
func (s *Service) Appointments(ctx context.Context, tenantID uuid.UUID, filter AppointmentFilter, limit, offset int) ([]Appointment, error) {
	if s.appointments == nil {
		return nil, fmt.Errorf("garages: list appointments: store does not support appointments")
	}
	out, err := s.appointments.ListAppointments(ctx, tenantID, filter, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("garages: list appointments: %w", err)
	}
	return out, nil
}

// TransitionAppointment moves an appointment through one explicit edge of
// the state machine. RBAC: GARAGE staff transition their own tenant's
// appointments only (the scope decides — a foreign tenant reads as
// not-found); ADMIN/SUPER_ADMIN override across tenants and the override
// is stamped into the audit row. DISPATCHER, CUSTOMER and TECHNICIAN are
// refused before the store is touched.
func (s *Service) TransitionAppointment(ctx context.Context, scope Scope, actorID uuid.UUID, role string, id uuid.UUID, to string) (Appointment, error) {
	if s.appointments == nil {
		return Appointment{}, fmt.Errorf("garages: transition appointment: store does not support appointments")
	}
	_, override := appointmentOverrideRoles[role]
	if !override && role != RoleGarage {
		return Appointment{}, fmt.Errorf("garages: transition appointment: %w", ErrTransitionForbidden)
	}
	if !override && scope.TenantID == nil {
		return Appointment{}, fmt.Errorf("garages: transition appointment: a tenant scope is required")
	}
	current, err := s.appointments.GetAppointment(ctx, id, scope)
	if err != nil {
		return Appointment{}, fmt.Errorf("garages: transition appointment: %w", err)
	}
	if appointmentTerminalStatuses[current.Status] || !canTransition(current.Status, to) {
		return Appointment{}, fmt.Errorf("garages: transition appointment %s: %w (%s → %s)", id, ErrInvalidTransition, current.Status, to)
	}
	audit := AppointmentAuditRecord{
		Action:   appointmentActionFor(to),
		ObjectID: current.ID,
		ActorID:  actorID,
		TenantID: current.TenantID,
		From:     current.Status,
		To:       to,
		JobID:    current.JobID,
		Override: override,
	}
	a, err := s.appointments.TransitionAppointment(ctx, id, scope, appointmentTransitionSources[to], to, audit)
	if err != nil {
		return Appointment{}, fmt.Errorf("garages: transition appointment: %w", err)
	}
	if err := s.publish(ctx, EventAppointmentStatusChanged, a.ID, a.TenantID, &actorID, appointmentEventPayload{
		AppointmentID:  a.ID,
		TenantID:       a.TenantID,
		JobID:          a.JobID,
		ScheduledFor:   a.ScheduledFor,
		Bay:            a.Bay,
		Status:         a.Status,
		PreviousStatus: current.Status,
	}); err != nil {
		return Appointment{}, err
	}
	return a, nil
}

// appointmentOverrideRoles are the operator roles allowed to override the
// tenant boundary on transitions (audited as overrides). DISPATCHER reads
// across tenants but does not intervene in garage calendars.
var appointmentOverrideRoles = map[string]bool{
	RoleAdmin:      true,
	RoleSuperAdmin: true,
}

// appointmentActionFor maps a target status onto its audit action.
func appointmentActionFor(to string) string {
	switch to {
	case AppointmentConfirmed:
		return ActionAppointmentConfirmed
	case AppointmentCompleted:
		return ActionAppointmentCompleted
	case AppointmentCancelled:
		return ActionAppointmentCancelled
	case AppointmentNoShow:
		return ActionAppointmentNoShow
	}
	return "garages.appointment." + to
}

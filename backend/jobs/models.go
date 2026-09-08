package jobs

import (
	"time"

	"github.com/google/uuid"
)

// ServiceRequest statuses, mirroring the service_requests CHECK constraint.
const (
	RequestStatusReceived  = "received"
	RequestStatusConverted = "converted"
	RequestStatusCancelled = "cancelled"
)

// JobAssignment statuses, mirroring the job_assignments CHECK constraint.
const (
	AssignmentStatusAssigned   = "assigned"
	AssignmentStatusAccepted   = "accepted"
	AssignmentStatusDeclined   = "declined"
	AssignmentStatusReassigned = "reassigned"
)

// ServiceRequest is the customer-facing intake record. Exactly one job may
// be built from a request: CreateJobFromRequest flips the status from
// RequestStatusReceived to RequestStatusConverted.
type ServiceRequest struct {
	ID          uuid.UUID
	CustomerID  uuid.UUID
	VehicleID   *uuid.UUID
	Description string
	// LocationLat and LocationLng are the pickup coordinates. They are
	// optional but must be provided together.
	LocationLat *float64
	LocationLng *float64
	AddressText string
	// Status is one of the RequestStatus* constants ("received",
	// "converted", "cancelled").
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Job is the operational unit produced from a service request. Its status
// moves only through the legal edges of the state machine and every move
// is recorded in job_transitions.
type Job struct {
	ID uuid.UUID
	// ServiceRequestID links back to the originating request; nil for
	// jobs created without one (e.g. imported historical jobs).
	ServiceRequestID *uuid.UUID
	CustomerID       uuid.UUID
	VehicleID        *uuid.UUID
	// Status is one of the Status* constants on the state machine.
	Status         Status
	ProblemSummary string
	TechnicianID   *uuid.UUID
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// JobAssignment records one dispatch attempt of a job to a technician.
// Reassignment appends a new row; the latest row per job is the live one.
type JobAssignment struct {
	ID           uuid.UUID
	JobID        uuid.UUID
	TechnicianID uuid.UUID
	// AssignedBy is the dispatcher (or system actor) that issued the
	// assignment; nil when assigned by automation.
	AssignedBy *uuid.UUID
	AcceptedAt *time.Time
	// Status is one of the AssignmentStatus* constants ("assigned",
	// "accepted", "declined", "reassigned").
	Status    string
	CreatedAt time.Time
}

// JobTransition is one row of the append-only job_transitions audit log.
// Rows are never updated or deleted.
type JobTransition struct {
	ID         int64
	JobID      uuid.UUID
	FromStatus Status
	ToStatus   Status
	ActorID    *uuid.UUID
	Reason     string
	CreatedAt  time.Time
}

package jobs

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// EventDomain is the JetStream domain prefix used when publishing: events
// go out on motivra.jobs.<event_type> (ADR-0002).
const EventDomain = "jobs"

// Published event types, following "<aggregate>.<event>.v<n>".
const (
	EventRequestReceived  = "request.received.v1"
	EventJobCreated       = "job.created.v1"
	EventJobStatusChanged = "job.status.changed.v1"
	EventJobAssigned      = "job.assigned.v1"
)

// Location bounds applied by CreateRequest; both coordinates must be given
// together and within WGS-84 ranges.
const (
	MinLatitude  = -90.0
	MaxLatitude  = 90.0
	MinLongitude = -180.0
	MaxLongitude = 180.0
)

// Pagination discipline applied by JobsByStatus (mirrors the store).
const (
	DefaultJobsLimit = 50
	MaxJobsLimit     = 200
)

// ErrAlreadyConverted is returned when a second job is requested for a
// service request that already produced one.
var ErrAlreadyConverted = errors.New("jobs: service request already converted to a job")

// RequestReceivedPayload is the payload of request.received.v1.
type RequestReceivedPayload struct {
	RequestID   uuid.UUID  `json:"request_id"`
	CustomerID  uuid.UUID  `json:"customer_id"`
	VehicleID   *uuid.UUID `json:"vehicle_id,omitempty"`
	AddressText string     `json:"address_text"`
}

// JobCreatedPayload is the payload of job.created.v1.
type JobCreatedPayload struct {
	JobID            uuid.UUID  `json:"job_id"`
	ServiceRequestID *uuid.UUID `json:"service_request_id,omitempty"`
	CustomerID       uuid.UUID  `json:"customer_id"`
	VehicleID        *uuid.UUID `json:"vehicle_id,omitempty"`
	ProblemSummary   string     `json:"problem_summary"`
}

// JobStatusChangedPayload is the payload of job.status.changed.v1.
type JobStatusChangedPayload struct {
	JobID   uuid.UUID  `json:"job_id"`
	From    Status     `json:"from_status"`
	To      Status     `json:"to_status"`
	Reason  string     `json:"reason"`
	ActorID *uuid.UUID `json:"actor_id,omitempty"`
}

// JobAssignedPayload is the payload of job.assigned.v1.
type JobAssignedPayload struct {
	JobID        uuid.UUID  `json:"job_id"`
	TechnicianID uuid.UUID  `json:"technician_id"`
	AssignedBy   *uuid.UUID `json:"assigned_by,omitempty"`
}

// Service is the jobs domain service. It owns the state-machine discipline:
// every status move goes through CanTransition before it touches the store,
// and every successful mutation publishes a domain event. publisher may be
// nil (tests, offline tooling); events are simply skipped then.
type Service struct {
	store     Store
	publisher platform.Publisher
}

// NewService returns a Service over store. publisher may be nil.
func NewService(store Store, publisher platform.Publisher) *Service {
	return &Service{store: store, publisher: publisher}
}

// CreateRequest validates and persists a new service request and publishes
// request.received.v1. The request starts in status 'received'.
func (s *Service) CreateRequest(ctx context.Context, r *ServiceRequest) error {
	if r == nil {
		return fmt.Errorf("jobs: create request: request is required")
	}
	if r.CustomerID == uuid.Nil {
		return fmt.Errorf("jobs: create request: customer_id is required")
	}
	if strings.TrimSpace(r.Description) == "" {
		return fmt.Errorf("jobs: create request: description is required")
	}
	if !LocationValid(r.LocationLat, r.LocationLng) {
		return fmt.Errorf("jobs: create request: location_lat and location_lng must be provided together and within WGS-84 bounds")
	}
	if r.Status == "" {
		r.Status = RequestStatusReceived
	}

	if err := s.store.CreateRequest(ctx, r); err != nil {
		return fmt.Errorf("jobs: create request: %w", err)
	}

	return s.publish(ctx, EventRequestReceived, r.ID, RequestReceivedPayload{
		RequestID:   r.ID,
		CustomerID:  r.CustomerID,
		VehicleID:   r.VehicleID,
		AddressText: r.AddressText,
	})
}

// CreateJobFromRequest converts a service request in status 'received' into
// a job in status CREATED, marks the request 'converted', and publishes
// job.created.v1. Calling it twice for the same request fails with an error
// wrapping ErrAlreadyConverted.
func (s *Service) CreateJobFromRequest(ctx context.Context, requestID uuid.UUID) (Job, error) {
	request, err := s.store.GetRequest(ctx, requestID)
	if err != nil {
		return Job{}, fmt.Errorf("jobs: create job from request: %w", err)
	}
	if request.Status != RequestStatusReceived {
		return Job{}, fmt.Errorf("jobs: create job from request %s: request status is %q: %w",
			requestID, request.Status, ErrAlreadyConverted)
	}

	job := Job{
		ServiceRequestID: &request.ID,
		CustomerID:       request.CustomerID,
		VehicleID:        request.VehicleID,
		Status:           StatusCreated,
		ProblemSummary:   request.Description,
	}
	// Store.CreateJob atomically claims the request (status 'received' ->
	// 'converted') in the same transaction as the job insert, so two
	// concurrent conversions cannot both win.
	if err := s.store.CreateJob(ctx, &job); err != nil {
		return Job{}, fmt.Errorf("jobs: create job from request: %w", err)
	}

	if err := s.publish(ctx, EventJobCreated, job.ID, JobCreatedPayload{
		JobID:            job.ID,
		ServiceRequestID: job.ServiceRequestID,
		CustomerID:       job.CustomerID,
		VehicleID:        job.VehicleID,
		ProblemSummary:   job.ProblemSummary,
	}); err != nil {
		return Job{}, err
	}
	return job, nil
}

// Transition moves the job to the requested status through the state
// machine, persists the move with its audit row, and publishes
// job.status.changed.v1 with from/to/reason. Illegal moves are rejected
// with an error wrapping ErrConflict and publish nothing.
func (s *Service) Transition(ctx context.Context, jobID uuid.UUID, to Status, actorID *uuid.UUID, reason string) error {
	job, err := s.store.GetJob(ctx, jobID)
	if err != nil {
		return fmt.Errorf("jobs: transition job: %w", err)
	}
	if err := CanTransition(job.Status, to); err != nil {
		return err
	}
	if err := s.store.UpdateJobStatus(ctx, jobID, to, actorID, reason); err != nil {
		return fmt.Errorf("jobs: transition job: %w", err)
	}

	return s.publish(ctx, EventJobStatusChanged, jobID, JobStatusChangedPayload{
		JobID:   jobID,
		From:    job.Status,
		To:      to,
		Reason:  reason,
		ActorID: actorID,
	})
}

// AssignTechnician dispatches the job to technicianID and publishes
// job.assigned.v1. Only a job in DISPATCHING may take a technician; a job
// already ASSIGNED is reassigned by first stepping it back to DISPATCHING
// through the legal reassignment edge (ASSIGNED -> DISPATCHING), then
// assigning. Every other current status is rejected with the state
// machine's conflict error.
func (s *Service) AssignTechnician(ctx context.Context, jobID uuid.UUID, technicianID uuid.UUID, assignedBy *uuid.UUID) error {
	job, err := s.store.GetJob(ctx, jobID)
	if err != nil {
		return fmt.Errorf("jobs: assign technician: %w", err)
	}
	switch job.Status {
	case StatusDispatching:
		// Direct assignment.
	case StatusAssigned:
		if job.TechnicianID != nil && *job.TechnicianID == technicianID {
			return fmt.Errorf("jobs: assign technician: job %s is already assigned to technician %s: %w",
				jobID, technicianID, ErrConflict)
		}
		// Reassignment discipline: back to DISPATCHING first. The edge is
		// guaranteed legal (ASSIGNED -> DISPATCHING) and persists its own
		// audit row and status.changed event.
		if err := s.Transition(ctx, jobID, StatusDispatching, assignedBy, "reassignment"); err != nil {
			return err
		}
	default:
		// Deliberately invalid; CanTransition yields the canonical conflict
		// message listing the legal moves from the current status.
		return CanTransition(job.Status, StatusAssigned)
	}

	if err := s.store.AssignTechnician(ctx, jobID, technicianID, assignedBy); err != nil {
		return fmt.Errorf("jobs: assign technician: %w", err)
	}

	return s.publish(ctx, EventJobAssigned, jobID, JobAssignedPayload{
		JobID:        jobID,
		TechnicianID: technicianID,
		AssignedBy:   assignedBy,
	})
}

// Accept records technicianID accepting the job's assignment: the job moves
// ASSIGNED -> ACCEPTED and job.status.changed.v1 is published.
func (s *Service) Accept(ctx context.Context, jobID uuid.UUID, technicianID uuid.UUID) error {
	job, err := s.store.GetJob(ctx, jobID)
	if err != nil {
		return fmt.Errorf("jobs: accept assignment: %w", err)
	}
	if err := CanTransition(job.Status, StatusAccepted); err != nil {
		return err
	}
	if job.TechnicianID != nil && *job.TechnicianID != technicianID {
		return fmt.Errorf("jobs: accept assignment: job %s is assigned to technician %s, not %s: %w",
			jobID, *job.TechnicianID, technicianID, ErrConflict)
	}
	if err := s.store.AcceptAssignment(ctx, jobID, technicianID); err != nil {
		return fmt.Errorf("jobs: accept assignment: %w", err)
	}

	return s.publish(ctx, EventJobStatusChanged, jobID, JobStatusChangedPayload{
		JobID:   jobID,
		From:    job.Status,
		To:      StatusAccepted,
		Reason:  "technician accepted assignment",
		ActorID: &technicianID,
	})
}

// GetJob returns the job with the given id, or an error wrapping
// ErrJobNotFound when it does not exist. Read path for the HTTP layer.
func (s *Service) GetJob(ctx context.Context, id uuid.UUID) (Job, error) {
	return s.store.GetJob(ctx, id)
}

// Transitions returns the job's append-only transition audit trail in
// chronological order (oldest first). Read path for the HTTP layer.
func (s *Service) Transitions(ctx context.Context, jobID uuid.UUID) ([]JobTransition, error) {
	return s.store.ListTransitions(ctx, jobID)
}

// JobsByStatus lists jobs in the given status, newest first, with the
// service's pagination discipline (defaults and caps).
func (s *Service) JobsByStatus(ctx context.Context, status Status, limit, offset int) ([]Job, error) {
	if !isKnownStatus(status) {
		return nil, fmt.Errorf("jobs: list jobs by status: unknown status %q", status)
	}
	if limit <= 0 {
		limit = DefaultJobsLimit
	}
	if limit > MaxJobsLimit {
		limit = MaxJobsLimit
	}
	if offset < 0 {
		offset = 0
	}
	jobs, err := s.store.ListJobsByStatus(ctx, status, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("jobs: list jobs by status: %w", err)
	}
	return jobs, nil
}

// publish sends one domain event on the jobs domain. It is a no-op when no
// publisher is configured. Publishing failures surface to the caller:
// events are part of the domain contract, not best-effort logging.
func (s *Service) publish(ctx context.Context, eventType string, aggregateID uuid.UUID, payload any) error {
	if s.publisher == nil {
		return nil
	}
	e, err := platform.NewEvent(eventType, aggregateID.String(), "", "", "", payload)
	if err != nil {
		return fmt.Errorf("jobs: build %s event: %w", eventType, err)
	}
	if err := s.publisher.Publish(ctx, EventDomain, e); err != nil {
		return fmt.Errorf("jobs: publish %s: %w", eventType, err)
	}
	return nil
}

// LocationValid reports whether the coordinate pair is present and within
// WGS-84 bounds. Exported for the HTTP layer.
func LocationValid(lat, lng *float64) bool {
	if (lat == nil) != (lng == nil) {
		return false
	}
	if lat == nil {
		return true
	}
	return !math.IsNaN(*lat) && !math.IsNaN(*lng) &&
		*lat >= MinLatitude && *lat <= MaxLatitude &&
		*lng >= MinLongitude && *lng <= MaxLongitude
}

// isKnownStatus reports whether status is one of the 18 enumerated
// statuses (including terminal ones and ESCALATED).
func isKnownStatus(status Status) bool {
	for _, s := range AllStatuses() {
		if s == status {
			return true
		}
	}
	return false
}

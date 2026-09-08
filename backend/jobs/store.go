package jobs

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrRequestNotFound is returned by request reads when the service request
// does not exist. The service layer translates it into a platform 404.
var ErrRequestNotFound = errors.New("jobs: service request not found")

// ErrJobNotFound is returned by job reads when the job does not exist.
var ErrJobNotFound = errors.New("jobs: job not found")

// Pagination defaults applied by ListJobsByStatus when limit is not
// provided or exceeds the cap.
const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// Store is the persistence contract for the jobs domain. Implementations
// must keep the job_transitions table append-only (insert-only) and must
// perform multi-write operations in single transactions.
type Store interface {
	// CreateRequest inserts r. It assigns r.ID when unset and populates
	// r.CreatedAt and r.UpdatedAt from the database.
	CreateRequest(ctx context.Context, r *ServiceRequest) error

	// GetRequest returns the service request with the given id, or an
	// error wrapping ErrRequestNotFound when it does not exist.
	GetRequest(ctx context.Context, id uuid.UUID) (ServiceRequest, error)

	// CreateJob inserts j. When j.ServiceRequestID is set, the originating
	// service request is atomically claimed in the same transaction: it
	// must be in status 'received' (it is moved to 'converted') or the
	// call fails with an error wrapping ErrAlreadyConverted. When
	// j.ServiceRequestID is nil, j is inserted as a standalone job.
	CreateJob(ctx context.Context, j *Job) error

	// GetJob returns the job with the given id, or an error wrapping
	// ErrJobNotFound when it does not exist.
	GetJob(ctx context.Context, id uuid.UUID) (Job, error)

	// UpdateJobStatus moves the job to newStatus and appends one
	// job_transitions row recording the previous status in a single
	// transaction. It fails with an error wrapping ErrJobNotFound when
	// the job does not exist. actorID may be nil (system actor) and
	// reason may be empty; both are stored verbatim in the audit row.
	UpdateJobStatus(ctx context.Context, jobID uuid.UUID, newStatus Status, actorID *uuid.UUID, reason string) error

	// AssignTechnician issues a dispatch assignment: it appends a
	// job_assignments row, marks any earlier open assignment of the job
	// as 'reassigned', moves the job to ASSIGNED with the technician set,
	// and appends the job_transitions row, all in one transaction. It
	// fails with an error wrapping ErrJobNotFound when the job does not
	// exist. assignedBy may be nil when the assignment is automated.
	AssignTechnician(ctx context.Context, jobID uuid.UUID, technicianID uuid.UUID, assignedBy *uuid.UUID) error

	// AcceptAssignment marks the job's open assignment as 'accepted'
	// (stamping accepted_at), moves the job to ACCEPTED and appends the
	// job_transitions row, all in one transaction. It fails with an error
	// wrapping ErrJobNotFound when the job does not exist.
	AcceptAssignment(ctx context.Context, jobID uuid.UUID, technicianID uuid.UUID) error

	// ListJobsByStatus returns jobs in the given status, newest first.
	// limit <= 0 selects defaultListLimit and limit is capped at
	// maxListLimit; offset < 0 is treated as 0.
	ListJobsByStatus(ctx context.Context, status Status, limit, offset int) ([]Job, error)

	// ListTransitions returns the job's transition audit trail in
	// chronological order (oldest first).
	ListTransitions(ctx context.Context, jobID uuid.UUID) ([]JobTransition, error)
}

// PostgresStore implements Store on PostgreSQL through pgx.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// Compile-time assertion that PostgresStore satisfies Store.
var _ Store = (*PostgresStore)(nil)

// NewPostgresStore returns a Store backed by pool. The pool must already
// have the jobs migrations applied (platform.MigrateUp with
// jobsnextmigrations.FS).
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

const requestColumns = `id, customer_id, vehicle_id, description, location_lat, location_lng, address_text, status, created_at, updated_at`

const jobColumns = `id, service_request_id, customer_id, vehicle_id, status, problem_summary, technician_id, created_at, updated_at`

// CreateRequest implements Store.
func (s *PostgresStore) CreateRequest(ctx context.Context, r *ServiceRequest) error {
	if r == nil {
		return fmt.Errorf("jobs: create request: request is required")
	}
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	err := s.pool.QueryRow(ctx, `
                INSERT INTO service_requests
                        (id, customer_id, vehicle_id, description, location_lat, location_lng, address_text, status)
                VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
                RETURNING created_at, updated_at`,
		r.ID, r.CustomerID, r.VehicleID, r.Description, r.LocationLat, r.LocationLng, r.AddressText, r.Status,
	).Scan(&r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return fmt.Errorf("jobs: insert service request: %w", err)
	}
	return nil
}

// GetRequest implements Store.
func (s *PostgresStore) GetRequest(ctx context.Context, id uuid.UUID) (ServiceRequest, error) {
	row := s.pool.QueryRow(ctx, `
                SELECT `+requestColumns+`
                FROM service_requests
                WHERE id = $1`, id)
	r, err := scanRequest(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ServiceRequest{}, fmt.Errorf("jobs: get service request %s: %w", id, ErrRequestNotFound)
		}
		return ServiceRequest{}, fmt.Errorf("jobs: get service request: %w", err)
	}
	return r, nil
}

// CreateJob implements Store. See the interface comment for the
// request-claiming semantics when ServiceRequestID is set.
func (s *PostgresStore) CreateJob(ctx context.Context, j *Job) error {
	if j == nil {
		return fmt.Errorf("jobs: create job: job is required")
	}
	if j.ID == uuid.Nil {
		j.ID = uuid.New()
	}
	if j.Status == "" {
		j.Status = StatusCreated
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("jobs: begin create job: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if j.ServiceRequestID != nil {
		tag, err := tx.Exec(ctx, `
                        UPDATE service_requests
                        SET status = $2, updated_at = now()
                        WHERE id = $1 AND status = $3`,
			*j.ServiceRequestID, RequestStatusConverted, RequestStatusReceived)
		if err != nil {
			return fmt.Errorf("jobs: claim service request: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("jobs: create job: service request %s is not in status %q: %w",
				*j.ServiceRequestID, RequestStatusReceived, ErrAlreadyConverted)
		}
	}

	err = tx.QueryRow(ctx, `
                INSERT INTO jobs
                        (id, service_request_id, customer_id, vehicle_id, status, problem_summary, technician_id)
                VALUES ($1, $2, $3, $4, $5, $6, $7)
                RETURNING created_at, updated_at`,
		j.ID, j.ServiceRequestID, j.CustomerID, j.VehicleID, j.Status, j.ProblemSummary, j.TechnicianID,
	).Scan(&j.CreatedAt, &j.UpdatedAt)
	if err != nil {
		return fmt.Errorf("jobs: insert job: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("jobs: commit create job: %w", err)
	}
	return nil
}

// GetJob implements Store.
func (s *PostgresStore) GetJob(ctx context.Context, id uuid.UUID) (Job, error) {
	row := s.pool.QueryRow(ctx, `
                SELECT `+jobColumns+`
                FROM jobs
                WHERE id = $1`, id)
	j, err := scanJob(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Job{}, fmt.Errorf("jobs: get job %s: %w", id, ErrJobNotFound)
		}
		return Job{}, fmt.Errorf("jobs: get job: %w", err)
	}
	return j, nil
}

// UpdateJobStatus implements Store.
func (s *PostgresStore) UpdateJobStatus(ctx context.Context, jobID uuid.UUID, newStatus Status, actorID *uuid.UUID, reason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("jobs: begin update job status: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var current Status
	err = tx.QueryRow(ctx, `
                SELECT status
                FROM jobs
                WHERE id = $1
                FOR UPDATE`, jobID).Scan(&current)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("jobs: update job status %s: %w", jobID, ErrJobNotFound)
		}
		return fmt.Errorf("jobs: lock job %s: %w", jobID, err)
	}

	if _, err := tx.Exec(ctx, `
                UPDATE jobs
                SET status = $2, updated_at = now()
                WHERE id = $1`, jobID, newStatus); err != nil {
		return fmt.Errorf("jobs: update job status: %w", err)
	}

	if _, err := tx.Exec(ctx, `
                INSERT INTO job_transitions (job_id, from_status, to_status, actor_id, reason)
                VALUES ($1, $2, $3, $4, $5)`,
		jobID, current, newStatus, actorID, reason); err != nil {
		return fmt.Errorf("jobs: insert job transition: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("jobs: commit update job status: %w", err)
	}
	return nil
}

// AssignTechnician implements Store.
func (s *PostgresStore) AssignTechnician(ctx context.Context, jobID uuid.UUID, technicianID uuid.UUID, assignedBy *uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("jobs: begin assign technician: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var current Status
	err = tx.QueryRow(ctx, `
                SELECT status
                FROM jobs
                WHERE id = $1
                FOR UPDATE`, jobID).Scan(&current)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("jobs: assign technician to job %s: %w", jobID, ErrJobNotFound)
		}
		return fmt.Errorf("jobs: lock job %s: %w", jobID, err)
	}

	// Retire any open assignment: a reassignment appends a new row and the
	// latest row per job is the live one.
	if _, err := tx.Exec(ctx, `
                UPDATE job_assignments
                SET status = $2
                WHERE job_id = $1 AND status = $3`,
		jobID, AssignmentStatusReassigned, AssignmentStatusAssigned); err != nil {
		return fmt.Errorf("jobs: retire previous assignments: %w", err)
	}

	if _, err := tx.Exec(ctx, `
                INSERT INTO job_assignments (job_id, technician_id, assigned_by, status)
                VALUES ($1, $2, $3, $4)`,
		jobID, technicianID, assignedBy, AssignmentStatusAssigned); err != nil {
		return fmt.Errorf("jobs: insert job assignment: %w", err)
	}

	if _, err := tx.Exec(ctx, `
                UPDATE jobs
                SET status = $2, technician_id = $3, updated_at = now()
                WHERE id = $1`, jobID, StatusAssigned, technicianID); err != nil {
		return fmt.Errorf("jobs: update job for assignment: %w", err)
	}

	if _, err := tx.Exec(ctx, `
                INSERT INTO job_transitions (job_id, from_status, to_status, actor_id, reason)
                VALUES ($1, $2, $3, $4, $5)`,
		jobID, current, StatusAssigned, assignedBy, "technician assigned"); err != nil {
		return fmt.Errorf("jobs: insert assignment transition: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("jobs: commit assign technician: %w", err)
	}
	return nil
}

// AcceptAssignment implements Store.
func (s *PostgresStore) AcceptAssignment(ctx context.Context, jobID uuid.UUID, technicianID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("jobs: begin accept assignment: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var current Status
	err = tx.QueryRow(ctx, `
                SELECT status
                FROM jobs
                WHERE id = $1
                FOR UPDATE`, jobID).Scan(&current)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("jobs: accept assignment for job %s: %w", jobID, ErrJobNotFound)
		}
		return fmt.Errorf("jobs: lock job %s: %w", jobID, err)
	}

	tag, err := tx.Exec(ctx, `
                UPDATE job_assignments
                SET status = $3, accepted_at = now()
                WHERE job_id = $1 AND technician_id = $2 AND status = $4`,
		jobID, technicianID, AssignmentStatusAccepted, AssignmentStatusAssigned)
	if err != nil {
		return fmt.Errorf("jobs: accept job assignment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("jobs: accept assignment: no open assignment of job %s to technician %s", jobID, technicianID)
	}

	if _, err := tx.Exec(ctx, `
                UPDATE jobs
                SET status = $2, updated_at = now()
                WHERE id = $1`, jobID, StatusAccepted); err != nil {
		return fmt.Errorf("jobs: update job for acceptance: %w", err)
	}

	if _, err := tx.Exec(ctx, `
                INSERT INTO job_transitions (job_id, from_status, to_status, actor_id, reason)
                VALUES ($1, $2, $3, $4, $5)`,
		jobID, current, StatusAccepted, &technicianID, "technician accepted assignment"); err != nil {
		return fmt.Errorf("jobs: insert acceptance transition: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("jobs: commit accept assignment: %w", err)
	}
	return nil
}

// ListJobsByStatus implements Store.
func (s *PostgresStore) ListJobsByStatus(ctx context.Context, status Status, limit, offset int) ([]Job, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.pool.Query(ctx, `
                SELECT `+jobColumns+`
                FROM jobs
                WHERE status = $1
                ORDER BY created_at DESC
                LIMIT $2 OFFSET $3`, status, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("jobs: list jobs by status: %w", err)
	}
	defer rows.Close()

	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("jobs: scan job: %w", err)
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobs: list jobs by status: %w", err)
	}
	return out, nil
}

// ListTransitions implements Store.
func (s *PostgresStore) ListTransitions(ctx context.Context, jobID uuid.UUID) ([]JobTransition, error) {
	rows, err := s.pool.Query(ctx, `
                SELECT id, job_id, from_status, to_status, actor_id, reason, created_at
                FROM job_transitions
                WHERE job_id = $1
                ORDER BY created_at ASC, id ASC`, jobID)
	if err != nil {
		return nil, fmt.Errorf("jobs: list transitions: %w", err)
	}
	defer rows.Close()

	var out []JobTransition
	for rows.Next() {
		var t JobTransition
		if err := rows.Scan(&t.ID, &t.JobID, &t.FromStatus, &t.ToStatus, &t.ActorID, &t.Reason, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("jobs: scan transition: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobs: list transitions: %w", err)
	}
	return out, nil
}

func scanRequest(row pgx.Row) (ServiceRequest, error) {
	var r ServiceRequest
	err := row.Scan(&r.ID, &r.CustomerID, &r.VehicleID, &r.Description, &r.LocationLat, &r.LocationLng,
		&r.AddressText, &r.Status, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return ServiceRequest{}, err
	}
	return r, nil
}

func scanJob(row pgx.Row) (Job, error) {
	var j Job
	err := row.Scan(&j.ID, &j.ServiceRequestID, &j.CustomerID, &j.VehicleID, &j.Status,
		&j.ProblemSummary, &j.TechnicianID, &j.CreatedAt, &j.UpdatedAt)
	if err != nil {
		return Job{}, err
	}
	return j, nil
}

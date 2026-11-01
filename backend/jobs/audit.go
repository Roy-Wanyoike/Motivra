package jobs

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Privileged-read auditing for the jobs domain (ADR-0004: privileged reads
// of personal data are audited; the operator job board is a cross-tenant
// read of customer-linked rows). Every page a platform operator pulls from
// the jobs list appends one record — the acting principal, their tenant
// scope, and the exact page shape — so the admin audit API
// (GET /v1/admin/audit, identity domain) can answer "who browsed which
// job rows, when, through which filter" after the fact.
//
// audit_log is the platform-level audit trail whose schema lives in the
// identity migration chain; writers outside identity hold an INSERT-only
// seam and never read other domains' tables (the dispatch decision
// recorder precedent). GARAGE and personal listings are tenant-bounded
// reads, not privileged ones, and are not audited.
//
// ActionJobsListed is the audit action of one operator listing page.
const ActionJobsListed = "jobs.job.listed"

// Audit object descriptors for collection reads: the object is the queried
// collection, not a single job row (the identity.users.listed convention);
// the page shape rides the metadata.
const (
	AuditObjectTypeJob      = "job"
	AuditObjectIDCollection = "collection"
)

// ListAuditRecord is one audited jobs-list page read.
type ListAuditRecord struct {
	// ActorID is the authenticated operator (claims.UserID).
	ActorID uuid.UUID
	// TenantID is the actor's tenant claim; nil for the tenant-less
	// operators who read the platform-wide board.
	TenantID *uuid.UUID
	// Filter is the narrow the page was requested with (both fields nil =
	// the unfiltered cross-status board).
	Filter JobFilter
	// Limit, Offset are the pagination arguments as applied.
	Limit, Offset int
	// Returned is the number of rows the page contained.
	Returned int
}

// AuditRecorder persists one jobs-list audit record. Implementations must
// be append-only: no update or delete path exists.
type AuditRecorder interface {
	RecordJobsListed(ctx context.Context, record ListAuditRecord) error
}

// PostgresAuditRecorder implements AuditRecorder on the shared audit_log
// table through a pgx pool.
type PostgresAuditRecorder struct {
	pool *pgxpool.Pool
}

// Compile-time assertion that PostgresAuditRecorder satisfies AuditRecorder.
var _ AuditRecorder = (*PostgresAuditRecorder)(nil)

// NewPostgresAuditRecorder returns an AuditRecorder backed by pool. The
// pool must be able to see the audit_log table (the identity migration
// chain's owner; every Motivra deployment shares one database — the
// cmd/dispatch decision-recorder posture). Wiring it is the operator-board
// audit obligation; without it, operator listings must not run silently
// unrecorded.
func NewPostgresAuditRecorder(pool *pgxpool.Pool) *PostgresAuditRecorder {
	return &PostgresAuditRecorder{pool: pool}
}

// RecordJobsListed appends the listing record. A failure is never
// swallowed: the service fails the request so the audit signal cannot be
// lost silently (ADR-0004).
func (r *PostgresAuditRecorder) RecordJobsListed(ctx context.Context, record ListAuditRecord) error {
	metadata := map[string]any{
		"limit":    record.Limit,
		"offset":   record.Offset,
		"returned": record.Returned,
	}
	if record.Filter.Status != nil {
		metadata["status"] = string(*record.Filter.Status)
	}
	if record.Filter.TechnicianID != nil {
		metadata["technician_id"] = record.Filter.TechnicianID.String()
	}
	if _, err := r.pool.Exec(ctx,
		`INSERT INTO audit_log (actor_id, action, object_type, object_id, tenant_id, metadata)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		record.ActorID, ActionJobsListed, AuditObjectTypeJob, AuditObjectIDCollection, record.TenantID, metadata,
	); err != nil {
		return fmt.Errorf("insert jobs list audit record: %w", err)
	}
	return nil
}

package garages

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Compile-time assertion that PostgresStore also satisfies the
// appointments half of the domain (issue #169). One store, two aggregate
// seams: capacity (#170) and appointments (#169) share the pool and the
// garages migration chain.
var _ AppointmentStore = (*PostgresStore)(nil)

const appointmentColumns = `id, tenant_id, job_id, scheduled_for, bay, notes, status, created_by, created_at, updated_at`

// pgExclusionViolation is the SQLSTATE of a failed EXCLUDE constraint —
// the database backstop of the double-booking check.
const pgExclusionViolation = "23P01"

// CreateAppointment implements AppointmentStore. One transaction performs
// the serialized slot check and the insert: a per-tenant advisory lock
// serializes concurrent bookings for the same garage, the overlapping
// active rows are locked FOR UPDATE and inspected (same effective bay →
// conflict; parallel technician capacity exceeded → conflict), then the
// row and its creation audit record are written together. The EXCLUDE
// constraint (migration 000002) is the concurrency backstop for anything
// that slips past the check. Any failure — the audit INSERT included —
// rolls the whole write back: the audit signal can never be lost silently.
func (s *PostgresStore) CreateAppointment(ctx context.Context, a *Appointment, slot CreateSlot, audit AppointmentAuditRecord) error {
	if a == nil {
		return fmt.Errorf("garages: create appointment: appointment is required")
	}
	if a.TenantID == uuid.Nil {
		return fmt.Errorf("garages: create appointment: tenant_id is required")
	}
	if a.JobID == uuid.Nil {
		return fmt.Errorf("garages: create appointment: job_id is required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("garages: create appointment: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Serialize per-tenant booking decisions so the technician-slot count
	// below is read against a stable set (the bay dimension is additionally
	// guarded by the EXCLUDE constraint regardless).
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, a.TenantID.String(),
	); err != nil {
		return fmt.Errorf("garages: create appointment: lock: %w", err)
	}

	rows, err := tx.Query(ctx, `
                SELECT bay
                FROM garage_appointments
                WHERE tenant_id = $1
                  AND status IN ('requested', 'confirmed')
                  AND scheduled_for < $3
                  AND scheduled_for + interval '1 hour' > $2
                FOR UPDATE`,
		a.TenantID, slot.Start, slot.End,
	)
	if err != nil {
		return fmt.Errorf("garages: create appointment: slot scan: %w", err)
	}
	var overlapping []string
	for rows.Next() {
		var bay *string
		if err := rows.Scan(&bay); err != nil {
			rows.Close()
			return fmt.Errorf("garages: create appointment: slot scan: %w", err)
		}
		overlapping = append(overlapping, effectiveBay(bay))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("garages: create appointment: slot scan: %w", err)
	}
	for _, bay := range overlapping {
		if bay == effectiveBay(slot.Bay) {
			return fmt.Errorf("garages: create appointment: %w", ErrSlotConflict)
		}
	}
	if slot.TechnicianSlots > 0 && len(overlapping) >= slot.TechnicianSlots {
		return fmt.Errorf("garages: create appointment: %w (technician capacity)", ErrSlotConflict)
	}

	if err := tx.QueryRow(ctx, `
                INSERT INTO garage_appointments (tenant_id, job_id, scheduled_for, bay, notes, status, created_by)
                VALUES ($1, $2, $3, $4, $5, $6, $7)
                RETURNING id, created_at, updated_at`,
		a.TenantID, a.JobID, a.ScheduledFor, a.Bay, a.Notes, a.Status, a.CreatedBy,
	).Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgExclusionViolation {
			return fmt.Errorf("garages: create appointment: %w", ErrSlotConflict)
		}
		return fmt.Errorf("garages: create appointment: insert: %w", err)
	}
	audit.ObjectID = a.ID
	if err := insertAppointmentAudit(ctx, tx, audit); err != nil {
		return fmt.Errorf("garages: create appointment: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("garages: create appointment: commit: %w", err)
	}
	return nil
}

// GetAppointment implements AppointmentStore. The scope predicate renders
// the ADR-0004 boundary inside SQL: an operator principal reads across
// tenants, everyone else reads their own tenant exactly, and a zero scope
// matches nothing.
func (s *PostgresStore) GetAppointment(ctx context.Context, id uuid.UUID, scope Scope) (Appointment, error) {
	row := s.pool.QueryRow(ctx, `
                SELECT `+appointmentColumns+`
                FROM garage_appointments
                WHERE id = $1 AND ($2::boolean OR tenant_id = $3::uuid)`,
		id, scope.Operator, scopeTenantOrNil(scope),
	)
	a, err := scanAppointment(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Appointment{}, fmt.Errorf("garages: get appointment %s: %w", id, ErrAppointmentNotFound)
		}
		return Appointment{}, fmt.Errorf("garages: get appointment: %w", err)
	}
	return a, nil
}

// ListAppointments implements AppointmentStore. The tenant is exact: the
// service resolves it from the authenticated scope (or the operator's
// addressed garage) and there is no unscoped mode. Ordered by
// scheduled_for, newest first.
func (s *PostgresStore) ListAppointments(ctx context.Context, tenantID uuid.UUID, filter AppointmentFilter, limit, offset int) ([]Appointment, error) {
	sql := `SELECT ` + appointmentColumns + `
                FROM garage_appointments
                WHERE tenant_id = $1`
	args := []any{tenantID}
	if filter.Status != nil {
		args = append(args, *filter.Status)
		sql += fmt.Sprintf(` AND status = $%d`, len(args))
	}
	if filter.From != nil {
		args = append(args, *filter.From)
		sql += fmt.Sprintf(` AND scheduled_for >= $%d`, len(args))
	}
	if filter.To != nil {
		args = append(args, *filter.To)
		sql += fmt.Sprintf(` AND scheduled_for < $%d`, len(args))
	}
	args = append(args, limit, offset)
	sql += fmt.Sprintf(` ORDER BY scheduled_for DESC, created_at DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args))

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("garages: list appointments: %w", err)
	}
	defer rows.Close()
	var out []Appointment
	for rows.Next() {
		a, err := scanAppointment(rows)
		if err != nil {
			return nil, fmt.Errorf("garages: list appointments: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("garages: list appointments: %w", err)
	}
	return out, nil
}

// TransitionAppointment implements AppointmentStore. One transaction:
// lock the scoped row FOR UPDATE, re-check the allowed source statuses
// (the service validated optimistically; the row lock makes the guard
// authoritative under concurrency), update the status and append the
// audit_log row. The audit INSERT failing rolls the transition back —
// same-transaction audit seam (dispatch DecisionRecorder precedent).
func (s *PostgresStore) TransitionAppointment(ctx context.Context, id uuid.UUID, scope Scope, allowedFrom []string, to string, audit AppointmentAuditRecord) (Appointment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Appointment{}, fmt.Errorf("garages: transition appointment: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row := tx.QueryRow(ctx, `
                SELECT `+appointmentColumns+`
                FROM garage_appointments
                WHERE id = $1 AND ($2::boolean OR tenant_id = $3::uuid)
                FOR UPDATE`,
		id, scope.Operator, scopeTenantOrNil(scope),
	)
	current, err := scanAppointment(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Appointment{}, fmt.Errorf("garages: transition appointment %s: %w", id, ErrAppointmentNotFound)
		}
		return Appointment{}, fmt.Errorf("garages: transition appointment: %w", err)
	}
	allowed := false
	for _, from := range allowedFrom {
		if current.Status == from {
			allowed = true
			break
		}
	}
	if !allowed {
		return Appointment{}, fmt.Errorf("garages: transition appointment %s: %w (%s → %s)", id, ErrInvalidTransition, current.Status, to)
	}
	audit.ObjectID = current.ID
	audit.TenantID = current.TenantID
	audit.From = current.Status
	audit.To = to
	audit.JobID = current.JobID
	var updatedAt time.Time
	if err := tx.QueryRow(ctx, `
                UPDATE garage_appointments
                SET status = $2, updated_at = now()
                WHERE id = $1
                RETURNING updated_at`,
		id, to,
	).Scan(&updatedAt); err != nil {
		return Appointment{}, fmt.Errorf("garages: transition appointment: update: %w", err)
	}
	if err := insertAppointmentAudit(ctx, tx, audit); err != nil {
		return Appointment{}, fmt.Errorf("garages: transition appointment: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Appointment{}, fmt.Errorf("garages: transition appointment: commit: %w", err)
	}
	current.Status = to
	current.UpdatedAt = updatedAt
	return current, nil
}

// insertAppointmentAudit appends one audit_log row on the passed
// transaction (the shared write-once trail owned by the identity chain;
// writers outside identity hold an INSERT-only seam and never read it).
func insertAppointmentAudit(ctx context.Context, tx pgx.Tx, audit AppointmentAuditRecord) error {
	metadata := map[string]any{
		"from":   audit.From,
		"to":     audit.To,
		"job_id": audit.JobID.String(),
	}
	if audit.Override {
		metadata["override"] = true
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO audit_log (actor_id, action, object_type, object_id, tenant_id, metadata)
                 VALUES ($1, $2, $3, $4, $5, $6)`,
		audit.ActorID, audit.Action, AppointmentObjectType, audit.ObjectID, audit.TenantID, metadata,
	); err != nil {
		return fmt.Errorf("insert appointment audit record: %w", err)
	}
	return nil
}

// scopeTenantOrNil renders the scope's tenant for the SQL predicates: nil
// for operators (their predicate is the operator bit) and the claimed
// tenant otherwise (a zero scope passes uuid.Nil, which matches nothing).
func scopeTenantOrNil(scope Scope) any {
	if scope.Operator || scope.TenantID == nil {
		return nil
	}
	return *scope.TenantID
}

func scanAppointment(row pgx.Row) (Appointment, error) {
	var (
		a          Appointment
		bay, notes *string
	)
	err := row.Scan(&a.ID, &a.TenantID, &a.JobID, &a.ScheduledFor, &bay, &notes, &a.Status, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return Appointment{}, err
	}
	a.Bay, a.Notes = bay, notes
	return a, nil
}

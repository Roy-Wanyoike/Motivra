package inspections

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrSessionNotFound is returned by session reads when the session does
// not exist or is outside the caller's scope. The service layer translates
// it into a platform 404.
var ErrSessionNotFound = errors.New("inspections: session not found")

// ErrReportNotFound is returned by report reads when the session has no
// issued report (yet) or is outside the caller's scope.
var ErrReportNotFound = errors.New("inspections: report not found")

// Pagination defaults applied by ListSessions when limit is not provided
// or exceeds the cap.
const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// SessionFilter narrows ListSessions. All fields are optional; nil matches
// everything. The scope always applies on top of the filter.
type SessionFilter struct {
	VehicleID *uuid.UUID
	JobID     *uuid.UUID
	Status    *Status
}

// Store is the persistence contract for the inspections domain.
// Implementations must keep inspection_findings, inspection_evidence,
// inspection_reports and inspection_session_transitions append-only (the
// migrations enforce this with triggers) and must perform multi-write
// operations in single transactions.
//
// Every read method takes a Scope derived from the authenticated principal
// (ADR-0004: the repository layer enforces scope; there is no unscoped
// variant). Rows the scope does not satisfy — including under the zero
// value — yield not-found or an empty page exactly like a missing row, so
// cross-principal probing cannot confirm existence.
type Store interface {
	// CreateSession inserts s. It assigns s.ID when unset and populates
	// s.CreatedAt and s.UpdatedAt from the database.
	CreateSession(ctx context.Context, s *Session) error

	// GetSession returns the session with the given id when it is visible
	// to scope, or an error wrapping ErrSessionNotFound otherwise —
	// including when it does not exist.
	GetSession(ctx context.Context, id uuid.UUID, scope Scope) (Session, error)

	// ListSessions returns up to limit sessions visible to scope and
	// matching filter, newest first (created_at DESC, id DESC).
	// limit <= 0 selects defaultListLimit and limit is capped at
	// maxListLimit; offset < 0 is treated as 0.
	ListSessions(ctx context.Context, scope Scope, filter SessionFilter, limit, offset int) ([]Session, error)

	// UpdateSessionStatus moves the session from expectedFrom to newStatus
	// and appends one inspection_session_transitions row, in a single
	// transaction. It fails with an error wrapping ErrConflict when the
	// stored status is not expectedFrom (closing the concurrent-submitter
	// race) and with an error wrapping ErrSessionNotFound when the session
	// does not exist. The status-appropriate timestamp (started_at,
	// submitted_at, reported_at) is stamped inside the transaction.
	UpdateSessionStatus(ctx context.Context, sessionID uuid.UUID, expectedFrom, newStatus Status, actorID *uuid.UUID, reason string) error

	// InsertFinding appends f to the session's findings. It assigns f.ID
	// when unset and populates f.CreatedAt. The database refuses the insert
	// when the session is not IN_PROGRESS (sealed trigger); it fails with
	// an error wrapping ErrSessionNotFound when the session does not exist.
	InsertFinding(ctx context.Context, f *Finding) error

	// ListFindings returns the session's findings in chronological order
	// (oldest first), for a session visible to scope (the boundary is
	// re-applied through the parent session, so an out-of-scope session
	// yields an empty list only through its own not-found check).
	ListFindings(ctx context.Context, sessionID uuid.UUID, scope Scope) ([]Finding, error)

	// InsertEvidence appends e to the session's evidence. It assigns e.ID
	// when unset and populates e.CreatedAt. The database refuses the insert
	// when the session is not IN_PROGRESS (sealed trigger); it fails with
	// an error wrapping ErrSessionNotFound when the session does not exist.
	InsertEvidence(ctx context.Context, e *Evidence) error

	// ListEvidence returns the session's evidence in chronological order
	// (oldest first), for a session visible to scope.
	ListEvidence(ctx context.Context, sessionID uuid.UUID, scope Scope) ([]Evidence, error)

	// InsertReport issues the report atomically: it locks the session,
	// requires status SUBMITTED (else an error wrapping ErrConflict),
	// computes the next report version and refuses one that does not match
	// r.Version (defense in depth for the hash), inserts the report, moves
	// the session to REPORT_AVAILABLE (stamping reported_at) and appends
	// the transition row — all in one transaction. r must carry SessionID,
	// Version, every snapshot field and ContentHash; the store assigns
	// r.ID when unset and populates r.CreatedAt.
	InsertReport(ctx context.Context, r *Report) error

	// GetReport returns the session's report — the latest version when
	// version is nil, else that exact version — for a session visible to
	// scope, or an error wrapping ErrReportNotFound.
	GetReport(ctx context.Context, sessionID uuid.UUID, version *int, scope Scope) (Report, error)
}

// PostgresStore implements Store on PostgreSQL through pgx.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// Compile-time assertion that PostgresStore satisfies Store.
var _ Store = (*PostgresStore)(nil)

// NewPostgresStore returns a Store backed by pool. The pool must already
// have the inspections migrations applied (platform.MigrateUp with
// inspectionsmigrations.FS).
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

const sessionColumns = `id, tenant_id, vehicle_id, job_id, customer_id, vehicle_class, template_id, template_name,
        template_version, inspector_id, status, started_at, submitted_at, reported_at, created_at, updated_at`

const findingColumns = `id, session_id, item_key, result, severity, notes, recorded_by, created_at`

const evidenceColumns = `id, session_id, finding_id, kind, content_hash, captured_at, captured_by, uploaded, notes, created_at`

const reportColumns = `id, session_id, version, vehicle_id, job_id, inspector_id, template_id, template_name,
        template_version, findings, evidence, findings_count, evidence_count, content_hash, issued_by, notes, created_at`

// sessionScopePredicate renders the boundary condition for the sessions
// table with unqualified column names.
func sessionScopePredicate(scope Scope, nextParam int) (string, []any) {
	return scopePredicate(scope, "tenant_id", "inspector_id", "customer_id", nextParam)
}

// CreateSession implements Store.
func (s *PostgresStore) CreateSession(ctx context.Context, sess *Session) error {
	if sess == nil {
		return fmt.Errorf("inspections: create session: session is required")
	}
	if sess.ID == uuid.Nil {
		sess.ID = uuid.New()
	}
	err := s.pool.QueryRow(ctx, `
                INSERT INTO inspection_sessions
                        (id, tenant_id, vehicle_id, job_id, customer_id, vehicle_class, template_id, template_name,
                         template_version, inspector_id, status)
                VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
                RETURNING created_at, updated_at`,
		sess.ID, sess.TenantID, sess.VehicleID, sess.JobID, sess.CustomerID, sess.VehicleClass,
		sess.TemplateID, sess.TemplateName, sess.TemplateVersion, sess.InspectorID, sess.Status,
	).Scan(&sess.CreatedAt, &sess.UpdatedAt)
	if err != nil {
		return fmt.Errorf("inspections: insert session: %w", err)
	}
	return nil
}

// GetSession implements Store. The scope predicate runs inside the query
// (ADR-0004): an out-of-scope session is indistinguishable from a missing
// one.
func (s *PostgresStore) GetSession(ctx context.Context, id uuid.UUID, scope Scope) (Session, error) {
	pred, predArgs := sessionScopePredicate(scope, 2)
	where := "id = $1"
	args := []any{id}
	if pred != "" {
		where += " AND " + pred
		args = append(args, predArgs...)
	}
	row := s.pool.QueryRow(ctx, `
                SELECT `+sessionColumns+`
                FROM inspection_sessions
                WHERE `+where, args...)
	sess, err := scanSession(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Session{}, fmt.Errorf("inspections: get session %s: %w", id, ErrSessionNotFound)
		}
		return Session{}, fmt.Errorf("inspections: get session: %w", err)
	}
	return sess, nil
}

// ListSessions implements Store: one offset page of the scoped registry,
// newest first.
func (s *PostgresStore) ListSessions(ctx context.Context, scope Scope, filter SessionFilter, limit, offset int) ([]Session, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	if offset < 0 {
		offset = 0
	}

	pred, predArgs := sessionScopePredicate(scope, 1)
	where := "TRUE"
	args := []any{}
	if pred != "" {
		where = pred
		args = append(args, predArgs...)
	}
	if filter.VehicleID != nil {
		args = append(args, *filter.VehicleID)
		where += fmt.Sprintf(" AND vehicle_id = $%d", len(args))
	}
	if filter.JobID != nil {
		args = append(args, *filter.JobID)
		where += fmt.Sprintf(" AND job_id = $%d", len(args))
	}
	if filter.Status != nil {
		args = append(args, *filter.Status)
		where += fmt.Sprintf(" AND status = $%d", len(args))
	}
	args = append(args, limit, offset)
	query := fmt.Sprintf(`
                SELECT `+sessionColumns+`
                FROM inspection_sessions
                WHERE %s
                ORDER BY created_at DESC, id DESC
                LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("inspections: list sessions: %w", err)
	}
	defer rows.Close()

	out := make([]Session, 0, limit)
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("inspections: scan session: %w", err)
		}
		out = append(out, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspections: list sessions: %w", err)
	}
	return out, nil
}

// UpdateSessionStatus implements Store. The FOR UPDATE lock serializes
// concurrent movers; the expectedFrom re-check closes the race the
// service-level check cannot.
func (s *PostgresStore) UpdateSessionStatus(ctx context.Context, sessionID uuid.UUID, expectedFrom, newStatus Status, actorID *uuid.UUID, reason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("inspections: begin update session status: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var current Status
	err = tx.QueryRow(ctx, `
                SELECT status
                FROM inspection_sessions
                WHERE id = $1
                FOR UPDATE`, sessionID).Scan(&current)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("inspections: update session status %s: %w", sessionID, ErrSessionNotFound)
		}
		return fmt.Errorf("inspections: lock session %s: %w", sessionID, err)
	}
	if current != expectedFrom {
		return fmt.Errorf("inspections: update session status %s: status moved to %s: %w",
			sessionID, current, ErrConflict)
	}

	stamp := statusTimestampColumn(newStatus)
	setClause := "status = $2, updated_at = now()"
	if stamp != "" {
		setClause += ", " + stamp + " = now()"
	}
	if _, err := tx.Exec(ctx, `
                UPDATE inspection_sessions
                SET `+setClause+`
                WHERE id = $1`, sessionID, newStatus); err != nil {
		return fmt.Errorf("inspections: update session status: %w", err)
	}

	if _, err := tx.Exec(ctx, `
                INSERT INTO inspection_session_transitions (session_id, from_status, to_status, actor_id, reason)
                VALUES ($1, $2, $3, $4, $5)`,
		sessionID, current, newStatus, actorID, reason); err != nil {
		return fmt.Errorf("inspections: insert session transition: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("inspections: commit update session status: %w", err)
	}
	return nil
}

// statusTimestampColumn names the context timestamp each status move
// stamps (empty for CREATED, which carries created_at).
func statusTimestampColumn(to Status) string {
	switch to {
	case StatusInProgress:
		return "started_at"
	case StatusSubmitted:
		return "submitted_at"
	case StatusReportAvailable:
		return "reported_at"
	default:
		return ""
	}
}

// InsertFinding implements Store.
func (s *PostgresStore) InsertFinding(ctx context.Context, f *Finding) error {
	if f == nil {
		return fmt.Errorf("inspections: insert finding: finding is required")
	}
	if f.ID == uuid.Nil {
		f.ID = uuid.New()
	}
	err := s.pool.QueryRow(ctx, `
                INSERT INTO inspection_findings (id, session_id, item_key, result, severity, notes, recorded_by)
                VALUES ($1, $2, $3, $4, $5, $6, $7)
                RETURNING created_at`,
		f.ID, f.SessionID, f.ItemKey, f.Result, f.Severity, f.Notes, f.RecordedBy,
	).Scan(&f.CreatedAt)
	if err != nil {
		if isForeignKeyViolation(err) {
			return fmt.Errorf("inspections: insert finding: %w", ErrSessionNotFound)
		}
		return fmt.Errorf("inspections: insert finding: %w", err)
	}
	return nil
}

// ListFindings implements Store. The EXISTS subquery re-applies the session
// visibility predicate, so findings of invisible sessions read as missing.
func (s *PostgresStore) ListFindings(ctx context.Context, sessionID uuid.UUID, scope Scope) ([]Finding, error) {
	pred, predArgs := scopePredicate(scope, "s.tenant_id", "s.inspector_id", "s.customer_id", 2)
	where := "f.session_id = $1"
	args := []any{sessionID}
	if pred != "" {
		where += " AND EXISTS (SELECT 1 FROM inspection_sessions s WHERE s.id = f.session_id AND " + pred + ")"
		args = append(args, predArgs...)
	}
	query := `
                SELECT ` + findingColumns + `
                FROM inspection_findings f
                WHERE ` + where + `
                ORDER BY f.created_at ASC, f.id ASC`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("inspections: list findings: %w", err)
	}
	defer rows.Close()

	out := make([]Finding, 0, 8)
	for rows.Next() {
		var f Finding
		if err := rows.Scan(&f.ID, &f.SessionID, &f.ItemKey, &f.Result, &f.Severity, &f.Notes, &f.RecordedBy, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("inspections: scan finding: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspections: list findings: %w", err)
	}
	return out, nil
}

// InsertEvidence implements Store.
func (s *PostgresStore) InsertEvidence(ctx context.Context, e *Evidence) error {
	if e == nil {
		return fmt.Errorf("inspections: insert evidence: evidence is required")
	}
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	err := s.pool.QueryRow(ctx, `
                INSERT INTO inspection_evidence (id, session_id, finding_id, kind, content_hash, captured_at, captured_by, uploaded, notes)
                VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
                RETURNING created_at`,
		e.ID, e.SessionID, e.FindingID, e.Kind, e.ContentHash, e.CapturedAt, e.CapturedBy, e.Uploaded, e.Notes,
	).Scan(&e.CreatedAt)
	if err != nil {
		if isForeignKeyViolation(err) {
			return fmt.Errorf("inspections: insert evidence: %w", ErrSessionNotFound)
		}
		return fmt.Errorf("inspections: insert evidence: %w", err)
	}
	return nil
}

// ListEvidence implements Store. The EXISTS subquery re-applies the session
// visibility predicate, so evidence of invisible sessions reads as missing.
func (s *PostgresStore) ListEvidence(ctx context.Context, sessionID uuid.UUID, scope Scope) ([]Evidence, error) {
	pred, predArgs := scopePredicate(scope, "s.tenant_id", "s.inspector_id", "s.customer_id", 2)
	where := "e.session_id = $1"
	args := []any{sessionID}
	if pred != "" {
		where += " AND EXISTS (SELECT 1 FROM inspection_sessions s WHERE s.id = e.session_id AND " + pred + ")"
		args = append(args, predArgs...)
	}
	query := `
                SELECT ` + evidenceColumns + `
                FROM inspection_evidence e
                WHERE ` + where + `
                ORDER BY e.created_at ASC, e.id ASC`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("inspections: list evidence: %w", err)
	}
	defer rows.Close()

	out := make([]Evidence, 0, 8)
	for rows.Next() {
		var e Evidence
		if err := rows.Scan(&e.ID, &e.SessionID, &e.FindingID, &e.Kind, &e.ContentHash, &e.CapturedAt, &e.CapturedBy, &e.Uploaded, &e.Notes, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("inspections: scan evidence: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspections: list evidence: %w", err)
	}
	return out, nil
}

// InsertReport implements Store. The session lock serializes issuers; the
// SUBMITTED re-check guarantees a report is assembled exactly from a
// frozen session, and the version re-check refuses any assembly whose
// declared version does not match the one the store computes (the content
// hash covers the version — a mismatch must fail closed).
func (s *PostgresStore) InsertReport(ctx context.Context, r *Report) error {
	if r == nil {
		return fmt.Errorf("inspections: insert report: report is required")
	}
	if r.ContentHash == "" {
		return fmt.Errorf("inspections: insert report: content_hash is required")
	}
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("inspections: begin insert report: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var current Status
	err = tx.QueryRow(ctx, `
                SELECT status
                FROM inspection_sessions
                WHERE id = $1
                FOR UPDATE`, r.SessionID).Scan(&current)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("inspections: insert report for session %s: %w", r.SessionID, ErrSessionNotFound)
		}
		return fmt.Errorf("inspections: lock session %s: %w", r.SessionID, err)
	}
	if current != StatusSubmitted {
		return fmt.Errorf("inspections: insert report: session %s is %s, want %s: %w",
			r.SessionID, current, StatusSubmitted, ErrConflict)
	}

	var nextVersion int
	if err := tx.QueryRow(ctx, `
                SELECT COALESCE(MAX(version), 0) + 1
                FROM inspection_reports
                WHERE session_id = $1`, r.SessionID).Scan(&nextVersion); err != nil {
		return fmt.Errorf("inspections: compute report version: %w", err)
	}
	if nextVersion != r.Version {
		return fmt.Errorf("inspections: insert report: computed version %d does not match declared version %d: %w",
			nextVersion, r.Version, ErrConflict)
	}

	if err := tx.QueryRow(ctx, `
                INSERT INTO inspection_reports
                        (id, session_id, version, vehicle_id, job_id, inspector_id, template_id, template_name, template_version,
                         findings, evidence, findings_count, evidence_count, content_hash, issued_by, notes)
                VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
                RETURNING created_at`,
		r.ID, r.SessionID, r.Version, r.VehicleID, r.JobID, r.InspectorID, r.TemplateID, r.TemplateName, r.TemplateVersion,
		r.FindingsSnapshot, r.EvidenceSnapshot, r.FindingsCount, r.EvidenceCount, r.ContentHash, r.IssuedBy, r.Notes,
	).Scan(&r.CreatedAt); err != nil {
		return fmt.Errorf("inspections: insert report: %w", err)
	}

	if _, err := tx.Exec(ctx, `
                UPDATE inspection_sessions
                SET status = $2, reported_at = now(), updated_at = now()
                WHERE id = $1`, r.SessionID, StatusReportAvailable); err != nil {
		return fmt.Errorf("inspections: update session for report: %w", err)
	}

	if _, err := tx.Exec(ctx, `
                INSERT INTO inspection_session_transitions (session_id, from_status, to_status, actor_id, reason)
                VALUES ($1, $2, $3, $4, $5)`,
		r.SessionID, StatusSubmitted, StatusReportAvailable, r.IssuedBy, "report issued"); err != nil {
		return fmt.Errorf("inspections: insert report transition: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("inspections: commit insert report: %w", err)
	}
	return nil
}

// GetReport implements Store. version nil selects the latest.
func (s *PostgresStore) GetReport(ctx context.Context, sessionID uuid.UUID, version *int, scope Scope) (Report, error) {
	pred, predArgs := scopePredicate(scope, "s.tenant_id", "s.inspector_id", "s.customer_id", 2)
	where := "r.session_id = $1"
	args := []any{sessionID}
	if pred != "" {
		where += " AND EXISTS (SELECT 1 FROM inspection_sessions s WHERE s.id = r.session_id AND " + pred + ")"
		args = append(args, predArgs...)
	}
	order := " ORDER BY r.version DESC LIMIT 1"
	if version != nil {
		where += " AND r.version = $" + fmt.Sprint(len(args)+1)
		args = append(args, *version)
		order = " LIMIT 1"
	}
	query := `
                SELECT ` + reportColumns + `
                FROM inspection_reports r
                WHERE ` + where + order

	row := s.pool.QueryRow(ctx, query, args...)
	r, err := scanReport(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Report{}, fmt.Errorf("inspections: get report for session %s: %w", sessionID, ErrReportNotFound)
		}
		return Report{}, fmt.Errorf("inspections: get report: %w", err)
	}
	return r, nil
}

func scanSession(row pgx.Row) (Session, error) {
	var s Session
	err := row.Scan(&s.ID, &s.TenantID, &s.VehicleID, &s.JobID, &s.CustomerID, &s.VehicleClass,
		&s.TemplateID, &s.TemplateName, &s.TemplateVersion, &s.InspectorID, &s.Status,
		&s.StartedAt, &s.SubmittedAt, &s.ReportedAt, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return Session{}, err
	}
	return s, nil
}

func scanReport(row pgx.Row) (Report, error) {
	var r Report
	err := row.Scan(&r.ID, &r.SessionID, &r.Version, &r.VehicleID, &r.JobID, &r.InspectorID,
		&r.TemplateID, &r.TemplateName, &r.TemplateVersion,
		&r.FindingsSnapshot, &r.EvidenceSnapshot, &r.FindingsCount, &r.EvidenceCount,
		&r.ContentHash, &r.IssuedBy, &r.Notes, &r.CreatedAt)
	if err != nil {
		return Report{}, err
	}
	return r, nil
}

// isForeignKeyViolation reports whether err is a PostgreSQL foreign-key
// violation (SQLSTATE 23503), which in this domain means the referenced
// session (or linked finding) does not exist.
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23503"
	}
	return false
}

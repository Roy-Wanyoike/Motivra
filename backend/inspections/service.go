package inspections

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// EventDomain is the JetStream domain prefix used when publishing: events
// go out on motivra.inspections.<event_type> (ADR-0002).
const EventDomain = "inspections"

// Published event types, following "<aggregate>.<event>.v<n>".
const (
	EventSessionCreated   = "session.created.v1"
	EventSessionStarted   = "session.started.v1"
	EventSessionSubmitted = "session.submitted.v1"
	EventReportIssued     = "report.issued.v1"
)

// Pagination discipline applied by Sessions (mirrors the store).
const (
	DefaultSessionsLimit = 50
	MaxSessionsLimit     = 200
)

// Validation bounds.
const (
	// MaxNotesLength caps free-text notes on findings, evidence and reports.
	MaxNotesLength = 2000
	// maxCapturedAtSkew bounds how far in the future a captured_at may sit
	// (clock-skew tolerance for offline capture).
	maxCapturedAtSkew = 5 * time.Minute
)

// sha256HexPattern is the exact content-hash form evidence and reports
// carry: 64 lowercase hex characters (a SHA-256 digest). The database CHECK
// constraints mirror it.
var sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// resultSeverityMatrix is the legal result/severity combinations. A PASS
// may still carry an observation (AMBER); failures and warnings may be
// AMBER or RED; a not-applicable item is by definition unremarkable.
var resultSeverityMatrix = map[FindingResult]map[Severity]bool{
	ResultPass:          {SeverityGreen: true, SeverityAmber: true},
	ResultFail:          {SeverityAmber: true, SeverityRed: true},
	ResultAttention:     {SeverityAmber: true, SeverityRed: true},
	ResultNotApplicable: {SeverityGreen: true},
}

// IsResultSeverityLegal reports whether the result/severity pair is one of
// the documented combinations. Exported for the HTTP layer and tests.
func IsResultSeverityLegal(result FindingResult, severity Severity) bool {
	return resultSeverityMatrix[result][severity]
}

// ErrSessionSealed is returned when findings or evidence are recorded on a
// session that is no longer IN_PROGRESS. The database triggers enforce the
// same rule (append-only + sealed migrations); this pre-check keeps the
// API error clean.
var ErrSessionSealed = errors.New("inspections: session is sealed for recording")

// ErrNotInspector is returned when an actor other than the session's
// inspector (or a control-plane operator) attempts to work the session.
var ErrNotInspector = errors.New("inspections: actor is not the session's inspector")

// ErrInvalidInput is wrapped by every request-validation error (unknown
// item, illegal result/severity pair, malformed hash, over-long notes...).
// The HTTP layer maps it onto a platform 422.
var ErrInvalidInput = errors.New("inspections: invalid input")

// invalidInput builds an ErrInvalidInput-wrapped validation error.
func invalidInput(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalidInput}, args...)...)
}

// SessionCreatedPayload is the payload of session.created.v1.
type SessionCreatedPayload struct {
	SessionID       uuid.UUID  `json:"session_id"`
	VehicleClass    string     `json:"vehicle_class"`
	VehicleID       *uuid.UUID `json:"vehicle_id,omitempty"`
	JobID           *uuid.UUID `json:"job_id,omitempty"`
	CustomerID      *uuid.UUID `json:"customer_id,omitempty"`
	TemplateID      uuid.UUID  `json:"template_id"`
	TemplateName    string     `json:"template_name"`
	TemplateVersion int        `json:"template_version"`
	InspectorID     uuid.UUID  `json:"inspector_id"`
}

// SessionStartedPayload is the payload of session.started.v1.
type SessionStartedPayload struct {
	SessionID   uuid.UUID  `json:"session_id"`
	InspectorID uuid.UUID  `json:"inspector_id"`
	VehicleID   *uuid.UUID `json:"vehicle_id,omitempty"`
}

// SessionSubmittedPayload is the payload of session.submitted.v1.
type SessionSubmittedPayload struct {
	SessionID     uuid.UUID  `json:"session_id"`
	InspectorID   uuid.UUID  `json:"inspector_id"`
	FindingsCount int        `json:"findings_count"`
	EvidenceCount int        `json:"evidence_count"`
	VehicleID     *uuid.UUID `json:"vehicle_id,omitempty"`
	JobID         *uuid.UUID `json:"job_id,omitempty"`
}

// ReportIssuedPayload is the payload of report.issued.v1.
type ReportIssuedPayload struct {
	SessionID       uuid.UUID `json:"session_id"`
	ReportID        uuid.UUID `json:"report_id"`
	Version         int       `json:"version"`
	ContentHash     string    `json:"content_hash"`
	InspectorID     uuid.UUID `json:"inspector_id"`
	FindingsCount   int       `json:"findings_count"`
	EvidenceCount   int       `json:"evidence_count"`
	TemplateVersion int       `json:"template_version"`
}

// Service is the inspections domain service. It owns the state-machine
// discipline (every status move goes through CanTransition before it
// touches the store), the template-driven session rules, the report
// integrity preconditions, domain metrics and event publishing. publisher
// and metrics may be nil (tests, offline tooling); events and
// instrumentation are simply skipped then.
type Service struct {
	store     Store
	publisher platform.Publisher
	metrics   *Metrics
}

// NewService returns a Service over store. publisher and metrics may be
// nil.
func NewService(store Store, publisher platform.Publisher, metrics *Metrics) *Service {
	return &Service{store: store, publisher: publisher, metrics: metrics}
}

// CreateSession validates and persists a new inspection session. The
// vehicle class resolves the active template from the code registry and
// the resolution is frozen onto the session. The session starts in status
// CREATED and session.created.v1 is published. The tenant (from the
// creator's claims) is stamped by the HTTP layer — never by client input.
func (s *Service) CreateSession(ctx context.Context, sess *Session) error {
	if sess == nil {
		return invalidInput("create session: session is required")
	}
	if sess.InspectorID == uuid.Nil {
		return invalidInput("create session: inspector_id is required")
	}
	if !IsVehicleClass(sess.VehicleClass) {
		return invalidInput("create session: vehicle_class %q is not a known vehicle class", sess.VehicleClass)
	}
	template, err := ActiveTemplateForClass(sess.VehicleClass)
	if err != nil {
		return fmt.Errorf("inspections: create session: %w", err)
	}
	if err := validateOptionalID("vehicle_id", sess.VehicleID); err != nil {
		return err
	}
	if err := validateOptionalID("job_id", sess.JobID); err != nil {
		return err
	}
	if err := validateOptionalID("customer_id", sess.CustomerID); err != nil {
		return err
	}
	if sess.Status != "" && sess.Status != StatusCreated {
		return invalidInput("create session: new sessions always start in status %s", StatusCreated)
	}

	sess.TemplateID = template.ID
	sess.TemplateName = template.Name
	sess.TemplateVersion = template.Version
	sess.Status = StatusCreated

	if err := s.store.CreateSession(ctx, sess); err != nil {
		return fmt.Errorf("inspections: create session: %w", err)
	}
	s.metrics.SessionCreated()

	return s.publish(ctx, EventSessionCreated, sess.ID, &sess.InspectorID, tenantString(sess.TenantID), SessionCreatedPayload{
		SessionID:       sess.ID,
		VehicleClass:    string(sess.VehicleClass),
		VehicleID:       sess.VehicleID,
		JobID:           sess.JobID,
		CustomerID:      sess.CustomerID,
		TemplateID:      sess.TemplateID,
		TemplateName:    sess.TemplateName,
		TemplateVersion: sess.TemplateVersion,
		InspectorID:     sess.InspectorID,
	})
}

// Start moves the session CREATED -> IN_PROGRESS and stamps started_at.
// Only the session's inspector (or a control-plane operator) may start it,
// and only through a scope that can see it.
func (s *Service) Start(ctx context.Context, sessionID uuid.UUID, actorID uuid.UUID, scope Scope) (Session, error) {
	sess, err := s.store.GetSession(ctx, sessionID, scope)
	if err != nil {
		return Session{}, fmt.Errorf("inspections: start session: %w", err)
	}
	if err := s.guardActor(sess, actorID, scope); err != nil {
		return Session{}, err
	}
	if err := CanTransition(sess.Status, StatusInProgress); err != nil {
		return Session{}, err
	}
	if err := s.store.UpdateSessionStatus(ctx, sessionID, sess.Status, StatusInProgress, &actorID, "inspection started"); err != nil {
		return Session{}, fmt.Errorf("inspections: start session: %w", err)
	}
	updated, err := s.store.GetSession(ctx, sessionID, scope)
	if err != nil {
		return Session{}, fmt.Errorf("inspections: start session: reload: %w", err)
	}
	if err := s.publish(ctx, EventSessionStarted, sessionID, &actorID, tenantString(updated.TenantID), SessionStartedPayload{
		SessionID:   sessionID,
		InspectorID: updated.InspectorID,
		VehicleID:   updated.VehicleID,
	}); err != nil {
		return Session{}, err
	}
	return updated, nil
}

// RecordFinding appends a finding to an IN_PROGRESS session. The item must
// belong to the session's template, the result/severity pair must be one
// of the documented combinations, and a corrected item is recorded as a
// NEW finding (append-only; the newest record per item wins at assembly).
func (s *Service) RecordFinding(ctx context.Context, sessionID uuid.UUID, f *Finding, actorID uuid.UUID, scope Scope) (Finding, error) {
	sess, err := s.store.GetSession(ctx, sessionID, scope)
	if err != nil {
		return Finding{}, fmt.Errorf("inspections: record finding: %w", err)
	}
	if err := s.guardActor(sess, actorID, scope); err != nil {
		return Finding{}, err
	}
	if sess.Status != StatusInProgress {
		return Finding{}, fmt.Errorf("inspections: record finding: session is %s: %w", sess.Status, ErrSessionSealed)
	}
	template, err := TemplateByID(sess.TemplateID)
	if err != nil {
		return Finding{}, fmt.Errorf("inspections: record finding: %w", err)
	}
	if err := validateFindingInput(template, f, actorID); err != nil {
		return Finding{}, err
	}
	f.SessionID = sess.ID
	if err := s.store.InsertFinding(ctx, f); err != nil {
		return Finding{}, fmt.Errorf("inspections: record finding: %w", err)
	}
	return *f, nil
}

// validateFindingInput applies the template-driven finding rules: the item
// must exist in the template (that is what makes the session
// template-driven), the result/severity pair must be documented, and notes
// must respect the length cap. The actor becomes the finding's recorded_by.
func validateFindingInput(template Template, f *Finding, actorID uuid.UUID) error {
	if f == nil {
		return invalidInput("record finding: finding is required")
	}
	if _, ok := template.Item(f.ItemKey); !ok {
		return invalidInput("record finding: item %q is not part of template %q", f.ItemKey, template.Name)
	}
	switch f.Result {
	case ResultPass, ResultFail, ResultAttention, ResultNotApplicable:
	default:
		return invalidInput("record finding: result %q is not a known finding result", f.Result)
	}
	switch f.Severity {
	case SeverityGreen, SeverityAmber, SeverityRed:
	default:
		return invalidInput("record finding: severity %q is not a known severity", f.Severity)
	}
	if !IsResultSeverityLegal(f.Result, f.Severity) {
		return invalidInput("record finding: result %s may not carry severity %s", f.Result, f.Severity)
	}
	if len(f.Notes) > MaxNotesLength {
		return invalidInput("record finding: notes exceed %d characters", MaxNotesLength)
	}
	if actorID != uuid.Nil {
		f.RecordedBy = &actorID
	}
	return nil
}

// AttachEvidence appends an evidence provenance record to an IN_PROGRESS
// session. The content hash must be a lowercase hex SHA-256, captured_at
// may not sit in the future beyond the clock-skew tolerance, and an
// optional finding linkage must reference a finding of the same session.
func (s *Service) AttachEvidence(ctx context.Context, sessionID uuid.UUID, e *Evidence, actorID uuid.UUID, scope Scope) (Evidence, error) {
	sess, err := s.store.GetSession(ctx, sessionID, scope)
	if err != nil {
		return Evidence{}, fmt.Errorf("inspections: attach evidence: %w", err)
	}
	if err := s.guardActor(sess, actorID, scope); err != nil {
		return Evidence{}, err
	}
	if sess.Status != StatusInProgress {
		return Evidence{}, fmt.Errorf("inspections: attach evidence: session is %s: %w", sess.Status, ErrSessionSealed)
	}
	if err := validateEvidenceInput(e, actorID); err != nil {
		return Evidence{}, err
	}
	if e.FindingID != nil {
		findings, err := s.store.ListFindings(ctx, sess.ID, scope)
		if err != nil {
			return Evidence{}, fmt.Errorf("inspections: attach evidence: %w", err)
		}
		linked := false
		for _, f := range findings {
			if f.ID == *e.FindingID {
				linked = true
				break
			}
		}
		if !linked {
			return Evidence{}, invalidInput("attach evidence: finding %s does not belong to session %s", *e.FindingID, sess.ID)
		}
	}
	e.SessionID = sess.ID
	if err := s.store.InsertEvidence(ctx, e); err != nil {
		return Evidence{}, fmt.Errorf("inspections: attach evidence: %w", err)
	}
	return *e, nil
}

// validateEvidenceInput applies the evidence provenance rules.
func validateEvidenceInput(e *Evidence, actorID uuid.UUID) error {
	if e == nil {
		return invalidInput("attach evidence: evidence is required")
	}
	if !sha256HexPattern.MatchString(e.ContentHash) {
		return invalidInput("attach evidence: content_hash must be a 64-character lowercase hex SHA-256")
	}
	if !IsEvidenceKind(e.Kind) {
		return invalidInput("attach evidence: kind %q is not a known evidence kind", e.Kind)
	}
	if e.CapturedAt.IsZero() {
		return invalidInput("attach evidence: captured_at is required")
	}
	if e.CapturedAt.After(time.Now().Add(maxCapturedAtSkew)) {
		return invalidInput("attach evidence: captured_at may not sit more than %s in the future", maxCapturedAtSkew)
	}
	if len(e.Notes) > MaxNotesLength {
		return invalidInput("attach evidence: notes exceed %d characters", MaxNotesLength)
	}
	if actorID != uuid.Nil {
		e.CapturedBy = &actorID
	}
	return nil
}

// Submit attests the inspection is complete: the session moves
// IN_PROGRESS -> SUBMITTED and findings plus evidence are sealed
// (database-enforced). The report integrity preconditions are validated
// first, so a session that reaches SUBMITTED can always be reported.
// session.submitted.v1 is published with the recorded counts.
func (s *Service) Submit(ctx context.Context, sessionID uuid.UUID, actorID uuid.UUID, scope Scope) (Session, error) {
	sess, err := s.store.GetSession(ctx, sessionID, scope)
	if err != nil {
		return Session{}, fmt.Errorf("inspections: submit session: %w", err)
	}
	if err := s.guardActor(sess, actorID, scope); err != nil {
		return Session{}, err
	}
	if err := CanTransition(sess.Status, StatusSubmitted); err != nil {
		return Session{}, err
	}
	if err := s.checkReadiness(ctx, sess, scope); err != nil {
		return Session{}, err
	}
	if err := s.store.UpdateSessionStatus(ctx, sessionID, sess.Status, StatusSubmitted, &actorID, "inspection submitted"); err != nil {
		return Session{}, fmt.Errorf("inspections: submit session: %w", err)
	}
	updated, err := s.store.GetSession(ctx, sessionID, scope)
	if err != nil {
		return Session{}, fmt.Errorf("inspections: submit session: reload: %w", err)
	}

	findings, err := s.store.ListFindings(ctx, sessionID, scope)
	if err != nil {
		return Session{}, fmt.Errorf("inspections: submit session: %w", err)
	}
	evidence, err := s.store.ListEvidence(ctx, sessionID, scope)
	if err != nil {
		return Session{}, fmt.Errorf("inspections: submit session: %w", err)
	}
	s.metrics.SessionSubmitted()

	if err := s.publish(ctx, EventSessionSubmitted, sessionID, &actorID, tenantString(updated.TenantID), SessionSubmittedPayload{
		SessionID:     sessionID,
		InspectorID:   updated.InspectorID,
		FindingsCount: len(SelectLatestFindings(findings)),
		EvidenceCount: len(evidence),
		VehicleID:     updated.VehicleID,
		JobID:         updated.JobID,
	}); err != nil {
		return Session{}, err
	}
	return updated, nil
}

// checkReadiness validates the report-assembly preconditions (defense in
// depth: enforced again in IssueReport for anything that somehow reached
// SUBMITTED without satisfying them). Children inherit the session's
// visibility, so the caller's scope is the correct read scope here.
func (s *Service) checkReadiness(ctx context.Context, sess Session, scope Scope) error {
	template, err := TemplateByID(sess.TemplateID)
	if err != nil {
		return fmt.Errorf("inspections: submit session: %w", err)
	}
	findings, err := s.store.ListFindings(ctx, sess.ID, scope)
	if err != nil {
		return fmt.Errorf("inspections: submit session: %w", err)
	}
	evidence, err := s.store.ListEvidence(ctx, sess.ID, scope)
	if err != nil {
		return fmt.Errorf("inspections: submit session: %w", err)
	}
	if err := CheckReportReadiness(template, sess, findings, evidence); err != nil {
		return err
	}
	return nil
}

// IssueReport assembles the verifiable report record: the latest finding
// per template item and the full evidence set are snapshotted and hashed
// into content_hash, the report row is inserted append-only, the session
// moves SUBMITTED -> REPORT_AVAILABLE and report.issued.v1 is published.
func (s *Service) IssueReport(ctx context.Context, sessionID uuid.UUID, actorID uuid.UUID, notes string, scope Scope) (Report, error) {
	sess, err := s.store.GetSession(ctx, sessionID, scope)
	if err != nil {
		return Report{}, fmt.Errorf("inspections: issue report: %w", err)
	}
	if err := s.guardActor(sess, actorID, scope); err != nil {
		return Report{}, err
	}
	if err := CanTransition(sess.Status, StatusReportAvailable); err != nil {
		return Report{}, err
	}
	template, err := TemplateByID(sess.TemplateID)
	if err != nil {
		return Report{}, fmt.Errorf("inspections: issue report: %w", err)
	}
	findings, err := s.store.ListFindings(ctx, sessionID, scope)
	if err != nil {
		return Report{}, fmt.Errorf("inspections: issue report: %w", err)
	}
	evidence, err := s.store.ListEvidence(ctx, sessionID, scope)
	if err != nil {
		return Report{}, fmt.Errorf("inspections: issue report: %w", err)
	}
	// Defense in depth: a session should never have reached SUBMITTED
	// without satisfying these, but assembly refuses regardless.
	if err := CheckReportReadiness(template, sess, findings, evidence); err != nil {
		return Report{}, err
	}

	started := time.Now()
	selected := SelectLatestFindings(findings)
	findingsJSON, err := marshalFindingsSnapshot(selected)
	if err != nil {
		return Report{}, err
	}
	evidenceJSON, err := marshalEvidenceSnapshot(evidence)
	if err != nil {
		return Report{}, err
	}

	report := Report{
		SessionID:        sess.ID,
		Version:          ReportVersionInitial,
		VehicleID:        sess.VehicleID,
		JobID:            sess.JobID,
		InspectorID:      sess.InspectorID,
		TemplateID:       sess.TemplateID,
		TemplateName:     sess.TemplateName,
		TemplateVersion:  sess.TemplateVersion,
		FindingsSnapshot: findingsJSON,
		EvidenceSnapshot: evidenceJSON,
		FindingsCount:    len(selected),
		EvidenceCount:    len(evidence),
		ContentHash:      HashReportInput(BuildReportInput(sess, selected, evidence, ReportVersionInitial)),
		IssuedBy:         &actorID,
		Notes:            notes,
	}
	if len(notes) > MaxNotesLength {
		return Report{}, invalidInput("issue report: notes exceed %d characters", MaxNotesLength)
	}

	if err := s.store.InsertReport(ctx, &report); err != nil {
		return Report{}, fmt.Errorf("inspections: issue report: %w", err)
	}
	s.metrics.ReportIssued(time.Since(started).Seconds())

	if err := s.publish(ctx, EventReportIssued, report.ID, &actorID, tenantString(sess.TenantID), ReportIssuedPayload{
		SessionID:       sess.ID,
		ReportID:        report.ID,
		Version:         report.Version,
		ContentHash:     report.ContentHash,
		InspectorID:     report.InspectorID,
		FindingsCount:   report.FindingsCount,
		EvidenceCount:   report.EvidenceCount,
		TemplateVersion: report.TemplateVersion,
	}); err != nil {
		return Report{}, err
	}
	return report, nil
}

// GetSession returns the session when visible to scope.
func (s *Service) GetSession(ctx context.Context, id uuid.UUID, scope Scope) (Session, error) {
	return s.store.GetSession(ctx, id, scope)
}

// Sessions lists sessions visible to scope with the service's pagination
// discipline (defaults and caps).
func (s *Service) Sessions(ctx context.Context, scope Scope, filter SessionFilter, limit, offset int) ([]Session, error) {
	if limit <= 0 {
		limit = DefaultSessionsLimit
	}
	if limit > MaxSessionsLimit {
		limit = MaxSessionsLimit
	}
	if offset < 0 {
		offset = 0
	}
	if filter.Status != nil && !isKnownStatus(*filter.Status) {
		return nil, invalidInput("list sessions: unknown status %q", *filter.Status)
	}
	return s.store.ListSessions(ctx, scope, filter, limit, offset)
}

// Findings returns the session's findings in chronological order. The
// session must be visible to scope first (an invisible session reads as
// missing).
func (s *Service) Findings(ctx context.Context, sessionID uuid.UUID, scope Scope) ([]Finding, error) {
	if _, err := s.store.GetSession(ctx, sessionID, scope); err != nil {
		return nil, fmt.Errorf("inspections: list findings: %w", err)
	}
	return s.store.ListFindings(ctx, sessionID, scope)
}

// Evidence returns the session's evidence records in chronological order.
func (s *Service) Evidence(ctx context.Context, sessionID uuid.UUID, scope Scope) ([]Evidence, error) {
	if _, err := s.store.GetSession(ctx, sessionID, scope); err != nil {
		return nil, fmt.Errorf("inspections: list evidence: %w", err)
	}
	return s.store.ListEvidence(ctx, sessionID, scope)
}

// GetReport returns the session's report — latest version unless version
// is given. The session must be visible to scope first.
func (s *Service) GetReport(ctx context.Context, sessionID uuid.UUID, version *int, scope Scope) (Report, error) {
	if _, err := s.store.GetSession(ctx, sessionID, scope); err != nil {
		return Report{}, fmt.Errorf("inspections: get report: %w", err)
	}
	return s.store.GetReport(ctx, sessionID, version, scope)
}

// Verify recomputes the report's integrity from the live session, findings
// and evidence rows. The session must be visible to scope; an unissued
// report reads as ErrReportNotFound.
func (s *Service) Verify(ctx context.Context, sessionID uuid.UUID, version *int, scope Scope) (ReportVerification, error) {
	sess, err := s.store.GetSession(ctx, sessionID, scope)
	if err != nil {
		return ReportVerification{}, fmt.Errorf("inspections: verify report: %w", err)
	}
	report, err := s.store.GetReport(ctx, sessionID, version, scope)
	if err != nil {
		return ReportVerification{}, fmt.Errorf("inspections: verify report: %w", err)
	}
	template, err := TemplateByID(sess.TemplateID)
	if err != nil {
		return ReportVerification{}, fmt.Errorf("inspections: verify report: %w", err)
	}
	findings, err := s.store.ListFindings(ctx, sessionID, scope)
	if err != nil {
		return ReportVerification{}, fmt.Errorf("inspections: verify report: %w", err)
	}
	evidence, err := s.store.ListEvidence(ctx, sessionID, scope)
	if err != nil {
		return ReportVerification{}, fmt.Errorf("inspections: verify report: %w", err)
	}
	return VerifyReport(template, sess, findings, evidence, report), nil
}

// guardActor enforces the mutation boundary: only the session's inspector
// or a control-plane operator (whose scope is the unrestricted one) may
// work a session. A tenant or customer principal that can READ the session
// still cannot act on it.
func (s *Service) guardActor(sess Session, actorID uuid.UUID, scope Scope) error {
	if scope.Unrestricted() || actorID == sess.InspectorID {
		return nil
	}
	return fmt.Errorf("inspections: actor %s is not the inspector of session %s: %w", actorID, sess.ID, ErrNotInspector)
}

// validateOptionalID requires optional uuid references to be set to a real
// identifier when present.
func validateOptionalID(field string, id *uuid.UUID) error {
	if id != nil && *id == uuid.Nil {
		return invalidInput("%s is not a valid identifier", field)
	}
	return nil
}

// tenantString renders the optional tenant for the event envelope.
func tenantString(t *uuid.UUID) string {
	if t == nil {
		return ""
	}
	return t.String()
}

// correlationFromContext sources the event correlation_id from the
// platform server's request-ID middleware (ADR-0002: correlation is
// propagated from the originating request). It is empty for non-HTTP
// callers.
func correlationFromContext(ctx context.Context) string {
	return middleware.GetReqID(ctx)
}

// publish sends one domain event on the inspections domain. It is a no-op
// when no publisher is configured. Publishing failures surface to the
// caller: events are part of the domain contract, not best-effort logging.
// Every attempt is counted (success/failure) on the domain metrics.
func (s *Service) publish(ctx context.Context, eventType string, aggregateID uuid.UUID, actorID *uuid.UUID, tenantID string, payload any) error {
	if s.publisher == nil {
		return nil
	}
	actor := ""
	if actorID != nil && *actorID != uuid.Nil {
		actor = actorID.String()
	}
	e, err := platform.NewEvent(eventType, aggregateID.String(), tenantID, actor, correlationFromContext(ctx), payload)
	if err == nil {
		err = s.publisher.Publish(ctx, EventDomain, e)
		if err != nil {
			err = fmt.Errorf("inspections: publish %s: %w", eventType, err)
		}
	} else {
		err = fmt.Errorf("inspections: build %s event: %w", eventType, err)
	}
	s.metrics.EventPublished(eventType, err)
	return err
}

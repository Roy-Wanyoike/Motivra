package inspections

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Human report verification (issue #185): the companion of the machine
// integrity recomputation (Verify / GET .../report/verification). Where the
// recomputation answers "does the report still match its live rows", this
// surface records the human verdict — VERIFIED or REJECTED — of an
// authorized reviewer, append-only, with the integrity snapshot frozen at
// decision time and an audit_log row written in the same transaction.
//
// Discipline (mirrors the findings/report precedents):
//   - records are append-only (no update or delete path exists anywhere);
//     a correction is a NEW record and the newest record per report wins;
//   - every decision writes the platform audit trail (ADR-0004) inside the
//     same transaction as the record insert — an audit failure fails the
//     request and leaves no unaudited decision behind;
//   - the reviewer identity comes exclusively from the verified token
//     (never client input).
const (
	// EventReportVerified is published as motivra.inspections.report.verified.v1.
	EventReportVerified = "report.verified.v1"
	// EventReportRejected is published as motivra.inspections.report.rejected.v1.
	EventReportRejected = "report.rejected.v1"
)

// Audit actions written to the platform audit_log for verification
// decisions. One action per outcome keeps the trail directly filterable;
// the full integrity snapshot rides the metadata.
const (
	AuditActionReportVerified = "inspections.report.verified"
	AuditActionReportRejected = "inspections.report.rejected"
)

// AuditObjectTypeReport is the audit object_type of a verification decision.
const AuditObjectTypeReport = "inspection_report"

// VerificationOutcome is the human verdict on a report.
type VerificationOutcome string

// The known verification outcomes.
const (
	OutcomeVerified VerificationOutcome = "VERIFIED"
	OutcomeRejected VerificationOutcome = "REJECTED"
)

// IsVerificationOutcome reports whether outcome is a known verdict.
func IsVerificationOutcome(outcome VerificationOutcome) bool {
	return outcome == OutcomeVerified || outcome == OutcomeRejected
}

// ReportVerificationRecord is one append-only human verification decision.
// ReviewerID is always the authenticated principal — never client input.
type ReportVerificationRecord struct {
	ID         uuid.UUID
	ReportID   uuid.UUID
	SessionID  uuid.UUID
	Version    int
	Outcome    VerificationOutcome
	ReviewerID uuid.UUID
	Note       string
	// IntegrityVerified snapshots the machine recomputation at decision
	// time: true only when every integrity check passed.
	IntegrityVerified bool
	CreatedAt         time.Time
}

// ReportVerificationAudit is the audit_log entry written atomically with a
// verification record. Service callers build it through
// verificationAuditEntry; the store persists it in the record's transaction.
type ReportVerificationAudit struct {
	ActorID    uuid.UUID
	TenantID   *uuid.UUID
	Action     string
	ObjectID   uuid.UUID
	ObjectType string
	Metadata   map[string]any
}

// VerificationInput is the validated request for a verification decision.
type VerificationInput struct {
	Outcome VerificationOutcome
	Note    string
	// ExpectedVersion is the optimistic-concurrency guard: when non-nil the
	// current report version must equal it or the decision conflicts.
	ExpectedVersion *int
}

// ErrSelfVerification is returned when the session's capturing inspector
// attempts to verify their own work (separation of duties, issue #185).
var ErrSelfVerification = errors.New("inspections: the capturing inspector may not verify their own inspection")

// ErrVerificationNotFound is returned when a session carries no verification
// records or is outside the caller's scope.
var ErrVerificationNotFound = errors.New("inspections: verification records not found")

// validateVerificationInput applies the request rules: a known outcome, a
// note within the shared length cap, a positive expected version.
func validateVerificationInput(in VerificationInput) error {
	if !IsVerificationOutcome(in.Outcome) {
		return invalidInput("record verification: outcome %q is not a known verification outcome", in.Outcome)
	}
	if len(in.Note) > MaxNotesLength {
		return invalidInput("record verification: note exceeds %d characters", MaxNotesLength)
	}
	if in.ExpectedVersion != nil && *in.ExpectedVersion < 1 {
		return invalidInput("record verification: expected_report_version must be a positive integer")
	}
	return nil
}

// verificationAuditEntry builds the audit entry for a decision. The full
// integrity snapshot rides the metadata so the trail answers "what did the
// reviewer see" without joining back to anything mutable.
func verificationAuditEntry(rec ReportVerificationRecord, integrity ReportVerification, tenantID *uuid.UUID, action string) ReportVerificationAudit {
	return ReportVerificationAudit{
		ActorID:    rec.ReviewerID,
		TenantID:   tenantID,
		Action:     action,
		ObjectType: AuditObjectTypeReport,
		ObjectID:   rec.ReportID,
		Metadata: map[string]any{
			"session_id":         rec.SessionID.String(),
			"report_version":     rec.Version,
			"outcome":            string(rec.Outcome),
			"integrity_verified": rec.IntegrityVerified,
			"hash_match":         integrity.HashMatch,
			"findings_match":     integrity.FindingsMatch,
			"evidence_match":     integrity.EvidenceMatch,
			"template_match":     integrity.TemplateMatch,
			"stored_hash":        integrity.StoredHash,
			"recomputed_hash":    integrity.RecomputedHash,
		},
	}
}

// ReportVerificationPayload is the payload of report.verified.v1 and
// report.rejected.v1.
type ReportVerificationPayload struct {
	SessionID         uuid.UUID `json:"session_id"`
	ReportID          uuid.UUID `json:"report_id"`
	Version           int       `json:"version"`
	ReviewerID        uuid.UUID `json:"reviewer_id"`
	Outcome           string    `json:"outcome"`
	IntegrityVerified bool      `json:"integrity_verified"`
	ContentHash       string    `json:"content_hash"`
}

// RecordVerificationOutcome appends the reviewer's verdict to an issued
// report. Rules (issue #185):
//
//   - the session must be visible to the caller's scope (else 404-grade
//     ErrSessionNotFound — no existence leak) and REPORT_AVAILABLE (else
//     ErrConflict: nothing is reviewable before the report is issued);
//   - the capturing inspector may never verify their own work (ErrSelfVerification);
//   - VERIFIED requires the current integrity recomputation to pass — a
//     failing check conflicts with an error naming every failed check;
//     REJECTED is always allowed on a REPORT_AVAILABLE report and records
//     the snapshot as observed;
//   - the record and its audit_log row commit in one transaction; audit
//     failure fails the request (ADR-0004);
//   - on success exactly one report.verified.v1 / report.rejected.v1 event
//     is published.
func (s *Service) RecordVerificationOutcome(ctx context.Context, sessionID uuid.UUID, reviewerID uuid.UUID, in VerificationInput, scope Scope) (ReportVerificationRecord, error) {
	if err := validateVerificationInput(in); err != nil {
		return ReportVerificationRecord{}, err
	}
	sess, err := s.store.GetSession(ctx, sessionID, scope)
	if err != nil {
		return ReportVerificationRecord{}, fmt.Errorf("inspections: record verification: %w", err)
	}
	if sess.Status != StatusReportAvailable {
		return ReportVerificationRecord{}, fmt.Errorf(
			"inspections: record verification: session is %s; only %s reports are reviewable: %w",
			sess.Status, StatusReportAvailable, ErrConflict)
	}
	if reviewerID == sess.InspectorID {
		return ReportVerificationRecord{}, fmt.Errorf(
			"inspections: reviewer %s inspected session %s: %w", reviewerID, sess.ID, ErrSelfVerification)
	}
	report, err := s.store.GetReport(ctx, sessionID, nil, scope)
	if err != nil {
		return ReportVerificationRecord{}, fmt.Errorf("inspections: record verification: %w", err)
	}
	if in.ExpectedVersion != nil && *in.ExpectedVersion != report.Version {
		return ReportVerificationRecord{}, fmt.Errorf(
			"inspections: record verification: expected report version %d but current version is %d: %w",
			*in.ExpectedVersion, report.Version, ErrConflict)
	}

	// The integrity snapshot at decision time, recomputed from the live rows
	// with the same math the machine verification endpoint uses.
	integrity, err := s.recomputeIntegrity(ctx, sess, report, scope)
	if err != nil {
		return ReportVerificationRecord{}, fmt.Errorf("inspections: record verification: %w", err)
	}
	if in.Outcome == OutcomeVerified && !integrity.Verified {
		return ReportVerificationRecord{}, fmt.Errorf(
			"inspections: record verification: integrity checks failed [%s]: %w",
			failedIntegrityChecks(integrity), ErrConflict)
	}

	rec := ReportVerificationRecord{
		ReportID:          report.ID,
		SessionID:         sess.ID,
		Version:           report.Version,
		Outcome:           in.Outcome,
		ReviewerID:        reviewerID,
		Note:              in.Note,
		IntegrityVerified: integrity.Verified,
	}
	action := AuditActionReportVerified
	if in.Outcome == OutcomeRejected {
		action = AuditActionReportRejected
	}
	audit := verificationAuditEntry(rec, integrity, sess.TenantID, action)
	if err := s.store.InsertReportVerification(ctx, &rec, &audit); err != nil {
		return ReportVerificationRecord{}, fmt.Errorf("inspections: record verification: %w", err)
	}

	event := EventReportVerified
	if in.Outcome == OutcomeRejected {
		event = EventReportRejected
	}
	if err := s.publish(ctx, event, report.ID, &reviewerID, tenantString(sess.TenantID), ReportVerificationPayload{
		SessionID:         sess.ID,
		ReportID:          report.ID,
		Version:           report.Version,
		ReviewerID:        reviewerID,
		Outcome:           string(in.Outcome),
		IntegrityVerified: integrity.Verified,
		ContentHash:       report.ContentHash,
	}); err != nil {
		return ReportVerificationRecord{}, err
	}
	return rec, nil
}

// recomputeIntegrity reruns VerifyReport against the live rows. The caller
// has already resolved sess and report through the scope.
func (s *Service) recomputeIntegrity(ctx context.Context, sess Session, report Report, scope Scope) (ReportVerification, error) {
	template, err := TemplateByID(sess.TemplateID)
	if err != nil {
		return ReportVerification{}, err
	}
	findings, err := s.store.ListFindings(ctx, sess.ID, scope)
	if err != nil {
		return ReportVerification{}, err
	}
	evidence, err := s.store.ListEvidence(ctx, sess.ID, scope)
	if err != nil {
		return ReportVerification{}, err
	}
	return VerifyReport(template, sess, findings, evidence, report), nil
}

// failedIntegrityChecks names every check a verification recomputation
// failed, so a refused VERIFIED names its reasons.
func failedIntegrityChecks(v ReportVerification) string {
	var failed []string
	if !v.HashMatch {
		failed = append(failed, "hash_match")
	}
	if !v.FindingsMatch {
		failed = append(failed, "findings_match")
	}
	if !v.EvidenceMatch {
		failed = append(failed, "evidence_match")
	}
	if !v.TemplateMatch {
		failed = append(failed, "template_match")
	}
	return strings.Join(failed, ", ")
}

// VerificationRecords returns the session's verification records oldest
// first (append-only trail). The session must be visible to scope first —
// an invisible session reads as missing, so cross-principal probing learns
// nothing.
func (s *Service) VerificationRecords(ctx context.Context, sessionID uuid.UUID, scope Scope) ([]ReportVerificationRecord, error) {
	if _, err := s.store.GetSession(ctx, sessionID, scope); err != nil {
		return nil, fmt.Errorf("inspections: list verification records: %w", err)
	}
	return s.store.ListReportVerifications(ctx, sessionID, scope)
}

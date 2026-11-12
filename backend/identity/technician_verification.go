package identity

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// Technician skill-verification (issue #166): a server-authoritative,
// audit-logged attribute that tells dispatch whether a technician passed a
// skills assessment. The lifecycle is `unverified | pending | verified |
// rejected` (see models.go); this file implements the only write path —
// staff approve/reject decisions — and the technician's own status read.
//
// Discipline (mirrors the inspections report-verification and dispatch
// decision-record precedents):
//   - every decision appends one technician_verifications row (append-only,
//     DB-trigger enforced) and one audit_log entry IN THE SAME TRANSACTION;
//     an audit failure fails the request and leaves no unaudited decision
//     behind (ADR-0004 fail-closed auditing);
//   - the staff-role matrix (authz.go) is enforced HERE, in the service,
//     not only at the middleware, so a denied attempt is itself audited
//     (issue #166 QA: "a non-verifier cannot grant verification — 403 +
//     audit attempt logged") — platform.RequireRole would reject before any
//     audit writer could run;
//   - separation of duties: an actor can never decide their own status;
//   - credential evidence is REFERENCED (assessment template/session ids
//     into Inspections, opaque evidence handles), never stored, and audit
//     metadata carries only redacted free text (docs/PII-HANDLING.md).
const (
	// Audit actions written to the platform audit trail. One action per
	// outcome keeps the trail directly filterable; the denial action
	// records refused attempts (RBAC or self-grant).
	ActionTechnicianVerificationApproved = "identity.technician.verification.approved"
	ActionTechnicianVerificationRejected = "identity.technician.verification.rejected"
	ActionTechnicianVerificationDenied   = "identity.technician.verification.denied"

	// AuditObjectTypeTechnicianVerification is the audit object_type of a
	// verification decision; the object id is the decision record's id.
	AuditObjectTypeTechnicianVerification = "technician_verification"

	// VerificationStatusAuditObjectType is the audit object_type of a
	// refused verification attempt; the object id is the TARGETED
	// technician's user id — the attempt is about that account.
	VerificationStatusAuditObjectType = "user"
)

// Bounds for decision input (mirror the technician contract's caps). The
// reason is staff-authored free text; it is stored bounded in the decision
// record and REDACTED before entering audit metadata.
const (
	MaxVerificationReasonLength = 2000
	MaxVerificationEvidenceRefs = 20
	MaxVerificationRefLength    = 128
)

// VerificationDecision is one validated staff decision. Status is exactly
// VerificationStatusVerified (approve) or VerificationStatusRejected
// (reject); "unverified" and "pending" are lifecycle states that no HTTP
// decision writes today (pending is reserved for the assessment-scheduling
// flow) but that the column, the enum and the contract of record carry.
type VerificationDecision struct {
	Status               string
	Reason               string
	AssessmentTemplateID *uuid.UUID
	AssessmentSessionID  *uuid.UUID
	EvidenceRefs         []string
}

// VerificationActor is the authenticated principal performing (or
// attempting) a decision, resolved from the validated access-token claims —
// never from client input.
type VerificationActor struct {
	ID       uuid.UUID
	Role     Role
	TenantID *uuid.UUID
}

// validateDecision applies the request rules: a known decision status (only
// approve/reject are decidable today), a reason that is mandatory and
// non-empty for a rejection, and bounded evidence references.
func validateDecision(d VerificationDecision) error {
	if d.Status != VerificationStatusVerified && d.Status != VerificationStatusRejected {
		return platform.ErrValidation("decision status must be verified (approve) or rejected (reject)",
			platform.FieldError{Field: "status", Issue: "invalid"})
	}
	reason := strings.TrimSpace(d.Reason)
	if d.Status == VerificationStatusRejected && reason == "" {
		return platform.ErrValidation("a rejection requires a reason",
			platform.FieldError{Field: "reason", Issue: "required"})
	}
	if len(reason) > MaxVerificationReasonLength {
		return platform.ErrValidation("reason exceeds the length cap",
			platform.FieldError{Field: "reason", Issue: "too_long"})
	}
	if len(d.EvidenceRefs) > MaxVerificationEvidenceRefs {
		return platform.ErrValidation("too many evidence references",
			platform.FieldError{Field: "evidence_refs", Issue: "too_many"})
	}
	for i, ref := range d.EvidenceRefs {
		ref = strings.TrimSpace(ref)
		if ref == "" || len(ref) > MaxVerificationRefLength {
			return platform.ErrValidation("evidence references must be 1-128 characters",
				platform.FieldError{Field: fmt.Sprintf("evidence_refs[%d]", i), Issue: "invalid"})
		}
	}
	return nil
}

// SetTechnicianVerificationStatus records decision for technicianID and
// returns the appended decision record.
//
// Order of enforcement (each step audited where it carries an
// authorization signal):
//  1. input validation (422);
//  2. the acting principal must hold a technician-verification role —
//     otherwise a denial audit row is written and the request fails 403
//     (a failed denial-audit write fails the request outright);
//  3. separation of duties: the actor may never decide their own status
//     (403, audited like any other denial);
//  4. the target must exist AND hold the TECHNICIAN role — anything else
//     reads as 404 "technician not found" (no account-state disclosure);
//  5. the record + status stamp + audit entry commit in one transaction.
func (s *Service) SetTechnicianVerificationStatus(ctx context.Context, actor VerificationActor, technicianID uuid.UUID, decision VerificationDecision) (TechnicianVerification, error) {
	if err := validateDecision(decision); err != nil {
		return TechnicianVerification{}, err
	}
	if !CanVerifyTechnicians(actor.Role) {
		return TechnicianVerification{}, s.auditVerificationDenial(ctx, actor, technicianID, decision.Status,
			fmt.Sprintf("actor role %s may not record verification decisions", actor.Role))
	}
	if actor.ID == technicianID {
		return TechnicianVerification{}, s.auditVerificationDenial(ctx, actor, technicianID, decision.Status,
			"self-verification is not permitted (separation of duties)")
	}

	technician, err := s.store.GetUser(ctx, technicianID)
	if err != nil {
		if isNotFound(err) {
			return TechnicianVerification{}, platform.ErrNotFound("technician not found")
		}
		return TechnicianVerification{}, fmt.Errorf("load technician: %w", err)
	}
	if !hasRole(technician.Roles, RoleTechnician) {
		return TechnicianVerification{}, platform.ErrNotFound("technician not found")
	}

	metadata := map[string]any{
		"technician_id":   technicianID.String(),
		"status":          decision.Status,
		"reason":          platform.RedactFreeText(decision.Reason),
		"previous_status": verificationStatusOrNull(technician.VerificationStatus),
		"evidence_refs":   redactedEvidenceRefs(decision.EvidenceRefs),
	}
	if decision.AssessmentTemplateID != nil {
		metadata["assessment_template_id"] = decision.AssessmentTemplateID.String()
	}
	if decision.AssessmentSessionID != nil {
		metadata["assessment_session_id"] = decision.AssessmentSessionID.String()
	}
	if decision.Status == VerificationStatusVerified {
		metadata["dispatch_eligible"] = true
	}

	rec := TechnicianVerification{
		ID:                   uuid.New(),
		TechnicianID:         technicianID,
		Status:               decision.Status,
		Reason:               strings.TrimSpace(decision.Reason),
		ActorID:              actor.ID,
		AssessmentTemplateID: decision.AssessmentTemplateID,
		AssessmentSessionID:  decision.AssessmentSessionID,
		EvidenceRefs:         trimmedEvidenceRefs(decision.EvidenceRefs),
	}
	audit := AuditEntry{
		ActorID:    &actor.ID,
		Action:     ActionTechnicianVerificationApproved,
		ObjectType: AuditObjectTypeTechnicianVerification,
		ObjectID:   rec.ID.String(),
		TenantID:   actor.TenantID,
		Metadata:   metadata,
	}
	if decision.Status == VerificationStatusRejected {
		audit.Action = ActionTechnicianVerificationRejected
	}
	if err := s.store.InsertTechnicianVerification(ctx, &rec, audit); err != nil {
		return TechnicianVerification{}, fmt.Errorf("record technician verification: %w", err)
	}
	return rec, nil
}

// TechnicianVerificationStatus is the technician's own read model (GET
// /v1/technician/verification-status): the current lifecycle status plus
// the newest decision record when one exists. A technician who was never
// assessed reads as unverified with no record — the lifecycle's start
// state, and the value dispatch's verified-only gate fails closed on.
func (s *Service) TechnicianVerificationStatus(ctx context.Context, callerID uuid.UUID) (string, *TechnicianVerification, error) {
	caller, err := s.store.GetUser(ctx, callerID)
	if err != nil {
		if isNotFound(err) {
			return "", nil, platform.ErrNotFound("user not found")
		}
		return "", nil, fmt.Errorf("load caller: %w", err)
	}
	status := caller.VerificationStatus
	if status == "" {
		status = VerificationStatusUnverified
	}
	latest, found, err := s.store.LatestTechnicianVerification(ctx, callerID)
	if err != nil {
		return "", nil, fmt.Errorf("load technician verification: %w", err)
	}
	if !found {
		return status, nil, nil
	}
	return status, &latest, nil
}

// auditVerificationDenial records a refused decision attempt (issue #166
// QA: the 403 must leave an audit trail). The write is fail-closed: a
// denial whose audit row cannot be written fails the request entirely —
// an unrecordable authorization refusal is an infrastructure problem, not
// a permission answer.
func (s *Service) auditVerificationDenial(ctx context.Context, actor VerificationActor, technicianID uuid.UUID, attemptedStatus, detail string) error {
	entry := AuditEntry{
		ActorID:    &actor.ID,
		Action:     ActionTechnicianVerificationDenied,
		ObjectType: VerificationStatusAuditObjectType,
		ObjectID:   technicianID.String(),
		TenantID:   actor.TenantID,
		Metadata: map[string]any{
			"technician_id":    technicianID.String(),
			"attempted_status": attemptedStatus,
			"actor_role":       string(actor.Role),
			"detail":           platform.RedactFreeText(detail),
		},
	}
	if err := RecordAudit(ctx, s.store, entry); err != nil {
		return &platform.Error{
			Status: http.StatusInternalServerError,
			Code:   "audit_write_failed",
			Title:  "Audit write failed",
			Detail: "the verification attempt could not be audited; the request was refused",
		}
	}
	return platform.ErrForbidden("insufficient role for verification decisions")
}

// hasRole reports whether roles contains role.
func hasRole(roles []Role, role Role) bool {
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}

// verificationStatusOrNull projects the stored status for audit metadata:
// nil stays null (never assessed), everything else is the enum value.
func verificationStatusOrNull(status string) any {
	if status == "" {
		return nil
	}
	return status
}

// trimmedEvidenceRefs returns the trimmed, non-empty evidence references
// for storage in the decision record (validation already bounded them).
func trimmedEvidenceRefs(refs []string) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref = strings.TrimSpace(ref); ref != "" {
			out = append(out, ref)
		}
	}
	return out
}

// redactedEvidenceRefs projects the evidence references for audit
// metadata: handles are opaque, but redaction is free — nothing PII slips
// through a misused handle field (docs/PII-HANDLING.md).
func redactedEvidenceRefs(refs []string) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref = strings.TrimSpace(ref); ref != "" {
			out = append(out, platform.RedactFreeText(ref))
		}
	}
	return out
}

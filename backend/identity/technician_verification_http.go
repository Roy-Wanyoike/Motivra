package identity

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// HTTP surface for technician skill-verification (issue #166). The staff
// decision endpoints authenticate at the middleware (platform.
// RequireAuthenticated) and authorize in the SERVICE, so a refused attempt
// is auditable (403 + audit row, issue QA) — platform.RequireRole would
// reject before any audit writer could run. The self-read is scoped to the
// caller's own identity by construction: there is no technician id
// parameter to widen.

// technicianVerificationDecisionRequest is the POST body of the
// approve/reject endpoints. reason is mandatory for reject; the assessment
// and evidence fields are optional credential-evidence REFERENCES.
type technicianVerificationDecisionRequest struct {
	Reason               string   `json:"reason,omitempty"`
	AssessmentTemplateID string   `json:"assessment_template_id,omitempty"`
	AssessmentSessionID  string   `json:"assessment_session_id,omitempty"`
	EvidenceRefs         []string `json:"evidence_refs,omitempty"`
}

// technicianVerificationResponse projects one append-only decision record.
// Nullable references render as null; evidence_refs is always an array.
type technicianVerificationResponse struct {
	ID                   uuid.UUID  `json:"id"`
	TechnicianID         uuid.UUID  `json:"technician_id"`
	Status               string     `json:"status"`
	Reason               string     `json:"reason"`
	ActorID              uuid.UUID  `json:"actor_id"`
	AssessmentTemplateID *uuid.UUID `json:"assessment_template_id"`
	AssessmentSessionID  *uuid.UUID `json:"assessment_session_id"`
	EvidenceRefs         []string   `json:"evidence_refs"`
	CreatedAt            string     `json:"created_at"`
}

// newTechnicianVerificationResponse projects a decision record onto the
// wire shape pinned by contracts/identity/openapi.yaml.
func newTechnicianVerificationResponse(rec TechnicianVerification) technicianVerificationResponse {
	refs := rec.EvidenceRefs
	if refs == nil {
		refs = []string{}
	}
	return technicianVerificationResponse{
		ID:                   rec.ID,
		TechnicianID:         rec.TechnicianID,
		Status:               rec.Status,
		Reason:               rec.Reason,
		ActorID:              rec.ActorID,
		AssessmentTemplateID: rec.AssessmentTemplateID,
		AssessmentSessionID:  rec.AssessmentSessionID,
		EvidenceRefs:         refs,
		CreatedAt:            rec.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
	}
}

// verificationStatusResponse is the GET /v1/technician/verification-status
// body: the caller's current lifecycle status plus the newest decision
// record when one exists (updated_at and verification stay null while the
// technician was never assessed).
type verificationStatusResponse struct {
	TechnicianID uuid.UUID                       `json:"technician_id"`
	Status       string                          `json:"status"`
	UpdatedAt    *string                         `json:"updated_at"`
	Verification *technicianVerificationResponse `json:"verification"`
}

// handleTechnicianVerificationDecision builds the shared approve/reject
// handler: decide decides the outcome so the two endpoints stay
// byte-identical in behaviour and differ only in the recorded status.
func handleTechnicianVerificationDecision(svc *Service, status string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := platform.ClaimsFromContext(r.Context())
		if !ok {
			platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
			return
		}
		actorID, err := uuid.Parse(claims.UserID)
		if err != nil {
			platform.WriteError(w, platform.ErrUnauthorized("access token subject is invalid"))
			return
		}
		technicianID, err := uuid.Parse(chi.URLParam(r, "technicianID"))
		if err != nil {
			platform.WriteError(w, platform.ErrValidation("technicianID must be a UUID",
				platform.FieldError{Field: "technicianID", Issue: "invalid"}))
			return
		}
		req, err := platform.DecodeJSON[technicianVerificationDecisionRequest](r, maxAuthBodyBytes)
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		tenantID, err := tenantIDFromClaims(claims)
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		decision := VerificationDecision{Status: status, Reason: req.Reason}
		if req.AssessmentTemplateID != "" {
			templateID, parseErr := uuid.Parse(req.AssessmentTemplateID)
			if parseErr != nil {
				platform.WriteError(w, platform.ErrValidation("assessment_template_id must be a UUID",
					platform.FieldError{Field: "assessment_template_id", Issue: "invalid"}))
				return
			}
			decision.AssessmentTemplateID = &templateID
		}
		if req.AssessmentSessionID != "" {
			sessionID, parseErr := uuid.Parse(req.AssessmentSessionID)
			if parseErr != nil {
				platform.WriteError(w, platform.ErrValidation("assessment_session_id must be a UUID",
					platform.FieldError{Field: "assessment_session_id", Issue: "invalid"}))
				return
			}
			decision.AssessmentSessionID = &sessionID
		}
		decision.EvidenceRefs = req.EvidenceRefs

		rec, err := svc.SetTechnicianVerificationStatus(r.Context(), VerificationActor{
			ID:       actorID,
			Role:     Role(claims.Role),
			TenantID: tenantID,
		}, technicianID, decision)
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, newTechnicianVerificationResponse(rec))
	}
}

// handleTechnicianVerificationApprove records a VERIFIED outcome (201).
func handleTechnicianVerificationApprove(svc *Service) http.HandlerFunc {
	return handleTechnicianVerificationDecision(svc, VerificationStatusVerified)
}

// handleTechnicianVerificationReject records a REJECTED outcome (201); the
// service requires a non-empty reason for rejections.
func handleTechnicianVerificationReject(svc *Service) http.HandlerFunc {
	return handleTechnicianVerificationDecision(svc, VerificationStatusRejected)
}

// handleTechnicianVerificationStatus serves the caller's own verification
// status (200). The subject is always the authenticated principal.
func handleTechnicianVerificationStatus(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := platform.ClaimsFromContext(r.Context())
		if !ok {
			platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
			return
		}
		callerID, err := uuid.Parse(claims.UserID)
		if err != nil {
			platform.WriteError(w, platform.ErrUnauthorized("access token subject is invalid"))
			return
		}
		status, latest, err := svc.TechnicianVerificationStatus(r.Context(), callerID)
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		resp := verificationStatusResponse{
			TechnicianID: callerID,
			Status:       status,
		}
		if latest != nil {
			projected := newTechnicianVerificationResponse(*latest)
			resp.UpdatedAt = &projected.CreatedAt
			resp.Verification = &projected
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

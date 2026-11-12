package inspections

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// Report verification HTTP surface (issue #185):
//
//	POST /v1/inspections/sessions/{sessionID}/report/verification/outcome
//	     body: { outcome: "VERIFIED"|"REJECTED", note?, expected_report_version? }
//	     201 -> ReportVerificationRecord
//	     RBAC: GARAGE, ADMIN, SUPER_ADMIN only (TECHNICIAN and DISPATCHER are
//	     denied — the capturing inspector never verifies their own work;
//	     CUSTOMER is denied — separation of duties).
//	GET  /v1/inspections/sessions/{sessionID}/report/verification/records
//	     200 -> ReportVerificationRecord[] (append-only, oldest first)
//	     Authenticated + scoped like every report read path.
//
// RegisterReportVerificationRoutes is called from Routes; it lives here so
// the verification surface stays one reviewable unit.
func RegisterReportVerificationRoutes(r chi.Router, svc *Service, requireAuth func(http.HandlerFunc) http.HandlerFunc) {
	requireReviewer := func(h http.HandlerFunc) http.HandlerFunc {
		return requireAuth(platform.RequireRole(VerificationReviewerRoles, h))
	}
	r.Post("/report/verification/outcome", requireReviewer(svc.handleRecordVerificationOutcome))
	r.Get("/report/verification/records", requireAuth(svc.handleListVerificationRecords))
}

// VerificationReviewerRoles are the roles allowed to record a human report
// verification (issue #185): GARAGE, ADMIN, SUPER_ADMIN. TECHNICIAN and
// DISPATCHER are denied — the capturing inspector never verifies their own
// work (separation of duties; the Inspect console gate mirrors this server
// side, ADR-0004 defense in depth).
var VerificationReviewerRoles = []string{RoleGarage, RoleAdmin, RoleSuperAdmin}

// verificationOutcomeRequest is the POST .../verification/outcome body.
type verificationOutcomeRequest struct {
	Outcome               string `json:"outcome"`
	Note                  string `json:"note"`
	ExpectedReportVersion *int   `json:"expected_report_version"`
}

// verificationRecordResponse is the wire form of a verification record.
type verificationRecordResponse struct {
	ID                uuid.UUID `json:"id"`
	ReportID          uuid.UUID `json:"report_id"`
	SessionID         uuid.UUID `json:"session_id"`
	ReportVersion     int       `json:"report_version"`
	Outcome           string    `json:"outcome"`
	ReviewerID        uuid.UUID `json:"reviewer_id"`
	Note              string    `json:"note"`
	IntegrityVerified bool      `json:"integrity_verified"`
	CreatedAt         time.Time `json:"created_at"`
}

func newVerificationRecordResponse(rec ReportVerificationRecord) verificationRecordResponse {
	return verificationRecordResponse{
		ID:                rec.ID,
		ReportID:          rec.ReportID,
		SessionID:         rec.SessionID,
		ReportVersion:     rec.Version,
		Outcome:           string(rec.Outcome),
		ReviewerID:        rec.ReviewerID,
		Note:              rec.Note,
		IntegrityVerified: rec.IntegrityVerified,
		CreatedAt:         rec.CreatedAt,
	}
}

// handleRecordVerificationOutcome implements POST
// .../report/verification/outcome. The reviewer identity comes exclusively
// from the verified token — never from the body.
func (s *Service) handleRecordVerificationOutcome(w http.ResponseWriter, r *http.Request) {
	claims, present := platform.ClaimsFromContext(r.Context())
	if !present {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return
	}
	reviewerID, err := uuid.Parse(claims.UserID)
	if err != nil {
		platform.WriteError(w, platform.ErrUnauthorized("authenticated subject is not a user identifier"))
		return
	}
	scope, err := ScopeFromClaims(claims)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	sessionID, err := uuidFromRequest(r, "sessionID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	req, err := platform.DecodeJSON[verificationOutcomeRequest](r, maxRequestBytes)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	in := VerificationInput{
		Outcome:         VerificationOutcome(strings.ToUpper(strings.TrimSpace(req.Outcome))),
		Note:            req.Note,
		ExpectedVersion: req.ExpectedReportVersion,
	}
	rec, err := s.RecordVerificationOutcome(r.Context(), sessionID, reviewerID, in, scope)
	if err != nil {
		writeVerificationError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newVerificationRecordResponse(rec))
}

// handleListVerificationRecords implements GET
// .../report/verification/records.
func (s *Service) handleListVerificationRecords(w http.ResponseWriter, r *http.Request) {
	scope, ok := scopeFromRequest(r)
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return
	}
	sessionID, err := uuidFromRequest(r, "sessionID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	records, err := s.VerificationRecords(r.Context(), sessionID, scope)
	if err != nil {
		writeVerificationError(w, err)
		return
	}
	out := make([]verificationRecordResponse, 0, len(records))
	for _, rec := range records {
		out = append(out, newVerificationRecordResponse(rec))
	}
	writeJSON(w, http.StatusOK, out)
}

// writeVerificationError maps verification domain errors onto platform API
// errors. Unknown errors fall through to platform.WriteError (500 without
// internals).
func writeVerificationError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, ErrSelfVerification):
		platform.WriteError(w, platform.ErrForbidden("the capturing inspector may not verify their own inspection"))
	case errors.Is(err, ErrConflict):
		platform.WriteError(w, platform.ErrConflict(err.Error()))
	case errors.Is(err, ErrSessionNotFound):
		platform.WriteError(w, platform.ErrNotFound("session not found"))
	case errors.Is(err, ErrReportNotFound):
		platform.WriteError(w, platform.ErrNotFound("report not found"))
	case errors.Is(err, ErrVerificationNotFound):
		platform.WriteError(w, platform.ErrNotFound("verification records not found"))
	case errors.Is(err, ErrInvalidInput):
		platform.WriteError(w, platform.ErrValidation(err.Error()))
	default:
		platform.WriteError(w, err)
	}
}

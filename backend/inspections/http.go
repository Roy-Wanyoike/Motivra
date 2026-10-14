package inspections

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// maxRequestBytes caps JSON request bodies accepted by the handlers.
const maxRequestBytes int64 = 64 << 10

// Routes registers the Inspections HTTP API on r:
//
//	POST /v1/inspections/sessions                create a session (staff roles)
//	GET  /v1/inspections/sessions                list sessions (staff roles)
//	GET  /v1/inspections/sessions/{sessionID}    fetch one session (authenticated, scoped)
//	POST /v1/inspections/sessions/{sessionID}/start        start the walk (staff roles)
//	POST /v1/inspections/sessions/{sessionID}/findings     record a finding (staff roles)
//	GET  /v1/inspections/sessions/{sessionID}/findings     list findings (authenticated, scoped)
//	POST /v1/inspections/sessions/{sessionID}/evidence     attach evidence reference (staff roles)
//	GET  /v1/inspections/sessions/{sessionID}/evidence     list evidence (authenticated, scoped)
//	POST /v1/inspections/sessions/{sessionID}/submit       submit + seal (staff roles)
//	POST /v1/inspections/sessions/{sessionID}/report       assemble + issue the report (staff roles)
//	GET  /v1/inspections/sessions/{sessionID}/report       fetch the report (authenticated, scoped)
//	GET  /v1/inspections/sessions/{sessionID}/report/verification  recompute report integrity
//	GET  /v1/inspections/templates                   list the system template registry
//	GET  /v1/inspections/templates/{templateID}      fetch one registered template
//
// Every read is scoped by Scope, derived exclusively from the validated
// access-token claims (never from client input). Invisible and unknown
// resources both answer 404, so responses leak no existence information
// (ADR-0004 cross-tenant denial). Mutation routes are additionally gated to
// the staff roles (platform.RequireRole); the actor guard inside the
// service keeps even staff members from working sessions they do not
// inspect (operators excepted, by design).
func Routes(r chi.Router, svc *Service, requireAuth func(http.HandlerFunc) http.HandlerFunc) {
	requireStaff := func(h http.HandlerFunc) http.HandlerFunc {
		return requireAuth(platform.RequireRole(StaffRoles, h))
	}
	r.Route("/v1/inspections/sessions", func(r chi.Router) {
		r.Post("/", requireStaff(svc.handleCreateSession))
		r.Get("/", requireStaff(svc.handleListSessions))
		r.Route("/{sessionID}", func(r chi.Router) {
			r.Get("/", requireAuth(svc.handleGetSession))
			r.Post("/start", requireStaff(svc.handleStart))
			r.Post("/findings", requireStaff(svc.handleRecordFinding))
			r.Get("/findings", requireAuth(svc.handleListFindings))
			r.Post("/evidence", requireStaff(svc.handleAttachEvidence))
			r.Get("/evidence", requireAuth(svc.handleListEvidence))
			r.Post("/submit", requireStaff(svc.handleSubmit))
			r.Post("/report", requireStaff(svc.handleIssueReport))
			r.Get("/report", requireAuth(svc.handleGetReport))
			r.Get("/report/verification", requireAuth(svc.handleVerifyReport))
		})
	})
	r.Route("/v1/inspections/templates", func(r chi.Router) {
		r.Get("/", requireAuth(svc.handleListTemplates))
		r.Get("/{templateID}", requireAuth(svc.handleGetTemplate))
	})
}

// createSessionRequest is the POST /v1/inspections/sessions body.
type createSessionRequest struct {
	VehicleID    *uuid.UUID   `json:"vehicle_id"`
	JobID        *uuid.UUID   `json:"job_id"`
	CustomerID   *uuid.UUID   `json:"customer_id"`
	VehicleClass VehicleClass `json:"vehicle_class"`
}

// findingRequest is the POST .../findings body.
type findingRequest struct {
	ItemKey  string `json:"item_key"`
	Result   string `json:"result"`
	Severity string `json:"severity"`
	Notes    string `json:"notes"`
}

// evidenceRequest is the POST .../evidence body.
type evidenceRequest struct {
	FindingID   *uuid.UUID `json:"finding_id,omitempty"`
	Kind        string     `json:"kind"`
	ContentHash string     `json:"content_hash"`
	CapturedAt  time.Time  `json:"captured_at"`
	Uploaded    bool       `json:"uploaded"`
	Notes       string     `json:"notes"`
}

// issueReportRequest is the POST .../report body.
type issueReportRequest struct {
	Notes string `json:"notes"`
}

// scopeFromRequest derives the caller's Scope from the validated access
// token in the request context — never from any client-supplied field.
// ok is false when no usable authenticated principal is present.
func scopeFromRequest(r *http.Request) (scope Scope, ok bool) {
	claims, present := platform.ClaimsFromContext(r.Context())
	if !present {
		return Scope{}, false
	}
	scope, err := ScopeFromClaims(claims)
	if err != nil {
		return Scope{}, false
	}
	return scope, true
}

// sessionResponse is the wire form of an inspection session.
type sessionResponse struct {
	ID              uuid.UUID  `json:"id"`
	TenantID        *uuid.UUID `json:"tenant_id,omitempty"`
	VehicleID       *uuid.UUID `json:"vehicle_id,omitempty"`
	JobID           *uuid.UUID `json:"job_id,omitempty"`
	CustomerID      *uuid.UUID `json:"customer_id,omitempty"`
	VehicleClass    string     `json:"vehicle_class"`
	TemplateID      uuid.UUID  `json:"template_id"`
	TemplateName    string     `json:"template_name"`
	TemplateVersion int        `json:"template_version"`
	InspectorID     uuid.UUID  `json:"inspector_id"`
	Status          Status     `json:"status"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	SubmittedAt     *time.Time `json:"submitted_at,omitempty"`
	ReportedAt      *time.Time `json:"reported_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// findingResponse is the wire form of a finding.
type findingResponse struct {
	ID         uuid.UUID  `json:"id"`
	SessionID  uuid.UUID  `json:"session_id"`
	ItemKey    string     `json:"item_key"`
	Result     string     `json:"result"`
	Severity   string     `json:"severity"`
	Notes      string     `json:"notes"`
	RecordedBy *uuid.UUID `json:"recorded_by,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// evidenceResponse is the wire form of an evidence record.
type evidenceResponse struct {
	ID          uuid.UUID  `json:"id"`
	SessionID   uuid.UUID  `json:"session_id"`
	FindingID   *uuid.UUID `json:"finding_id,omitempty"`
	Kind        string     `json:"kind"`
	ContentHash string     `json:"content_hash"`
	CapturedAt  time.Time  `json:"captured_at"`
	CapturedBy  *uuid.UUID `json:"captured_by,omitempty"`
	Uploaded    bool       `json:"uploaded"`
	Notes       string     `json:"notes"`
	CreatedAt   time.Time  `json:"created_at"`
}

// reportResponse is the wire form of an issued report. The snapshots are
// decoded to their typed arrays so clients receive readable documents (the
// report record stores them as raw JSON).
type reportResponse struct {
	ID              uuid.UUID          `json:"id"`
	SessionID       uuid.UUID          `json:"session_id"`
	Version         int                `json:"version"`
	VehicleID       *uuid.UUID         `json:"vehicle_id,omitempty"`
	JobID           *uuid.UUID         `json:"job_id,omitempty"`
	InspectorID     uuid.UUID          `json:"inspector_id"`
	TemplateID      uuid.UUID          `json:"template_id"`
	TemplateName    string             `json:"template_name"`
	TemplateVersion int                `json:"template_version"`
	Findings        []findingSnapshot  `json:"findings"`
	Evidence        []evidenceSnapshot `json:"evidence"`
	FindingsCount   int                `json:"findings_count"`
	EvidenceCount   int                `json:"evidence_count"`
	ContentHash     string             `json:"content_hash"`
	IssuedBy        *uuid.UUID         `json:"issued_by,omitempty"`
	Notes           string             `json:"notes"`
	CreatedAt       time.Time          `json:"created_at"`
}

// handleCreateSession implements POST /v1/inspections/sessions. The
// authenticated staff member becomes the inspector; the tenant comes from
// the claims — never from the body.
func (s *Service) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	claims, ok := platform.ClaimsFromContext(r.Context())
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return
	}
	inspectorID, err := uuid.Parse(claims.UserID)
	if err != nil {
		platform.WriteError(w, platform.ErrUnauthorized("authenticated subject is not a user identifier"))
		return
	}
	req, err := platform.DecodeJSON[createSessionRequest](r, maxRequestBytes)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	if !IsVehicleClass(req.VehicleClass) {
		platform.WriteError(w, platform.ErrValidation("vehicle_class is not a known vehicle class",
			platform.FieldError{Field: "vehicle_class", Issue: "must be one of the documented vehicle classes"}))
		return
	}

	sess := &Session{
		TenantID:     tenantIDFromClaims(claims),
		VehicleID:    req.VehicleID,
		JobID:        req.JobID,
		CustomerID:   req.CustomerID,
		VehicleClass: req.VehicleClass,
		InspectorID:  inspectorID,
		Status:       StatusCreated,
	}
	if err := s.CreateSession(r.Context(), sess); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newSessionResponse(sess))
}

// handleListSessions implements GET /v1/inspections/sessions with optional
// ?status=, ?vehicle_id=, ?job_id=, ?limit=, ?offset= filters.
func (s *Service) handleListSessions(w http.ResponseWriter, r *http.Request) {
	scope, ok := scopeFromRequest(r)
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return
	}
	query := r.URL.Query()

	var filter SessionFilter
	if raw := strings.TrimSpace(query.Get("status")); raw != "" {
		status, err := parseStatus(raw)
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		filter.Status = &status
	}
	if raw := strings.TrimSpace(query.Get("vehicle_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			platform.WriteError(w, platform.ErrValidation("vehicle_id is not a valid identifier",
				platform.FieldError{Field: "vehicle_id", Issue: "must be a uuid"}))
			return
		}
		filter.VehicleID = &id
	}
	if raw := strings.TrimSpace(query.Get("job_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			platform.WriteError(w, platform.ErrValidation("job_id is not a valid identifier",
				platform.FieldError{Field: "job_id", Issue: "must be a uuid"}))
			return
		}
		filter.JobID = &id
	}
	limit, offset, err := listPagination(r)
	if err != nil {
		platform.WriteError(w, err)
		return
	}

	sessions, err := s.Sessions(r.Context(), scope, filter, limit, offset)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	out := make([]sessionResponse, 0, len(sessions))
	for i := range sessions {
		out = append(out, newSessionResponse(&sessions[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetSession implements GET /v1/inspections/sessions/{sessionID}.
func (s *Service) handleGetSession(w http.ResponseWriter, r *http.Request) {
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
	sess, err := s.GetSession(r.Context(), sessionID, scope)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newSessionResponse(&sess))
}

// handleStart implements POST .../start.
func (s *Service) handleStart(w http.ResponseWriter, r *http.Request) {
	actorID, scope, ok := actorAndScope(w, r)
	if !ok {
		return
	}
	sessionID, err := uuidFromRequest(r, "sessionID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	sess, err := s.Start(r.Context(), sessionID, actorID, scope)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newSessionResponse(&sess))
}

// handleRecordFinding implements POST .../findings.
func (s *Service) handleRecordFinding(w http.ResponseWriter, r *http.Request) {
	actorID, scope, ok := actorAndScope(w, r)
	if !ok {
		return
	}
	sessionID, err := uuidFromRequest(r, "sessionID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	req, err := platform.DecodeJSON[findingRequest](r, maxRequestBytes)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	finding, err := s.RecordFinding(r.Context(), sessionID, &Finding{
		ItemKey:  req.ItemKey,
		Result:   FindingResult(req.Result),
		Severity: Severity(req.Severity),
		Notes:    req.Notes,
	}, actorID, scope)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newFindingResponse(&finding))
}

// handleListFindings implements GET .../findings.
func (s *Service) handleListFindings(w http.ResponseWriter, r *http.Request) {
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
	findings, err := s.Findings(r.Context(), sessionID, scope)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	out := make([]findingResponse, 0, len(findings))
	for i := range findings {
		out = append(out, newFindingResponse(&findings[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAttachEvidence implements POST .../evidence (the evidence
// *reference*: hash + provenance; the media itself is out of scope).
func (s *Service) handleAttachEvidence(w http.ResponseWriter, r *http.Request) {
	actorID, scope, ok := actorAndScope(w, r)
	if !ok {
		return
	}
	sessionID, err := uuidFromRequest(r, "sessionID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	req, err := platform.DecodeJSON[evidenceRequest](r, maxRequestBytes)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	evidence, err := s.AttachEvidence(r.Context(), sessionID, &Evidence{
		FindingID:   req.FindingID,
		Kind:        EvidenceKind(req.Kind),
		ContentHash: req.ContentHash,
		CapturedAt:  req.CapturedAt,
		Uploaded:    req.Uploaded,
		Notes:       req.Notes,
	}, actorID, scope)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newEvidenceResponse(&evidence))
}

// handleListEvidence implements GET .../evidence.
func (s *Service) handleListEvidence(w http.ResponseWriter, r *http.Request) {
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
	evidence, err := s.Evidence(r.Context(), sessionID, scope)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	out := make([]evidenceResponse, 0, len(evidence))
	for i := range evidence {
		out = append(out, newEvidenceResponse(&evidence[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSubmit implements POST .../submit.
func (s *Service) handleSubmit(w http.ResponseWriter, r *http.Request) {
	actorID, scope, ok := actorAndScope(w, r)
	if !ok {
		return
	}
	sessionID, err := uuidFromRequest(r, "sessionID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	sess, err := s.Submit(r.Context(), sessionID, actorID, scope)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newSessionResponse(&sess))
}

// handleIssueReport implements POST .../report. The request body is
// optional (an empty body issues the report with no notes), so a bodyless
// POST reaches the state machine instead of failing JSON decoding — the
// 409 of an illegal move must not be masked by a 422.
func (s *Service) handleIssueReport(w http.ResponseWriter, r *http.Request) {
	actorID, scope, ok := actorAndScope(w, r)
	if !ok {
		return
	}
	sessionID, err := uuidFromRequest(r, "sessionID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	req, err := decodeOptionalJSON[issueReportRequest](r, maxRequestBytes)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	report, err := s.IssueReport(r.Context(), sessionID, actorID, req.Notes, scope)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newReportResponse(&report))
}

// handleGetReport implements GET .../report with optional ?version=.
func (s *Service) handleGetReport(w http.ResponseWriter, r *http.Request) {
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
	version, err := optionalVersion(r)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	report, err := s.GetReport(r.Context(), sessionID, version, scope)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newReportResponse(&report))
}

// handleVerifyReport implements GET .../report/verification with optional
// ?version=. Integrity drift answers 200 with verified=false — the
// endpoint always works; the *report* is what fails the check.
func (s *Service) handleVerifyReport(w http.ResponseWriter, r *http.Request) {
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
	version, err := optionalVersion(r)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	verification, err := s.Verify(r.Context(), sessionID, version, scope)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, verification)
}

// handleListTemplates implements GET /v1/inspections/templates with an
// optional ?vehicle_class= filter.
func (s *Service) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	templates := SystemTemplates()
	if raw := strings.TrimSpace(r.URL.Query().Get("vehicle_class")); raw != "" {
		class := VehicleClass(strings.ToUpper(raw))
		if !IsVehicleClass(class) {
			platform.WriteError(w, platform.ErrValidation("vehicle_class is not a known vehicle class",
				platform.FieldError{Field: "vehicle_class", Issue: "must be one of the documented vehicle classes"}))
			return
		}
		filtered := make([]Template, 0, 1)
		for _, t := range templates {
			if t.VehicleClass == class {
				filtered = append(filtered, t)
			}
		}
		templates = filtered
	}
	writeJSON(w, http.StatusOK, templates)
}

// handleGetTemplate implements GET /v1/inspections/templates/{templateID}.
func (s *Service) handleGetTemplate(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(chi.URLParam(r, "templateID"))
	if raw == "" {
		platform.WriteError(w, platform.ErrValidation("template id is required",
			platform.FieldError{Field: "templateID", Issue: "is required"}))
		return
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		platform.WriteError(w, platform.ErrValidation("template id is not a valid identifier",
			platform.FieldError{Field: "templateID", Issue: "must be a uuid"}))
		return
	}
	template, err := TemplateByID(id)
	if err != nil {
		platform.WriteError(w, platform.ErrNotFound("template not found"))
		return
	}
	writeJSON(w, http.StatusOK, template)
}

// actorAndScope extracts the authenticated actor and scope shared by the
// mutation handlers, writing the error response itself when extraction
// fails. It is only called from http.HandlerFunc contexts (the w required
// for that lives on the caller).
func actorAndScope(w http.ResponseWriter, r *http.Request) (actorID uuid.UUID, scope Scope, ok bool) {
	claims, present := platform.ClaimsFromContext(r.Context())
	if !present {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return uuid.Nil, Scope{}, false
	}
	actorID, err := uuid.Parse(claims.UserID)
	if err != nil {
		platform.WriteError(w, platform.ErrUnauthorized("authenticated subject is not a user identifier"))
		return uuid.Nil, Scope{}, false
	}
	scope, err = ScopeFromClaims(claims)
	if err != nil {
		platform.WriteError(w, err)
		return uuid.Nil, Scope{}, false
	}
	return actorID, scope, true
}

// tenantIDFromClaims renders the claims' tenant as a uuid pointer, or nil
// when the claims carry no tenant (personal accounts).
func tenantIDFromClaims(claims platform.Claims) *uuid.UUID {
	if claims.TenantID == "" {
		return nil
	}
	id, err := uuid.Parse(claims.TenantID)
	if err != nil {
		return nil
	}
	return &id
}

// parseStatus validates a raw status string against the known 4-status
// enumeration.
func parseStatus(raw string) (Status, error) {
	candidate := Status(strings.ToUpper(strings.TrimSpace(raw)))
	if isKnownStatus(candidate) {
		return candidate, nil
	}
	return "", platform.ErrValidation("status is not a known inspection session status",
		platform.FieldError{Field: "status", Issue: "must be one of the documented session statuses"})
}

// uuidFromRequest parses the named path parameter as a UUID.
func uuidFromRequest(r *http.Request, name string) (uuid.UUID, error) {
	raw := strings.TrimSpace(chi.URLParam(r, name))
	if raw == "" {
		return uuid.Nil, platform.ErrValidation("resource id is required",
			platform.FieldError{Field: name, Issue: "is required"})
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, platform.ErrValidation("resource id is not a valid identifier",
			platform.FieldError{Field: name, Issue: "must be a uuid"})
	}
	return id, nil
}

// optionalVersion parses ?version= (absent -> nil = latest).
func optionalVersion(r *http.Request) (*int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("version"))
	if raw == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return nil, platform.ErrValidation("version is not a valid report version",
			platform.FieldError{Field: "version", Issue: "must be a positive integer"})
	}
	return &n, nil
}

// listPagination parses ?limit and ?offset. Defaults and the cap mirror
// Store.ListSessions semantics.
func listPagination(r *http.Request) (limit int, offset int, err error) {
	query := r.URL.Query()
	limit = DefaultSessionsLimit
	offset = 0
	if raw := query.Get("limit"); raw != "" {
		n, parseErr := strconv.Atoi(raw)
		if parseErr != nil {
			return 0, 0, platform.ErrValidation("limit is not a number",
				platform.FieldError{Field: "limit", Issue: "must be an integer"})
		}
		limit = n
	}
	if raw := query.Get("offset"); raw != "" {
		n, parseErr := strconv.Atoi(raw)
		if parseErr != nil {
			return 0, 0, platform.ErrValidation("offset is not a number",
				platform.FieldError{Field: "offset", Issue: "must be an integer"})
		}
		offset = n
	}
	if limit < 1 {
		return 0, 0, platform.ErrValidation("limit must be positive",
			platform.FieldError{Field: "limit", Issue: "must be at least 1"})
	}
	if limit > MaxSessionsLimit {
		limit = MaxSessionsLimit
	}
	if offset < 0 {
		return 0, 0, platform.ErrValidation("offset must be non-negative",
			platform.FieldError{Field: "offset", Issue: "must be at least 0"})
	}
	return limit, offset, nil
}

// writeDomainError maps inspections domain errors onto platform API
// errors. Anything unrecognized falls through to platform.WriteError,
// which renders unknown errors as a 500 without leaking internals.
func writeDomainError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, ErrConflict):
		platform.WriteError(w, platform.ErrConflict(err.Error()))
	case errors.Is(err, ErrSessionSealed), errors.Is(err, ErrReportIncomplete), errors.Is(err, ErrInvalidInput):
		platform.WriteError(w, platform.ErrValidation(err.Error()))
	case errors.Is(err, ErrNotInspector):
		platform.WriteError(w, platform.ErrForbidden("only the session's inspector may perform this action"))
	case errors.Is(err, ErrSessionNotFound):
		platform.WriteError(w, platform.ErrNotFound("session not found"))
	case errors.Is(err, ErrReportNotFound):
		platform.WriteError(w, platform.ErrNotFound("report not found"))
	default:
		platform.WriteError(w, err)
	}
}

func newSessionResponse(s *Session) sessionResponse {
	return sessionResponse{
		ID:              s.ID,
		TenantID:        s.TenantID,
		VehicleID:       s.VehicleID,
		JobID:           s.JobID,
		CustomerID:      s.CustomerID,
		VehicleClass:    string(s.VehicleClass),
		TemplateID:      s.TemplateID,
		TemplateName:    s.TemplateName,
		TemplateVersion: s.TemplateVersion,
		InspectorID:     s.InspectorID,
		Status:          s.Status,
		StartedAt:       s.StartedAt,
		SubmittedAt:     s.SubmittedAt,
		ReportedAt:      s.ReportedAt,
		CreatedAt:       s.CreatedAt,
		UpdatedAt:       s.UpdatedAt,
	}
}

func newFindingResponse(f *Finding) findingResponse {
	return findingResponse{
		ID:         f.ID,
		SessionID:  f.SessionID,
		ItemKey:    f.ItemKey,
		Result:     string(f.Result),
		Severity:   string(f.Severity),
		Notes:      f.Notes,
		RecordedBy: f.RecordedBy,
		CreatedAt:  f.CreatedAt,
	}
}

func newEvidenceResponse(e *Evidence) evidenceResponse {
	return evidenceResponse{
		ID:          e.ID,
		SessionID:   e.SessionID,
		FindingID:   e.FindingID,
		Kind:        string(e.Kind),
		ContentHash: e.ContentHash,
		CapturedAt:  e.CapturedAt,
		CapturedBy:  e.CapturedBy,
		Uploaded:    e.Uploaded,
		Notes:       e.Notes,
		CreatedAt:   e.CreatedAt,
	}
}

func newReportResponse(r *Report) reportResponse {
	var findings []findingSnapshot
	var evidence []evidenceSnapshot
	// The snapshots were frozen by this service; malformed stored JSON is
	// a corruption signal, rendered as empty arrays by the decode fallback.
	_ = json.Unmarshal(r.FindingsSnapshot, &findings)
	_ = json.Unmarshal(r.EvidenceSnapshot, &evidence)
	if findings == nil {
		findings = []findingSnapshot{}
	}
	if evidence == nil {
		evidence = []evidenceSnapshot{}
	}
	return reportResponse{
		ID:              r.ID,
		SessionID:       r.SessionID,
		Version:         r.Version,
		VehicleID:       r.VehicleID,
		JobID:           r.JobID,
		InspectorID:     r.InspectorID,
		TemplateID:      r.TemplateID,
		TemplateName:    r.TemplateName,
		TemplateVersion: r.TemplateVersion,
		Findings:        findings,
		Evidence:        evidence,
		FindingsCount:   r.FindingsCount,
		EvidenceCount:   r.EvidenceCount,
		ContentHash:     r.ContentHash,
		IssuedBy:        r.IssuedBy,
		Notes:           r.Notes,
		CreatedAt:       r.CreatedAt,
	}
}

// writeJSON renders body as a JSON response with the given status.
// decodeOptionalJSON decodes an optional JSON request body: an absent or
// whitespace-only body yields the zero value, a present body must be valid
// JSON within maxBytes, and anything else is a platform validation error.
// Used where the request semantics make the body optional (report issuance
// carries no required fields).
func decodeOptionalJSON[T any](r *http.Request, maxBytes int64) (T, error) {
	var out T
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxBytes))
	if err != nil {
		return out, platform.ErrValidation("request body exceeds the size limit or could not be read")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, platform.ErrValidation("request body is not valid JSON: " + err.Error())
	}
	return out, nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

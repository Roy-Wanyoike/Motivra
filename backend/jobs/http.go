package jobs

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// RBAC vocabulary for the jobs domain (ADR-0004). Customers create
// requests and jobs; dispatcher/admin roles move and assign jobs;
// technicians accept their assignments.
const (
	RoleCustomer   = "CUSTOMER"
	RoleDispatcher = "DISPATCHER"
	RoleTechnician = "TECHNICIAN"
	RoleAdmin      = "ADMIN"
	RoleSuperAdmin = "SUPER_ADMIN"
)

// dispatchRoles are the roles allowed to move jobs through the state
// machine and to assign technicians: the platform-operator set, which also
// defines the operator read scope.
var dispatchRoles = OperatorRoles

// maxRequestBytes caps JSON request bodies accepted by the handlers.
const maxRequestBytes int64 = 64 << 10

// Routes registers the Jobs HTTP API on r:
//
//	POST /v1/requests                        create a service request (any authenticated user)
//	POST /v1/requests/{requestID}/job        convert a request into a job (its customer or an operator)
//	GET  /v1/jobs?status=&limit=&offset      list jobs by status (DISPATCHER/ADMIN/SUPER_ADMIN)
//	GET  /v1/jobs/{jobID}                    fetch one job (its customer, the assigned technician, or an operator)
//	POST /v1/jobs/{jobID}/transitions        move a job through the machine (DISPATCHER/ADMIN/SUPER_ADMIN)
//	GET  /v1/jobs/{jobID}/transitions        read the transition audit trail (same visibility as fetching the job)
//	POST /v1/jobs/{jobID}/assignments        dispatch a technician (DISPATCHER/ADMIN/SUPER_ADMIN)
//	POST /v1/jobs/{jobID}/accept             technician accepts the assignment (TECHNICIAN)
//
// Every read is scoped by ReadScope, derived exclusively from the validated
// access-token claims (never from client input). Invisible and unknown
// resources both answer 404, so responses leak no existence information
// (ADR-0004 cross-tenant denial).
//
// requireAuth wraps every handler with the platform authentication gate
// (pass platform.RequireAuthenticated). Role-gated routes are wrapped with
// platform.RequireRole inside the route group. The AuthMiddleware must
// already be part of the router middleware chain so ClaimsFromContext
// resolves.
func Routes(r chi.Router, svc *Service, requireAuth func(http.HandlerFunc) http.HandlerFunc) {
	r.Route("/v1/requests", func(r chi.Router) {
		r.Post("/", requireAuth(svc.handleCreateRequest))
		r.Route("/{requestID}", func(r chi.Router) {
			r.Post("/job", requireAuth(svc.handleCreateJobFromRequest))
		})
	})
	r.Route("/v1/jobs", func(r chi.Router) {
		r.Get("/", platform.RequireRole(dispatchRoles, svc.handleListJobs))
		r.Route("/{jobID}", func(r chi.Router) {
			r.Get("/", requireAuth(svc.handleGetJob))
			r.Get("/transitions", requireAuth(svc.handleListTransitions))
			r.Post("/transitions", platform.RequireRole(dispatchRoles, svc.handleTransition))
			r.Post("/assignments", platform.RequireRole(dispatchRoles, svc.handleAssign))
			r.Post("/accept", platform.RequireRole([]string{RoleTechnician}, svc.handleAccept))
		})
	})
}

// createRequestRequest is the POST /v1/requests body.
type createRequestRequest struct {
	VehicleID   *uuid.UUID `json:"vehicle_id"`
	Description string     `json:"description"`
	LocationLat *float64   `json:"location_lat"`
	LocationLng *float64   `json:"location_lng"`
	AddressText string     `json:"address_text"`
}

// transitionRequest is the POST /v1/jobs/{jobID}/transitions body.
type transitionRequest struct {
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// assignmentRequest is the POST /v1/jobs/{jobID}/assignments body.
type assignmentRequest struct {
	TechnicianID *uuid.UUID `json:"technician_id"`
}

// readScope derives the caller's ReadScope from the validated access token
// in the request context — never from any client-supplied field. The viewer
// is the token subject; platform-operator roles (dispatchRoles) read across
// resources. ok is false when no usable authenticated principal is present.
func readScope(r *http.Request) (scope ReadScope, ok bool) {
	claims, present := platform.ClaimsFromContext(r.Context())
	if !present {
		return ReadScope{}, false
	}
	viewer, err := uuid.Parse(claims.UserID)
	if err != nil {
		return ReadScope{}, false
	}
	return ReadScope{ViewerID: viewer, Operator: IsOperatorRole(claims.Role)}, true
}

// requestResponse is the wire form of a service request.
type requestResponse struct {
	ID          uuid.UUID  `json:"id"`
	CustomerID  uuid.UUID  `json:"customer_id"`
	VehicleID   *uuid.UUID `json:"vehicle_id,omitempty"`
	Description string     `json:"description"`
	LocationLat *float64   `json:"location_lat,omitempty"`
	LocationLng *float64   `json:"location_lng,omitempty"`
	AddressText string     `json:"address_text"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// jobResponse is the wire form of a job.
type jobResponse struct {
	ID               uuid.UUID  `json:"id"`
	ServiceRequestID *uuid.UUID `json:"service_request_id,omitempty"`
	CustomerID       uuid.UUID  `json:"customer_id"`
	VehicleID        *uuid.UUID `json:"vehicle_id,omitempty"`
	Status           Status     `json:"status"`
	ProblemSummary   string     `json:"problem_summary"`
	TechnicianID     *uuid.UUID `json:"technician_id,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// transitionResponse is the wire form of one audit row.
type transitionResponse struct {
	ID         int64      `json:"id"`
	JobID      uuid.UUID  `json:"job_id"`
	FromStatus Status     `json:"from_status"`
	ToStatus   Status     `json:"to_status"`
	ActorID    *uuid.UUID `json:"actor_id,omitempty"`
	Reason     string     `json:"reason"`
	CreatedAt  time.Time  `json:"created_at"`
}

// handleCreateRequest implements POST /v1/requests. The authenticated user
// becomes the customer.
func (s *Service) handleCreateRequest(w http.ResponseWriter, r *http.Request) {
	claims, ok := platform.ClaimsFromContext(r.Context())
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return
	}
	customerID, err := uuid.Parse(claims.UserID)
	if err != nil {
		platform.WriteError(w, platform.ErrUnauthorized("authenticated subject is not a user identifier"))
		return
	}
	req, err := platform.DecodeJSON[createRequestRequest](r, maxRequestBytes)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	if strings.TrimSpace(req.Description) == "" {
		platform.WriteError(w, platform.ErrValidation("description is required",
			platform.FieldError{Field: "description", Issue: "is required"}))
		return
	}
	if !LocationValid(req.LocationLat, req.LocationLng) {
		platform.WriteError(w, platform.ErrValidation(
			"location_lat and location_lng must be provided together and within WGS-84 bounds",
			platform.FieldError{Field: "location", Issue: "must be a paired, in-bounds coordinate"}))
		return
	}

	sr := &ServiceRequest{
		CustomerID:  customerID,
		VehicleID:   req.VehicleID,
		Description: req.Description,
		LocationLat: req.LocationLat,
		LocationLng: req.LocationLng,
		AddressText: req.AddressText,
		Status:      RequestStatusReceived,
	}
	if err := s.CreateRequest(r.Context(), sr); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newRequestResponse(sr))
}

// handleCreateJobFromRequest implements POST /v1/requests/{requestID}/job.
// Only the request's customer or an operator can convert it; anyone else
// gets the same 404 as an unknown request.
func (s *Service) handleCreateJobFromRequest(w http.ResponseWriter, r *http.Request) {
	scope, ok := readScope(r)
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return
	}
	requestID, err := uuidFromRequest(r, "requestID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	job, err := s.CreateJobFromRequest(r.Context(), requestID, scope)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newJobResponse(&job))
}

// handleGetJob implements GET /v1/jobs/{jobID}. The job is read through
// the caller's scope: a foreign job answers the same 404 as a missing one.
func (s *Service) handleGetJob(w http.ResponseWriter, r *http.Request) {
	scope, ok := readScope(r)
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return
	}
	jobID, err := uuidFromRequest(r, "jobID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	job, err := s.GetJob(r.Context(), jobID, scope)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newJobResponse(&job))
}

// handleTransition implements POST /v1/jobs/{jobID}/transitions. Only
// dispatcher/admin roles reach this handler (RequireRole in the route
// group); the state machine itself remains the final authority.
func (s *Service) handleTransition(w http.ResponseWriter, r *http.Request) {
	claims, ok := platform.ClaimsFromContext(r.Context())
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return
	}
	jobID, err := uuidFromRequest(r, "jobID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	req, err := platform.DecodeJSON[transitionRequest](r, maxRequestBytes)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	to, err := parseStatus(req.To)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	actorID, err := uuid.Parse(claims.UserID)
	if err != nil {
		platform.WriteError(w, platform.ErrUnauthorized("authenticated subject is not a user identifier"))
		return
	}
	scope, ok := readScope(r)
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return
	}
	if err := s.Transition(r.Context(), jobID, to, &actorID, req.Reason, scope); err != nil {
		writeDomainError(w, err)
		return
	}
	job, err := s.GetJob(r.Context(), jobID, scope)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newJobResponse(&job))
}

// handleAssign implements POST /v1/jobs/{jobID}/assignments.
func (s *Service) handleAssign(w http.ResponseWriter, r *http.Request) {
	claims, ok := platform.ClaimsFromContext(r.Context())
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return
	}
	jobID, err := uuidFromRequest(r, "jobID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	req, err := platform.DecodeJSON[assignmentRequest](r, maxRequestBytes)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	if req.TechnicianID == nil || *req.TechnicianID == uuid.Nil {
		platform.WriteError(w, platform.ErrValidation("technician_id is required",
			platform.FieldError{Field: "technician_id", Issue: "is required"}))
		return
	}
	var assignedBy *uuid.UUID
	if actorID, parseErr := uuid.Parse(claims.UserID); parseErr == nil {
		assignedBy = &actorID
	}
	scope, ok := readScope(r)
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return
	}
	if err := s.AssignTechnician(r.Context(), jobID, *req.TechnicianID, assignedBy, scope); err != nil {
		writeDomainError(w, err)
		return
	}
	job, err := s.GetJob(r.Context(), jobID, scope)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newJobResponse(&job))
}

// handleAccept implements POST /v1/jobs/{jobID}/accept. The authenticated
// TECHNICIAN (claims.UserID) is the accepting technician, and the job is
// read through their own scope: technicians cannot probe jobs they are not
// assigned to (404, no existence leak).
func (s *Service) handleAccept(w http.ResponseWriter, r *http.Request) {
	claims, ok := platform.ClaimsFromContext(r.Context())
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return
	}
	jobID, err := uuidFromRequest(r, "jobID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	technicianID, err := uuid.Parse(claims.UserID)
	if err != nil {
		platform.WriteError(w, platform.ErrUnauthorized("authenticated subject is not a user identifier"))
		return
	}
	scope, ok := readScope(r)
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return
	}
	if err := s.Accept(r.Context(), jobID, technicianID, scope); err != nil {
		writeDomainError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListTransitions implements GET /v1/jobs/{jobID}/transitions. The
// job must be visible to the caller's scope first (else 404), then the
// trail is read through the same scope.
func (s *Service) handleListTransitions(w http.ResponseWriter, r *http.Request) {
	scope, ok := readScope(r)
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return
	}
	jobID, err := uuidFromRequest(r, "jobID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	if _, err := s.GetJob(r.Context(), jobID, scope); err != nil {
		writeDomainError(w, err)
		return
	}
	transitions, err := s.Transitions(r.Context(), jobID, scope)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	out := make([]transitionResponse, 0, len(transitions))
	for i := range transitions {
		out = append(out, newTransitionResponse(&transitions[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleListJobs implements GET /v1/jobs?status=&limit=&offset. The route
// is RBAC-gated to platform operators, and the store still scopes the page
// by the caller (defense in depth: middleware alone is never the only
// check, ADR-0004).
func (s *Service) handleListJobs(w http.ResponseWriter, r *http.Request) {
	scope, ok := readScope(r)
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return
	}
	query := r.URL.Query()
	rawStatus := strings.TrimSpace(query.Get("status"))
	if rawStatus == "" {
		platform.WriteError(w, platform.ErrValidation("status is required",
			platform.FieldError{Field: "status", Issue: "is required"}))
		return
	}
	status, err := parseStatus(rawStatus)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	limit, offset, err := listPagination(r)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	jobs, err := s.JobsByStatus(r.Context(), status, limit, offset, scope)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	out := make([]jobResponse, 0, len(jobs))
	for i := range jobs {
		out = append(out, newJobResponse(&jobs[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

// parseStatus validates a raw status string against the known 18-status
// enumeration.
func parseStatus(raw string) (Status, error) {
	candidate := Status(strings.ToUpper(strings.TrimSpace(raw)))
	for _, s := range AllStatuses() {
		if s == candidate {
			return s, nil
		}
	}
	return "", platform.ErrValidation("status is not a known job status",
		platform.FieldError{Field: "status", Issue: "must be one of the documented job statuses"})
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

// listPagination parses ?limit and ?offset. Defaults and the cap mirror
// Store.ListJobsByStatus semantics.
func listPagination(r *http.Request) (limit int, offset int, err error) {
	query := r.URL.Query()
	limit = DefaultJobsLimit
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
	if limit > MaxJobsLimit {
		limit = MaxJobsLimit
	}
	if offset < 0 {
		return 0, 0, platform.ErrValidation("offset must be non-negative",
			platform.FieldError{Field: "offset", Issue: "must be at least 0"})
	}
	return limit, offset, nil
}

// writeDomainError maps jobs domain errors onto platform API errors.
// Anything unrecognized falls through to platform.WriteError, which
// renders unknown errors as a 500 without leaking internals.
func writeDomainError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, ErrAlreadyConverted):
		platform.WriteError(w, platform.ErrConflict(err.Error()))
	case errors.Is(err, ErrConflict):
		platform.WriteError(w, platform.ErrConflict(err.Error()))
	case errors.Is(err, ErrRequestNotFound):
		platform.WriteError(w, platform.ErrNotFound("service request not found"))
	case errors.Is(err, ErrJobNotFound):
		platform.WriteError(w, platform.ErrNotFound("job not found"))
	default:
		platform.WriteError(w, err)
	}
}

func newRequestResponse(sr *ServiceRequest) requestResponse {
	return requestResponse{
		ID:          sr.ID,
		CustomerID:  sr.CustomerID,
		VehicleID:   sr.VehicleID,
		Description: sr.Description,
		LocationLat: sr.LocationLat,
		LocationLng: sr.LocationLng,
		AddressText: sr.AddressText,
		Status:      sr.Status,
		CreatedAt:   sr.CreatedAt,
		UpdatedAt:   sr.UpdatedAt,
	}
}

func newJobResponse(j *Job) jobResponse {
	return jobResponse{
		ID:               j.ID,
		ServiceRequestID: j.ServiceRequestID,
		CustomerID:       j.CustomerID,
		VehicleID:        j.VehicleID,
		Status:           j.Status,
		ProblemSummary:   j.ProblemSummary,
		TechnicianID:     j.TechnicianID,
		CreatedAt:        j.CreatedAt,
		UpdatedAt:        j.UpdatedAt,
	}
}

func newTransitionResponse(t *JobTransition) transitionResponse {
	return transitionResponse{
		ID:         t.ID,
		JobID:      t.JobID,
		FromStatus: t.FromStatus,
		ToStatus:   t.ToStatus,
		ActorID:    t.ActorID,
		Reason:     t.Reason,
		CreatedAt:  t.CreatedAt,
	}
}

// writeJSON renders body as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

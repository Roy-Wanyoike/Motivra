package garages

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// appointmentListDefaults bound the calendar window read.
const (
	appointmentListDefaultLimit = 50
	appointmentListMaxLimit     = 200
)

// appointmentRoutes registers the appointments surface of the garages
// domain (issue #169) on the already-authenticated router:
//
//	POST /v1/garages/{garageID}/appointments        open an appointment (CUSTOMER against a garage; GARAGE in its own tenant)
//	GET  /v1/garages/{garageID}/appointments        calendar window read (own tenant; operators per tenant)
//	GET  /v1/appointments/{appointmentID}           single read (own tenant; operators)
//	POST /v1/appointments/{appointmentID}/confirm   requested → confirmed
//	POST /v1/appointments/{appointmentID}/complete  confirmed → completed
//	POST /v1/appointments/{appointmentID}/cancel    requested|confirmed → cancelled
//	POST /v1/appointments/{appointmentID}/no-show   confirmed → no_show
//
// Every scope is derived exclusively from the validated access-token
// claims — never from client input. Errors are RFC 7807 problem+json.
func (s *Service) appointmentRoutes(r chi.Router, requireAuth func(http.HandlerFunc) http.HandlerFunc) {
	r.Route("/v1/garages/{garageID}/appointments", func(r chi.Router) {
		r.Post("/", requireAuth(s.handleCreateAppointment))
		r.Get("/", requireAuth(s.handleListAppointments))
	})
	r.Route("/v1/appointments", func(r chi.Router) {
		r.Get("/{appointmentID}", requireAuth(s.handleGetAppointment))
		r.Post("/{appointmentID}/confirm", requireAuth(s.appointmentTransitionHandler(AppointmentConfirmed)))
		r.Post("/{appointmentID}/complete", requireAuth(s.appointmentTransitionHandler(AppointmentCompleted)))
		r.Post("/{appointmentID}/cancel", requireAuth(s.appointmentTransitionHandler(AppointmentCancelled)))
		r.Post("/{appointmentID}/no-show", requireAuth(s.appointmentTransitionHandler(AppointmentNoShow)))
	})
}

// appointmentCreateRequest is the POST /v1/garages/{garageID}/appointments
// body. The tenant is the route address, never a body field.
type appointmentCreateRequest struct {
	JobID        *uuid.UUID `json:"job_id"`
	ScheduledFor string     `json:"scheduled_for"`
	Bay          *string    `json:"bay"`
	Notes        *string    `json:"notes"`
}

// appointmentResponse is the wire form of an appointment (the #169
// contract shape; garage_tenant_id names the owning garage organization).
type appointmentResponse struct {
	ID             uuid.UUID `json:"id"`
	JobID          uuid.UUID `json:"job_id"`
	GarageTenantID uuid.UUID `json:"garage_tenant_id"`
	ScheduledFor   time.Time `json:"scheduled_for"`
	Bay            *string   `json:"bay,omitempty"`
	Notes          *string   `json:"notes,omitempty"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func newAppointmentResponse(a Appointment) appointmentResponse {
	return appointmentResponse{
		ID:             a.ID,
		JobID:          a.JobID,
		GarageTenantID: a.TenantID,
		ScheduledFor:   a.ScheduledFor,
		Bay:            a.Bay,
		Notes:          a.Notes,
		Status:         a.Status,
		CreatedAt:      a.CreatedAt,
		UpdatedAt:      a.UpdatedAt,
	}
}

func newAppointmentResponses(list []Appointment) []appointmentResponse {
	out := make([]appointmentResponse, 0, len(list))
	for _, a := range list {
		out = append(out, newAppointmentResponse(a))
	}
	return out
}

// appointmentPathTenant parses the {garageID}/{appointmentID} route
// parameter as a UUID; a malformed identifier is a validation failure.
func appointmentPathTenant(r *http.Request, param string) (uuid.UUID, error) {
	raw := chi.URLParam(r, param)
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, platform.ErrValidation("path parameter "+param+" must be a UUID",
			platform.FieldError{Field: param, Issue: "must be a UUID"})
	}
	return id, nil
}

// appointmentPrincipal resolves the verified actor and the appointment
// scope of a request. ok=false means the handler already wrote the 401.
func appointmentPrincipal(r *http.Request) (uuid.UUID, platform.Claims, Scope, bool) {
	claims, ok := platform.ClaimsFromContext(r.Context())
	if !ok {
		return uuid.Nil, platform.Claims{}, Scope{}, false
	}
	actorID, err := uuid.Parse(claims.UserID)
	if err != nil {
		return uuid.Nil, platform.Claims{}, Scope{}, false
	}
	scope, ok := readScope(r)
	if !ok {
		return uuid.Nil, platform.Claims{}, Scope{}, false
	}
	return actorID, claims, scope, true
}

// handleCreateAppointment implements POST /v1/garages/{garageID}/appointments.
// A CUSTOMER opens a request against the addressed garage; a GARAGE
// principal opens one in its own tenant (the addressed tenant must equal
// the token's tenant_id claim). Everything else is refused. The slot is
// validated against the garage's declared capacity and the write is
// double-booking-guarded in the store transaction.
func (s *Service) handleCreateAppointment(w http.ResponseWriter, r *http.Request) {
	actorID, claims, scope, ok := appointmentPrincipal(r)
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("authenticated user identifier required"))
		return
	}
	tenantID, err := appointmentPathTenant(r, "garageID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	switch {
	case claims.Role == RoleCustomer:
		// A customer books against the addressed garage; the route is the
		// target and no tenant claim is expected on a personal token.
	case claims.Role == RoleGarage && scope.TenantID != nil && *scope.TenantID == tenantID:
		// Garage staff opening an appointment in their own tenant.
	default:
		platform.WriteError(w, platform.ErrForbidden(
			"appointments are opened by customers against the addressed garage or by the garage in its own tenant"))
		return
	}
	req, err := platform.DecodeJSON[appointmentCreateRequest](r, maxRequestBytes)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	if req.JobID == nil || *req.JobID == uuid.Nil {
		platform.WriteError(w, platform.ErrValidation("job_id is required",
			platform.FieldError{Field: "job_id", Issue: "is required"}))
		return
	}
	if req.ScheduledFor == "" {
		platform.WriteError(w, platform.ErrValidation("scheduled_for is required",
			platform.FieldError{Field: "scheduled_for", Issue: "is required"}))
		return
	}
	scheduledFor, err := time.Parse(time.RFC3339, req.ScheduledFor)
	if err != nil {
		platform.WriteError(w, platform.ErrValidation("scheduled_for must be an RFC 3339 timestamp",
			platform.FieldError{Field: "scheduled_for", Issue: "must be an RFC 3339 timestamp"}))
		return
	}
	a, err := s.CreateAppointment(r.Context(), actorID, tenantID, *req.JobID, scheduledFor, req.Bay, req.Notes)
	if err != nil {
		writeAppointmentError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newAppointmentResponse(a))
}

// handleListAppointments implements GET
// /v1/garages/{garageID}/appointments — the tenant-scoped calendar window
// read with ?from=&to=&status=&limit=&offset=. GARAGE staff read their own
// tenant only; operators read the addressed tenant (ADR-0004 cross-tenant
// read obligation); every other role is refused.
func (s *Service) handleListAppointments(w http.ResponseWriter, r *http.Request) {
	_, _, scope, ok := appointmentPrincipal(r)
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return
	}
	tenantID, err := appointmentPathTenant(r, "garageID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	if scope.Operator {
		// Operators address the tenant explicitly; the collection has no
		// unscoped mode.
	} else if scope.TenantID != nil && *scope.TenantID == tenantID {
		// Own-tenant read.
	} else {
		platform.WriteError(w, platform.ErrForbidden(
			"appointment listings are scoped to the caller's own tenant"))
		return
	}
	filter, limit, offset, err := appointmentListQuery(r)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	list, err := s.Appointments(r.Context(), tenantID, filter, limit, offset)
	if err != nil {
		writeAppointmentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newAppointmentResponses(list))
}

// handleGetAppointment implements GET /v1/appointments/{appointmentID}.
// Own-tenant read for GARAGE principals, cross-tenant read for operators;
// a foreign tenant is indistinguishable from a missing row (404).
func (s *Service) handleGetAppointment(w http.ResponseWriter, r *http.Request) {
	_, claims, scope, ok := appointmentPrincipal(r)
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return
	}
	if !scope.Operator && claims.Role != RoleGarage {
		platform.WriteError(w, platform.ErrForbidden(
			"appointments are readable by the owning garage and platform operators"))
		return
	}
	if !scope.Operator && scope.TenantID == nil {
		platform.WriteError(w, platform.ErrForbidden("a tenant_id claim is required"))
		return
	}
	id, err := appointmentPathTenant(r, "appointmentID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	a, err := s.Appointment(r.Context(), scope, id)
	if err != nil {
		writeAppointmentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newAppointmentResponse(a))
}

// appointmentTransitionHandler builds the POST
// /v1/appointments/{appointmentID}/<action> handlers. RBAC and the state
// machine live in the service; the handler only resolves the verified
// principal and the target status.
func (s *Service) appointmentTransitionHandler(to string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actorID, claims, scope, ok := appointmentPrincipal(r)
		if !ok {
			platform.WriteError(w, platform.ErrUnauthorized("authenticated user identifier required"))
			return
		}
		if scope.Operator {
			// Operator override: ADMIN/SUPER_ADMIN only (the service
			// re-checks; the audited override is stamped there).
			if claims.Role != RoleAdmin && claims.Role != RoleSuperAdmin {
				platform.WriteError(w, platform.ErrForbidden(
					"appointment transitions are performed by the owning garage; ADMIN/SUPER_ADMIN may override"))
				return
			}
		} else {
			if claims.Role != RoleGarage {
				platform.WriteError(w, platform.ErrForbidden(
					"appointment transitions are performed by the owning garage; ADMIN/SUPER_ADMIN may override"))
				return
			}
			if scope.TenantID == nil {
				platform.WriteError(w, platform.ErrForbidden("a tenant_id claim is required"))
				return
			}
		}
		id, err := appointmentPathTenant(r, "appointmentID")
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		a, err := s.TransitionAppointment(r.Context(), scope, actorID, claims.Role, id, to)
		if err != nil {
			writeAppointmentError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, newAppointmentResponse(a))
	}
}

// appointmentListQuery parses the calendar window read's query string.
// from/to are RFC 3339 instants, status must be a lifecycle state, limit
// is capped and offset non-negative.
func appointmentListQuery(r *http.Request) (AppointmentFilter, int, int, error) {
	var filter AppointmentFilter
	q := r.URL.Query()
	if raw := q.Get("status"); raw != "" {
		if !appointmentKnownStatus(raw) {
			return filter, 0, 0, platform.ErrValidation("unknown status filter",
				platform.FieldError{Field: "status", Issue: "must be one of requested, confirmed, completed, cancelled, no_show"})
		}
		filter.Status = &raw
	}
	parseInstant := func(name string) (*time.Time, error) {
		raw := q.Get(name)
		if raw == "" {
			return nil, nil
		}
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, platform.ErrValidation(name+" must be an RFC 3339 timestamp",
				platform.FieldError{Field: name, Issue: "must be an RFC 3339 timestamp"})
		}
		return &t, nil
	}
	from, err := parseInstant("from")
	if err != nil {
		return filter, 0, 0, err
	}
	filter.From = from
	to, err := parseInstant("to")
	if err != nil {
		return filter, 0, 0, err
	}
	filter.To = to
	limit := appointmentListDefaultLimit
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > appointmentListMaxLimit {
			return filter, 0, 0, platform.ErrValidation("limit must be between 1 and "+strconv.Itoa(appointmentListMaxLimit),
				platform.FieldError{Field: "limit", Issue: "is out of range"})
		}
		limit = n
	}
	offset := 0
	if raw := q.Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return filter, 0, 0, platform.ErrValidation("offset must be a non-negative integer",
				platform.FieldError{Field: "offset", Issue: "is invalid"})
		}
		offset = n
	}
	return filter, limit, offset, nil
}

// appointmentKnownStatus reports whether raw is a lifecycle state.
func appointmentKnownStatus(raw string) bool {
	switch raw {
	case AppointmentRequested, AppointmentConfirmed, AppointmentCompleted, AppointmentCancelled, AppointmentNoShow:
		return true
	}
	return false
}

// writeAppointmentError maps the appointments domain errors onto platform
// API errors. Invalid transitions and slot conflicts are 409 conflicts;
// slot validation failures are 422; not-found is 404. Anything
// unrecognized falls through to platform.WriteError, which renders
// unknown errors as a 500 without leaking internals.
func writeAppointmentError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, ErrAppointmentNotFound):
		platform.WriteError(w, platform.ErrNotFound("appointment not found"))
	case errors.Is(err, ErrInvalidTransition):
		platform.WriteError(w, platform.ErrConflict(err.Error()))
	case errors.Is(err, ErrTransitionForbidden):
		platform.WriteError(w, platform.ErrForbidden(err.Error()))
	case errors.Is(err, ErrSlotConflict):
		platform.WriteError(w, platform.ErrConflict("the requested slot is no longer available"))
	case errors.Is(err, ErrOutsideOperatingHours), errors.Is(err, ErrCapacityNotDeclared), errors.Is(err, ErrPastSlot):
		platform.WriteError(w, platform.ErrValidation(err.Error()))
	default:
		platform.WriteError(w, err)
	}
}

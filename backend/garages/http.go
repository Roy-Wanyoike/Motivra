package garages

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// maxRequestBytes caps JSON request bodies accepted by the handlers
// (mirrors backend/jobs).
const maxRequestBytes int64 = 64 << 10

// capacityWriteRoles are the roles allowed to read AND write the garage's
// own capacity record: the GARAGE organizational principal only (issue
// #170). Operators consume capacity through this domain's service API and
// events (dispatch scoring, ops oversight) — the single-tenant endpoint has
// no tenant selector, and the scope never comes from client input.
var capacityWriteRoles = []string{RoleGarage}

// Routes registers the Garages HTTP API on r:
//
//	GET /v1/garage/capacity   read this garage's declared capacity (GARAGE)
//	PUT /v1/garage/capacity   write this garage's declared capacity (GARAGE)
//
// Every scope is derived exclusively from the validated access-token claims
// — never from client input. The tenant comes from the token's tenant_id
// claim; a GARAGE token without one fails closed (403), a malformed claim
// fails closed (401), and unknown tenants are indistinguishable from unset
// configuration. Errors are RFC 7807 problem+json.
//
// requireAuth wraps every handler with the platform authentication gate
// (pass platform.RequireAuthenticated). Role-gated routes are wrapped with
// platform.RequireRole inside the route group. The AuthMiddleware must
// already be part of the router middleware chain so ClaimsFromContext
// resolves.
func Routes(r chi.Router, svc *Service, requireAuth func(http.HandlerFunc) http.HandlerFunc) {
	r.Route("/v1/garage/capacity", func(r chi.Router) {
		r.Get("/", requireAuth(platform.RequireRole(capacityWriteRoles, svc.handleGetCapacity)))
		r.Put("/", requireAuth(platform.RequireRole(capacityWriteRoles, svc.handlePutCapacity)))
	})
}

// capacityRequest is the PUT /v1/garage/capacity body. All four fields are
// required (the write replaces the whole configuration; partial writes are
// rejected so a dropped field can never silently zero declared capacity).
type capacityRequest struct {
	Bays            *int   `json:"bays"`
	OpeningTime     string `json:"opening_time"`
	ClosingTime     string `json:"closing_time"`
	TechnicianSlots *int   `json:"technician_slots"`
}

// capacityResponse is the wire form of the capacity record. updated_at and
// updated_by are omitted while the tenant has never written a row (the
// response then carries the documented defaults).
type capacityResponse struct {
	Bays            int        `json:"bays"`
	OpeningTime     string     `json:"opening_time"`
	ClosingTime     string     `json:"closing_time"`
	TechnicianSlots int        `json:"technician_slots"`
	UpdatedAt       *time.Time `json:"updated_at,omitempty"`
	UpdatedBy       *uuid.UUID `json:"updated_by,omitempty"`
}

func newCapacityResponse(c Capacity) capacityResponse {
	out := capacityResponse{
		Bays:            c.Bays,
		OpeningTime:     c.OpeningTime,
		ClosingTime:     c.ClosingTime,
		TechnicianSlots: c.TechnicianSlots,
		UpdatedBy:       c.UpdatedBy,
	}
	if !c.UpdatedAt.IsZero() {
		updatedAt := c.UpdatedAt
		out.UpdatedAt = &updatedAt
	}
	return out
}

// handleGetCapacity implements GET /v1/garage/capacity. The record is the
// caller's own tenant's row, derived from the token's tenant_id claim; a
// tenant that never configured capacity gets the documented defaults.
func (s *Service) handleGetCapacity(w http.ResponseWriter, r *http.Request) {
	scope, ok := garageScope(w, r)
	if !ok {
		return
	}
	capacity, err := s.Capacity(r.Context(), *scope.TenantID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newCapacityResponse(capacity))
}

// handlePutCapacity implements PUT /v1/garage/capacity. Validation happens
// server-side (never trusted from the client): non-negative counts and a
// well-formed HH:MM operating window with opening < closing. The written
// row's tenant is the token's tenant claim — the body carries no tenant at
// all.
func (s *Service) handlePutCapacity(w http.ResponseWriter, r *http.Request) {
	scope, ok := garageScope(w, r)
	if !ok {
		return
	}
	claims, ok := platform.ClaimsFromContext(r.Context())
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return
	}
	actorID, err := uuid.Parse(claims.UserID)
	if err != nil {
		platform.WriteError(w, platform.ErrUnauthorized("authenticated subject is not a user identifier"))
		return
	}
	req, err := platform.DecodeJSON[capacityRequest](r, maxRequestBytes)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	if req.Bays == nil {
		platform.WriteError(w, platform.ErrValidation("bays is required",
			platform.FieldError{Field: "bays", Issue: "is required"}))
		return
	}
	if req.TechnicianSlots == nil {
		platform.WriteError(w, platform.ErrValidation("technician_slots is required",
			platform.FieldError{Field: "technician_slots", Issue: "is required"}))
		return
	}
	if req.OpeningTime == "" {
		platform.WriteError(w, platform.ErrValidation("opening_time is required",
			platform.FieldError{Field: "opening_time", Issue: "is required"}))
		return
	}
	if req.ClosingTime == "" {
		platform.WriteError(w, platform.ErrValidation("closing_time is required",
			platform.FieldError{Field: "closing_time", Issue: "is required"}))
		return
	}
	capacity, err := s.UpdateCapacity(r.Context(), scope, &actorID, *req.Bays, req.OpeningTime, req.ClosingTime, *req.TechnicianSlots)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newCapacityResponse(capacity))
}

// writeDomainError maps garages domain errors onto platform API errors.
// Validation failures are 422 with the field issue named. Anything
// unrecognized falls through to platform.WriteError, which renders unknown
// errors as a 500 without leaking internals.
func writeDomainError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, ErrCapacityNotFound):
		platform.WriteError(w, platform.ErrNotFound("capacity not configured"))
	default:
		platform.WriteError(w, platform.ErrValidation(err.Error()))
	}
}

// writeJSON renders body as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

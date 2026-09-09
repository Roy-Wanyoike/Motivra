package vehicles

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// Administrative roles allowed to record mileage on vehicles they do not
// own. Mirrors the platform RBAC vocabulary (ADR-0004).
const (
	RoleAdmin      = "ADMIN"
	RoleSuperAdmin = "SUPER_ADMIN"
)

// maxRequestBytes caps JSON request bodies accepted by the handlers.
const maxRequestBytes int64 = 64 << 10

// Routes registers the Vehicle Identity HTTP API on r:
//
//	POST   /v1/vehicles                        register a vehicle
//	GET    /v1/vehicles                        scoped, paginated registry listing
//	GET    /v1/vehicles/{vehicleID}            fetch one vehicle
//	GET    /v1/vehicles/{vehicleID}/passport   read the Vehicle Passport
//	GET    /v1/vehicles/{vehicleID}/history    paginated history (newest first)
//	POST   /v1/vehicles/{vehicleID}/mileage    record an odometer reading
//
// requireAuth wraps every handler with the platform authentication gate
// (pass platform.RequireAuthenticated). The AuthMiddleware must already be
// part of the router middleware chain so ClaimsFromContext resolves.
func Routes(r chi.Router, svc *Service, requireAuth func(http.HandlerFunc) http.HandlerFunc) {
	r.Route("/v1/vehicles", func(r chi.Router) {
		r.Post("/", requireAuth(svc.handleCreateVehicle))
		r.Get("/", requireAuth(svc.handleListVehicles))
		r.Route("/{vehicleID}", func(r chi.Router) {
			r.Get("/", requireAuth(svc.handleGetVehicle))
			r.Get("/passport", requireAuth(svc.handleGetPassport))
			r.Get("/history", requireAuth(svc.handleListHistory))
			r.Post("/mileage", requireAuth(svc.handleRecordMileage))
		})
	})
}

// createVehicleRequest is the POST /v1/vehicles body.
type createVehicleRequest struct {
	VIN               string `json:"vin"`
	Make              string `json:"make"`
	Model             string `json:"model"`
	YearOfManufacture int    `json:"year_of_manufacture"`
	Plate             string `json:"plate"`
	Color             string `json:"color"`
}

// mileageRequest is the POST /v1/vehicles/{vehicleID}/mileage body.
type mileageRequest struct {
	OdometerKm *int64 `json:"odometer_km"`
}

// vehicleResponse is the wire form of a registry vehicle.
type vehicleResponse struct {
	ID              uuid.UUID  `json:"id"`
	TenantID        *uuid.UUID `json:"tenant_id,omitempty"`
	OwnerUserID     uuid.UUID  `json:"owner_user_id"`
	VIN             string     `json:"vin"`
	Make            string     `json:"make"`
	Model           string     `json:"model"`
	Year            int        `json:"year_of_manufacture"`
	Plate           string     `json:"plate,omitempty"`
	Color           string     `json:"color,omitempty"`
	MileageLatestKm int64      `json:"mileage_latest_km"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// historyEventResponse is the wire form of one history event.
type historyEventResponse struct {
	ID          uuid.UUID  `json:"id"`
	VehicleID   uuid.UUID  `json:"vehicle_id"`
	EventType   string     `json:"event_type"`
	Summary     string     `json:"summary"`
	EvidenceRef string     `json:"evidence_ref,omitempty"`
	OccurredAt  time.Time  `json:"occurred_at"`
	OdometerKm  *int64     `json:"odometer_km,omitempty"`
	RecordedBy  *uuid.UUID `json:"recorded_by,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// vehicleListResponse is the wire form of one registry listing page. It
// reuses vehicleResponse for the rows; next_cursor is omitted on the last
// page.
type vehicleListResponse struct {
	Vehicles   []vehicleResponse `json:"vehicles"`
	NextCursor *string           `json:"next_cursor,omitempty"`
}

// passportResponse is the wire form of the read-only Vehicle Passport.
type passportResponse struct {
	ID                uuid.UUID  `json:"id"`
	TenantID          *uuid.UUID `json:"tenant_id,omitempty"`
	OwnerUserID       uuid.UUID  `json:"owner_user_id"`
	VIN               string     `json:"vin"`
	Make              string     `json:"make"`
	Model             string     `json:"model"`
	Year              int        `json:"year_of_manufacture"`
	Plate             string     `json:"plate,omitempty"`
	Color             string     `json:"color,omitempty"`
	MileageLatestKm   int64      `json:"mileage_latest_km"`
	ServiceEventCount int64      `json:"service_event_count"`
	LastServiceAt     *time.Time `json:"last_service_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
}

func newVehicleResponse(v *Vehicle) vehicleResponse {
	return vehicleResponse{
		ID:              v.ID,
		TenantID:        v.TenantID,
		OwnerUserID:     v.OwnerUserID,
		VIN:             v.VIN,
		Make:            v.Make,
		Model:           v.Model,
		Year:            v.Year,
		Plate:           v.Plate,
		Color:           v.Color,
		MileageLatestKm: v.MileageLatestKm,
		CreatedAt:       v.CreatedAt,
		UpdatedAt:       v.UpdatedAt,
	}
}

func newHistoryEventResponse(e *HistoryEvent) historyEventResponse {
	return historyEventResponse{
		ID:          e.ID,
		VehicleID:   e.VehicleID,
		EventType:   e.EventType,
		Summary:     e.Summary,
		EvidenceRef: e.EvidenceRef,
		OccurredAt:  e.OccurredAt,
		OdometerKm:  e.OdometerKm,
		RecordedBy:  e.RecordedBy,
		CreatedAt:   e.CreatedAt,
	}
}

func newPassportResponse(p Passport) passportResponse {
	return passportResponse{
		ID:                p.ID,
		TenantID:          p.TenantID,
		OwnerUserID:       p.OwnerUserID,
		VIN:               p.VIN,
		Make:              p.Make,
		Model:             p.Model,
		Year:              p.Year,
		Plate:             p.Plate,
		Color:             p.Color,
		MileageLatestKm:   p.MileageLatestKm,
		ServiceEventCount: p.ServiceEventCount,
		LastServiceAt:     p.LastServiceAt,
		CreatedAt:         p.CreatedAt,
	}
}

// handleCreateVehicle implements POST /v1/vehicles. The authenticated user
// becomes the owner; the tenant claim (when present) attaches the vehicle
// to the caller's organization.
func (s *Service) handleCreateVehicle(w http.ResponseWriter, r *http.Request) {
	claims, ok := platform.ClaimsFromContext(r.Context())
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return
	}
	ownerID, err := uuid.Parse(claims.UserID)
	if err != nil {
		platform.WriteError(w, platform.ErrUnauthorized("authenticated subject is not a user identifier"))
		return
	}
	tenantID, err := tenantIDFromClaims(claims)
	if err != nil {
		platform.WriteError(w, platform.ErrUnauthorized("authenticated tenant claim is not a tenant identifier"))
		return
	}
	req, err := platform.DecodeJSON[createVehicleRequest](r, maxRequestBytes)
	if err != nil {
		platform.WriteError(w, err)
		return
	}

	v := &Vehicle{
		TenantID:    tenantID,
		OwnerUserID: ownerID,
		VIN:         req.VIN,
		Make:        req.Make,
		Model:       req.Model,
		Year:        req.YearOfManufacture,
		Plate:       req.Plate,
		Color:       req.Color,
	}
	if err := s.RegisterVehicle(r.Context(), v, ownerID); err != nil {
		platform.WriteError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newVehicleResponse(v))
}

// handleGetVehicle implements GET /v1/vehicles/{vehicleID}.
func (s *Service) handleGetVehicle(w http.ResponseWriter, r *http.Request) {
	claims, ok := platform.ClaimsFromContext(r.Context())
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return
	}
	vehicleID, err := vehicleIDFromRequest(r)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	scope, err := scopeFromClaims(claims)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	v, err := s.GetVehicle(r.Context(), vehicleID, scope)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newVehicleResponse(&v))
}

// handleGetPassport implements GET /v1/vehicles/{vehicleID}/passport.
func (s *Service) handleGetPassport(w http.ResponseWriter, r *http.Request) {
	claims, ok := platform.ClaimsFromContext(r.Context())
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return
	}
	vehicleID, err := vehicleIDFromRequest(r)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	scope, err := scopeFromClaims(claims)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	p, err := s.Passport(r.Context(), vehicleID, scope)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPassportResponse(p))
}

// handleListHistory implements GET /v1/vehicles/{vehicleID}/history with
// ?limit (default 50, capped at 200) and ?offset. The vehicle must be
// visible to the caller's scope.
func (s *Service) handleListHistory(w http.ResponseWriter, r *http.Request) {
	claims, ok := platform.ClaimsFromContext(r.Context())
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return
	}
	vehicleID, err := vehicleIDFromRequest(r)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	scope, err := scopeFromClaims(claims)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	limit, offset, err := historyPagination(r)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	events, err := s.History(r.Context(), vehicleID, scope, limit, offset)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	out := make([]historyEventResponse, 0, len(events))
	for i := range events {
		out = append(out, newHistoryEventResponse(&events[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRecordMileage implements POST /v1/vehicles/{vehicleID}/mileage.
// The vehicle must be visible to the caller's scope and only the vehicle
// owner or an ADMIN/SUPER_ADMIN may record a reading.
func (s *Service) handleRecordMileage(w http.ResponseWriter, r *http.Request) {
	claims, ok := platform.ClaimsFromContext(r.Context())
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return
	}
	vehicleID, err := vehicleIDFromRequest(r)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	scope, err := scopeFromClaims(claims)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	req, err := platform.DecodeJSON[mileageRequest](r, maxRequestBytes)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	if req.OdometerKm == nil {
		platform.WriteError(w, platform.ErrValidation("odometer_km is required",
			platform.FieldError{Field: "odometer_km", Issue: "is required"}))
		return
	}

	v, err := s.GetVehicle(r.Context(), vehicleID, scope)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	if !canMutate(claims, v) {
		platform.WriteError(w, platform.ErrForbidden("only the vehicle owner or an administrator may record mileage"))
		return
	}
	recorder, err := uuid.Parse(claims.UserID)
	if err != nil {
		platform.WriteError(w, platform.ErrUnauthorized("authenticated subject is not a user identifier"))
		return
	}
	if err := s.RecordMileage(r.Context(), vehicleID, *req.OdometerKm, &recorder, scope); err != nil {
		platform.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// canMutate reports whether the authenticated claims may record mileage on
// v: the owner themself, or an administrator role.
func canMutate(claims platform.Claims, v Vehicle) bool {
	if claims.Role == RoleAdmin || claims.Role == RoleSuperAdmin {
		return true
	}
	userID, err := uuid.Parse(claims.UserID)
	if err != nil {
		return false
	}
	return userID == v.OwnerUserID
}

// scopeFromClaims derives the vehicle data boundary from the authenticated
// claims (never from client input, ADR-0004): a tenant claim scopes an
// organizational principal to its organization; a personal principal is
// scoped to the vehicles it owns; the control-plane roles (ADMIN,
// SUPER_ADMIN) read across tenants under audit obligation. A malformed
// tenant or subject claim is an authentication failure.
func scopeFromClaims(claims platform.Claims) (VehicleScope, error) {
	if claims.Role == RoleAdmin || claims.Role == RoleSuperAdmin {
		return VehicleScope{}, nil
	}
	tenantID, err := tenantIDFromClaims(claims)
	if err != nil {
		return VehicleScope{}, platform.ErrUnauthorized("authenticated tenant claim is not a tenant identifier")
	}
	if tenantID != nil {
		return VehicleScope{TenantID: tenantID}, nil
	}
	ownerID, err := uuid.Parse(claims.UserID)
	if err != nil {
		return VehicleScope{}, platform.ErrUnauthorized("authenticated subject is not a user identifier")
	}
	return VehicleScope{OwnerID: &ownerID}, nil
}

// handleListVehicles implements GET /v1/vehicles with ?limit (default 50,
// capped at 100) and the opaque keyset ?cursor. The scope comes from the
// JWT claims only; there is deliberately no tenant query parameter.
func (s *Service) handleListVehicles(w http.ResponseWriter, r *http.Request) {
	claims, ok := platform.ClaimsFromContext(r.Context())
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return
	}
	scope, err := scopeFromClaims(claims)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	limit, cursor, err := listVehiclesPagination(r)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	vehicles, next, err := s.ListVehicles(r.Context(), scope, cursor, limit)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	out := make([]vehicleResponse, 0, len(vehicles))
	for i := range vehicles {
		out = append(out, newVehicleResponse(&vehicles[i]))
	}
	resp := vehicleListResponse{Vehicles: out}
	if next != nil {
		encoded, err := EncodeVehicleCursor(*next)
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		resp.NextCursor = &encoded
	}
	writeJSON(w, http.StatusOK, resp)
}

// tenantIDFromClaims parses the tenant claim. A personal account (no
// tenant) yields nil; a malformed claim is an authentication failure.
func tenantIDFromClaims(claims platform.Claims) (*uuid.UUID, error) {
	if claims.TenantID == "" {
		return nil, nil
	}
	tenantID, err := uuid.Parse(claims.TenantID)
	if err != nil {
		return nil, err
	}
	return &tenantID, nil
}

// vehicleIDFromRequest parses the {vehicleID} path parameter.
func vehicleIDFromRequest(r *http.Request) (uuid.UUID, error) {
	raw := strings.TrimSpace(chi.URLParam(r, "vehicleID"))
	if raw == "" {
		return uuid.Nil, platform.ErrValidation("vehicle id is required",
			platform.FieldError{Field: "vehicle_id", Issue: "is required"})
	}
	vehicleID, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, platform.ErrValidation("vehicle id is not a valid identifier",
			platform.FieldError{Field: "vehicle_id", Issue: "must be a uuid"})
	}
	return vehicleID, nil
}

// listVehiclesPagination parses ?limit and the opaque ?cursor for the
// registry listing. The defaults and the cap mirror Store.ListVehicles
// semantics.
func listVehiclesPagination(r *http.Request) (limit int, cursor *VehicleCursor, err error) {
	query := r.URL.Query()
	limit = defaultListLimit
	if raw := query.Get("limit"); raw != "" {
		n, parseErr := strconv.Atoi(raw)
		if parseErr != nil {
			return 0, nil, platform.ErrValidation("limit is not a number",
				platform.FieldError{Field: "limit", Issue: "must be an integer"})
		}
		limit = n
	}
	if limit < 1 {
		return 0, nil, platform.ErrValidation("limit must be positive",
			platform.FieldError{Field: "limit", Issue: "must be at least 1"})
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	if raw := query.Get("cursor"); raw != "" {
		c, parseErr := ParseVehicleCursor(raw)
		if parseErr != nil {
			return 0, nil, parseErr
		}
		cursor = &c
	}
	return limit, cursor, nil
}

// historyPagination parses ?limit and ?offset. The defaults and the cap
// mirror Store.ListHistory semantics.
func historyPagination(r *http.Request) (limit int, offset int, err error) {
	query := r.URL.Query()
	limit = defaultHistoryLimit
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
	if limit > maxHistoryLimit {
		limit = maxHistoryLimit
	}
	if offset < 0 {
		return 0, 0, platform.ErrValidation("offset must be non-negative",
			platform.FieldError{Field: "offset", Issue: "must be at least 0"})
	}
	return limit, offset, nil
}

// writeJSON renders body as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

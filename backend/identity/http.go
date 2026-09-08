package identity

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// maxAuthBodyBytes caps authentication request bodies; the largest legal
// payload (long password, names, device strings) is far below this.
const maxAuthBodyBytes = 16 * 1024

// registerRequest is the POST /v1/auth/register body.
type registerRequest struct {
	Email      string `json:"email"`
	Phone      string `json:"phone,omitempty"`
	Password   string `json:"password"`
	FullName   string `json:"full_name"`
	DeviceName string `json:"device_name,omitempty"`
}

// loginRequest is the POST /v1/auth/login body.
type loginRequest struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	DeviceName string `json:"device_name,omitempty"`
}

// refreshRequest is the POST /v1/auth/refresh body.
type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// logoutRequest is the POST /v1/auth/logout body. Possession of the refresh
// token is the logout credential, matching the rotation model (ADR-0004).
type logoutRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// userResponse is the API projection of a user: credential material is
// never serialized.
type userResponse struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	Phone     string    `json:"phone"`
	FullName  string    `json:"full_name"`
	Status    string    `json:"status"`
	Roles     []string  `json:"roles"`
	CreatedAt string    `json:"created_at"`
}

// newUserResponse projects a User onto the wire format.
func newUserResponse(u User) userResponse {
	roles := make([]string, 0, len(u.Roles))
	for _, r := range u.Roles {
		roles = append(roles, string(r))
	}
	return userResponse{
		ID:        u.ID,
		Email:     u.Email,
		Phone:     u.Phone,
		FullName:  u.FullName,
		Status:    u.Status,
		Roles:     roles,
		CreatedAt: u.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
	}
}

// userListResponse is the paginated GET /v1/admin/users body.
type userListResponse struct {
	Users  []userResponse `json:"users"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
}

// clientIP resolves the caller IP for audit rows: X-Forwarded-For (first
// hop added by our edge) wins, otherwise the connection RemoteAddr.
func clientIP(r *http.Request) string {
	if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); fwd != "" {
		if first := strings.SplitN(fwd, ",", 2)[0]; first != "" {
			return strings.TrimSpace(first)
		}
	}
	return strings.TrimSpace(r.RemoteAddr)
}

// writeJSON renders v as an application/json body with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil { // unreachable for these payload types; stay total
		platform.WriteError(w, platform.ErrInternal())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// Routes registers the identity HTTP API on r: the public authentication
// endpoints plus the protected profile and admin listing routes. Protected
// routes rely on platform.AuthMiddleware having populated the request
// context (mounted by the service binary) and enforce authn/authz here.
func Routes(r chi.Router, svc *Service) {
	r.Post("/v1/auth/register", handleRegister(svc))
	r.Post("/v1/auth/login", handleLogin(svc))
	r.Post("/v1/auth/refresh", handleRefresh(svc))
	r.Post("/v1/auth/logout", handleLogout(svc))
	r.Get("/v1/users/me", platform.RequireAuthenticated(handleProfile(svc)))
	r.Get("/v1/admin/users", platform.RequireRole(
		[]string{string(RoleAdmin), string(RoleSuperAdmin)},
		handleListUsers(svc),
	))
}

// handleRegister creates an account and returns the first token pair (201).
func handleRegister(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, err := platform.DecodeJSON[registerRequest](r, maxAuthBodyBytes)
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		resp, err := svc.Register(r.Context(), req.Email, req.Phone, req.Password, req.FullName, req.DeviceName, clientIP(r))
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, resp)
	}
}

// handleLogin authenticates credentials and returns a token pair (200).
// Unknown emails and wrong passwords render the identical 401 body.
func handleLogin(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, err := platform.DecodeJSON[loginRequest](r, maxAuthBodyBytes)
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		resp, err := svc.Login(r.Context(), req.Email, req.Password, req.DeviceName, clientIP(r))
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// handleRefresh rotates a refresh session and returns the new pair (200).
func handleRefresh(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, err := platform.DecodeJSON[refreshRequest](r, maxAuthBodyBytes)
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		resp, err := svc.RefreshTokens(r.Context(), req.RefreshToken)
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// handleLogout revokes the session behind the presented refresh token (204).
func handleLogout(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, err := platform.DecodeJSON[logoutRequest](r, maxAuthBodyBytes)
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		var actor uuid.UUID
		if claims, ok := platform.ClaimsFromContext(r.Context()); ok {
			actor, _ = uuid.Parse(claims.UserID)
		}
		if err := svc.LogoutByRefreshToken(r.Context(), req.RefreshToken, actor); err != nil {
			platform.WriteError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleProfile returns the authenticated caller's profile (200).
func handleProfile(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := platform.ClaimsFromContext(r.Context())
		if !ok {
			platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
			return
		}
		userID, err := uuid.Parse(claims.UserID)
		if err != nil {
			platform.WriteError(w, platform.ErrUnauthorized("access token subject is invalid"))
			return
		}
		user, err := svc.Profile(r.Context(), userID)
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, newUserResponse(user))
	}
}

// handleListUsers returns a paginated account listing for ADMIN and
// SUPER_ADMIN callers (200).
func handleListUsers(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, offset, err := paginationFromQuery(r)
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		users, err := svc.ListUsers(r.Context(), limit, offset)
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		out := make([]userResponse, 0, len(users))
		for _, u := range users {
			out = append(out, newUserResponse(u))
		}
		writeJSON(w, http.StatusOK, userListResponse{Users: out, Limit: clampLimit(limit), Offset: clampOffset(offset)})
	}
}

// paginationFromQuery parses ?limit and ?offset; unset values fall back to
// the service defaults and invalid numbers are validation errors.
func paginationFromQuery(r *http.Request) (limit, offset int, err error) {
	q := r.URL.Query()
	limit = defaultListUsersLimit
	offset = 0
	if raw := q.Get("limit"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil {
			return 0, 0, platform.ErrValidation("limit must be an integer",
				platform.FieldError{Field: "limit", Issue: "invalid"})
		}
		limit = parsed
	}
	if raw := q.Get("offset"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil {
			return 0, 0, platform.ErrValidation("offset must be an integer",
				platform.FieldError{Field: "offset", Issue: "invalid"})
		}
		offset = parsed
	}
	return limit, offset, nil
}

// clampLimit mirrors the service-side limit clamping for echo-back.
func clampLimit(limit int) int {
	if limit <= 0 {
		return defaultListUsersLimit
	}
	if limit > maxListUsersLimit {
		return maxListUsersLimit
	}
	return limit
}

// clampOffset mirrors the service-side offset clamping for echo-back.
func clampOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	return offset
}

package identity

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

// verifyRequest is the POST /v1/auth/verify body. The single-use token is
// the credential; the endpoint is public because verification links are
// clicked without a session (issue #86).
type verifyRequest struct {
	Token string `json:"token"`
}

// resendVerificationRequest is the POST /v1/auth/verification/resend body.
// Channel defaults to email; phone resends apply to accounts with a phone.
type resendVerificationRequest struct {
	Email   string              `json:"email"`
	Channel VerificationChannel `json:"channel,omitempty"`
}

// changePasswordRequest is the POST /v1/auth/password body (issue #86).
type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// userResponse is the API projection of a user: credential material is
// never serialized. The verification timestamps let clients render the
// "verify your account" state without a second endpoint (issue #86).
type userResponse struct {
	ID              uuid.UUID `json:"id"`
	Email           string    `json:"email"`
	Phone           string    `json:"phone"`
	FullName        string    `json:"full_name"`
	Status          string    `json:"status"`
	Roles           []string  `json:"roles"`
	EmailVerifiedAt *string   `json:"email_verified_at"`
	PhoneVerifiedAt *string   `json:"phone_verified_at"`
	CreatedAt       string    `json:"created_at"`
}

// formatTimePtr projects an optional timestamp onto the wire format used
// for created_at (nil stays null).
func formatTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	formatted := t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
	return &formatted
}

// newUserResponse projects a User onto the wire format.
func newUserResponse(u User) userResponse {
	roles := make([]string, 0, len(u.Roles))
	for _, r := range u.Roles {
		roles = append(roles, string(r))
	}
	return userResponse{
		ID:              u.ID,
		Email:           u.Email,
		Phone:           u.Phone,
		FullName:        u.FullName,
		Status:          u.Status,
		Roles:           roles,
		EmailVerifiedAt: formatTimePtr(u.EmailVerifiedAt),
		PhoneVerifiedAt: formatTimePtr(u.PhoneVerifiedAt),
		CreatedAt:       u.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
	}
}

// userListResponse is the paginated GET /v1/admin/users body.
type userListResponse struct {
	Users  []userResponse `json:"users"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
}

// sessionResponse is the API projection of a refresh session (issue #87).
// Credential material (the refresh-token digest) is never serialized — the
// service zeroes it before projection, and this type has no field for it.
type sessionResponse struct {
	ID                uuid.UUID `json:"id"`
	DeviceName        string    `json:"device_name"`
	IPAddress         string    `json:"ip_address"`
	CreatedAt         string    `json:"created_at"`
	ExpiresAt         string    `json:"expires_at"`
	AbsoluteExpiresAt string    `json:"absolute_expires_at"`
	RevokedAt         *string   `json:"revoked_at"`
	Current           bool      `json:"current"`
}

// newSessionResponse projects a Session (RefreshTokenHash already stripped
// by the service) and marks the caller's current session.
func newSessionResponse(sess Session, currentSessionID string) sessionResponse {
	resp := sessionResponse{
		ID:                sess.ID,
		DeviceName:        sess.DeviceName,
		IPAddress:         sess.IPAddress,
		CreatedAt:         sess.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		ExpiresAt:         sess.ExpiresAt.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		AbsoluteExpiresAt: sess.AbsoluteExpiresAt.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		RevokedAt:         formatTimePtr(sess.RevokedAt),
	}
	if sess.ID.String() == currentSessionID {
		resp.Current = true
	}
	return resp
}

// sessionListResponse is the GET /v1/auth/sessions body.
type sessionListResponse struct {
	Sessions []sessionResponse `json:"sessions"`
}

// revokedAllResponse is the DELETE /v1/auth/sessions body.
type revokedAllResponse struct {
	Revoked int `json:"revoked"`
}

// clientIP resolves the caller IP for audit rows and rate-limit keys.
// X-Forwarded-For is honoured only when the service was built with
// WithTrustForwarded (i.e. it sits behind a trusted proxy); otherwise the
// header is attacker-controlled and the connection RemoteAddr is used.
func (s *Service) clientIP(r *http.Request) string {
	if s.trustForwarded {
		if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); fwd != "" {
			if first := strings.SplitN(fwd, ",", 2)[0]; first != "" {
				return strings.TrimSpace(first)
			}
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
// endpoints plus the protected profile, session-management and admin
// listing routes. Protected routes rely on platform.AuthMiddleware having
// populated the request context (mounted by the service binary) and
// enforce authn/authz here. limiter (optional) throttles the public auth
// endpoints per client IP — argon2id verification is expensive, so
// unthrottled auth is also a CPU/memory DoS vector (hardening issue #81).
// Either limiter backend (in-process or Redis, issue #87) satisfies
// platform.AuthLimiter. Pass nil to disable.
func Routes(r chi.Router, svc *Service, limiter platform.AuthLimiter) {
	throttle := func(h http.HandlerFunc) http.HandlerFunc { return h }
	if limiter != nil {
		limiterMW := limiter.Middleware(func(r *http.Request) string {
			return svc.clientIP(r)
		})
		throttle = func(h http.HandlerFunc) http.HandlerFunc {
			return limiterMW(h).ServeHTTP
		}
	}
	r.Post("/v1/auth/register", throttle(handleRegister(svc)))
	r.Post("/v1/auth/login", throttle(handleLogin(svc)))
	r.Post("/v1/auth/refresh", throttle(handleRefresh(svc)))
	r.Post("/v1/auth/logout", throttle(handleLogout(svc)))
	// Verification flow (issue #86): public token consumption and resend.
	r.Post("/v1/auth/verify", throttle(handleVerify(svc)))
	r.Post("/v1/auth/verification/resend", throttle(handleVerificationResend(svc)))
	// Authenticated password change (issue #86); throttled before the
	// auth check so unauthenticated spam still hits the limiter.
	r.Post("/v1/auth/password", platform.RequireAuthenticated(throttle(handleChangePassword(svc))))
	// Session management API (issue #87): visibility and revocation control
	// over the caller's refresh sessions. The {sessionID} route must be
	// registered before the bare collection DELETE so chi keeps both.
	r.Get("/v1/auth/sessions", platform.RequireAuthenticated(handleListSessions(svc)))
	r.Delete("/v1/auth/sessions/{sessionID}", platform.RequireAuthenticated(handleRevokeSession(svc)))
	r.Delete("/v1/auth/sessions", platform.RequireAuthenticated(handleRevokeAllSessions(svc)))
	// JWKS (ADR-0007, issue #87): the issuer's PUBLIC signing keys, the only
	// trust anchor verifying services need. Public by design — it carries no
	// secret material.
	r.Get("/.well-known/jwks.json", handleJWKS(svc))
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
		resp, err := svc.Register(r.Context(), req.Email, req.Phone, req.Password, req.FullName, req.DeviceName, svc.clientIP(r))
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
		resp, err := svc.Login(r.Context(), req.Email, req.Password, req.DeviceName, svc.clientIP(r))
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

// handleVerify consumes a single-use verification token and marks the
// owning channel verified (200 with the channel).
func handleVerify(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, err := platform.DecodeJSON[verifyRequest](r, maxAuthBodyBytes)
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		channel, err := svc.Verify(r.Context(), req.Token)
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"channel": string(channel)})
	}
}

// handleResendVerificationResponse is the 202 body of the resend endpoint.
// Token/expires_at are populated only by the dev-only echo.
type handleResendVerificationResponse struct {
	Channel   string     `json:"channel"`
	Token     string     `json:"token,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// handleVerificationResend re-issues a verification token within the
// cooldown + limit policy (202; the body is neutral for unknown emails so
// the endpoint cannot enumerate accounts).
func handleVerificationResend(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, err := platform.DecodeJSON[resendVerificationRequest](r, maxAuthBodyBytes)
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		channel, echo, err := svc.ResendVerification(r.Context(), req.Email, req.Channel)
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		resp := handleResendVerificationResponse{Channel: string(channel)}
		if echo != nil {
			resp.Token = echo.Token
			resp.ExpiresAt = &echo.ExpiresAt
		}
		writeJSON(w, http.StatusAccepted, resp)
	}
}

// handleChangePassword rotates the authenticated caller's password after a
// successful current-password check and breach policy (204). Every live
// session of the user is revoked.
func handleChangePassword(svc *Service) http.HandlerFunc {
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
		req, err := platform.DecodeJSON[changePasswordRequest](r, maxAuthBodyBytes)
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		if err := svc.ChangePassword(r.Context(), userID, req.CurrentPassword, req.NewPassword); err != nil {
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

// handleListSessions returns the authenticated caller's refresh sessions
// (200). Revoked sessions are included (they render as "signed out"
// devices); the caller's current session carries current=true. The
// response never contains credential material.
func handleListSessions(svc *Service) http.HandlerFunc {
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
		sessions, err := svc.ListSessions(r.Context(), userID)
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		out := make([]sessionResponse, 0, len(sessions))
		for _, sess := range sessions {
			out = append(out, newSessionResponse(sess, claims.SessionID))
		}
		writeJSON(w, http.StatusOK, sessionListResponse{Sessions: out})
	}
}

// handleRevokeSession revokes the session with the {sessionID} path
// parameter (204). Unknown ids and other users' sessions are the same 404.
// Audited.
func handleRevokeSession(svc *Service) http.HandlerFunc {
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
		sessionID, err := uuid.Parse(chi.URLParam(r, "sessionID"))
		if err != nil {
			platform.WriteError(w, platform.ErrValidation("sessionID must be a UUID",
				platform.FieldError{Field: "sessionID", Issue: "invalid"}))
			return
		}
		if err := svc.RevokeSession(r.Context(), userID, sessionID, userID); err != nil {
			platform.WriteError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleRevokeAllSessions revokes every live session of the authenticated
// caller — sign-out everywhere, including the calling session — and
// returns the revoked count (200). Audited with the count.
func handleRevokeAllSessions(svc *Service) http.HandlerFunc {
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
		revoked, err := svc.RevokeAllSessions(r.Context(), userID, userID)
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, revokedAllResponse{Revoked: revoked})
	}
}

// handleJWKS serves the issuer's public key set (ADR-0007): the only
// trust anchor verifying services hold. Cacheable briefly; rotation is
// picked up by the validators' unknown-kid refresh.
func handleJWKS(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		keys, ok := svc.issuer.JWKS()
		if !ok {
			platform.WriteError(w, platform.ErrNotFound("no JWKS key set is configured (legacy HS256 signing)"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(keys)
	}
}

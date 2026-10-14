package identity

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// Service is the identity domain's application service. It composes the
// SessionStore persistence contract with the token Issuer and guarantees
// that every authentication-relevant state change is audited; a failed
// audit record fails the request. Successful registration and role grants
// additionally publish domain events through the platform Publisher
// (ADR-0002); publisher may be nil (tests, offline tooling, deployments
// without MOTIVRA_NATS_URL), in which case publishing is skipped.
type Service struct {
	store          SessionStore
	issuer         *Issuer
	publisher      platform.Publisher
	trustForwarded bool

	// breacher implements the ADR-0004 breached-password check on every NEW
	// password (registration, password change), before hashing. nil disables
	// the check (unit tests, tooling); production always wires one.
	breacher PasswordBreacher
	// breachFailClosed selects the availability policy when the breacher
	// errors: false fails open with an audit note, true refuses the request.
	breachFailClosed bool
	// verification carries the email/phone verification flow settings
	// (token TTL, resend cooldown/limit, dev echo).
	verification VerificationConfig
}

// NewService builds a Service over the given store, token issuer and event
// publisher. Options tweak deployment-sensitive behaviour.
func NewService(store SessionStore, issuer *Issuer, publisher platform.Publisher, opts ...ServiceOption) *Service {
	s := &Service{
		store:        store,
		issuer:       issuer,
		publisher:    publisher,
		verification: DefaultVerificationConfig(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// ServiceOption customises a Service at construction.
type ServiceOption func(*Service)

// WithTrustForwarded makes client-IP resolution honour X-Forwarded-For.
// Enable only behind a trusted proxy; direct exposure lets callers spoof
// the IP recorded in audit rows and used as the rate-limit key (ADR-0004,
// hardening issue #81).
func WithTrustForwarded(trust bool) ServiceOption {
	return func(s *Service) { s.trustForwarded = trust }
}

// WithPasswordBreacher wires the ADR-0004 breached-password check (issue
// #86) onto every new password, before hashing. A nil breacher disables
// the check. failClosed selects the policy when the checker errors: false
// fails open and records an identity.password.breach_check_unavailable
// audit note; true refuses the request while the check is unavailable.
func WithPasswordBreacher(breacher PasswordBreacher, failClosed bool) ServiceOption {
	return func(s *Service) {
		s.breacher = breacher
		s.breachFailClosed = failClosed
	}
}

// WithVerification installs the email/phone verification flow settings
// (issue #86).
func WithVerification(cfg VerificationConfig) ServiceOption {
	return func(s *Service) { s.verification = cfg }
}

// emailPattern is a pragmatic RFC-5321-shaped address check: one local part,
// one @, dot-separated domain with an alphabetic TLD of two or more chars.
var emailPattern = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

// maxEmailLength is the RFC 5321 limit for a forward- or reverse-path.
const maxEmailLength = 254

// Pagination bounds for the admin user listing.
const (
	defaultListUsersLimit = 20
	maxListUsersLimit     = 100
)

// invalidLogin is the single error returned for unknown emails and wrong
// passwords alike, so responses never reveal which failed.
func invalidLogin() *platform.Error {
	return platform.ErrUnauthorized("invalid email or password")
}

// checkPasswordBreach runs the breached-password policy for a NEW password
// (ADR-0004 breach-list checking, issue #86). It returns the fail-open
// audit note (empty when the check completed cleanly or was skipped) so
// the caller can record the degradation after it knows the acting user.
// Passwords outside the configured length bounds are not checked here —
// HashPassword rejects them first, and a doomed length never justifies a
// network round trip.
func (s *Service) checkPasswordBreach(ctx context.Context, password string) (string, error) {
	if s.breacher == nil || len(password) < MinPasswordLength || len(password) > MaxPasswordLength {
		return "", nil
	}
	breached, err := s.breacher.PasswordBreached(ctx, password)
	if err != nil {
		if s.breachFailClosed {
			return "", &platform.Error{
				Status: http.StatusServiceUnavailable,
				Code:   "breach_check_unavailable",
				Title:  "Password breach check unavailable",
				Detail: "the password could not be checked against breach lists; try again later",
			}
		}
		// Fail-open WITH an audit note: the caller records the degradation
		// once the acting user is known.
		return fmt.Sprintf("fail-open: breach check unavailable: %v", err), nil
	}
	if breached {
		return "", platform.ErrValidation(
			"password appears in a known breach or common-password corpus",
			platform.FieldError{Field: "password", Issue: "breached"})
	}
	return "", nil
}

// auditBreachNote records the fail-open degradation as its own audit row
// (object: the acting user). Audit failures propagate: signals are never
// dropped silently.
func (s *Service) auditBreachNote(ctx context.Context, userID uuid.UUID, note string) error {
	return s.audit(ctx, userID, ActionBreachCheckUnavailable, "user", userID.String(), map[string]any{
		"note": note,
	})
}

// Register validates the signup input, checks the password against the
// breach policy, hashes it, creates the user with an initial CUSTOMER
// role, issues single-use verification tokens for every present contact
// channel (issue #86) and issues the first token pair. Duplicate emails
// surface as platform.ErrConflict; the registration, breach degradation
// and verification issuances are audited.
func (s *Service) Register(ctx context.Context, email, phone, password, fullName, deviceName, ip string) (AccessResponse, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	phone = strings.TrimSpace(phone)
	fullName = strings.TrimSpace(fullName)

	fields := make([]platform.FieldError, 0, 3)
	if !emailPattern.MatchString(email) || len(email) > maxEmailLength {
		fields = append(fields, platform.FieldError{Field: "email", Issue: "invalid"})
	}
	if fullName == "" {
		fields = append(fields, platform.FieldError{Field: "full_name", Issue: "required"})
	}
	if len(fields) > 0 {
		return AccessResponse{}, platform.ErrValidation("the registration request is invalid", fields...)
	}
	// ADR-0004 breach check BEFORE hashing (issue #86): a known-breached
	// password is rejected without paying the argon2id cost.
	breachNote, err := s.checkPasswordBreach(ctx, password)
	if err != nil {
		return AccessResponse{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return AccessResponse{}, err
	}

	user := User{
		ID:           uuid.New(),
		Email:        email,
		Phone:        phone,
		FullName:     fullName,
		PasswordHash: hash,
		Status:       StatusActive,
		Roles:        []Role{RoleCustomer},
	}
	if err := s.store.CreateUser(ctx, &user); err != nil {
		return AccessResponse{}, err
	}
	if breachNote != "" {
		if err := s.auditBreachNote(ctx, user.ID, breachNote); err != nil {
			return AccessResponse{}, err
		}
	}
	resp, err := s.issuer.Issue(ctx, s.store, user, deviceName, ip)
	if err != nil {
		return AccessResponse{}, err
	}
	if err := s.audit(ctx, user.ID, ActionUserRegistered, "user", user.ID.String(), map[string]any{
		"email":       user.Email,
		"device_name": platform.RedactFreeText(deviceName),
		"ip_address":  ip,
	}); err != nil {
		return AccessResponse{}, err
	}
	// Verification tokens (issue #86): one per present channel. The raw
	// tokens are delivered out-of-band; without MOTIVRA_VERIFICATION_DEV_ECHO
	// they are only observable through that delivery channel.
	var echoes []VerificationEcho
	issueChannel := func(channel VerificationChannel) error {
		raw, expiresAt, err := s.issueVerificationToken(ctx, user, channel, "registration")
		if err != nil {
			return err
		}
		if s.verification.DevEcho {
			echoes = append(echoes, VerificationEcho{Channel: channel, Token: raw, ExpiresAt: expiresAt})
		}
		return nil
	}
	if err := issueChannel(ChannelEmail); err != nil {
		return AccessResponse{}, err
	}
	if user.Phone != "" {
		if err := issueChannel(ChannelPhone); err != nil {
			return AccessResponse{}, err
		}
	}
	resp.Verification = echoes
	// Every write of the registration succeeded (user row, session,
	// audit); the event marks that completed fact. The payload carries
	// IDs and state names only, never credential material. The
	// registrant is the actor of their own account creation.
	if err := s.publish(ctx, EventUserCreated, user.ID.String(), "", user.ID.String(), UserCreatedPayload{
		UserID: user.ID,
		Status: user.Status,
		Roles:  user.Roles,
	}); err != nil {
		return AccessResponse{}, err
	}
	return resp, nil
}

// Login authenticates an email/password pair and issues a fresh token pair.
// Unknown emails and wrong passwords return the identical unauthorized
// error; suspended or deleted accounts return a forbidden error only after
// the password has been verified.
func (s *Service) Login(ctx context.Context, email, password, deviceName, ip string) (AccessResponse, error) {
	user, err := s.store.GetUserByEmail(ctx, strings.TrimSpace(email))
	if err != nil {
		if isNotFound(err) {
			return AccessResponse{}, invalidLogin()
		}
		return AccessResponse{}, fmt.Errorf("lookup user: %w", err)
	}
	ok, err := VerifyPassword(password, user.PasswordHash)
	if err != nil {
		return AccessResponse{}, fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		return AccessResponse{}, invalidLogin()
	}
	if user.Status != StatusActive {
		return AccessResponse{}, platform.ErrForbidden("account is not active")
	}
	resp, err := s.issuer.Issue(ctx, s.store, user, deviceName, ip)
	if err != nil {
		return AccessResponse{}, err
	}
	if err := s.audit(ctx, user.ID, ActionUserLogin, "user", user.ID.String(), map[string]any{
		"device_name": platform.RedactFreeText(deviceName),
		"ip_address":  ip,
	}); err != nil {
		return AccessResponse{}, err
	}
	return resp, nil
}

// ChangePassword authenticates the caller with their current password,
// applies the breach policy to the new one (BEFORE hashing, issue #86),
// persists it and revokes every live session of the user — including the
// one that performed the change — so all devices re-authenticate with the
// new credential. The change and the session sweep are audited.
func (s *Service) ChangePassword(ctx context.Context, userID uuid.UUID, currentPassword, newPassword string) error {
	user, err := s.store.GetUser(ctx, userID)
	if err != nil {
		if isNotFound(err) {
			return platform.ErrNotFound("user not found")
		}
		return fmt.Errorf("load user: %w", err)
	}
	ok, err := VerifyPassword(currentPassword, user.PasswordHash)
	if err != nil {
		return fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		return platform.ErrUnauthorized("current password is incorrect")
	}

	// ADR-0004 breach check BEFORE hashing (issue #86). The actor is
	// known here, so a fail-open degradation is audited immediately.
	breachNote, err := s.checkPasswordBreach(ctx, newPassword)
	if err != nil {
		return err
	}
	if breachNote != "" {
		if err := s.auditBreachNote(ctx, user.ID, breachNote); err != nil {
			return err
		}
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := s.store.UpdatePassword(ctx, user.ID, hash); err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	revoked, err := s.store.RevokeUserSessions(ctx, user.ID)
	if err != nil {
		return fmt.Errorf("revoke user sessions: %w", err)
	}
	return s.audit(ctx, user.ID, ActionPasswordChanged, "user", user.ID.String(), map[string]any{
		"sessions_revoked": revoked,
	})
}

// RefreshTokens rotates the session behind the given opaque refresh token
// and returns a fresh pair. Reuse of a rotated token, unknown tokens,
// expired tokens and non-active users are rejected.
func (s *Service) RefreshTokens(ctx context.Context, refreshToken string) (AccessResponse, error) {
	resp, previous, err := s.issuer.Refresh(ctx, s.store, refreshToken)
	if err != nil {
		return AccessResponse{}, err
	}
	if err := s.audit(ctx, previous.UserID, ActionSessionRefreshed, "session", previous.ID.String(), map[string]any{
		"device_name": platform.RedactFreeText(previous.DeviceName),
		"ip_address":  previous.IPAddress,
	}); err != nil {
		return AccessResponse{}, err
	}
	return resp, nil
}

// Logout revokes the session with the given id and audits the revocation.
// actorID is the authenticated principal performing the logout (the session
// owner in the normal flow; the zero UUID records a system actor).
// Revoking an already-revoked session is idempotent; unknown sessions yield
// platform.ErrNotFound.
func (s *Service) Logout(ctx context.Context, sessionID, actorID uuid.UUID) error {
	if err := s.issuer.Revoke(ctx, s.store, sessionID); err != nil {
		if isNotFound(err) {
			return platform.ErrNotFound("session not found")
		}
		return err
	}
	return s.audit(ctx, actorID, ActionSessionRevoked, "session", sessionID.String(), nil)
}

// Profile returns the authenticated user without credential material:
// PasswordHash is always cleared on the returned copy.
func (s *Service) Profile(ctx context.Context, userID uuid.UUID) (User, error) {
	user, err := s.store.GetUser(ctx, userID)
	if err != nil {
		if isNotFound(err) {
			return User{}, platform.ErrNotFound("user not found")
		}
		return User{}, fmt.Errorf("load user: %w", err)
	}
	user.PasswordHash = ""
	return user, nil
}

// ListUsers returns a newest-first page of accounts for admin console use.
// limit is clamped to [1, 100] (default 20) and offset to >= 0. Credential
// material is stripped from every returned user.
func (s *Service) ListUsers(ctx context.Context, limit, offset int) ([]User, error) {
	if limit <= 0 {
		limit = defaultListUsersLimit
	}
	if limit > maxListUsersLimit {
		limit = maxListUsersLimit
	}
	if offset < 0 {
		offset = 0
	}
	users, err := s.store.ListUsers(ctx, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	for i := range users {
		users[i].PasswordHash = ""
	}
	return users, nil
}

// LogoutByRefreshToken revokes the session owning the given opaque refresh
// token and audits the revocation. Possession of the refresh token is the
// logout credential (public endpoint); actorID attributes the audit row
// when the caller is authenticated (zero UUID records a system actor).
// Unknown or already-revoked tokens yield the same unauthorized error so
// the endpoint never reveals session state.
func (s *Service) LogoutByRefreshToken(ctx context.Context, refreshToken string, actorID uuid.UUID) error {
	if strings.TrimSpace(refreshToken) == "" {
		return platform.ErrValidation("refresh_token is required",
			platform.FieldError{Field: "refresh_token", Issue: "required"})
	}
	sess, err := s.store.GetSessionByRefreshHash(ctx, s.issuer.RefreshTokenHash(refreshToken))
	if err != nil {
		if isNotFound(err) {
			return invalidLogin()
		}
		return fmt.Errorf("lookup session: %w", err)
	}
	if sess.RevokedAt != nil {
		return invalidLogin()
	}
	if err := s.store.RevokeSession(ctx, sess.ID); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return s.audit(ctx, actorID, ActionSessionRevoked, "session", sess.ID.String(), map[string]any{
		"via": "refresh_token",
	})
}

// AssignRole grants role to the user, optionally scoped to a tenant,
// records the privileged action in the audit trail and publishes
// role.granted.v1. The store write happens first; the audit row and the
// event both follow it, and any failure fails the request. Unknown roles
// and duplicate grants surface the store's validation and conflict errors.
// actorID is the authenticated principal performing the grant (the zero
// UUID records a system actor). There is no HTTP surface yet — the admin
// role-assignment route needs a contract addition (noted in PR for #28);
// this method is the service-layer seam that route will call.
func (s *Service) AssignRole(ctx context.Context, userID uuid.UUID, role Role, tenantID *uuid.UUID, actorID uuid.UUID) error {
	if err := s.store.AssignRole(ctx, userID, role, tenantID); err != nil {
		return err
	}
	if err := s.audit(ctx, actorID, ActionRoleGranted, "user", userID.String(), map[string]any{
		"role": string(role),
	}); err != nil {
		return err
	}
	tenant := ""
	if tenantID != nil {
		tenant = tenantID.String()
	}
	actor := ""
	if actorID != (uuid.UUID{}) {
		actor = actorID.String()
	}
	return s.publish(ctx, EventRoleGranted, userID.String(), tenant, actor, RoleGrantedPayload{
		UserID:   userID,
		Role:     role,
		TenantID: tenantID,
	})
}

// audit appends one audit entry attributed to actorID (the zero UUID
// records a nil, system actor); failures propagate.
func (s *Service) audit(ctx context.Context, actorID uuid.UUID, action, objectType, objectID string, metadata map[string]any) error {
	var actor *uuid.UUID
	if actorID != (uuid.UUID{}) {
		id := actorID
		actor = &id
	}
	entry := AuditEntry{
		ActorID:    actor,
		Action:     action,
		ObjectType: objectType,
		ObjectID:   objectID,
		Metadata:   metadata,
	}
	if err := RecordAudit(ctx, s.store, entry); err != nil {
		return fmt.Errorf("audit %s: %w", action, err)
	}
	return nil
}

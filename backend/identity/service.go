package identity

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// Service is the identity domain's application service. It composes the
// SessionStore persistence contract with the token Issuer and guarantees
// that every authentication-relevant state change is audited; a failed
// audit record fails the request.
type Service struct {
	store  SessionStore
	issuer *Issuer
}

// NewService builds a Service over the given store and token issuer.
func NewService(store SessionStore, issuer *Issuer) *Service {
	return &Service{store: store, issuer: issuer}
}

// emailPattern is a pragmatic RFC-5321-shaped address check: one local part,
// one @, dot-separated domain with an alphabetic TLD of two or more chars.
var emailPattern = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

// maxEmailLength is the RFC 5321 limit for a forward- or reverse-path.
const maxEmailLength = 254

// invalidLogin is the single error returned for unknown emails and wrong
// passwords alike, so responses never reveal which failed.
func invalidLogin() *platform.Error {
	return platform.ErrUnauthorized("invalid email or password")
}

// Register validates the signup input, hashes the password, creates the
// user with an initial CUSTOMER role and issues the first token pair.
// Duplicate emails surface as platform.ErrConflict; the registration and
// issuance are audited.
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
	resp, err := s.issuer.Issue(ctx, s.store, user, deviceName, ip)
	if err != nil {
		return AccessResponse{}, err
	}
	if err := s.audit(ctx, user.ID, ActionUserRegistered, "user", user.ID.String(), map[string]any{
		"email":       user.Email,
		"device_name": deviceName,
		"ip_address":  ip,
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
		"device_name": deviceName,
		"ip_address":  ip,
	}); err != nil {
		return AccessResponse{}, err
	}
	return resp, nil
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
		"device_name": previous.DeviceName,
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

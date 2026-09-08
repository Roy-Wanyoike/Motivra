package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// Token defaults (ADR-0004): short-lived HS256 access tokens, 30-day
// idle-capped refresh sessions, 256-bit opaque refresh tokens.
const (
	DefaultAccessTTL  = 15 * time.Minute
	DefaultRefreshTTL = 30 * 24 * time.Hour

	refreshTokenBytes = 32 // 256 bits of entropy
)

// AccessResponse is the credential pair returned by registration, login and
// refresh. RefreshToken is the opaque client token (base64url, 256 bits);
// only its SHA-256 digest is persisted.
type AccessResponse struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// Issuer creates sessions and signs HS256 access tokens. It is the first
// implementation of the issuer-agnostic token abstraction (ADR-0004):
// swapping to an external OIDC provider later changes this type only.
type Issuer struct {
	secret     []byte
	issuer     string
	audience   string
	accessTTL  time.Duration
	refreshTTL time.Duration
}

// NewIssuer builds an Issuer for the given shared HS256 secret, token issuer
// and audience, using the default 15-minute access TTL and 30-day refresh
// TTL. The secret must be distributed out-of-band to every validating
// service.
func NewIssuer(secret, issuer, audience string) *Issuer {
	return &Issuer{
		secret:     []byte(secret),
		issuer:     issuer,
		audience:   audience,
		accessTTL:  DefaultAccessTTL,
		refreshTTL: DefaultRefreshTTL,
	}
}

// Issue creates a new refresh session for user and returns a fresh
// access/refresh pair. The session records the device name and client IP.
// The access token carries sub, tenant_id (the tenant of the highest-
// priority org-scoped role grant, empty for personal accounts), role (the
// highest-priority role), roles (all roles), sid (session id), iss, aud,
// iat and exp.
func (i *Issuer) Issue(ctx context.Context, store SessionStore, user User, deviceName, ip string) (AccessResponse, error) {
	return i.issue(ctx, store, user, deviceName, ip)
}

// Refresh validates an opaque refresh token, rotates its session (revoking
// the presented row and creating a new one) and returns a fresh
// access/refresh pair. Unknown, revoked, expired tokens and tokens of
// non-active users all yield the same unauthorized error.
func (i *Issuer) Refresh(ctx context.Context, store SessionStore, refreshToken string) (AccessResponse, Session, error) {
	if refreshToken == "" {
		return AccessResponse{}, Session{}, platform.ErrUnauthorized("refresh token is invalid")
	}
	sum := RefreshTokenHash(refreshToken)
	sess, err := store.GetSessionByRefreshHash(ctx, sum)
	if err != nil {
		if isNotFound(err) {
			return AccessResponse{}, Session{}, platform.ErrUnauthorized("refresh token is invalid")
		}
		return AccessResponse{}, Session{}, fmt.Errorf("lookup session: %w", err)
	}
	if sess.RevokedAt != nil {
		return AccessResponse{}, Session{}, platform.ErrUnauthorized("refresh token is invalid")
	}
	if !time.Now().Before(sess.ExpiresAt) {
		return AccessResponse{}, Session{}, platform.ErrUnauthorized("refresh token is invalid")
	}
	user, err := store.GetUser(ctx, sess.UserID)
	if err != nil {
		if isNotFound(err) {
			return AccessResponse{}, Session{}, platform.ErrUnauthorized("refresh token is invalid")
		}
		return AccessResponse{}, Session{}, fmt.Errorf("load user: %w", err)
	}
	if user.Status != StatusActive {
		return AccessResponse{}, Session{}, platform.ErrForbidden("account is not active")
	}

	// Rotate first: revoke the presented session, then mint its successor.
	if err := store.RevokeSession(ctx, sess.ID); err != nil {
		return AccessResponse{}, Session{}, fmt.Errorf("revoke rotated session: %w", err)
	}
	resp, err := i.issue(ctx, store, user, sess.DeviceName, sess.IPAddress)
	if err != nil {
		return AccessResponse{}, Session{}, err
	}
	return resp, sess, nil
}

// Revoke revokes a session by id. Revoking an unknown session yields a
// not-found error; revoking an already-revoked session is idempotent.
func (i *Issuer) Revoke(ctx context.Context, store SessionStore, sessionID uuid.UUID) error {
	if err := store.RevokeSession(ctx, sessionID); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

// issue is the shared session-creation and signing path for Issue and Refresh.
func (i *Issuer) issue(ctx context.Context, store SessionStore, user User, deviceName, ip string) (AccessResponse, error) {
	tenantID, roles, err := i.claimContext(ctx, store, user)
	if err != nil {
		return AccessResponse{}, err
	}

	sessionID := uuid.New()
	refreshToken, err := newRefreshToken()
	if err != nil {
		return AccessResponse{}, err
	}
	now := time.Now()
	sess := Session{
		ID:               sessionID,
		UserID:           user.ID,
		RefreshTokenHash: RefreshTokenHash(refreshToken),
		DeviceName:       deviceName,
		IPAddress:        ip,
		ExpiresAt:        now.Add(i.refreshTTL),
	}
	if err := store.CreateSession(ctx, sess); err != nil {
		return AccessResponse{}, fmt.Errorf("create session: %w", err)
	}

	expiresAt := now.Add(i.accessTTL)
	claims := jwt.MapClaims{
		"iss":       i.issuer,
		"aud":       i.audience,
		"sub":       user.ID.String(),
		"sid":       sessionID.String(),
		"tenant_id": tenantID,
		"role":      HighestRole(roles),
		"roles":     roleStrings(roles),
		"iat":       now.Unix(),
		"exp":       expiresAt.Unix(),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(i.secret)
	if err != nil {
		return AccessResponse{}, fmt.Errorf("sign access token: %w", err)
	}
	return AccessResponse{AccessToken: signed, RefreshToken: refreshToken, ExpiresAt: expiresAt}, nil
}

// claimContext resolves the tenant_id and roles claims. When the store
// exposes the RoleGrantsProvider capability the claims reflect its current
// role grants (the authoritative assignments); otherwise the roles carried
// by the user record are used. The tenant is the tenant of the
// highest-priority org-scoped grant; personal grants contribute no tenant.
func (i *Issuer) claimContext(ctx context.Context, store SessionStore, user User) (string, []Role, error) {
	roles := user.Roles
	grants := []RoleGrant(nil)
	if provider, ok := store.(RoleGrantsProvider); ok {
		loaded, err := provider.GetRoleGrants(ctx, user.ID)
		if err != nil && !isNotFound(err) {
			return "", nil, fmt.Errorf("load role grants: %w", err)
		}
		if err == nil {
			grants = loaded
			roles = rolesFromGrants(loaded)
		}
	}

	best := -1
	tenant := ""
	for _, g := range grants {
		if g.TenantID == nil {
			continue
		}
		if p, ok := rolePriority[g.Role]; ok && p > best {
			best = p
			tenant = g.TenantID.String()
		}
	}
	return tenant, roles, nil
}

// roleStrings projects roles onto their string form for the roles claim.
func roleStrings(roles []Role) []string {
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		out = append(out, string(r))
	}
	return out
}

// RoleGrantsProvider is an optional SessionStore capability: the ability to
// list a user's role grants with their tenant scopes. PostgresStore
// implements it; the identity Issuer type-asserts it to resolve the
// tenant_id claim and degrades gracefully to a tenant-less claim when a
// store does not implement it.
type RoleGrantsProvider interface {
	// GetRoleGrants returns every role grant of the user.
	GetRoleGrants(ctx context.Context, userID uuid.UUID) ([]RoleGrant, error)
}

// newRefreshToken generates a 256-bit opaque refresh token, base64url-encoded.
func newRefreshToken() (string, error) {
	raw := make([]byte, refreshTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate refresh token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// RefreshTokenHash derives the hex-encoded SHA-256 digest stored in the
// sessions table for an opaque refresh token.
func RefreshTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

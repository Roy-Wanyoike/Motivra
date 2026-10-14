package identity

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// Token defaults (ADR-0004): short-lived asymmetric access tokens
// (EdDSA/RS256 by default — legacy HS256 only behind the migration flag,
// ADR-0007), 30-day idle-capped refresh sessions, 256-bit opaque refresh
// tokens, and the default post-registration window in which an unverified
// account may still mint sessions (issue #86 verification grace).
const (
	DefaultAccessTTL  = 15 * time.Minute
	DefaultRefreshTTL = 30 * 24 * time.Hour

	// DefaultVerificationGrace is the window after registration during
	// which an unverified account can still log in and rotate refresh
	// sessions. Once it ends, session minting is refused until every
	// present contact channel is verified (issue #86).
	DefaultVerificationGrace = 24 * time.Hour

	refreshTokenBytes = 32 // 256 bits of entropy
)

// AccessResponse is the credential pair returned by registration, login and
// refresh. RefreshToken is the opaque client token (base64url, 256 bits);
// only its HMAC-SHA256 digest is persisted. Verification is populated only
// by registration/resend responses and only when the dev-only token echo is
// enabled (MOTIVRA_VERIFICATION_DEV_ECHO) — production tokens are delivered
// out-of-band and never echo.
type AccessResponse struct {
	AccessToken  string             `json:"access_token"`
	RefreshToken string             `json:"refresh_token"`
	ExpiresAt    time.Time          `json:"expires_at"`
	Verification []VerificationEcho `json:"verification,omitempty"`
}

// Issuer creates sessions and signs access tokens. Since ADR-0007 (issue
// #87) signing is asymmetric by default: a platform.JWTSigner (EdDSA or
// RS256, kid-stamped) is injected via WithSigner and the Issuer exposes its
// public half through JWKS(). Without a signer the Issuer falls back to
// the legacy HS256 shared-secret path — kept ONLY for the migration window
// and tests; production identity boots refuse it (cmd/identity). It is the
// first implementation of the issuer-agnostic token abstraction
// (ADR-0004): swapping to an external OIDC provider later changes this
// type only.
type Issuer struct {
	secret            []byte
	signer            *platform.JWTSigner
	issuer            string
	audience          string
	accessTTL         time.Duration
	refreshTTL        time.Duration
	absoluteTTL       time.Duration
	verificationGrace time.Duration
}

// IssuerOption customises an Issuer at construction.
type IssuerOption func(*Issuer)

// WithVerificationGrace sets the post-registration window during which an
// unverified account may still obtain refresh sessions (issue #86). A value
// of zero or less disables the gate entirely (documented escape hatch for
// deployments that opt out of verification enforcement); the default is
// DefaultVerificationGrace.
func WithVerificationGrace(grace time.Duration) IssuerOption {
	return func(i *Issuer) {
		if grace > 0 {
			i.verificationGrace = grace
			return
		}
		i.verificationGrace = 0
	}
}

// WithSigner installs the asymmetric token signer (ADR-0007). When set,
// every access token is signed with its private key and stamped with its
// kid; the public half is served at /.well-known/jwks.json. A nil signer
// keeps the legacy HS256 path (migration window / tests).
func WithSigner(signer *platform.JWTSigner) IssuerOption {
	return func(i *Issuer) { i.signer = signer }
}

// NewIssuer builds an Issuer for the given opaque-token digest key, token
// issuer and audience, using the default 15-minute access TTL, 30-day
// refresh TTL, 90-day absolute session cap and 24-hour verification
// grace. The secret keys the HMAC digests of opaque tokens (refresh
// sessions, verification tokens) so a database leak alone cannot be
// cross-checked against guessed tokens (ADR-0004); it is shared with
// NO verifier service in the asymmetric posture.
func NewIssuer(secret, issuer, audience string, opts ...IssuerOption) *Issuer {
	i := &Issuer{
		secret:            []byte(secret),
		issuer:            issuer,
		audience:          audience,
		accessTTL:         DefaultAccessTTL,
		refreshTTL:        DefaultRefreshTTL,
		absoluteTTL:       platform.DefaultAbsoluteSessionTTL,
		verificationGrace: DefaultVerificationGrace,
	}
	for _, opt := range opts {
		opt(i)
	}
	return i
}

// Issue creates a new refresh session for user and returns a fresh
// access/refresh pair. The session records the device name and client IP.
// The access token carries sub, tenant_id (the tenant of the highest-
// priority org-scoped role grant, empty for personal accounts), role (the
// highest-priority role), roles (all roles), sid (session id), iss, aud,
// iat and exp.
func (i *Issuer) Issue(ctx context.Context, store SessionStore, user User, deviceName, ip string) (AccessResponse, error) {
	return i.issue(ctx, store, user, deviceName, ip, time.Time{})
}

// Refresh validates an opaque refresh token, rotates its session (revoking
// the presented row and creating a new one) and returns a fresh
// access/refresh pair. Unknown, revoked, expired tokens, tokens of
// non-active users and tokens of accounts that stayed unverified past the
// verification grace window all yield the same rejection semantics.
func (i *Issuer) Refresh(ctx context.Context, store SessionStore, refreshToken string) (AccessResponse, Session, error) {
	if refreshToken == "" {
		return AccessResponse{}, Session{}, platform.ErrUnauthorized("refresh token is invalid")
	}
	sum := i.RefreshTokenHash(refreshToken)
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
	// Absolute cap: rotation extends the idle expiry but never this bound,
	// so every session ends at most absoluteTTL after its first login
	// (hardening issue #81).
	if !time.Now().Before(sess.AbsoluteExpiresAt) {
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
	// Verification gate (issue #86), checked BEFORE rotation: refusing
	// after revoking the presented session would destroy the user's only
	// live session. The same predicate runs again in issue() so fresh
	// logins are gated too.
	if verificationRefused(time.Now(), user, i.verificationGrace) {
		return AccessResponse{}, Session{}, platform.ErrForbidden(
			"account verification is required; the post-registration grace window has ended")
	}

	// Rotate first: revoke the presented session, then mint its successor.
	if err := store.RevokeSession(ctx, sess.ID); err != nil {
		return AccessResponse{}, Session{}, fmt.Errorf("revoke rotated session: %w", err)
	}
	resp, err := i.issue(ctx, store, user, sess.DeviceName, sess.IPAddress, sess.AbsoluteExpiresAt)
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

// verificationRefused reports whether minting any new session for user
// (fresh login or rotation) must be refused because the account is still
// unverified and the post-registration grace window measured from
// user.CreatedAt has ended (issue #86). grace <= 0 disables the gate.
func verificationRefused(now time.Time, user User, grace time.Duration) bool {
	if grace <= 0 {
		return false
	}
	if user.verificationSatisfied() {
		return false
	}
	return !now.Before(user.CreatedAt.Add(grace))
}

// issue is the shared session-creation and signing path for Issue and
// Refresh. absolute is the non-extendable session bound: zero for fresh
// logins (now+absoluteTTL), the predecessor's value across rotations.
func (i *Issuer) issue(ctx context.Context, store SessionStore, user User, deviceName, ip string, absolute time.Time) (AccessResponse, error) {
	// Verification gate (issue #86): every session-minting path funnels
	// through here, so login, registration and rotation all enforce it.
	if verificationRefused(time.Now(), user, i.verificationGrace) {
		return AccessResponse{}, platform.ErrForbidden(
			"account verification is required; the post-registration grace window has ended")
	}
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
	if absolute.IsZero() {
		absolute = now.Add(i.absoluteTTL)
	}
	sess := Session{
		ID:                sessionID,
		UserID:            user.ID,
		RefreshTokenHash:  i.RefreshTokenHash(refreshToken),
		DeviceName:        deviceName,
		IPAddress:         ip,
		ExpiresAt:         now.Add(i.refreshTTL),
		AbsoluteExpiresAt: absolute,
	}
	if err := store.CreateSession(ctx, sess); err != nil {
		return AccessResponse{}, fmt.Errorf("create session: %w", err)
	}

	expiresAt := now.Add(i.accessTTL)
	claims := platform.AccessClaims(platform.Claims{
		UserID:    user.ID.String(),
		TenantID:  tenantID,
		Role:      HighestRole(roles),
		SessionID: sessionID.String(),
		IssuedAt:  now,
		ExpiresAt: expiresAt,
	}, roleStrings(roles), i.issuer, i.audience)
	signed, err := i.sign(claims)
	if err != nil {
		return AccessResponse{}, fmt.Errorf("sign access token: %w", err)
	}
	return AccessResponse{AccessToken: signed, RefreshToken: refreshToken, ExpiresAt: expiresAt}, nil
}

// sign produces the compact access token: through the asymmetric signer
// (kid-stamped EdDSA/RS256, ADR-0007) when installed, else the legacy
// HS256 shared-secret path.
func (i *Issuer) sign(claims jwt.MapClaims) (string, error) {
	if i.signer != nil {
		return i.signer.Sign(claims)
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(i.secret)
}

// JWKS returns the JSON Web Key Set advertising the signer's PUBLIC key —
// and nothing else. ok is false (and the payload empty) when the Issuer
// runs the legacy HS256 path, in which case there is no key set to serve.
func (i *Issuer) JWKS() (json.RawMessage, bool) {
	if i == nil || i.signer == nil {
		return nil, false
	}
	return i.signer.JWKS(), true
}

// SigningAlgorithm reports the algorithm access tokens are signed with
// ("EdDSA", "RS256" or the legacy "HS256"); used by boot logs and tests.
func (i *Issuer) SigningAlgorithm() string {
	if i.signer != nil {
		return i.signer.Algorithm()
	}
	return "HS256"
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

// newOpaqueToken generates a 256-bit opaque token, base64url-encoded. It is
// the shared generator for refresh tokens and single-use verification
// tokens (issue #86).
func newOpaqueToken() (string, error) {
	raw := make([]byte, refreshTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate opaque token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// newRefreshToken generates a 256-bit opaque refresh token,
// base64url-encoded.
func newRefreshToken() (string, error) {
	return newOpaqueToken()
}

// TokenHash derives the hex-encoded HMAC-SHA256 digest stored for an opaque
// token — refresh sessions and single-use verification tokens alike
// (ADR-0004: tokens are never persisted in plaintext; the digest is keyed
// with the issuer secret so a database leak alone cannot be cross-checked
// against guessed tokens).
func (i *Issuer) TokenHash(token string) string {
	mac := hmac.New(sha256.New, i.secret)
	_, _ = mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

// RefreshTokenHash derives the hex-encoded HMAC-SHA256 digest stored in the
// sessions table for an opaque refresh token (ADR-0004).
func (i *Issuer) RefreshTokenHash(token string) string {
	return i.TokenHash(token)
}

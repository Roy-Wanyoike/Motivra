package platform

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims are the Motivra access-token claims every service understands.
// Subject carries the user ID; tenant_id is empty for personal accounts.
type Claims struct {
	UserID    string
	TenantID  string
	Role      string
	SessionID string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// JWTValidator validates access tokens issued by the identity service.
// It is issuer-agnostic by configuration (ADR-0004): swapping to an
// external OIDC provider later changes only this constructor, not its
// consumers.
type JWTValidator struct {
	secret   []byte
	issuer   string
	audience string
	leeway   time.Duration
}

// NewJWTValidator builds a validator for the given shared secret, expected
// issuer and audience.
func NewJWTValidator(secret, issuer, audience string) *JWTValidator {
	return &JWTValidator{
		secret:   []byte(secret),
		issuer:   issuer,
		audience: audience,
		leeway:   30 * time.Second,
	}
}

// Validate parses and verifies signature, expiry, issuer and audience,
// returning the platform Claims on success.
func (v *JWTValidator) Validate(tokenString string) (Claims, error) {
	if strings.TrimSpace(tokenString) == "" {
		return Claims{}, fmt.Errorf("token is empty")
	}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithIssuer(v.issuer),
		jwt.WithAudience(v.audience),
		jwt.WithLeeway(v.leeway),
		jwt.WithExpirationRequired(),
	)
	var raw jwt.MapClaims
	if _, err := parser.ParseWithClaims(tokenString, &raw, func(t *jwt.Token) (any, error) {
		return v.secret, nil
	}); err != nil {
		return Claims{}, fmt.Errorf("invalid token: %w", err)
	}

	claims := Claims{
		UserID:    stringClaim(raw, "sub"),
		TenantID:  stringClaim(raw, "tenant_id"),
		Role:      stringClaim(raw, "role"),
		SessionID: stringClaim(raw, "sid"),
	}
	if claims.UserID == "" {
		return Claims{}, fmt.Errorf("invalid token: missing subject")
	}
	if exp, err := raw.GetExpirationTime(); err == nil && exp != nil {
		claims.ExpiresAt = exp.Time
	}
	if iat, err := raw.GetIssuedAt(); err == nil && iat != nil {
		claims.IssuedAt = iat.Time
	}
	return claims, nil
}

func stringClaim(raw jwt.MapClaims, key string) string {
	if v, ok := raw[key].(string); ok {
		return v
	}
	return ""
}

// ClaimsFromContext returns the authenticated claims stored by the auth
// middleware and whether they exist.
func ClaimsFromContext(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(ctxKeyClaims).(Claims)
	return c, ok
}

// ctxKeyClaims is the context key under which the auth middleware stores
// validated claims.
const ctxKeyClaims ctxKey = 20

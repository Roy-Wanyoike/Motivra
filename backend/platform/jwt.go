package platform

import (
	"context"
	"crypto"
	"errors"
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

// Signing algorithms accepted by the platform, and the access-token claims
// builder shared by the issuer (identity) and by tooling.
const (
	// AlgorithmEdDSA is Ed25519 signing (the default key type, ADR-0007).
	AlgorithmEdDSA = "EdDSA"
	// AlgorithmRS256 is RSA PKCS#1 v1.5 signing (interop option, ADR-0007).
	AlgorithmRS256 = "RS256"
	// AlgorithmHS256 is the legacy shared-secret algorithm. It is accepted
	// only when explicitly configured (migration window, ADR-0007) — a
	// validator built from public keys alone can NEVER verify (let alone
	// produce) HS256 tokens, which is what makes verifier compromise
	// non-forging (issue #87).
	AlgorithmHS256 = "HS256"

	// jwtKIDHeader is the JOSE header carrying the key id.
	jwtKIDHeader = "kid"
)

// AccessClaims builds the canonical access-token claim set: the identity
// Issuer signs exactly this shape, and the shape is deliberately
// centralised so issuer and verifiers cannot drift. roles is the caller's
// full role list (the platform Claims projection carries only the
// highest-priority role).
func AccessClaims(c Claims, roles []string, issuer, audience string) jwt.MapClaims {
	return jwt.MapClaims{
		"iss":       issuer,
		"aud":       audience,
		"sub":       c.UserID,
		"sid":       c.SessionID,
		"tenant_id": c.TenantID,
		"role":      c.Role,
		"roles":     roles,
		"iat":       c.IssuedAt.Unix(),
		"exp":       c.ExpiresAt.Unix(),
	}
}

// asymmetricAlgorithms lists the accepted non-symmetric algorithms.
var asymmetricAlgorithms = []string{AlgorithmEdDSA, AlgorithmRS256}

// JWTValidator validates access tokens issued by the identity service.
// Since ADR-0007 (issue #87) it is key-source-based:
//
//   - asymmetric (target posture): EdDSA/RS256 public keys held via a JWKS
//     endpoint and/or a distributed public-key file, selected by the token
//     `kid` header — the validator provably holds NO signing material;
//   - symmetric (migration window only): the pre-ADR HS256 shared secret,
//     accepted only when explicitly enabled (MOTIVRA_JWT_ALLOW_HS256).
//
// It is issuer-agnostic by configuration (ADR-0004): swapping to an
// external OIDC provider later changes only this constructor, not its
// consumers.
type JWTValidator struct {
	issuer   string
	audience string
	leeway   time.Duration

	// secret is the legacy HS256 shared secret; nil unless explicitly
	// configured for the migration window (or by the legacy constructor).
	secret []byte

	// staticKeys are public keys from the distributed key set (files),
	// selected by kid. They win over JWKS keys on kid collision.
	staticKeys map[string]crypto.PublicKey

	// jwks is the remote key-set cache; nil when no JWKS URL is configured.
	jwks *jwksCache
}

// NewJWTValidator builds the LEGACY HS256 validator for the given shared
// secret, expected issuer and audience. It remains for the migration
// window and for tests that mint HS256 tokens directly; new deployments
// configure asymmetric verification via NewValidatorFromConfig.
func NewJWTValidator(secret, issuer, audience string) *JWTValidator {
	return &JWTValidator{
		secret:   []byte(secret),
		issuer:   issuer,
		audience: audience,
		leeway:   30 * time.Second,
	}
}

// ValidatorOption customises an asymmetric validator at construction.
type ValidatorOption func(*JWTValidator)

// WithValidatorIssuerAudience pins the expected iss/aud claims.
func WithValidatorIssuerAudience(issuer, audience string) ValidatorOption {
	return func(v *JWTValidator) { v.issuer, v.audience = issuer, audience }
}

// WithJWKSURL verifies EdDSA/RS256 tokens against public keys fetched from
// the identity service's /.well-known/jwks.json (rotation-aware: unknown
// kids trigger a bounded refetch; see jwksCache).
func WithJWKSURL(url string) ValidatorOption {
	return func(v *JWTValidator) { v.jwks = newJWKSCache(url) }
}

// WithJWKSRefreshGap overrides the anti-stampede gap between unknown-kid
// refetches (default 1s). Zero is legitimate (test rigs); it only bounds
// refetches triggered by UNSEEN kids.
func WithJWKSRefreshGap(d time.Duration) ValidatorOption {
	return func(v *JWTValidator) {
		if v.jwks != nil {
			v.jwks.minRefresh = d
		}
	}
}

// WithPublicKeyPEM adds one static Ed25519/RSA public key (distributed key
// set) selected by kid. Use when verifiers cannot reach the issuer's JWKS.
func WithPublicKeyPEM(kid string, pub crypto.PublicKey) ValidatorOption {
	return func(v *JWTValidator) {
		if v.staticKeys == nil {
			v.staticKeys = map[string]crypto.PublicKey{}
		}
		v.staticKeys[kid] = pub
	}
}

// WithLegacyHS256 enables HS256 acceptance with the given shared secret in
// ADDITION to any asymmetric keys — the migration-window escape hatch
// (MOTIVRA_JWT_ALLOW_HS256). Never enabled by any default.
func WithLegacyHS256(secret string) ValidatorOption {
	return func(v *JWTValidator) { v.secret = []byte(secret) }
}

// NewValidator builds a validator from options. Callers must configure at
// least one key source (JWKS, public key, or the explicit legacy secret).
func NewValidator(opts ...ValidatorOption) *JWTValidator {
	v := &JWTValidator{leeway: 30 * time.Second}
	for _, opt := range opts {
		opt(v)
	}
	return v
}

// DefaultJWKSURL is the JWKS endpoint implied by the compose topology
// (service name `identity`, container port 8080). It is the default for
// non-test environments so a frozen compose file still yields the
// asymmetric posture with zero per-service configuration.
const DefaultJWKSURL = "http://identity:8080/.well-known/jwks.json"

// NewValidatorFromConfig builds the validator the service binaries use:
// asymmetric keys from cfg.JWT.JWKSURL / cfg.JWT.PublicKeyFile, with
// HS256 added only when the migration flag is set. In test environments
// the legacy HS256 secret alone keeps unit tests (which mint HS256 tokens
// directly) working; the JWKS default is NOT applied in test so tests
// never touch the network.
func NewValidatorFromConfig(cfg Config) (*JWTValidator, error) {
	jwtCfg := cfg.JWT
	opts := []ValidatorOption{WithValidatorIssuerAudience(jwtCfg.Issuer, jwtCfg.Audience)}

	configured := false
	if jwtCfg.PublicKeyFile != "" {
		pub, err := LoadJWTPublicKeyFromPEMFile(jwtCfg.PublicKeyFile)
		if err != nil {
			return nil, err
		}
		kid := deriveKIDorEmpty(pub)
		opts = append(opts, WithPublicKeyPEM(kid, pub))
		configured = true
	}
	if jwtCfg.JWKSURL != "" {
		opts = append(opts, WithJWKSURL(jwtCfg.JWKSURL))
		configured = true
	} else if cfg.App.Env != EnvTest {
		// Compose topology default (ADR-0007): the identity service is
		// reachable as http://identity:8080 from every service network.
		opts = append(opts, WithJWKSURL(DefaultJWKSURL))
		configured = true
	}
	if jwtCfg.AllowHS256 && jwtCfg.Secret != "" {
		opts = append(opts, WithLegacyHS256(jwtCfg.Secret))
		configured = true
	}
	if !configured && cfg.App.Env == EnvTest && jwtCfg.Secret != "" {
		// Tests keep the legacy constructor semantics.
		opts = append(opts, WithLegacyHS256(jwtCfg.Secret))
		configured = true
	}
	if !configured {
		return nil, errors.New("jwt validator: no key source configured (set MOTIVRA_JWT_JWKS_URL or MOTIVRA_JWT_PUBLIC_KEY_FILE, or enable the HS256 migration flag with a secret)")
	}
	return NewValidator(opts...), nil
}

// deriveKIDorEmpty returns the derived kid for pub, or "" when the key
// cannot be fingerprinted (the option then registers an unusable entry and
// validation fails loudly — callers should surface construction errors).
func deriveKIDorEmpty(pub crypto.PublicKey) string {
	kid, err := deriveKID(pub)
	if err != nil {
		return ""
	}
	return kid
}

// HasPrivateKeyMaterial reports whether this validator could EVER mint a
// token: a shared HS256 secret is signing material by definition. A
// validator built from JWKS/public keys only answers false — the
// test-proven "verifiers cannot sign" property of issue #87.
func (v *JWTValidator) HasPrivateKeyMaterial() bool {
	return v != nil && v.secret != nil
}

// ReadyCheck reports whether every configured key source is usable. It
// backs the /readyz "jwks" checker of verifier binaries: the cache is
// warmed proactively, then the health of the most recent fetch decides. A
// validator with no JWKS source is always ready.
func (v *JWTValidator) ReadyCheck(ctx context.Context) error {
	if v == nil || v.jwks == nil {
		return nil
	}
	v.jwks.ensure()
	if v.jwks.healthy() {
		return nil
	}
	return fmt.Errorf("jwks endpoint %s has not yielded any usable signing key", sanitizeJWKSLogURL(v.jwks.url))
}

// acceptedAlgorithms returns the JOSE algs this validator may verify.
func (v *JWTValidator) acceptedAlgorithms() []string {
	algs := make([]string, 0, 3)
	if len(v.staticKeys) > 0 || v.jwks != nil {
		algs = append(algs, asymmetricAlgorithms...)
	}
	if v.secret != nil {
		algs = append(algs, AlgorithmHS256)
	}
	return algs
}

// Validate parses and verifies signature, expiry, issuer and audience,
// returning the platform Claims on success. For asymmetric tokens the kid
// header selects the verification key; tokens without kid are refused
// when only asymmetric sources are configured.
func (v *JWTValidator) Validate(tokenString string) (Claims, error) {
	if strings.TrimSpace(tokenString) == "" {
		return Claims{}, fmt.Errorf("token is empty")
	}
	algs := v.acceptedAlgorithms()
	if len(algs) == 0 {
		return Claims{}, fmt.Errorf("invalid token: no verification keys configured")
	}
	parser := jwt.NewParser(
		jwt.WithValidMethods(algs),
		jwt.WithIssuer(v.issuer),
		jwt.WithAudience(v.audience),
		jwt.WithLeeway(v.leeway),
		jwt.WithExpirationRequired(),
	)
	var raw jwt.MapClaims
	if _, err := parser.ParseWithClaims(tokenString, &raw, func(t *jwt.Token) (any, error) {
		return v.keyFor(t)
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

// keyFor resolves the verification key for a parsed token: kid-selected
// public key for EdDSA/RS256, the shared secret for legacy HS256.
func (v *JWTValidator) keyFor(t *jwt.Token) (any, error) {
	kid, _ := t.Header[jwtKIDHeader].(string)
	switch t.Method.Alg() {
	case AlgorithmHS256:
		if v.secret == nil {
			return nil, fmt.Errorf("HS256 tokens are not accepted by this service (asymmetric keys only, ADR-0007)")
		}
		return v.secret, nil
	case AlgorithmEdDSA, AlgorithmRS256:
		if kid == "" {
			return nil, errors.New("token header is missing the kid claim")
		}
		if pub, ok := v.staticKeys[kid]; ok {
			if !algMatchesKey(t.Method.Alg(), pub) {
				return nil, fmt.Errorf("kid %q is not registered for algorithm %s", kid, t.Method.Alg())
			}
			return pub, nil
		}
		if v.jwks != nil {
			pub, err := v.jwks.lookup(kid, t.Method.Alg())
			if err != nil {
				return nil, err
			}
			return pub, nil
		}
		return nil, fmt.Errorf("unknown kid %q", kid)
	default:
		return nil, fmt.Errorf("unexpected algorithm %q", t.Method.Alg())
	}
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

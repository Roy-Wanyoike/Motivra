package platform

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"

	"github.com/golang-jwt/jwt/v5"
)

// JWTSigner is the asymmetric access-token signer of the identity service
// (issue #87, ADR-0007): it holds the ONLY private key material in the
// platform and signs EdDSA (Ed25519) or RS256 (RSA) tokens. Verifying
// services never receive this type — they hold public keys exclusively
// (JWTValidator via JWKS or a distributed public-key file).
//
// Every token carries a `kid` header identifying the signing key so
// verifiers can select the matching public key across rotations; the
// default kid is derived deterministically from the public key (first 16
// bytes of SHA-256 over the DER-encoded SPKI, hex), so the same key file
// always yields the same kid.
type JWTSigner struct {
	method    jwt.SigningMethod
	private   crypto.Signer
	public    crypto.PublicKey
	kid       string
	issuer    string
	audience  string
	jwksCache json.RawMessage
}

// SignerConfig carries the constructor inputs of a JWTSigner.
type SignerConfig struct {
	// Issuer and Audience are stamped into no claims by the signer itself;
	// they are kept for symmetry with the validator so tooling can compare
	// an issuer/verifier pair.
	Issuer   string
	Audience string
	// KeyID overrides the derived kid when non-empty.
	KeyID string
}

// GenerateJWTSigner creates an ephemeral Ed25519 signer. Intended for
// development and test bootstrapping ONLY: the key lives in memory and dies
// with the process, so JWKS consumers must re-fetch after restarts (they
// do — unknown-kid refresh and access-TTL expiry bound the disruption).
// Staging and production refuse ephemeral keys (NewSignerFromConfig).
func GenerateJWTSigner(cfg SignerConfig) (*JWTSigner, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519 key: %w", err)
	}
	return newJWTSigner(jwt.SigningMethodEdDSA, priv, pub, cfg)
}

// LoadJWTSignerFromPEM parses a PEM-encoded private key (Ed25519 or RSA:
// PKCS#8 "PRIVATE KEY" or PKCS#1 "RSA PRIVATE KEY") into a signer. The
// signing algorithm follows the key type: Ed25519 → EdDSA, RSA → RS256.
func LoadJWTSignerFromPEM(pemBytes []byte, cfg SignerConfig) (*JWTSigner, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("jwt signer: no PEM block found in private key material")
	}
	var (
		key crypto.Signer
		pub crypto.PublicKey
	)
	switch block.Type {
	case "PRIVATE KEY":
		parsed, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes)
		if parseErr != nil {
			return nil, fmt.Errorf("jwt signer: parse PKCS#8 private key: %w", parseErr)
		}
		switch k := parsed.(type) {
		case ed25519.PrivateKey:
			key, pub = k, k.Public()
		case *rsa.PrivateKey:
			key, pub = k, k.Public()
		default:
			return nil, fmt.Errorf("jwt signer: unsupported private key type %T (want Ed25519 or RSA)", parsed)
		}
	case "RSA PRIVATE KEY":
		parsed, parseErr := x509.ParsePKCS1PrivateKey(block.Bytes)
		if parseErr != nil {
			return nil, fmt.Errorf("jwt signer: parse PKCS#1 private key: %w", parseErr)
		}
		key, pub = parsed, parsed.Public()
	case "ED25519 PRIVATE KEY":
		parsed, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes)
		if parseErr != nil {
			return nil, fmt.Errorf("jwt signer: parse Ed25519 PKCS#8 private key: %w", parseErr)
		}
		k, ok := parsed.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("jwt signer: Ed25519 PEM block does not carry an Ed25519 key (%T)", parsed)
		}
		key, pub = k, k.Public()
	default:
		return nil, fmt.Errorf("jwt signer: unsupported PEM block type %q (want PRIVATE KEY or RSA PRIVATE KEY)", block.Type)
	}

	method, ok := signingMethodFor(pub)
	if !ok {
		return nil, fmt.Errorf("jwt signer: no signing method for key type %T", pub)
	}
	return newJWTSigner(method, key, pub, cfg)
}

// LoadJWTSignerFromPEMFile reads path and parses it as private key
// material (LoadJWTSignerFromPEM).
func LoadJWTSignerFromPEMFile(path string, cfg SignerConfig) (*JWTSigner, error) {
	raw, err := os.ReadFile(path) // never through a shared map: file is the secret surface
	if err != nil {
		return nil, fmt.Errorf("jwt signer: read private key file: %w", err)
	}
	return LoadJWTSignerFromPEM(raw, cfg)
}

// LoadJWTPublicKeyFromPEM parses a PEM "PUBLIC KEY" (SPKI) block carrying
// an Ed25519 or RSA public key. Verifier services use this to consume a
// distributed key set without ever touching private material.
func LoadJWTPublicKeyFromPEM(pemBytes []byte) (crypto.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("jwt public key: no PEM block found")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("jwt public key: parse SPKI: %w", err)
	}
	switch parsed.(type) {
	case ed25519.PublicKey, *rsa.PublicKey:
		return parsed, nil
	default:
		return nil, fmt.Errorf("jwt public key: unsupported key type %T (want Ed25519 or RSA)", parsed)
	}
}

// LoadJWTPublicKeyFromPEMFile reads path and parses it as public key
// material (LoadJWTPublicKeyFromPEM).
func LoadJWTPublicKeyFromPEMFile(path string) (crypto.PublicKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("jwt public key: read file: %w", err)
	}
	return LoadJWTPublicKeyFromPEM(raw)
}

// signingMethodFor maps a public key type onto the accepted JWT algorithm.
func signingMethodFor(pub crypto.PublicKey) (jwt.SigningMethod, bool) {
	switch pub.(type) {
	case ed25519.PublicKey:
		return jwt.SigningMethodEdDSA, true
	case *rsa.PublicKey:
		return jwt.SigningMethodRS256, true
	default:
		return nil, false
	}
}

// deriveKID fingerprints the public key: hex(SHA-256(SPKI DER))[:32]. The
// same key always yields the same kid across restarts and replicas, which
// is what makes rotation observable and cacheable.
func deriveKID(pub crypto.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("jwt signer: marshal public key: %w", err)
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:16]), nil
}

func newJWTSigner(method jwt.SigningMethod, priv crypto.Signer, pub crypto.PublicKey, cfg SignerConfig) (*JWTSigner, error) {
	kid := cfg.KeyID
	if kid == "" {
		derived, err := deriveKID(pub)
		if err != nil {
			return nil, err
		}
		kid = derived
	}
	s := &JWTSigner{
		method:   method,
		private:  priv,
		public:   pub,
		kid:      kid,
		issuer:   cfg.Issuer,
		audience: cfg.Audience,
	}
	s.jwksCache = marshalJWKS([]JWK{s.jwk()})
	return s, nil
}

// NewSignerFromConfig builds the signer the identity binary uses:
// PEM file when configured; ephemeral Ed25519 in development/test;
// refusal elsewhere (asymmetric signing is the target posture — an
// operator must provision a key file, or explicitly opt into the legacy
// HS256 migration path via MOTIVRA_JWT_ALLOW_HS256, which this function
// is not called for).
func NewSignerFromConfig(cfg Config) (*JWTSigner, error) {
	sc := SignerConfig{Issuer: cfg.JWT.Issuer, Audience: cfg.JWT.Audience, KeyID: cfg.JWT.KeyID}
	if cfg.JWT.PrivateKeyFile != "" {
		return LoadJWTSignerFromPEMFile(cfg.JWT.PrivateKeyFile, sc)
	}
	switch cfg.App.Env {
	case EnvDevelopment, EnvTest:
		return GenerateJWTSigner(sc)
	default:
		return nil, fmt.Errorf("MOTIVRA_JWT_PRIVATE_KEY_FILE is required in %s (asymmetric signing, ADR-0007); or set MOTIVRA_JWT_ALLOW_HS256=1 to opt into the documented legacy migration window", cfg.App.Env)
	}
}

// Method returns the JWT signing method (EdDSA or RS256).
func (s *JWTSigner) Method() jwt.SigningMethod { return s.method }

// Algorithm returns the JOSE alg header value ("EdDSA" or "RS256").
func (s *JWTSigner) Algorithm() string { return s.method.Alg() }

// KID returns the key id stamped into every token header.
func (s *JWTSigner) KID() string { return s.kid }

// PublicKey returns the public half of the signing key — the ONLY part a
// verifying service ever needs (and the only part identity's own
// protected-route validator receives).
func (s *JWTSigner) PublicKey() crypto.PublicKey { return s.public }

// Sign claims and returns the compact JWS with kid and typ headers set.
func (s *JWTSigner) Sign(claims jwt.MapClaims) (string, error) {
	if s == nil {
		return "", errors.New("jwt signer: signer is not configured")
	}
	token := jwt.NewWithClaims(s.method, claims)
	token.Header["kid"] = s.kid
	token.Header["typ"] = "JWT"
	signed, err := token.SignedString(s.private)
	if err != nil {
		return "", fmt.Errorf("jwt signer: sign: %w", err)
	}
	return signed, nil
}

// jwk renders this signer's public part as one JWK.
func (s *JWTSigner) jwk() JWK {
	return publicKeyToJWK(s.public, s.kid, s.method.Alg())
}

// JWKS returns the JSON Web Key Set document advertising this signer's
// PUBLIC key (and nothing else). Served verbatim by the identity service
// at /.well-known/jwks.json.
func (s *JWTSigner) JWKS() json.RawMessage {
	if s == nil {
		return json.RawMessage(`{"keys":[]}`)
	}
	return s.jwksCache
}

// bigIntBase64URL encodes a non-negative big.Int as unpadded base64url
// (RFC 7518 §6.2 for RSA n/e fields).
func bigIntBase64URL(i *big.Int) string {
	return base64.RawURLEncoding.EncodeToString(i.Bytes())
}

package platform

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

// ed25519PKCS8PEM renders an Ed25519 private key as the PKCS#8 PEM the
// signer loader accepts (the deployment format of ADR-0007 key files).
func ed25519PKCS8PEM(t *testing.T) (string, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), priv
}

func TestGenerateJWTSignerShape(t *testing.T) {
	t.Parallel()
	s, err := GenerateJWTSigner(SignerConfig{Issuer: "motivra-identity", Audience: "motivra"})
	require.NoError(t, err)
	require.Equal(t, AlgorithmEdDSA, s.Algorithm())
	require.NotEmpty(t, s.KID(), "a derived kid is always present")

	// The JWKS document carries the public key ONLY: no private material
	// can be reconstructed from it.
	var doc struct {
		Keys []map[string]string `json:"keys"`
	}
	require.NoError(t, json.Unmarshal(s.JWKS(), &doc))
	require.Len(t, doc.Keys, 1)
	key := doc.Keys[0]
	require.Equal(t, "OKP", key["kty"])
	require.Equal(t, "Ed25519", key["crv"])
	require.Equal(t, AlgorithmEdDSA, key["alg"])
	require.Equal(t, "sig", key["use"])
	require.Equal(t, s.KID(), key["kid"])
	require.NotContains(t, key, "d", "no private scalar field may appear (the Ed25519 JWK carries x only)")
}

// TestSignerKidDeterministic pins rotation ergonomics: the same key always
// yields the same kid (so verifiers' caches survive restarts), while the
// key file remains the single source of identity.
func TestSignerKidDeterministic(t *testing.T) {
	t.Parallel()
	pemText, _ := ed25519PKCS8PEM(t)
	a, err := LoadJWTSignerFromPEM([]byte(pemText), SignerConfig{})
	require.NoError(t, err)
	b, err := LoadJWTSignerFromPEM([]byte(pemText), SignerConfig{})
	require.NoError(t, err)
	require.Equal(t, a.KID(), b.KID())

	c, err := LoadJWTSignerFromPEM([]byte(pemText), SignerConfig{KeyID: "explicit-kid"})
	require.NoError(t, err)
	require.Equal(t, "explicit-kid", c.KID(), "an explicit MOTIVRA_JWT_KEY_ID wins")
}

// TestEd25519SignerRoundTrip signs with the loaded key and verifies with a
// public-key-only validator — the exact verifier posture of ADR-0007.
func TestEd25519SignerRoundTrip(t *testing.T) {
	t.Parallel()
	pemText, _ := ed25519PKCS8PEM(t)
	signer, err := LoadJWTSignerFromPEM([]byte(pemText), SignerConfig{Issuer: "motivra-identity", Audience: "motivra"})
	require.NoError(t, err)

	token, err := signer.Sign(AccessClaims(Claims{
		UserID:    "user-1",
		Role:      "CUSTOMER",
		SessionID: "sess-1",
		IssuedAt:  timeNow(),
		ExpiresAt: timeNow().Add(15 * time.Minute),
	}, []string{"CUSTOMER"}, "motivra-identity", "motivra"))
	require.NoError(t, err)
	require.NotEmpty(t, token)

	validator := NewValidator(
		WithValidatorIssuerAudience("motivra-identity", "motivra"),
		WithPublicKeyPEM(signer.KID(), signer.PublicKey()),
	)
	claims, err := validator.Validate(token)
	require.NoError(t, err)
	require.Equal(t, "user-1", claims.UserID)
	require.Equal(t, "sess-1", claims.SessionID)
	require.False(t, validator.HasPrivateKeyMaterial(),
		"a validator built from a public key carries no signing material")
}

// TestRSASignerRoundTrip proves the RS256 interop path end to end.
func TestRSASignerRoundTrip(t *testing.T) {
	t.Parallel()
	key, err := rsaGenerateForTest(2048)
	require.NoError(t, err)
	der := x509MarshalPKCS1ForTest(key)
	signer, err := LoadJWTSignerFromPEM(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}), SignerConfig{})
	require.NoError(t, err)
	require.Equal(t, AlgorithmRS256, signer.Algorithm())

	token, err := signer.Sign(jwt.MapClaims{"iss": "i", "aud": "a", "sub": "u", "exp": timeNow().Add(time.Minute).Unix()})
	require.NoError(t, err)

	validator := NewValidator(WithValidatorIssuerAudience("i", "a"), WithPublicKeyPEM(signer.KID(), signer.PublicKey()))
	claims, err := validator.Validate(token)
	require.NoError(t, err)
	require.Equal(t, "u", claims.UserID)
}

// TestLoadJWTPublicKeyRefusesPrivateKeyPEM: a verifier configured with the
// WRONG file (a private key) is refused at construction — misconfigurations
// must fail boot, not quietly widen trust.
func TestLoadJWTPublicKeyRefusesPrivateKeyPEM(t *testing.T) {
	t.Parallel()
	pemText, _ := ed25519PKCS8PEM(t)
	_, err := LoadJWTPublicKeyFromPEM([]byte(pemText))
	require.Error(t, err, "SPKI public-key parsing must not accept private key blocks")
}

// TestSignerRefusesGarbagePEM keeps the boot error legible.
func TestSignerRefusesGarbagePEM(t *testing.T) {
	t.Parallel()
	_, err := LoadJWTSignerFromPEM([]byte("not a pem"), SignerConfig{})
	require.Error(t, err)
	_, err = LoadJWTSignerFromPEM([]byte(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1}})), SignerConfig{})
	require.Error(t, err)
}

// TestNewSignerFromConfigRefusesEphemeralInProduction pins the ADR-0007
// boot posture: staging/production demand a provisioned key file.
func TestNewSignerFromConfigRefusesEphemeralInProduction(t *testing.T) {
	t.Parallel()
	cfg := LoadForTest()
	cfg.App.Env = EnvProduction
	_, err := NewSignerFromConfig(cfg)
	require.ErrorContains(t, err, "MOTIVRA_JWT_PRIVATE_KEY_FILE is required in production")

	cfg.App.Env = EnvDevelopment
	signer, err := NewSignerFromConfig(cfg)
	require.NoError(t, err, "development generates an ephemeral key")
	require.Equal(t, AlgorithmEdDSA, signer.Algorithm())
}

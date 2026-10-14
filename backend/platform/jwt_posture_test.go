package platform

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// timeNow is the shared "now" for token-fixture timestamps in this package.
func timeNow() time.Time { return time.Now() }

// rsaGenerateForTest and x509MarshalPKCS1ForTest keep the RSA test fixtures
// short.
func rsaGenerateForTest(bits int) (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, bits)
}

func x509MarshalPKCS1ForTest(key *rsa.PrivateKey) []byte {
	return x509.MarshalPKCS1PrivateKey(key)
}

// TestHS256OnlyAcceptedBehindExplicitConfig pins the ADR-0007 flip: a
// validator built WITHOUT the legacy secret refuses HS256 tokens outright —
// even ones signed with a secret it could otherwise have known. A verifier
// therefore cannot mint OR accept shared-secret tokens once migrated.
func TestHS256OnlyAcceptedBehindExplicitConfig(t *testing.T) {
	t.Parallel()
	token := makeToken(t, validClaims())

	// The asymmetric-only posture: one public key, no secret.
	signer, err := GenerateJWTSigner(SignerConfig{})
	require.NoError(t, err)
	asymmetric := NewValidator(
		WithValidatorIssuerAudience("motivra-identity", "motivra"),
		WithPublicKeyPEM(signer.KID(), signer.PublicKey()),
	)
	_, err = asymmetric.Validate(token)
	require.ErrorContains(t, err, "HS256", "refused either by the method allow-list or the key resolver")
	require.False(t, asymmetric.HasPrivateKeyMaterial())

	// The legacy constructor keeps working through the migration window.
	legacy := NewJWTValidator(testSecret, "motivra-identity", "motivra")
	_, err = legacy.Validate(token)
	require.NoError(t, err)
	require.True(t, legacy.HasPrivateKeyMaterial())

	// The explicit option adds HS256 on top of asymmetric sources.
	dual := NewValidator(
		WithValidatorIssuerAudience("motivra-identity", "motivra"),
		WithLegacyHS256(testSecret),
	)
	_, err = dual.Validate(token)
	require.NoError(t, err)
	require.True(t, dual.HasPrivateKeyMaterial(), "the migration window DOES carry signing material — visible via HasPrivateKeyMaterial")
}

// TestValidatorFromConfigPostures pins what each deployment shape builds.
func TestValidatorFromConfigPostures(t *testing.T) {
	t.Parallel()

	// Test environment: legacy shared secret (existing suites mint HS256).
	testCfg := LoadForTest()
	v, err := NewValidatorFromConfig(testCfg)
	require.NoError(t, err)
	require.True(t, v.HasPrivateKeyMaterial(), "test posture is the legacy validator")

	// Development: JWKS default applies; no secret, no private material.
	devCfg := LoadForTest()
	devCfg.App.Env = EnvDevelopment
	devCfg.JWT.Secret = ""
	v, err = NewValidatorFromConfig(devCfg)
	require.NoError(t, err)
	require.False(t, v.HasPrivateKeyMaterial())

	// Production with ONLY the migration flag + secret: still legacy.
	prodCfg := LoadForTest()
	prodCfg.App.Env = EnvProduction
	prodCfg.JWT.JWKSURL = ""
	prodCfg.JWT.AllowHS256 = true
	v, err = NewValidatorFromConfig(prodCfg)
	require.NoError(t, err)
	require.True(t, v.HasPrivateKeyMaterial(), "the migration window carries the shared secret by definition")

	// A config with no key source at all is a construction error.
	emptyCfg := LoadForTest()
	emptyCfg.JWT.Secret = ""
	_, err = NewValidatorFromConfig(emptyCfg)
	require.Error(t, err)
}

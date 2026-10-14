package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

// jwksServer is a JWKS endpoint test double whose key set can be swapped —
// the rotation control surface of the runbook.
type jwksServer struct {
	srv    *httptest.Server
	keys   atomic.Value // json.RawMessage
	hits   atomic.Int64
	status atomic.Int64
}

func newJWKSServer(t *testing.T, doc json.RawMessage) *jwksServer {
	t.Helper()
	s := &jwksServer{}
	s.keys.Store(doc)
	s.status.Store(http.StatusOK)
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		s.hits.Add(1)
		w.WriteHeader(int(s.status.Load()))
		_, _ = w.Write(s.keys.Load().(json.RawMessage))
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func ed25519JWKSDoc(t *testing.T, signer *JWTSigner) json.RawMessage {
	t.Helper()
	return signer.JWKS()
}

// TestJWKSValidatorEndToEnd is the issue #87 acceptance flow: the issuer
// signs with its private key; the verifier knows NOTHING but the JWKS URL —
// no secret, no private key file — and still validates claims.
func TestJWKSValidatorEndToEnd(t *testing.T) {
	t.Parallel()
	signer, err := GenerateJWTSigner(SignerConfig{Issuer: "motivra-identity", Audience: "motivra"})
	require.NoError(t, err)
	server := newJWKSServer(t, ed25519JWKSDoc(t, signer))

	validator := NewValidator(
		WithValidatorIssuerAudience("motivra-identity", "motivra"),
		WithJWKSURL(server.srv.URL+"/.well-known/jwks.json"),
	)
	require.False(t, validator.HasPrivateKeyMaterial(), "JWKS-backed verifier holds no signing material")
	require.NoError(t, validator.ReadyCheck(context.Background()), "readyz green once the key set is loaded")

	token, err := signer.Sign(AccessClaims(Claims{
		UserID:    "user-9",
		Role:      "CUSTOMER",
		SessionID: "sess-9",
		IssuedAt:  timeNow(),
		ExpiresAt: timeNow().Add(15 * time.Minute),
	}, []string{"CUSTOMER"}, "motivra-identity", "motivra"))
	require.NoError(t, err)

	claims, err := validator.Validate(token)
	require.NoError(t, err)
	require.Equal(t, "user-9", claims.UserID)
	require.Equal(t, "sess-9", claims.SessionID)
}

// TestJWKSRotationInvalidatesOldKeys is the runbook's rotation endpoint
// (issue #87 acceptance: old tokens invalid within one access-TTL of
// rotation): once the operator removes the retired kid from the served key
// set, tokens signed by the OLD key are refused at the very next
// validation — far inside one access TTL — while the NEW key verifies.
func TestJWKSRotationInvalidatesOldKeys(t *testing.T) {
	t.Parallel()
	oldSigner, err := GenerateJWTSigner(SignerConfig{Issuer: "motivra-identity", Audience: "motivra"})
	require.NoError(t, err)
	newSigner, err := GenerateJWTSigner(SignerConfig{Issuer: "motivra-identity", Audience: "motivra"})
	require.NoError(t, err)
	server := newJWKSServer(t, ed25519JWKSDoc(t, oldSigner))

	validator := NewValidator(
		WithValidatorIssuerAudience("motivra-identity", "motivra"),
		WithJWKSURL(server.srv.URL+"/.well-known/jwks.json"),
		WithJWKSRefreshGap(0), // the rig swaps the key set within milliseconds
	)
	oldToken, err := oldSigner.Sign(AccessClaims(Claims{
		UserID: "user-1", IssuedAt: timeNow(), ExpiresAt: timeNow().Add(15 * time.Minute),
	}, nil, "motivra-identity", "motivra"))
	require.NoError(t, err)
	newToken, err := newSigner.Sign(AccessClaims(Claims{
		UserID: "user-1", IssuedAt: timeNow(), ExpiresAt: timeNow().Add(15 * time.Minute),
	}, nil, "motivra-identity", "motivra"))
	require.NoError(t, err)

	_, err = validator.Validate(oldToken)
	require.NoError(t, err, "pre-rotation: the old key verifies")

	// ROTATION: the issuer now serves ONLY the new key (the two-phase
	// runbook's final step). No restart, no cache flush — the unknown-kid
	// refresh does the work.
	server.keys.Store(ed25519JWKSDoc(t, newSigner))

	_, err = validator.Validate(newToken)
	require.NoError(t, err, "the new kid is picked up by the unknown-kid refetch")

	_, err = validator.Validate(oldToken)
	require.Error(t, err, "the retired kid is gone from the key set: old tokens die immediately, well within one access TTL")
}

// TestJWKSCacheServesStaleOnOutage: availability beats hard auth outage —
// a fetch failure keeps the last good key set (tokens keep verifying) while
// readyz goes red so the operator sees the degraded feed.
func TestJWKSCacheServesStaleOnOutage(t *testing.T) {
	t.Parallel()
	signer, err := GenerateJWTSigner(SignerConfig{})
	require.NoError(t, err)
	server := newJWKSServer(t, ed25519JWKSDoc(t, signer))

	cache := newJWKSCache(server.srv.URL + "/.well-known/jwks.json")
	base := time.Now()
	cache.now = func() time.Time { return base }
	_, err = cache.lookup(signer.KID(), AlgorithmEdDSA)
	require.NoError(t, err)

	// The endpoint breaks; the cache ages past its TTL and the next lookup
	// attempts the (failing) refresh.
	server.status.Store(http.StatusInternalServerError)
	base = base.Add(jwksCacheTTL + time.Minute)
	_, err = cache.lookup(signer.KID(), AlgorithmEdDSA)
	require.NoError(t, err, "the stale key set keeps verification alive through an outage")
	require.False(t, cache.healthy(), "readyz reports the degraded JWKS")
}

// TestJWKSCacheRateLimitsUnknownKidRefresh: a flood of forged kid headers
// must not turn verifiers into a request amplifier against the issuer.
func TestJWKSCacheRateLimitsUnknownKidRefresh(t *testing.T) {
	t.Parallel()
	signer, err := GenerateJWTSigner(SignerConfig{})
	require.NoError(t, err)
	server := newJWKSServer(t, ed25519JWKSDoc(t, signer))
	cache := newJWKSCache(server.srv.URL + "/.well-known/jwks.json")
	cache.now = func() time.Time { return time.Now() }

	for i := 0; i < 50; i++ {
		_, _ = cache.lookup("forged-kid", AlgorithmEdDSA)
	}
	require.Less(t, server.hits.Load(), int64(10), "unknown-kid refreshes are gap-limited")
}

// TestJWKSValidatorRefusesForeignAndTampered covers the negative space.
func TestJWKSValidatorRefusesForeignAndTampered(t *testing.T) {
	t.Parallel()
	signer, err := GenerateJWTSigner(SignerConfig{Issuer: "motivra-identity", Audience: "motivra"})
	require.NoError(t, err)
	other, err := GenerateJWTSigner(SignerConfig{Issuer: "motivra-identity", Audience: "motivra"})
	require.NoError(t, err)
	server := newJWKSServer(t, ed25519JWKSDoc(t, signer))

	validator := NewValidator(
		WithValidatorIssuerAudience("motivra-identity", "motivra"),
		WithJWKSURL(server.srv.URL+"/.well-known/jwks.json"),
	)
	token, err := other.Sign(AccessClaims(Claims{
		UserID: "user-2", IssuedAt: timeNow(), ExpiresAt: timeNow().Add(time.Minute),
	}, nil, "motivra-identity", "motivra"))
	require.NoError(t, err)

	// The foreign kid is unknown on first sight; after the bounded refetch
	// it is STILL unknown (not in the set) — refused either way.
	_, err = validator.Validate(token)
	require.Error(t, err)

	// Missing-kid asymmetric tokens are refused (kid is mandatory).
	unsignedKid := jwtNewWithoutKid(t, signer)
	_, err = validator.Validate(unsignedKid)
	require.ErrorContains(t, err, "kid")
}

// jwtNewWithoutKid mints a correctly-signed token WITHOUT the kid header.
func jwtNewWithoutKid(t *testing.T, signer *JWTSigner) string {
	t.Helper()
	tok := jwt.NewWithClaims(signer.Method(), jwt.MapClaims{
		"iss": "motivra-identity", "aud": "motivra", "sub": "user-2",
		"exp": timeNow().Add(time.Minute).Unix(),
	})
	signed, err := tok.SignedString(signer.private)
	require.NoError(t, err)
	return signed
}

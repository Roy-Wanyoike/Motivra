package inspections

import (
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mediaTestKey signs test URLs for the media signer (>= 32 bytes).
var mediaTestKey = []byte("evidence-media-test-signing-key-0123456789")

// newTestMediaSigner returns a signer keyed by mediaTestKey with the clock
// pinned at base, so TTL behaviour is deterministic without sleeps.
func newTestMediaSigner(t *testing.T, base time.Time) *MediaSigner {
	t.Helper()
	signer, err := NewMediaSigner(mediaTestKey)
	require.NoError(t, err)
	signer.now = func() time.Time { return base }
	return signer
}

// mediaTestGrant builds a well-formed grant for arbitrary identifiers.
func mediaTestGrant(sessionID, evidenceID, subject uuid.UUID) MediaGrant {
	return MediaGrant{
		SessionID:   sessionID,
		EvidenceID:  evidenceID,
		ContentHash: validHash("media-test"),
		Subject:     subject,
	}
}

// TestMediaSignerRoundTripWithinTTL proves the positive case of issue #186:
// a minted URL verifies within its TTL and carries every binding.
func TestMediaSignerRoundTripWithinTTL(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	signer := newTestMediaSigner(t, base)
	sessionID, evidenceID, subject := uuid.New(), uuid.New(), uuid.New()

	grant, query, err := signer.Sign(mediaTestGrant(sessionID, evidenceID, subject))
	require.NoError(t, err)

	// The grant is finalized: GET, evidence-media audience, now+TTL expiry.
	assert.Equal(t, http.MethodGet, grant.Method)
	assert.Equal(t, AudienceEvidenceMedia, grant.Audience)
	assert.Equal(t, base.Add(signer.TTL()), grant.ExpiresAt)

	// The query carries the full binding.
	assert.Equal(t, MediaURLVersion, query.Get("v"))
	assert.Equal(t, AudienceEvidenceMedia, query.Get("aud"))
	assert.Equal(t, grant.ExpiresAt.Unix(), mustInt64(t, query.Get("exp")))
	assert.Equal(t, subject.String(), query.Get("sub"))
	assert.Equal(t, grant.ContentHash, query.Get("hash"))
	assert.Regexp(t, `^[0-9a-f]{64}$`, query.Get("sig"))

	// Verification succeeds across the whole TTL window (inclusive of the
	// exact expiry second — zero leeway, honest cutoff).
	for _, at := range []time.Time{base, base.Add(time.Minute), base.Add(signer.TTL())} {
		at := at
		signer.now = func() time.Time { return at }
		got, err := signer.Verify(http.MethodGet, sessionID, evidenceID, query)
		require.NoError(t, err, "verify at %s", at)
		assert.Equal(t, grant, got)
	}
}

// TestMediaSignerRejectsExpiredURL proves the expired-URL refusal of issue
// #186: one second past expires_at the URL is dead.
func TestMediaSignerRejectsExpiredURL(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	signer := newTestMediaSigner(t, base)
	sessionID, evidenceID, subject := uuid.New(), uuid.New(), uuid.New()

	_, query, err := signer.Sign(mediaTestGrant(sessionID, evidenceID, subject))
	require.NoError(t, err)

	signer.now = func() time.Time { return base.Add(signer.TTL()).Add(time.Second) }
	_, err = signer.Verify(http.MethodGet, sessionID, evidenceID, query)
	require.ErrorIs(t, err, ErrMediaGrantExpired)
}

// TestMediaSignerRejectsTamperedSignature proves the tamper refusal of issue
// #186: any mutation of any signed parameter — or of the method/object the
// signature is presented for — fails verification.
func TestMediaSignerRejectsTamperedSignature(t *testing.T) {
	t.Parallel()
	base := time.Now().UTC()
	signer := newTestMediaSigner(t, base)
	sessionID, evidenceID, subject := uuid.New(), uuid.New(), uuid.New()

	_, query, err := signer.Sign(mediaTestGrant(sessionID, evidenceID, subject))
	require.NoError(t, err)

	mutations := map[string]func(q url.Values){
		"flip signature byte":  func(q url.Values) { q.Set("sig", flipHexChar(q.Get("sig"))) },
		"replace signature":    func(q url.Values) { q.Set("sig", strings.Repeat("ab", 32)) },
		"drop signature":       func(q url.Values) { q.Del("sig") },
		"extend expiry":        func(q url.Values) { q.Set("exp", "9999999999") },
		"alter content hash":   func(q url.Values) { q.Set("hash", validHash("tampered")) },
		"alter audience":       func(q url.Values) { q.Set("aud", "evidence-upload") },
		"alter subject":        func(q url.Values) { q.Set("sub", uuid.NewString()) },
		"alter version":        func(q url.Values) { q.Set("v", "2") },
		"drop audience":        func(q url.Values) { q.Del("aud") },
		"garbage expiry":       func(q url.Values) { q.Set("exp", "not-a-number") },
		"garbage subject":      func(q url.Values) { q.Set("sub", "not-a-uuid") },
		"uppercase hash reuse": func(q url.Values) { q.Set("hash", strings.ToUpper(q.Get("hash"))) },
	}
	for name, mutate := range mutations {
		mutate, query := mutate, cloneQuery(t, query)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			mutate(query)
			_, err := signer.Verify(http.MethodGet, sessionID, evidenceID, query)
			require.ErrorIs(t, err, ErrMediaGrantInvalid)
		})
	}

	// The signature is bound to the method and the exact object: presenting
	// a valid URL for another method or object is tampering.
	_, err = signer.Verify(http.MethodPost, sessionID, evidenceID, query)
	require.ErrorIs(t, err, ErrMediaGrantInvalid)
	_, err = signer.Verify(http.MethodGet, uuid.New(), evidenceID, query)
	require.ErrorIs(t, err, ErrMediaGrantInvalid)
	_, err = signer.Verify(http.MethodGet, sessionID, uuid.New(), query)
	require.ErrorIs(t, err, ErrMediaGrantInvalid)
}

// TestMediaSignerRejectsWrongAudience proves the purpose binding: a valid
// MAC minted for another purpose is refused even though it verifies.
func TestMediaSignerRejectsWrongAudience(t *testing.T) {
	t.Parallel()
	signer := newTestMediaSigner(t, time.Now().UTC())
	sessionID, evidenceID, subject := uuid.New(), uuid.New(), uuid.New()

	grant := mediaTestGrant(sessionID, evidenceID, subject)
	grant.Method = http.MethodGet
	grant.Audience = "evidence-upload" // an attacker-minted-looking purpose
	grant.ExpiresAt = signer.now().Add(signer.TTL()).UTC().Truncate(time.Second)
	query := url.Values{
		"v":    []string{MediaURLVersion},
		"aud":  []string{grant.Audience},
		"exp":  []string{strconv.FormatInt(grant.ExpiresAt.Unix(), 10)},
		"sub":  []string{subject.String()},
		"hash": []string{grant.ContentHash},
		"sig":  []string{hex.EncodeToString(signer.mac(grant))},
	}

	_, err := signer.Verify(http.MethodGet, sessionID, evidenceID, query)
	require.ErrorIs(t, err, ErrMediaGrantAudience)
}

// TestMediaSignerRejectsWrongKey proves key separation: URLs minted under
// one key never verify under another (and a compromised key is rotated by
// replacing the env value alone).
func TestMediaSignerRejectsWrongKey(t *testing.T) {
	t.Parallel()
	signer := newTestMediaSigner(t, time.Now().UTC())
	other, err := NewMediaSigner([]byte("another-evidence-media-signing-key-0123456789"))
	require.NoError(t, err)
	sessionID, evidenceID, subject := uuid.New(), uuid.New(), uuid.New()

	_, query, err := signer.Sign(mediaTestGrant(sessionID, evidenceID, subject))
	require.NoError(t, err)

	_, err = other.Verify(http.MethodGet, sessionID, evidenceID, query)
	require.ErrorIs(t, err, ErrMediaGrantInvalid)
}

// TestMediaSignerKeyLength pins the key-length rule (>= 32 bytes in every
// environment) and that errors never echo key material.
func TestMediaSignerKeyLength(t *testing.T) {
	t.Parallel()
	marker := "TOPSECRETKEYMATERIAL"
	tests := []struct {
		name    string
		key     []byte
		wantErr bool
	}{
		{"nil key", nil, true},
		{"31 bytes", bytesOf(31, 'k'), true},
		{"32 bytes", bytesOf(32, 'k'), false},
		{"64 bytes", bytesOf(64, 'k'), false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			signer, err := NewMediaSigner(tc.key)
			if tc.wantErr {
				require.Error(t, err)
				assert.Nil(t, signer)
				assert.NotContains(t, err.Error(), marker, "errors must never carry key material")
				return
			}
			require.NoError(t, err)
		})
	}
	// The marker only rides the key of the error-message case above.
	_, err := NewMediaSigner([]byte(marker))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), marker)
}

// TestMediaSignerFromEnvPosture pins the ADR-0007-style env contract of the
// evidence-media signing key: env-only source, required outside
// development/test, ephemeral fallback only in development/test, and the TTL
// override clamped to the issue's short-TTL band.
func TestMediaSignerFromEnvPosture(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		key     string
		ttlEnv  string
		wantErr string
		wantTTL time.Duration
	}{
		{name: "production requires the key", env: "production", wantErr: EnvEvidenceSigningKey},
		{name: "staging requires the key", env: "staging", wantErr: EnvEvidenceSigningKey},
		{name: "development falls back to ephemeral", env: "development", wantTTL: DefaultEvidenceURLTTL},
		{name: "test falls back to ephemeral", env: "test", wantTTL: DefaultEvidenceURLTTL},
		{name: "short key refused even in development", env: "development", key: "short", wantErr: EnvEvidenceSigningKey},
		{name: "valid key accepted in production", env: "production", key: string(bytesOf(48, 'e')), wantTTL: DefaultEvidenceURLTTL},
		{name: "ttl override accepted inside the band", env: "production", key: string(bytesOf(48, 'e')), ttlEnv: "60s", wantTTL: time.Minute},
		{name: "ttl below the band refused", env: "production", key: string(bytesOf(48, 'e')), ttlEnv: "5s", wantErr: EnvEvidenceURLTTL},
		{name: "ttl above the band refused", env: "production", key: string(bytesOf(48, 'e')), ttlEnv: "1h", wantErr: EnvEvidenceURLTTL},
		{name: "garbage ttl refused", env: "production", key: string(bytesOf(48, 'e')), ttlEnv: "soon", wantErr: EnvEvidenceURLTTL},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvEvidenceSigningKey, tc.key)
			t.Setenv(EnvEvidenceURLTTL, tc.ttlEnv)
			signer, err := NewMediaSignerFromEnv(tc.env)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				// The env value (when present) never appears in errors.
				if tc.key != "" {
					assert.NotContains(t, err.Error(), tc.key)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantTTL, signer.TTL())
		})
	}
}

// TestMediaSignerEphemeralKeysArePerProcess proves the development fallback
// is ephemeral: two signers constructed without a key never cross-verify.
func TestMediaSignerEphemeralKeysArePerProcess(t *testing.T) {
	t.Setenv(EnvEvidenceSigningKey, "")
	t.Setenv(EnvEvidenceURLTTL, "")
	a, err := NewMediaSignerFromEnv("development")
	require.NoError(t, err)
	b, err := NewMediaSignerFromEnv("development")
	require.NoError(t, err)
	sessionID, evidenceID, subject := uuid.New(), uuid.New(), uuid.New()

	_, query, err := a.Sign(mediaTestGrant(sessionID, evidenceID, subject))
	require.NoError(t, err)
	_, err = b.Verify(http.MethodGet, sessionID, evidenceID, query)
	require.ErrorIs(t, err, ErrMediaGrantInvalid)
}

// TestEvidenceURLTTLDefaults pins the TTL contract constants of issue #186
// (short TTL ~5 min; no configuration can exceed the band).
func TestEvidenceURLTTLDefaults(t *testing.T) {
	assert.Equal(t, 5*time.Minute, DefaultEvidenceURLTTL)
	assert.Equal(t, DefaultEvidenceURLTTL, MaxEvidenceURLTTL)
	assert.Greater(t, DefaultEvidenceURLTTL, MinEvidenceURLTTL)
}

// --- small helpers -------------------------------------------------------

func mustInt64(t *testing.T, raw string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(raw, 10, 64)
	require.NoError(t, err)
	return n
}

func flipHexChar(s string) string {
	if s == "" {
		return s
	}
	replacement := byte('0')
	if s[0] == '0' {
		replacement = '1'
	}
	return string(replacement) + s[1:]
}

func cloneQuery(t *testing.T, q url.Values) url.Values {
	t.Helper()
	out := url.Values{}
	for k, vs := range q {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

func bytesOf(n int, fill byte) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = fill
	}
	return out
}

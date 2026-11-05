package inspections

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// Evidence-media signed URLs (issue #186, P1 SECURITY): inspection media may
// contain PII and license plates, so the first URL surface that ever ships
// for evidence is a short-TTL, purpose-bound capability — never a permanent
// public link.
//
// The scheme is a straightforward HMAC: the signer (keyed only by an
// environment-provided secret, ADR-0007 naming pattern) renders a canonical
// string that pins the HTTP method, the exact object path, the evidence
// content hash, the expiry, the audience (purpose) and the principal the URL
// was minted for, and MACs it with HMAC-SHA256. Verification recomputes the
// MAC and compares it in constant time (hmac.Equal), then enforces the TTL.
// Every signature-layer failure is an authentication failure (401) — expired
// and malformed credentials are indistinguishable in status, so the only
// oracle is the signed-URL holder's own expires_at claim.
//
// The signing key NEVER appears in logs, errors, audit metadata or
// responses. It is read from the process environment only (EnvEvidenceSigningKey);
// there is no default value and no fallback key material anywhere in this file.

const (
	// EnvEvidenceSigningKey is the environment variable holding the
	// HMAC-SHA256 evidence-media signing key (ADR-0007 env-contract
	// pattern). Required (>= MinEvidenceSigningKeyBytes) in staging and
	// production; development/test fall back to an ephemeral per-process
	// key when unset (single-instance convenience — URLs die with the
	// process, exactly like the ephemeral JWT dev signer, ADR-0007).
	EnvEvidenceSigningKey = "MOTIVRA_INSPECTIONS_EVIDENCE_SIGNING_KEY"

	// EnvEvidenceURLTTL optionally overrides the signed-URL TTL (Go
	// duration). Values outside [MinEvidenceURLTTL, MaxEvidenceURLTTL] are
	// refused at construction — no configuration can issue a long-lived
	// capability URL (issue #186: recommend 60–300s).
	EnvEvidenceURLTTL = "MOTIVRA_INSPECTIONS_EVIDENCE_URL_TTL"
)

// TTL bounds of a signed evidence-media URL. The default sits at the top of
// the issue's recommended 60–300s band; MaxEvidenceURLTTL caps the override
// so an operator cannot mint longer-lived capability URLs by configuration.
const (
	// DefaultEvidenceURLTTL is the signed-URL lifetime when
	// EnvEvidenceURLTTL is unset: five minutes.
	DefaultEvidenceURLTTL = 5 * time.Minute
	// MinEvidenceURLTTL is the shortest configurable lifetime.
	MinEvidenceURLTTL = 30 * time.Second
	// MaxEvidenceURLTTL is the longest configurable lifetime (issue #186
	// band ceiling). There is deliberately no way to configure more.
	MaxEvidenceURLTTL = 5 * time.Minute
	// MinEvidenceSigningKeyBytes is the minimum signing-key length. The
	// key is high-entropy secret material; anything shorter is refused in
	// every environment.
	MinEvidenceSigningKeyBytes = 32
)

// MediaURLVersion is the version stamp carried in every signed URL (query
// parameter v) and bound into the MAC input, so the canonical form can
// evolve without accepting old-form signatures.
const MediaURLVersion = "1"

// AudienceEvidenceMedia is the purpose (audience) bound into every
// evidence-media URL. A signature minted for another purpose is refused even
// when its MAC verifies — signatures are purpose-scoped, not bearer tokens.
const AudienceEvidenceMedia = "evidence-media"

// MediaObjectPath is the canonical object path of the signed fetch route
// (registered by RegisterEvidenceMediaRoutes). It is part of the MAC input,
// which pins every signature to this exact route — never bucket-wide.
const MediaObjectPath = "/v1/inspections/media"

// evidenceMACDomain is the MAC-input domain separator. Versioned with
// MediaURLVersion so key/mint-form rotations cannot cross-verify.
const evidenceMACDomain = "motivra-evidence-media-" + MediaURLVersion

// Audit actions written to the platform audit_log for the evidence-media
// surface (ADR-0004 audit obligations, issue #186): one for minting (a
// metadata reveal) and one for serving bytes (an actual access).
const (
	AuditActionEvidenceMediaURLIssued = "inspections.evidence.media.url_issued"
	AuditActionEvidenceMediaServed    = "inspections.evidence.media.served"
)

// AuditObjectTypeEvidence is the audit object_type of evidence-media entries.
const AuditObjectTypeEvidence = "inspection_evidence"

// Signature-layer errors. All of them map to HTTP 401 at the media surface:
// an invalid, tampered, wrong-purpose or expired URL is an authentication
// failure, and distinguishing them beyond the detail string would hand
// callers a probing oracle.
var (
	// ErrMediaNotFound is returned when the evidence row (or the stored
	// object behind a valid signature) does not exist. Invisible and
	// unknown resources answer 404 exactly like a missing row (ADR-0004).
	ErrMediaNotFound = errors.New("inspections: evidence media not found")
	// ErrMediaGrantInvalid covers malformed, tampered or wrong-key
	// signatures.
	ErrMediaGrantInvalid = errors.New("inspections: media URL signature is invalid")
	// ErrMediaGrantExpired is returned when a structurally valid signature
	// is presented past its expiry.
	ErrMediaGrantExpired = errors.New("inspections: media URL has expired")
	// ErrMediaGrantAudience is returned when a valid signature is presented
	// for a different purpose than the route serves.
	ErrMediaGrantAudience = errors.New("inspections: media URL is not valid for this purpose")
	// ErrMediaSigningUnavailable is returned when the media stack is not
	// wired (no signing key and/or no storage+audit backend). The media
	// surface fails CLOSED: nothing is minted or served unaudited.
	ErrMediaSigningUnavailable = errors.New("inspections: evidence media is not configured")
)

// mediaGrantHashPattern is the exact content-hash form bound into a signed
// URL (mirrors the evidence CHECK constraint).
var mediaGrantHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// mediaGrantSigPattern is the hex form of the signature parameter.
var mediaGrantSigPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// MediaGrant is the authorization carried by one signed evidence-media URL.
// It is minted by MediaSigner.Sign after the issuing request's scope was
// verified against BOTH the session and the evidence row, and recovered by
// MediaSigner.Verify on the fetch route.
type MediaGrant struct {
	SessionID  uuid.UUID
	EvidenceID uuid.UUID
	// ContentHash is the evidence row's content hash at mint time. The
	// fetch handler re-asserts it against the live row, so a URL is bound
	// to the exact object version it was minted for.
	ContentHash string
	// Subject is the principal the URL was minted for (the authenticated
	// caller of the mint endpoint). It rides the MAC and is used as the
	// audit actor for fetches, where no bearer token exists.
	Subject uuid.UUID
	// Audience is the purpose binding (AudienceEvidenceMedia today).
	Audience string
	// Method is the HTTP method the signature is valid for.
	Method string
	// ExpiresAt is the exact expiry (zero-leeway: past ExpiresAt the URL
	// is refused).
	ExpiresAt time.Time
}

// MediaSigner mints and verifies the short-TTL signed URLs of the
// evidence-media retrieval path (issue #186). It holds the ONLY copy of the
// signing key in the domain; the key is never logged, never rendered into
// errors and never leaves the process.
type MediaSigner struct {
	key []byte
	ttl time.Duration
	// now is the clock. A field (not a global) so tests can travel time
	// deterministically without sleeps.
	now func() time.Time
}

// NewMediaSigner returns a MediaSigner keyed by key. The key must carry at
// least MinEvidenceSigningKeyBytes bytes of entropy — short keys are refused
// in every environment, including tests.
func NewMediaSigner(key []byte) (*MediaSigner, error) {
	if len(key) < MinEvidenceSigningKeyBytes {
		// The error carries the LENGTH, never the material.
		return nil, fmt.Errorf("%s must be at least %d bytes of entropy (got %d bytes); generate one: openssl rand -base64 48",
			EnvEvidenceSigningKey, MinEvidenceSigningKeyBytes, len(key))
	}
	k := make([]byte, len(key))
	copy(k, key)
	return &MediaSigner{key: k, ttl: DefaultEvidenceURLTTL, now: time.Now}, nil
}

// NewMediaSignerFromEnv resolves the evidence-media signing posture from the
// process environment (ADR-0007 env-contract pattern, issue #186):
//
//   - EnvEvidenceSigningKey is the ONLY key source. It is required with at
//     least MinEvidenceSigningKeyBytes bytes in every environment; when
//     unset, development/test receive an ephemeral per-process key (dev
//     single-instance convenience — minted URLs die with the process, the
//     exact posture of the ephemeral JWT dev signer, ADR-0007) and
//     staging/production refuse to construct the signer.
//   - EnvEvidenceURLTTL optionally sets the lifetime, clamped to
//     [MinEvidenceURLTTL, MaxEvidenceURLTTL]; malformed values are errors,
//     never silently defaulted.
//
// env is one of the platform environment names. The key value itself is
// never included in any error message.
func NewMediaSignerFromEnv(env string) (*MediaSigner, error) {
	ttl, err := EvidenceURLTTLFromEnv()
	if err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(os.Getenv(EnvEvidenceSigningKey))
	if raw == "" {
		switch env {
		case platform.EnvDevelopment, platform.EnvTest:
			key := make([]byte, MinEvidenceSigningKeyBytes)
			if _, err := rand.Read(key); err != nil {
				return nil, fmt.Errorf("evidence media: generate ephemeral development signing key: %w", err)
			}
			signer, err := NewMediaSigner(key)
			if err != nil {
				return nil, err
			}
			signer.ttl = ttl
			return signer, nil
		default:
			return nil, fmt.Errorf("%s is required in %s (short-TTL signed URLs for evidence media, issue #186); generate one: openssl rand -base64 48",
				EnvEvidenceSigningKey, env)
		}
	}
	signer, err := NewMediaSigner([]byte(raw))
	if err != nil {
		return nil, err
	}
	signer.ttl = ttl
	return signer, nil
}

// EvidenceURLTTLFromEnv resolves the signed-URL TTL from the environment:
// DefaultEvidenceURLTTL when unset; a malformed or out-of-bounds value is an
// error (fail loud — a silently lengthened capability URL is a security
// regression, issue #186).
func EvidenceURLTTLFromEnv() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(EnvEvidenceURLTTL))
	if raw == "" {
		return DefaultEvidenceURLTTL, nil
	}
	ttl, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s %q is not a valid duration", EnvEvidenceURLTTL, raw)
	}
	if ttl < MinEvidenceURLTTL || ttl > MaxEvidenceURLTTL {
		return 0, fmt.Errorf("%s must be between %s and %s (short-TTL evidence media, issue #186); got %s",
			EnvEvidenceURLTTL, MinEvidenceURLTTL, MaxEvidenceURLTTL, ttl)
	}
	return ttl, nil
}

// TTL returns the signed-URL lifetime this signer mints with.
func (m *MediaSigner) TTL() time.Duration {
	if m == nil {
		return DefaultEvidenceURLTTL
	}
	return m.ttl
}

// mediaStack bundles the evidence-media dependencies attached via WithMedia
// (issue #186): the signer mints and verifies the short-TTL signed URLs; the
// backend resolves evidence rows, opens object bytes and writes the access
// audits. Both halves are mandatory — a stack missing either keeps the whole
// media surface fail-closed (503), because media is never served unsigned or
// unaudited.
type mediaStack struct {
	signer  *MediaSigner
	backend MediaBackend
}

// mediaStack returns the wired stack. ok is false when no stack was attached
// or either half is missing; callers fail closed with
// ErrMediaSigningUnavailable (503) rather than minting or serving anything.
func (s *Service) mediaStack() (*mediaStack, bool) {
	if s == nil || s.media == nil || s.media.signer == nil || s.media.backend == nil {
		return nil, false
	}
	return s.media, true
}

// WithMedia attaches the evidence-media stack (issue #186) to a Service.
// signer and backend come from the process wiring (cmd/inspections,
// cmd/gateway): the signer is built from the environment-only signing key
// (NewMediaSignerFromEnv), the backend from the domain's database pool
// (NewPostgresMediaStore). Passing a nil signer or nil backend is allowed and
// means "media not configured" — the endpoints answer 503 fail-closed.
func WithMedia(signer *MediaSigner, backend MediaBackend) Option {
	return func(s *Service) {
		if s == nil {
			return
		}
		s.media = &mediaStack{signer: signer, backend: backend}
	}
}

// IssueEvidenceMediaURL authorizes and mints one short-TTL signed URL for an
// evidence record's media (issue #186). The caller's scope is applied to
// BOTH the session and the evidence row before anything is minted —
// invisible and unknown resources answer 404 exactly like a missing row
// (ADR-0004 cross-tenant denial) — and the mint itself is audited: an audit
// failure fails the request, so no minted capability can ever go unaudited.
// It returns the finalized grant (with ExpiresAt) and the signed query
// parameters for the fetch URL.
func (s *Service) IssueEvidenceMediaURL(ctx context.Context, sessionID, evidenceID, actorID uuid.UUID, scope Scope) (MediaGrant, url.Values, error) {
	stack, ok := s.mediaStack()
	if !ok {
		return MediaGrant{}, nil, ErrMediaSigningUnavailable
	}
	if actorID == uuid.Nil {
		return MediaGrant{}, nil, invalidInput("issue evidence media url: actor is required")
	}
	// The session boundary first: an out-of-scope (or unknown) session is a
	// plain 404, leaking no existence information.
	if _, err := s.GetSession(ctx, sessionID, scope); err != nil {
		return MediaGrant{}, nil, err
	}
	// The evidence row next, through the same scope (ListEvidence re-applies
	// the session boundary), so evidence of another session is 404 too.
	rows, err := s.store.ListEvidence(ctx, sessionID, scope)
	if err != nil {
		return MediaGrant{}, nil, err
	}
	var evidence *Evidence
	for i := range rows {
		if rows[i].ID == evidenceID {
			evidence = &rows[i]
			break
		}
	}
	if evidence == nil {
		return MediaGrant{}, nil, ErrMediaNotFound
	}

	grant, query, err := stack.signer.Sign(MediaGrant{
		SessionID:   sessionID,
		EvidenceID:  evidenceID,
		ContentHash: evidence.ContentHash,
		Subject:     actorID,
	})
	if err != nil {
		return MediaGrant{}, nil, err
	}
	// ADR-0004 audit obligation: the mint (a metadata reveal that creates a
	// time-boxed capability) is written to the platform audit trail, and an
	// audit failure fails the request (the verification precedent: no
	// unaudited security decision ever commits).
	audit := EvidenceMediaAudit{
		ActorID:     actorID,
		TenantID:    scope.TenantID,
		Action:      AuditActionEvidenceMediaURLIssued,
		SessionID:   sessionID,
		EvidenceID:  evidenceID,
		ContentHash: evidence.ContentHash,
		Audience:    grant.Audience,
		ExpiresAt:   grant.ExpiresAt,
	}
	if err := stack.backend.InsertMediaAudit(ctx, audit); err != nil {
		return MediaGrant{}, nil, fmt.Errorf("inspections: issue evidence media url: %w", err)
	}
	return grant, query, nil
}

// OpenEvidenceMedia resolves the object behind a VERIFIED grant (the fetch
// path; the signature was already checked constant-time by
// requireMediaSignature): it re-asserts the content-hash binding against the
// live evidence row, opens the object through the storage seam and writes
// the access-audit row — an audit failure fails the request, so bytes are
// never served unaudited (ADR-0004).
func (s *Service) OpenEvidenceMedia(ctx context.Context, grant MediaGrant) (MediaObject, error) {
	stack, ok := s.mediaStack()
	if !ok {
		return MediaObject{}, ErrMediaSigningUnavailable
	}
	evidence, err := stack.backend.EvidenceByID(ctx, grant.SessionID, grant.EvidenceID)
	if err != nil {
		return MediaObject{}, err
	}
	// The signature was minted for THIS object version; if the live row no
	// longer carries the same content hash the URL is stale — 404, never
	// bytes of some other version.
	if evidence.ContentHash != grant.ContentHash {
		return MediaObject{}, fmt.Errorf("inspections: media object %s: %w", grant.EvidenceID, ErrMediaNotFound)
	}
	obj, err := stack.backend.Open(ctx, MediaObjectRef{
		SessionID:   grant.SessionID,
		EvidenceID:  grant.EvidenceID,
		ContentHash: grant.ContentHash,
		Kind:        evidence.Kind,
	})
	if err != nil {
		return MediaObject{}, err
	}
	audit := EvidenceMediaAudit{
		ActorID:     grant.Subject,
		Action:      AuditActionEvidenceMediaServed,
		SessionID:   grant.SessionID,
		EvidenceID:  grant.EvidenceID,
		ContentHash: grant.ContentHash,
		Audience:    grant.Audience,
	}
	if err := stack.backend.InsertMediaAudit(ctx, audit); err != nil {
		_ = obj.Reader.Close()
		return MediaObject{}, fmt.Errorf("inspections: serve evidence media: %w", err)
	}
	return obj, nil
}

// canonical renders the MAC input for a grant. Field order is fixed; the
// domain separator pins the URL version. Every field that identifies WHAT is
// authorized (method, object path, evidence identity, content hash) and FOR
// WHOM/UNTIL WHEN (subject, expiry, audience) is inside the MAC — nothing
// security-relevant rides outside the signature.
func (m *MediaSigner) canonical(grant MediaGrant) string {
	return strings.Join([]string{
		evidenceMACDomain,
		grant.Method,
		grant.objectPath(),
		grant.SessionID.String(),
		grant.EvidenceID.String(),
		grant.ContentHash,
		strconv.FormatInt(grant.ExpiresAt.Unix(), 10),
		grant.Audience,
		grant.Subject.String(),
	}, "\n")
}

// objectPath is the exact request path the grant is valid on. Path-pinned:
// a signature is scoped to this one object route, never a path prefix.
func (g MediaGrant) objectPath() string {
	return MediaObjectPath + "/" + g.SessionID.String() + "/" + g.EvidenceID.String()
}

// mac computes the HMAC-SHA256 over the grant's canonical form.
func (m *MediaSigner) mac(grant MediaGrant) []byte {
	h := hmac.New(sha256.New, m.key)
	_, _ = h.Write([]byte(m.canonical(grant)))
	return h.Sum(nil)
}

// Sign mints a signed URL for grant: it stamps Method (GET), Audience and
// ExpiresAt (now+TTL), computes the HMAC-SHA256 signature and renders the
// query parameters of the fetch URL. The returned grant carries the final
// ExpiresAt for the response body's expires_at claim.
func (m *MediaSigner) Sign(grant MediaGrant) (MediaGrant, url.Values, error) {
	if m == nil {
		return MediaGrant{}, nil, ErrMediaSigningUnavailable
	}
	if grant.SessionID == uuid.Nil || grant.EvidenceID == uuid.Nil || grant.Subject == uuid.Nil {
		return MediaGrant{}, nil, invalidInput("sign media url: session, evidence and subject identifiers are required")
	}
	if !mediaGrantHashPattern.MatchString(grant.ContentHash) {
		return MediaGrant{}, nil, invalidInput("sign media url: content_hash must be a lowercase hex SHA-256")
	}
	grant.Method = http.MethodGet
	grant.Audience = AudienceEvidenceMedia
	grant.ExpiresAt = m.now().Add(m.ttl).UTC().Truncate(time.Second)

	query := url.Values{}
	query.Set("v", MediaURLVersion)
	query.Set("aud", grant.Audience)
	query.Set("exp", strconv.FormatInt(grant.ExpiresAt.Unix(), 10))
	query.Set("sub", grant.Subject.String())
	query.Set("hash", grant.ContentHash)
	query.Set("sig", hex.EncodeToString(m.mac(grant)))
	return grant, query, nil
}

// Verify validates a signed fetch request: parameter shape, constant-time
// MAC comparison, zero-leeway TTL and the purpose binding. method and the
// session/evidence identifiers come from the REQUEST (never from the query),
// so a signature cannot be re-pointed at another method or object. On
// success it returns the recovered grant.
func (m *MediaSigner) Verify(method string, sessionID, evidenceID uuid.UUID, query url.Values) (MediaGrant, error) {
	if m == nil {
		return MediaGrant{}, ErrMediaSigningUnavailable
	}
	if query.Get("v") != MediaURLVersion {
		return MediaGrant{}, ErrMediaGrantInvalid
	}
	rawExp := query.Get("exp")
	expUnix, err := strconv.ParseInt(rawExp, 10, 64)
	if err != nil || expUnix <= 0 {
		return MediaGrant{}, ErrMediaGrantInvalid
	}
	sub, err := uuid.Parse(query.Get("sub"))
	if err != nil {
		return MediaGrant{}, ErrMediaGrantInvalid
	}
	contentHash := query.Get("hash")
	if !mediaGrantHashPattern.MatchString(contentHash) {
		return MediaGrant{}, ErrMediaGrantInvalid
	}
	sig := query.Get("sig")
	if !mediaGrantSigPattern.MatchString(sig) {
		return MediaGrant{}, ErrMediaGrantInvalid
	}
	audience := query.Get("aud")
	if audience == "" {
		return MediaGrant{}, ErrMediaGrantInvalid
	}

	grant := MediaGrant{
		SessionID:   sessionID,
		EvidenceID:  evidenceID,
		ContentHash: contentHash,
		Subject:     sub,
		Audience:    audience,
		Method:      method,
		ExpiresAt:   time.Unix(expUnix, 0).UTC(),
	}
	// Constant-time comparison (hmac.Equal → subtle.ConstantTimeCompare):
	// signature verification does not leak how many bytes matched.
	if !hmac.Equal(m.mac(grant), mustHex(sig)) {
		return MediaGrant{}, ErrMediaGrantInvalid
	}
	// Zero-leeway TTL: expires_at is the exact, honest cutoff the client
	// was told. Past it the URL is dead.
	if m.now().After(grant.ExpiresAt) {
		return MediaGrant{}, ErrMediaGrantExpired
	}
	if grant.Audience != AudienceEvidenceMedia {
		return MediaGrant{}, ErrMediaGrantAudience
	}
	return grant, nil
}

// mustHex decodes a 64-hex string that mediaGrantSigPattern already
// validated; a decode failure is impossible and yields an unusable
// (never-equal) buffer rather than a panic.
func mustHex(s string) []byte {
	out, err := hex.DecodeString(s)
	if err != nil {
		return make([]byte, sha256.Size)
	}
	return out
}

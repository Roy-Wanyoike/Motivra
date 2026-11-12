package inspections

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// Evidence-media HTTP surface tests (issue #186). The four mandatory proofs
// of the issue live here end-to-end through the real router:
//
//   - an expired URL is rejected            (TestEvidenceMediaFetchRejectsExpiredURL)
//   - a tampered signature is rejected      (TestEvidenceMediaFetchRejectsTamperedSignature)
//   - a valid URL works within the TTL      (TestEvidenceMediaFetchRoundTrip)
//   - anonymous direct access is rejected   (TestEvidenceMediaFetchRejectsAnonymousDirectAccess)

// fakeMediaBackend is an in-memory MediaBackend: the evidence rows and the
// platform audit trail it writes, plus object bytes standing in for the
// future S3-compatible adapter (the seam's second half).
type fakeMediaBackend struct {
	mu          sync.Mutex
	evidence    map[uuid.UUID]Evidence // by evidence id
	objects     map[string][]byte      // "sessionID/evidenceID" -> bytes
	contentType map[string]string      // same key -> stored content type
	audits      []EvidenceMediaAudit
	failAudit   error // when set, InsertMediaAudit fails (audit-obligation tests)
}

func newFakeMediaBackend() *fakeMediaBackend {
	return &fakeMediaBackend{
		evidence:    make(map[uuid.UUID]Evidence),
		objects:     make(map[string][]byte),
		contentType: make(map[string]string),
	}
}

func (f *fakeMediaBackend) EvidenceByID(_ context.Context, sessionID, evidenceID uuid.UUID) (Evidence, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.evidence[evidenceID]
	if !ok || e.SessionID != sessionID {
		return Evidence{}, fmt.Errorf("inspections: get evidence %s: %w", evidenceID, ErrMediaNotFound)
	}
	return e, nil
}

func (f *fakeMediaBackend) Open(_ context.Context, ref MediaObjectRef) (MediaObject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := ref.SessionID.String() + "/" + ref.EvidenceID.String()
	b, ok := f.objects[key]
	if !ok {
		return MediaObject{}, fmt.Errorf("inspections: open media object: %w", ErrMediaNotFound)
	}
	return MediaObject{
		Reader:      io.NopCloser(strings.NewReader(string(b))),
		Size:        int64(len(b)),
		ContentType: f.contentType[key],
	}, nil
}

func (f *fakeMediaBackend) InsertMediaAudit(_ context.Context, a EvidenceMediaAudit) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failAudit != nil {
		return f.failAudit
	}
	a.CreatedAt = time.Now().UTC()
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeMediaBackend) auditActions(t *testing.T) []EvidenceMediaAudit {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]EvidenceMediaAudit(nil), f.audits...)
}

func (f *fakeMediaBackend) setObject(t *testing.T, sessionID, evidenceID uuid.UUID, contentType string, body []byte) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	key := sessionID.String() + "/" + evidenceID.String()
	f.objects[key] = body
	f.contentType[key] = contentType
}

// mediaFixture is a seeded media test world: one tenant session in
// IN_PROGRESS with one PHOTO evidence record, its bytes behind the backend,
// and the staff principal that captured them.
type mediaFixture struct {
	handler    http.Handler
	store      *fakeStore
	backend    *fakeMediaBackend
	signer     *MediaSigner
	session    Session
	evidence   Evidence
	tenantID   uuid.UUID
	staffID    uuid.UUID
	staffToken string
}

// newMediaFixture wires the full API (auth middleware + Routes, exactly like
// cmd/inspections) over a media-enabled service.
func newMediaFixture(t *testing.T) *mediaFixture {
	t.Helper()
	store := newFakeStore()
	backend := newFakeMediaBackend()
	signer, err := NewMediaSigner(mediaTestKey)
	require.NoError(t, err)
	svc := NewService(store, &capturePublisher{}, nil, WithMedia(signer, backend))

	tenantID := uuid.New()
	staffID := uuid.New()
	scope := Scope{TenantID: &tenantID}
	sess := &Session{
		TenantID:     &tenantID,
		VehicleClass: ClassSedan,
		InspectorID:  staffID,
		Status:       StatusCreated,
	}
	ctx := context.Background()
	require.NoError(t, svc.CreateSession(ctx, sess))
	_, err = svc.Start(ctx, sess.ID, staffID, scope)
	require.NoError(t, err)
	evidence, err := svc.AttachEvidence(ctx, sess.ID, &Evidence{
		Kind:        EvidenceKindPhoto,
		ContentHash: validHash("media-fixture-photo"),
		CapturedAt:  time.Now().UTC().Add(-time.Hour),
		CapturedBy:  &staffID,
		Uploaded:    true,
	}, staffID, scope)
	require.NoError(t, err)

	backend.evidence[evidence.ID] = evidence
	backend.setObject(t, sess.ID, evidence.ID, "image/jpeg", []byte("fake-jpeg-bytes-for-evidence-media"))

	validator := platform.NewJWTValidator(httpTestSecret, "motivra-identity", "motivra")
	router := chi.NewRouter()
	router.Use(platform.AuthMiddleware(validator))
	Routes(router, svc, platform.RequireAuthenticated)

	return &mediaFixture{
		handler:    router,
		store:      store,
		backend:    backend,
		signer:     signer,
		session:    *sess,
		evidence:   evidence,
		tenantID:   tenantID,
		staffID:    staffID,
		staffToken: mintTenantToken(t, staffID.String(), RoleTechnician, tenantID.String()),
	}
}

// mediaPath is the mint endpoint's contract path.
func (f *mediaFixture) mediaPath() string {
	return "/v1/inspections/sessions/" + f.session.ID.String() + "/evidence/" + f.evidence.ID.String() + "/media"
}

// objectPath is the signed fetch route's path for the fixture's evidence.
func (f *mediaFixture) objectPath() string {
	return MediaObjectPath + "/" + f.session.ID.String() + "/" + f.evidence.ID.String()
}

// mint issues a signed URL through the API with the fixture's staff token.
func (f *mediaFixture) mint(t *testing.T) (string, url.Values, time.Time) {
	t.Helper()
	return f.mintWithToken(t, f.staffToken)
}

func (f *mediaFixture) mintWithToken(t *testing.T, token string) (string, url.Values, time.Time) {
	t.Helper()
	w := do(t, f.handler, http.MethodGet, f.mediaPath(), token, nil)
	require.Equal(t, http.StatusOK, w.Code, "mint: %s", w.Body.String())
	body := decodeBody(t, w)
	rawURL, ok := body["url"].(string)
	require.True(t, ok, "url must be a string: %s", w.Body.String())
	method, ok := body["method"].(string)
	require.True(t, ok)
	require.Equal(t, http.MethodGet, method)
	expiresRaw, ok := body["expires_at"].(string)
	require.True(t, ok, "expires_at must be an RFC3339 string: %s", w.Body.String())
	expiresAt, err := time.Parse(time.RFC3339, expiresRaw)
	require.NoError(t, err)

	parsed, err := url.Parse(rawURL)
	require.NoError(t, err)
	require.Equal(t, f.objectPath(), parsed.Path, "the signed URL is path-pinned to the object route")
	return rawURL, parsed.Query(), expiresAt
}

// TestEvidenceMediaMintContract pins the issue's exact response shape and
// the bindings inside the minted URL.
func TestEvidenceMediaMintContract(t *testing.T) {
	t.Parallel()
	f := newMediaFixture(t)

	rawURL, query, expiresAt := f.mint(t)
	assert.True(t, strings.HasPrefix(rawURL, "http://"), "development requests without TLS mint an http URL: %s", rawURL)

	assert.Equal(t, MediaURLVersion, query.Get("v"))
	assert.Equal(t, AudienceEvidenceMedia, query.Get("aud"))
	assert.Equal(t, f.evidence.ContentHash, query.Get("hash"))
	assert.Equal(t, f.staffID.String(), query.Get("sub"))
	assert.Equal(t, expiresAt.Unix(), mustInt64(t, query.Get("exp")))
	assert.Regexp(t, `^[0-9a-f]{64}$`, query.Get("sig"))

	// The TTL matches the signer's configured window (default 5 minutes).
	assert.WithinDuration(t, time.Now().Add(DefaultEvidenceURLTTL), expiresAt, 5*time.Second)

	// The mint is audited with the authenticated actor.
	audits := f.backend.auditActions(t)
	require.Len(t, audits, 1)
	assert.Equal(t, AuditActionEvidenceMediaURLIssued, audits[0].Action)
	assert.Equal(t, f.staffID, audits[0].ActorID)
	assert.Equal(t, f.session.ID, audits[0].SessionID)
	assert.Equal(t, f.evidence.ID, audits[0].EvidenceID)
}

// TestEvidenceMediaMintRequiresAuthentication: the mint endpoint is a normal
// authenticated, scoped read — never open.
func TestEvidenceMediaMintRequiresAuthentication(t *testing.T) {
	t.Parallel()
	f := newMediaFixture(t)
	w := do(t, f.handler, http.MethodGet, f.mediaPath(), "", nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "application/problem+json", w.Header().Get("Content-Type"))
}

// TestEvidenceMediaMintScopeEnforcement pins the issue's scope rule: the
// caller's scope is applied to BOTH the session and the evidence row before
// minting; invisible and unknown resources answer 404.
func TestEvidenceMediaMintScopeEnforcement(t *testing.T) {
	t.Parallel()
	f := newMediaFixture(t)

	otherTenantToken := mintTenantToken(t, uuid.New().String(), RoleTechnician, uuid.New().String())
	unrelatedToken := mintToken(t, uuid.New().String(), RoleCustomer)
	customerID := uuid.New()
	customerToken := mintToken(t, customerID.String(), RoleCustomer)
	otherSessionEvidenceID := uuid.New()

	// A second, customer-visible session in the same store, driven to
	// IN_PROGRESS so it can carry evidence (the sealed trigger mirrors the
	// database discipline).
	customerSession := &Session{
		TenantID:     &f.tenantID,
		VehicleClass: ClassSedan,
		InspectorID:  f.staffID,
		CustomerID:   &customerID,
		Status:       StatusCreated,
	}
	require.NoError(t, f.store.CreateSession(context.Background(), customerSession))
	_, err := NewService(f.store, &capturePublisher{}, nil).Start(context.Background(), customerSession.ID, f.staffID, Scope{TenantID: &f.tenantID})
	require.NoError(t, err)

	otherEvidence := &Evidence{
		ID:          otherSessionEvidenceID,
		SessionID:   customerSession.ID,
		Kind:        EvidenceKindPhoto,
		ContentHash: validHash("other-session"),
		CapturedAt:  time.Now().UTC(),
		Uploaded:    true,
	}
	require.NoError(t, f.store.InsertEvidence(context.Background(), otherEvidence))

	tests := []struct {
		name  string
		token string
		sID   uuid.UUID
		eID   uuid.UUID
		want  int
	}{
		{"cross-tenant staff is 404", otherTenantToken, f.session.ID, f.evidence.ID, http.StatusNotFound},
		{"unrelated personal principal is 404", unrelatedToken, f.session.ID, f.evidence.ID, http.StatusNotFound},
		{"unknown evidence is 404", f.staffToken, f.session.ID, uuid.New(), http.StatusNotFound},
		{"unknown session is 404", f.staffToken, uuid.New(), f.evidence.ID, http.StatusNotFound},
		{"evidence of another session is 404", f.staffToken, customerSession.ID, f.evidence.ID, http.StatusNotFound},
		{"customer of the session can mint", customerToken, customerSession.ID, otherSessionEvidenceID, http.StatusOK},
		{"capturing staff can mint", f.staffToken, f.session.ID, f.evidence.ID, http.StatusOK},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := "/v1/inspections/sessions/" + tc.sID.String() + "/evidence/" + tc.eID.String() + "/media"
			w := do(t, f.handler, http.MethodGet, path, tc.token, nil)
			assert.Equal(t, tc.want, w.Code, "body: %s", w.Body.String())
		})
	}
}

// TestEvidenceMediaFetchRoundTrip is the issue's "valid URL works within
// TTL" proof, end to end: the URL minted by the API serves the bytes with NO
// Authorization header — the signature is the credential.
func TestEvidenceMediaFetchRoundTrip(t *testing.T) {
	t.Parallel()
	f := newMediaFixture(t)
	rawURL, _, _ := f.mint(t)

	w := do(t, f.handler, http.MethodGet, rawURL, "", nil)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, "fake-jpeg-bytes-for-evidence-media", w.Body.String())
	assert.Equal(t, "image/jpeg", w.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "private, no-store", w.Header().Get("Cache-Control"))

	// The serve is audited with the minted-for subject.
	audits := f.backend.auditActions(t)
	require.Len(t, audits, 2, "one url_issued + one served audit")
	assert.Equal(t, AuditActionEvidenceMediaServed, audits[1].Action)
	assert.Equal(t, f.staffID, audits[1].ActorID)
}

// TestEvidenceMediaFetchRejectsAnonymousDirectAccess is the issue's
// "anonymous direct evidence access rejected" proof: the object route (and
// every evidence metadata route) answers 401 without a credential.
func TestEvidenceMediaFetchRejectsAnonymousDirectAccess(t *testing.T) {
	t.Parallel()
	f := newMediaFixture(t)

	tests := []struct {
		name string
		path string
	}{
		{"object route without any query", f.objectPath()},
		{"object route with garbage query", f.objectPath() + "?v=1&aud=evidence-media&exp=1&sub=" + f.staffID.String() + "&hash=" + validHash("x") + "&sig=deadbeef"},
		{"evidence metadata list", "/v1/inspections/sessions/" + f.session.ID.String() + "/evidence"},
		{"mint endpoint", f.mediaPath()},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := do(t, f.handler, http.MethodGet, tc.path, "", nil)
			assert.Equal(t, http.StatusUnauthorized, w.Code, "body: %s", w.Body.String())
			assert.Equal(t, "application/problem+json", w.Header().Get("Content-Type"))
		})
	}
	assert.Empty(t, f.backend.auditActions(t), "refused requests mint nothing and audit no serve")
}

// TestEvidenceMediaFetchRejectsTamperedSignature is the issue's tamper
// proof: any modification of a minted URL is refused 401.
func TestEvidenceMediaFetchRejectsTamperedSignature(t *testing.T) {
	t.Parallel()
	f := newMediaFixture(t)
	rawURL, query, _ := f.mint(t)
	parsed, err := url.Parse(rawURL)
	require.NoError(t, err)

	mutations := map[string]func(q url.Values){
		"flip a signature byte": func(q url.Values) { q.Set("sig", flipHexChar(q.Get("sig"))) },
		"drop the signature":    func(q url.Values) { q.Del("sig") },
		"extend the expiry":     func(q url.Values) { q.Set("exp", "9999999999") },
		"swap the content hash": func(q url.Values) { q.Set("hash", validHash("evil")) },
		"swap the audience":     func(q url.Values) { q.Set("aud", "evidence-upload") },
		"swap the subject":      func(q url.Values) { q.Set("sub", uuid.NewString()) },
		"bump the version":      func(q url.Values) { q.Set("v", "2") },
	}
	for name, mutate := range mutations {
		mutate, q := mutate, cloneQuery(t, query)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			mutate(q)
			tampered := parsed.Scheme + "://" + parsed.Host + parsed.Path + "?" + q.Encode()
			w := do(t, f.handler, http.MethodGet, tampered, "", nil)
			assert.Equal(t, http.StatusUnauthorized, w.Code, "body: %s", w.Body.String())
		})
	}

	// Cross-object replay: a valid URL presented for another object (path
	// swapped, query kept) fails — the signature is path-pinned.
	swapped := MediaObjectPath + "/" + uuid.NewString() + "/" + f.evidence.ID.String() + "?" + query.Encode()
	w := do(t, f.handler, http.MethodGet, swapped, "", nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code, "cross-object replay: %s", w.Body.String())
}

// TestEvidenceMediaFetchRejectsExpiredURL is the issue's expiry proof,
// deterministic through the signer's injected clock (no sleeps): one second
// past expires_at the URL is refused; at the exact expiry second it still
// works (zero-leeway, honest cutoff).
func TestEvidenceMediaFetchRejectsExpiredURL(t *testing.T) {
	t.Parallel()
	f := newMediaFixture(t)
	base := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	f.signer.now = func() time.Time { return base }

	rawURL, _, _ := f.mint(t)
	w := do(t, f.handler, http.MethodGet, rawURL, "", nil)
	require.Equal(t, http.StatusOK, w.Code, "within TTL the URL works: %s", w.Body.String())

	// Exactly at expires_at: still valid.
	f.signer.now = func() time.Time { return base.Add(DefaultEvidenceURLTTL) }
	w = do(t, f.handler, http.MethodGet, rawURL, "", nil)
	require.Equal(t, http.StatusOK, w.Code, "the expiry second is inclusive: %s", w.Body.String())

	// One second later: dead.
	f.signer.now = func() time.Time { return base.Add(DefaultEvidenceURLTTL).Add(time.Second) }
	w = do(t, f.handler, http.MethodGet, rawURL, "", nil)
	require.Equal(t, http.StatusUnauthorized, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "expired")
}

// TestEvidenceMediaFetchObjectBinding proves the content-hash binding: a
// signature minted for one object version refuses to serve another, and an
// object the storage seam does not have is a plain 404.
func TestEvidenceMediaFetchObjectBinding(t *testing.T) {
	t.Parallel()
	f := newMediaFixture(t)
	rawURL, _, _ := f.mint(t)

	// The evidence row's content hash drifts (a stale URL for a re-hashed
	// object): the fetch refuses with 404.
	f.backend.mu.Lock()
	drifted := f.backend.evidence[f.evidence.ID]
	drifted.ContentHash = validHash("re-captured")
	f.backend.evidence[f.evidence.ID] = drifted
	f.backend.mu.Unlock()
	w := do(t, f.handler, http.MethodGet, rawURL, "", nil)
	assert.Equal(t, http.StatusNotFound, w.Code, "stale object version: %s", w.Body.String())

	// A valid signature for an object the backend does not store: 404.
	f.backend.mu.Lock()
	f.backend.evidence[f.evidence.ID] = f.evidence // restore the row
	delete(f.backend.objects, f.session.ID.String()+"/"+f.evidence.ID.String())
	f.backend.mu.Unlock()
	w = do(t, f.handler, http.MethodGet, rawURL, "", nil)
	assert.Equal(t, http.StatusNotFound, w.Code, "missing object: %s", w.Body.String())
}

// TestEvidenceMediaAuditRows pins the ADR-0004 audit obligations and that
// credential material never leaks into the audit trail.
func TestEvidenceMediaAuditRows(t *testing.T) {
	t.Parallel()
	f := newMediaFixture(t)
	rawURL, query, _ := f.mint(t)
	w := do(t, f.handler, http.MethodGet, rawURL, "", nil)
	require.Equal(t, http.StatusOK, w.Code)

	audits := f.backend.auditActions(t)
	require.Len(t, audits, 2)
	assert.Equal(t, AuditActionEvidenceMediaURLIssued, audits[0].Action)
	assert.Equal(t, AuditActionEvidenceMediaServed, audits[1].Action)
	assert.Equal(t, f.tenantID, *audits[0].TenantID, "the mint audit carries the actor's tenant")
	assert.Nil(t, audits[1].TenantID, "the fetch has no bearer principal beyond the MAC-bound subject")

	for i, a := range audits {
		meta, err := json.Marshal(a.auditMetadata())
		require.NoError(t, err)
		assert.NotContains(t, string(meta), query.Get("sig"), "audit %d must never carry the signature", i)
		assert.NotContains(t, string(meta), string(mediaTestKey), "audit %d must never carry the signing key", i)
		assert.Contains(t, string(meta), f.evidence.ContentHash)
	}
}

// TestEvidenceMediaFailClosedWithoutStack: without the media stack (no
// signing key / no backend wired) both endpoints fail CLOSED with 503 —
// an unconfigured deployment never mints or serves.
func TestEvidenceMediaFailClosedWithoutStack(t *testing.T) {
	t.Parallel()
	f := newMediaFixture(t)
	validator := platform.NewJWTValidator(httpTestSecret, "motivra-identity", "motivra")
	svc := NewService(f.store, &capturePublisher{}, nil) // no WithMedia
	router := chi.NewRouter()
	router.Use(platform.AuthMiddleware(validator))
	Routes(router, svc, platform.RequireAuthenticated)

	w := do(t, router, http.MethodGet, f.mediaPath(), f.staffToken, nil)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), EnvEvidenceSigningKey)

	w = do(t, router, http.MethodGet, f.objectPath()+"?v=1", "", nil)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code, "body: %s", w.Body.String())
}

// TestEvidenceMediaAuditFailureFailsTheRequest pins the ADR-0004 discipline
// that media is never minted nor served unaudited: an audit write failure
// fails the mint (500, no URL) and the fetch (500, no bytes).
func TestEvidenceMediaAuditFailureFailsTheRequest(t *testing.T) {
	t.Parallel()
	f := newMediaFixture(t)
	f.backend.failAudit = fmt.Errorf("audit sink down")

	w := do(t, f.handler, http.MethodGet, f.mediaPath(), f.staffToken, nil)
	assert.Equal(t, http.StatusInternalServerError, w.Code, "mint without audit must fail: %s", w.Body.String())
	assert.NotContains(t, w.Body.String(), "sig=")

	// The fetch path needs a minted URL; mint directly with the audit sink
	// restored, then break it again for the serve.
	f.backend.failAudit = nil
	rawURL, _, _ := f.mint(t)
	f.backend.failAudit = fmt.Errorf("audit sink down")
	w = do(t, f.handler, http.MethodGet, rawURL, "", nil)
	assert.Equal(t, http.StatusInternalServerError, w.Code, "serve without audit must fail: %s", w.Body.String())
	assert.NotContains(t, w.Body.String(), "fake-jpeg-bytes", "no media bytes are streamed when the audit write fails")
}

// TestEvidenceMediaMethodBinding: the signed URL is GET-bound; the router
// refuses other verbs on the object route (405), so a stolen URL cannot be
// used to POST/PATCH anything.
func TestEvidenceMediaMethodBinding(t *testing.T) {
	t.Parallel()
	f := newMediaFixture(t)
	rawURL, _, _ := f.mint(t)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		w := do(t, f.handler, method, rawURL, "", nil)
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code, "%s must not be served: %s", method, w.Body.String())
	}
}

// compile-time interface assertion for the fake backend.
var _ MediaBackend = (*fakeMediaBackend)(nil)

// keep httptest referenced if helpers above change shape.
var _ = httptest.NewRecorder

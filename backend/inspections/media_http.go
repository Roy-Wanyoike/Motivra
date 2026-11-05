package inspections

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// Evidence-media HTTP surface (issue #186, P1 SECURITY):
//
//	GET /v1/inspections/sessions/{sessionID}/evidence/{evidenceID}/media
//	     authenticated + scoped exactly like every other evidence read; it
//	     mints a short-TTL HMAC-signed URL and answers
//	     200 { "url": ..., "expires_at": ..., "method": "GET" }.
//	     Access is audited (inspections.evidence.media.url_issued).
//	GET /v1/inspections/media/{sessionID}/{evidenceID}?v=..&aud=..&exp=..&sub=..&hash=..&sig=..
//	     the signed fetch route itself: a capability URL with NO bearer
//	     token — the constant-time-verified signature is the credential,
//	     the TTL and purpose binding are enforced server-side, and every
//	     served fetch is audited (inspections.evidence.media.served).
//
// There is deliberately no other route that serves evidence bytes: anonymous
// access without a valid signature is refused 401 everywhere (the metadata
// list endpoints keep their standard RequireAuthenticated gate).
//
// RegisterEvidenceMediaRoutes is called once from Routes; it lives here so
// the media surface stays one reviewable unit (the verification precedent).
func RegisterEvidenceMediaRoutes(r chi.Router, svc *Service, requireAuth func(http.HandlerFunc) http.HandlerFunc) {
	// Mint endpoint — the issue's exact contract path. Registered by
	// absolute path (same {sessionID} param names as the sessions subtree,
	// which it extends without re-registering any existing route).
	r.Get("/v1/inspections/sessions/{sessionID}/evidence/{evidenceID}/media", requireAuth(svc.handleGetEvidenceMedia))
	// Signed object fetch — signature-gated, never bearer-gated, never open.
	r.Route(MediaObjectPath, func(r chi.Router) {
		r.Get("/{sessionID}/{evidenceID}", svc.requireMediaSignature(svc.handleServeEvidenceMedia))
	})
}

// mediaGrantContextKey carries the verified grant from the signature
// middleware to the fetch handler.
type mediaGrantContextKey struct{}

// errMediaUnavailable is the fail-closed answer of an unconfigured media
// stack: no signer (no key) and/or no storage+audit backend, nothing served.
func errMediaUnavailable() *platform.Error {
	return &platform.Error{
		Status: http.StatusServiceUnavailable,
		Code:   "service_unavailable",
		Title:  "Evidence media is not available",
		Detail: "evidence-media signing is not configured on this deployment (" + EnvEvidenceSigningKey + ")",
	}
}

// requireMediaSignature is the signature-verification middleware of the
// object fetch route (issue #186): it verifies the HMAC in constant time,
// enforces the zero-leeway TTL and the purpose binding, and hands the
// recovered grant to the handler through the request context. Everything
// that fails here is an authentication failure (401) — the route never
// serves bytes to an unsigned, tampered, wrong-purpose or expired request.
func (s *Service) requireMediaSignature(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stack, ok := s.mediaStack()
		if !ok {
			platform.WriteError(w, errMediaUnavailable())
			return
		}
		// The route parameters are part of the credential: a malformed
		// identifier cannot be part of a signature we minted.
		sessionID, err := uuidFromRequest(r, "sessionID")
		if err != nil {
			platform.WriteError(w, platform.ErrUnauthorized("signed URL is not valid"))
			return
		}
		evidenceID, err := uuidFromRequest(r, "evidenceID")
		if err != nil {
			platform.WriteError(w, platform.ErrUnauthorized("signed URL is not valid"))
			return
		}
		grant, err := stack.signer.Verify(r.Method, sessionID, evidenceID, r.URL.Query())
		if err != nil {
			writeMediaError(w, err)
			return
		}
		// Path pinning: a verified grant is only valid on the exact
		// canonical object path — %-encoding or re-routing games answer 401.
		if r.URL.Path != grant.objectPath() {
			platform.WriteError(w, platform.ErrUnauthorized("signed URL is not valid for this path"))
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), mediaGrantContextKey{}, grant)))
	}
}

// mediaGrantFromRequest recovers the middleware-verified grant.
func mediaGrantFromRequest(r *http.Request) (MediaGrant, bool) {
	grant, ok := r.Context().Value(mediaGrantContextKey{}).(MediaGrant)
	return grant, ok
}

// evidenceMediaGrantResponse is the mint endpoint's body (the issue's exact
// contract shape).
type evidenceMediaGrantResponse struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
	Method    string    `json:"method"`
}

// mediaURLFromRequest renders the absolute fetch URL for the minted grant.
// The scheme follows the request's TLS termination (X-Forwarded-Proto first,
// the standard reverse-proxy signal): every TLS-terminated topology mints
// https URLs; a plain-http development request mints an http localhost URL.
// The signature does not cover the host — it covers method, object path,
// hash, expiry, audience and subject — so the URL's authority is the
// signature, not the host it is rendered against.
func mediaURLFromRequest(r *http.Request, grant MediaGrant, query url.Values) string {
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
		if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
			scheme = strings.TrimSpace(strings.SplitN(proto, ",", 2)[0])
		}
	}
	u := url.URL{Scheme: scheme, Host: r.Host, Path: grant.objectPath(), RawQuery: query.Encode()}
	return u.String()
}

// handleGetEvidenceMedia implements GET
// /v1/inspections/sessions/{sessionID}/evidence/{evidenceID}/media (the
// issue's required contract): authorize through the standard inspections
// scope against BOTH the session and the evidence row, then mint.
func (s *Service) handleGetEvidenceMedia(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mediaStack(); !ok {
		platform.WriteError(w, errMediaUnavailable())
		return
	}
	actorID, scope, ok := actorAndScope(w, r)
	if !ok {
		return
	}
	sessionID, err := uuidFromRequest(r, "sessionID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	evidenceID, err := uuidFromRequest(r, "evidenceID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	grant, query, err := s.IssueEvidenceMediaURL(r.Context(), sessionID, evidenceID, actorID, scope)
	if err != nil {
		writeMediaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, evidenceMediaGrantResponse{
		URL:       mediaURLFromRequest(r, grant, query),
		ExpiresAt: grant.ExpiresAt,
		Method:    grant.Method,
	})
}

// handleServeEvidenceMedia implements GET /v1/inspections/media/{sessionID}/
// {evidenceID} behind requireMediaSignature. The grant arrives pre-verified;
// this handler binds it to the live evidence row, opens the object, writes
// the access-audit row and streams the bytes with no-store caching (PII
// media is never cacheable by shared caches).
func (s *Service) handleServeEvidenceMedia(w http.ResponseWriter, r *http.Request) {
	grant, ok := mediaGrantFromRequest(r)
	if !ok {
		platform.WriteError(w, platform.ErrUnauthorized("signature verification required"))
		return
	}
	obj, err := s.OpenEvidenceMedia(r.Context(), grant)
	if err != nil {
		writeMediaError(w, err)
		return
	}
	defer func() { _ = obj.Reader.Close() }()

	contentType := strings.TrimSpace(obj.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	if obj.Size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(obj.Size, 10))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, obj.Reader)
}

// writeMediaError maps evidence-media errors onto platform API errors.
// Signature-layer failures are uniformly 401 (authentication failure — no
// probing oracle); unknown errors fall through to platform.WriteError (500
// without internals).
func writeMediaError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, ErrMediaSigningUnavailable):
		platform.WriteError(w, errMediaUnavailable())
	case errors.Is(err, ErrSessionNotFound):
		platform.WriteError(w, platform.ErrNotFound("session not found"))
	case errors.Is(err, ErrMediaNotFound):
		platform.WriteError(w, platform.ErrNotFound("media not found"))
	case errors.Is(err, ErrMediaGrantExpired):
		platform.WriteError(w, platform.ErrUnauthorized("signed URL has expired; request a fresh one"))
	case errors.Is(err, ErrMediaGrantInvalid), errors.Is(err, ErrMediaGrantAudience):
		platform.WriteError(w, platform.ErrUnauthorized("signed URL is not valid"))
	default:
		platform.WriteError(w, err)
	}
}

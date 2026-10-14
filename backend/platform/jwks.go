package platform

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// jwksFetchTimeout bounds a single JWKS fetch; jwksMinRefreshInterval
// rate-limits refetches triggered by unknown kids so a flood of forged kid
// headers cannot turn verifiers into a DoS amplifier against the issuer.
const (
	jwksFetchTimeout     = 5 * time.Second
	jwksCacheTTL         = 5 * time.Minute
	jwksMinRefreshGap    = time.Second
	jwksHTTPMaxBodyBytes = 1 << 20 // 1 MiB is far beyond any legitimate key set
)

// JWK is one RFC 7517 JSON Web Key as advertised by the identity service.
// Only the fields Motivra uses are modelled; the raw form survives
// round-trips through the maps of remote documents.
type JWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use,omitempty"`
	Crv string `json:"crv,omitempty"` // OKP only: Ed25519
	X   string `json:"x,omitempty"`   // OKP only: base64url public key
	N   string `json:"n,omitempty"`   // RSA only: modulus
	E   string `json:"e,omitempty"`   // RSA only: exponent
}

// jwksDocument is the wire form of a key set.
type jwksDocument struct {
	Keys []JWK `json:"keys"`
}

// publicKeyToJWK renders pub (Ed25519 or RSA) as a signing JWK under kid.
func publicKeyToJWK(pub crypto.PublicKey, kid, alg string) JWK {
	switch k := pub.(type) {
	case ed25519.PublicKey:
		return JWK{
			Kty: "OKP",
			Kid: kid,
			Alg: alg,
			Use: "sig",
			Crv: "Ed25519",
			X:   base64.RawURLEncoding.EncodeToString(k),
		}
	case *rsa.PublicKey:
		return JWK{
			Kty: "RSA",
			Kid: kid,
			Alg: alg,
			Use: "sig",
			N:   bigIntBase64URL(k.N),
			E:   bigIntBase64URL(big.NewInt(int64(k.E))),
		}
	default:
		return JWK{}
	}
}

// jwkToPublicKey converts a JWK into the crypto public key the JWT library
// verifies with. Keys of other families are refused, not skipped silently.
func jwkToPublicKey(j JWK) (crypto.PublicKey, error) {
	switch j.Kty {
	case "OKP":
		if j.Crv != "Ed25519" {
			return nil, fmt.Errorf("jwks: unsupported curve %q", j.Crv)
		}
		raw, err := base64.RawURLEncoding.DecodeString(j.X)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("jwks: invalid Ed25519 x for kid %q", j.Kid)
		}
		return ed25519.PublicKey(raw), nil
	case "RSA":
		n, err := base64.RawURLEncoding.DecodeString(j.N)
		if err != nil || len(n) == 0 {
			return nil, fmt.Errorf("jwks: invalid RSA modulus for kid %q", j.Kid)
		}
		e, err := base64.RawURLEncoding.DecodeString(j.E)
		if err != nil || len(e) == 0 || len(e) > 4 {
			return nil, fmt.Errorf("jwks: invalid RSA exponent for kid %q", j.Kid)
		}
		exp := 0
		for _, b := range e {
			exp = exp<<8 | int(b)
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exp}, nil
	default:
		return nil, fmt.Errorf("jwks: unsupported key type %q", j.Kty)
	}
}

// jwksCache fetches and caches the identity service's public key set for
// verifier services (issue #87, ADR-0007). Rotation story:
//
//   - A token with an UNKNOWN kid triggers a refetch (rate-limited to one
//     per jwksMinRefreshGap), so a rotated-in key is picked up within one
//     verification of the first new-kid token.
//   - The whole set is refreshed lazily after jwksCacheTTL, so a rotated-OUT
//     kid disappears at most one TTL later — and old tokens are refused the
//     moment the kid is gone. Because access tokens live shorter than the
//     TTL+gap by policy (15-minute default), every stale token is invalid
//     within one access TTL of rotation completion (RUNBOOK §8).
//   - A failed refetch serves the stale set (availability beats a hard
//     auth outage; the readyz check surfaces persistent fetch failures).
type jwksCache struct {
	url    string
	client *http.Client

	// minRefresh bounds unknown-kid-triggered refetches (anti-stampede
	// against forged kid floods); the first (cache-warming) fetch and the
	// TTL refresh bypass it.
	minRefresh time.Duration

	mu        sync.Mutex
	keys      map[string]crypto.PublicKey // kid -> public key
	fetchedAt time.Time
	lastTry   time.Time
	lastErr   error
	now       func() time.Time
}

// newJWKSCache builds a cache over the given JWKS endpoint.
func newJWKSCache(url string) *jwksCache {
	return &jwksCache{
		url:        url,
		client:     &http.Client{Timeout: jwksFetchTimeout},
		minRefresh: jwksMinRefreshGap,
		now:        time.Now,
	}
}

// lookup returns the public key for kid/alg, refetching the set when the
// kid is unknown (or the cache went stale). alg must match the advertised
// key family (EdDSA for OKP/Ed25519, RS256 for RSA).
func (c *jwksCache) lookup(kid, alg string) (crypto.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	_, known := c.keys[kid]
	if !known {
		// Unknown kid: refresh, bounded by the anti-stampede gap. The FIRST
		// fetch (empty cache) bypasses the gap — a verifier must warm up
		// immediately, and a rotated-in key must be picked up on its first
		// sighting.
		if !c.lastTry.IsZero() && now.Sub(c.lastTry) < c.minRefresh {
			return nil, fmt.Errorf("jwks: unknown kid %q", kid)
		}
		c.lastTry = now
		c.fetchLocked()
	} else if now.Sub(c.fetchedAt) > jwksCacheTTL {
		// Known kid but stale set: proactive TTL refresh; serve stale on failure.
		c.fetchLocked()
	}
	key, known := c.keys[kid]
	if !known {
		if c.lastErr != nil {
			return nil, fmt.Errorf("jwks: unknown kid %q (last fetch: %v)", kid, c.lastErr)
		}
		return nil, fmt.Errorf("jwks: unknown kid %q", kid)
	}
	if !algMatchesKey(alg, key) {
		return nil, fmt.Errorf("jwks: alg %q does not match the key registered for kid %q", alg, kid)
	}
	return key, nil
}

// fetchLocked retrieves and swaps the key set. The stale set survives a
// failed fetch (lastErr records the failure for diagnostics and readyz).
func (c *jwksCache) fetchLocked() {
	keys, err := fetchJWKS(context.Background(), c.client, c.url)
	if err != nil {
		c.lastErr = err
		if c.keys == nil {
			c.keys = map[string]crypto.PublicKey{}
		}
		return
	}
	c.lastErr = nil
	c.keys = keys
	c.fetchedAt = c.now()
}

// healthy reports whether the key set is trustworthy right now: the most
// recent fetch succeeded AND yielded at least one usable key. A stale set
// after a failed refetch keeps serving tokens but reports degraded
// (readyz red).
func (c *jwksCache) healthy() bool {
	if c == nil {
		return true // no JWKS configured: nothing to be unhealthy about
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr == nil && len(c.keys) > 0
}

// ensure warms the cache: a fetch runs when the set is empty. ReadyCheck
// uses it so /readyz exercises the endpoint even before the first token.
func (c *jwksCache) ensure() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.keys) == 0 {
		c.fetchLocked()
	}
}

// fetchJWKS retrieves the document at url and converts every supported key.
func fetchJWKS(ctx context.Context, client *http.Client, url string) (map[string]crypto.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("jwks: build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jwks: fetch %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks: fetch %s: unexpected status %d", url, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, jwksHTTPMaxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("jwks: read body: %w", err)
	}
	if len(raw) > jwksHTTPMaxBodyBytes {
		return nil, errors.New("jwks: response exceeds the 1 MiB cap")
	}
	var doc jwksDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("jwks: parse document: %w", err)
	}
	keys := make(map[string]crypto.PublicKey, len(doc.Keys))
	for _, j := range doc.Keys {
		if j.Use != "" && j.Use != "sig" {
			continue
		}
		if j.Kid == "" {
			continue // unnamed keys cannot be selected by the kid header
		}
		if j.Alg != "" && j.Alg != "EdDSA" && j.Alg != "RS256" {
			continue
		}
		pub, err := jwkToPublicKey(j)
		if err != nil {
			return nil, err
		}
		keys[j.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, errors.New("jwks: document carries no usable signing keys")
	}
	return keys, nil
}

// algMatchesKey reports whether the JOSE alg can verify with the key type.
func algMatchesKey(alg string, key crypto.PublicKey) bool {
	switch key.(type) {
	case ed25519.PublicKey:
		return alg == "EdDSA"
	case *rsa.PublicKey:
		return alg == "RS256"
	default:
		return false
	}
}

// marshalJWKS renders a key set document.
func marshalJWKS(keys []JWK) json.RawMessage {
	raw, err := json.Marshal(jwksDocument{Keys: keys})
	if err != nil { // unreachable for JWK field types; stay total
		return json.RawMessage(`{"keys":[]}`)
	}
	return raw
}

// sanitizeJWKSLogURL trims credentials from a JWKS URL for safe logging.
func sanitizeJWKSLogURL(url string) string {
	if i := strings.Index(url, "@"); i >= 0 && strings.Contains(url[:i], "://") {
		if j := strings.Index(url[:i], "://"); j >= 0 {
			return url[:j+3] + "***" + url[i:]
		}
	}
	return url
}

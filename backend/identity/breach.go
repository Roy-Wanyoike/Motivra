package identity

import (
	"context"
	"crypto/sha1" //nolint:gosec // HIBP's k-anonymity protocol is defined over SHA-1; not a password-storage use.
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// PasswordBreacher is the breached-password check seam of ADR-0004
// ("Password policy: length-first (minimum 10), breach-list checking, no
// composition theater", issue #86). Implementations answer whether a NEW
// password (registration, password change) appears in a known breach or
// common-password corpus. The check always runs BEFORE argon2id hashing so
// doomed passwords never pay the 64 MB derivation cost.
//
// The error return means "the check could not be completed" (network
// failure, upstream 5xx) — never "the password is fine". The Service
// applies the configured availability policy to that error: fail-open with
// an audit note (default) or fail-closed (MOTIVRA_BREACH_FAIL_CLOSED).
type PasswordBreacher interface {
	PasswordBreached(ctx context.Context, password string) (bool, error)
}

// DefaultHIBPBaseURL is the production HIBP k-anonymity range endpoint.
const DefaultHIBPBaseURL = "https://api.pwnedpasswords.com"

// DefaultHIBPTimeout bounds a single HIBP range lookup. It is a constructor
// parameter (not an env contract) so deployments can tune it without a new
// variable; the default keeps registration latency bounded.
const DefaultHIBPTimeout = 3 * time.Second

// hibpMaxBodyBytes caps the range response read (a full range page is well
// under 100 KB; the cap only guards against a hostile or broken endpoint).
const hibpMaxBodyBytes = 2 << 20

// HIBPChecker implements PasswordBreacher with the Have I Been Pwned
// k-anonymity API: only the first five characters of the password's SHA-1
// digest ever leave the process, so the password itself is never disclosed
// to the remote service. Env-gated at the composition root
// (MOTIVRA_HIBP_ENABLED, default off in development).
type HIBPChecker struct {
	baseURL string
	client  *http.Client
}

// NewHIBPChecker returns a checker against the given range-endpoint base
// URL (use DefaultHIBPBaseURL in production, an httptest server in tests)
// with a per-request timeout.
func NewHIBPChecker(baseURL string, timeout time.Duration) *HIBPChecker {
	return &HIBPChecker{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: timeout},
	}
}

// PasswordBreached performs the k-anonymity lookup: SHA-1 the password,
// request the 5-character-prefix range, and match the remaining digest
// suffix case-insensitively against the returned "SUFFIX:COUNT" lines.
func (h *HIBPChecker) PasswordBreached(ctx context.Context, password string) (bool, error) {
	sum := sha1.Sum([]byte(password))
	digest := strings.ToUpper(hex.EncodeToString(sum[:]))
	prefix, suffix := digest[:5], digest[5:]

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.baseURL+"/range/"+prefix, nil)
	if err != nil {
		return false, fmt.Errorf("hibp request: %w", err)
	}
	req.Header.Set("User-Agent", "motivra-identity")
	req.Header.Set("Add-Padding", "true")
	resp, err := h.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("hibp range lookup: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("hibp range lookup: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, hibpMaxBodyBytes))
	if err != nil {
		return false, fmt.Errorf("hibp range body: %w", err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			continue // tolerate blank/malformed lines
		}
		if !strings.EqualFold(strings.TrimSpace(line[:colon]), suffix) {
			continue
		}
		count := 0
		if _, err := fmt.Sscanf(strings.TrimSpace(line[colon+1:]), "%d", &count); err != nil {
			// Our own suffix appeared but is unreadable: the check
			// did not complete, so surface an error instead of a
			// silent not-breached answer.
			return false, fmt.Errorf("hibp range body: malformed count for suffix: %w", err)
		}
		return count > 0, nil
	}
	return false, nil
}

// defaultDenylist is the embedded local fallback corpus: a small selection
// of the persistently top-ranked breached and common passwords. It answers
// without network access — the floor under MOTIVRA_HIBP_ENABLED=false and
// the fallback when HIBP is unreachable.
var defaultDenylist = []string{
	"123456", "password", "123456789", "12345678", "12345", "qwerty",
	"1234567890", "1234567", "abc123", "111111", "password1", "password123",
	"qwerty123", "iloveyou", "admin", "welcome", "monkey", "dragon",
	"letmein", "football", "000000", "123123", "654321", "superman",
	"batman", "trustno1", "hello123", "freedom", "whatever", "qazwsx",
	"google", "zaq12wsx", "asdfghjkl", "passw0rd", "starwars", "1q2w3e4r",
	"lovely", "solo", "master", "sunshine", "princess", "shadow",
	"motivra", "motivra123", "changeme", "secret",
}

// DenylistChecker implements PasswordBreacher with a local, in-memory set.
// Matching is exact and case-insensitive (substring matching would
// false-positive long passphrases containing these strings).
type DenylistChecker struct {
	entries map[string]struct{}
}

// NewDenylistChecker returns a checker seeded with the embedded top-common
// password list.
func NewDenylistChecker() *DenylistChecker {
	d := &DenylistChecker{entries: make(map[string]struct{}, len(defaultDenylist))}
	for _, pw := range defaultDenylist {
		d.entries[strings.ToLower(pw)] = struct{}{}
	}
	return d
}

// LoadFile merges a configuration-supplied denylist file (one password per
// line; blank lines and # comments ignored) into the checker. Called at
// boot: an unreadable file fails service startup rather than silently
// running without the operator's entries.
func (d *DenylistChecker) LoadFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read breach denylist: %w", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		d.entries[strings.ToLower(line)] = struct{}{}
	}
	return nil
}

// PasswordBreached reports exact, case-insensitive membership. It never
// errors: the corpus is fully loaded at construction time.
func (d *DenylistChecker) PasswordBreached(_ context.Context, password string) (bool, error) {
	_, hit := d.entries[strings.ToLower(password)]
	return hit, nil
}

// ChainBreacher composes PasswordBreacher implementations in fixed order
// (local denylist first, HIBP second) and short-circuits on the first
// positive answer — a denylist hit never sends a network request. A
// checker error is not absorbed by later entries: an incomplete check must
// stay visible to the Service's fail-open/fail-closed policy, so the
// denylist answering "no" can never silently stand in for a failed HIBP
// lookup.
type ChainBreacher struct {
	checkers []PasswordBreacher
}

// NewBreacher assembles the deployment's breach-check chain from the
// environment configuration: the local denylist (embedded list plus the
// optional MOTIVRA_BREACH_DENYLIST_FILE entries) always runs, and the HIBP
// k-anonymity checker is appended when MOTIVRA_HIBP_ENABLED is true.
func NewBreacher(cfg BreachConfig) (PasswordBreacher, error) {
	deny := NewDenylistChecker()
	if cfg.DenylistFile != "" {
		if err := deny.LoadFile(cfg.DenylistFile); err != nil {
			return nil, err
		}
	}
	checkers := []PasswordBreacher{deny}
	if cfg.HIBPEnabled {
		checkers = append(checkers, NewHIBPChecker(DefaultHIBPBaseURL, DefaultHIBPTimeout))
	}
	return &ChainBreacher{checkers: checkers}, nil
}

// PasswordBreached runs the chain in order and returns the first positive.
// The last error wins when every checker that ran errored.
func (c *ChainBreacher) PasswordBreached(ctx context.Context, password string) (bool, error) {
	var lastErr error
	for _, checker := range c.checkers {
		breached, err := checker.PasswordBreached(ctx, password)
		if err != nil {
			lastErr = err
			continue
		}
		if breached {
			return true, nil
		}
	}
	if lastErr != nil {
		return false, lastErr
	}
	return false, nil
}

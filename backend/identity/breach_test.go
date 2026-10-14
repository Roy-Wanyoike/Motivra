package identity

import (
	"context"
	"crypto/sha1" //nolint:gosec // mirrors HIBP's protocol for test fixtures only.
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// stubBreacher is the injected checker interface fixture: the acceptance
// criterion "breached passwords rejected at registration (test with a
// known-breached sample via an injected checker interface)" runs through
// it, plus the fail-open/fail-closed policy paths.
type stubBreacher struct {
	// breached is the blanket answer for every password.
	breached bool
	// breachPassword, when set, makes ONLY that password report breached
	// (multi-step flows that register cleanly and then try a breached
	// change).
	breachPassword string
	err            error

	calls int
}

func (s *stubBreacher) PasswordBreached(_ context.Context, password string) (bool, error) {
	s.calls++
	if s.breachPassword != "" {
		return password == s.breachPassword, nil
	}
	return s.breached, s.err
}

// newBreachTestService wires a Service with the given checker and policy.
func newBreachTestService(breacher PasswordBreacher, failClosed bool) (*Service, *fakeStore) {
	store := newFakeStore()
	cfg := DefaultVerificationConfig()
	cfg.DevEcho = true
	svc := NewService(store, newTestIssuer(), nil,
		WithPasswordBreacher(breacher, failClosed),
		WithVerification(cfg),
	)
	return svc, store
}

// knownBreachedSample is a policy-length password (>10 chars) standing in
// for a corpus hit; short passwords are skipped by the check on purpose.
const knownBreachedSample = "breached-sample-password"

func TestRegisterRejectsBreachedPasswordViaInjectedChecker(t *testing.T) {
	stub := &stubBreacher{breached: true}
	svc, store := newBreachTestService(stub, false)

	_, err := svc.Register(t.Context(), "breached@example.com", "", knownBreachedSample, "Breached", "", "")
	e := appErr(t, err)
	require.Equal(t, 422, e.Status, "breached passwords are rejected at registration")
	require.Equal(t, "validation_failed", e.Code)
	require.NotEmpty(t, e.FieldErrors)
	require.Equal(t, "password", e.FieldErrors[0].Field)
	require.Equal(t, "breached", e.FieldErrors[0].Issue)
	require.Equal(t, 1, stub.calls, "the checker runs exactly once per registration")
	require.Empty(t, store.users, "no account is created for a breached password")
	require.Empty(t, store.audits, "rejected registrations are not audited")
}

func TestRegisterAcceptsStrongPasswordViaInjectedChecker(t *testing.T) {
	stub := &stubBreacher{breached: false}
	svc, store := newBreachTestService(stub, false)

	resp, err := svc.Register(t.Context(), "strong@example.com", "", "correct-horse-9", "Strong", "", "")
	require.NoError(t, err)
	require.NotEmpty(t, resp.AccessToken)
	require.Equal(t, 1, stub.calls)
	require.Len(t, store.auditsByAction(ActionUserRegistered), 1)
	require.Empty(t, store.auditsByAction(ActionBreachCheckUnavailable))
}

func TestBreachCheckFailOpenWithAuditNote(t *testing.T) {
	stub := &stubBreacher{err: errors.New("hibp unreachable")}
	svc, store := newBreachTestService(stub, false)

	resp, err := svc.Register(t.Context(), "degraded@example.com", "", "acceptable-pass", "Degraded", "", "")
	require.NoError(t, err, "fail-open lets the registration proceed")
	require.NotEmpty(t, resp.AccessToken)

	notes := store.auditsByAction(ActionBreachCheckUnavailable)
	require.Len(t, notes, 1, "the fail-open trip is recorded as its own audit event")
	require.NotNil(t, notes[0].ActorID)
	require.Equal(t, "user", notes[0].ObjectType)
	require.Contains(t, notes[0].Metadata["note"], "fail-open")

	// The same policy applies on the password-change path.
	err = svc.ChangePassword(t.Context(), mustUser(t, store, "degraded@example.com"), "acceptable-pass", "another-good-pass")
	require.NoError(t, err)
	require.Len(t, store.auditsByAction(ActionBreachCheckUnavailable), 2)
}

func TestBreachCheckFailClosed(t *testing.T) {
	stub := &stubBreacher{err: errors.New("hibp unreachable")}
	svc, store := newBreachTestService(stub, true)

	_, err := svc.Register(t.Context(), "closed@example.com", "", "acceptable-pass", "Closed", "", "")
	e := appErr(t, err)
	require.Equal(t, http.StatusServiceUnavailable, e.Status)
	require.Equal(t, "breach_check_unavailable", e.Code)
	require.Empty(t, store.users, "fail-closed refuses the write")

	// The same policy on the password-change path: seed the user directly so
	// the breach check is the step under test.
	hash, hashErr := HashPassword("whatever-pass")
	require.NoError(t, hashErr)
	user := User{ID: uuid.New(), Email: "closed2@example.com", FullName: "Closed", PasswordHash: hash, Status: StatusActive}
	require.NoError(t, store.CreateUser(t.Context(), &user))
	err = svc.ChangePassword(t.Context(), user.ID, "whatever-pass", "acceptable-pass")
	require.Equal(t, http.StatusServiceUnavailable, appErr(t, err).Status)
	ok, err := VerifyPassword("whatever-pass", mustHash(t, store, user.ID))
	require.NoError(t, err)
	require.True(t, ok, "fail-closed leaves the stored password untouched")
}

// mustUser loads a user by email or fails the test.
func mustUser(t *testing.T, store *fakeStore, email string) uuid.UUID {
	t.Helper()
	u, err := store.GetUserByEmail(t.Context(), email)
	require.NoError(t, err)
	return u.ID
}

func TestDenylistChecker(t *testing.T) {
	deny := NewDenylistChecker()

	for _, breached := range []string{"password123", "Password123", "QWERTY", "MOTIVRA"} {
		hit, err := deny.PasswordBreached(t.Context(), breached)
		require.NoError(t, err)
		require.True(t, hit, "%q is on the embedded denylist", breached)
	}
	// Matching is exact: whitespace makes a different password, and real
	// HIBP digests would differ too — no silent trimming either side.
	hit, err := deny.PasswordBreached(t.Context(), "  qwerty  ")
	require.NoError(t, err)
	require.False(t, hit, "whitespace-distinguished variants are different passwords")
	ok, err := deny.PasswordBreached(t.Context(), "correct-horse-9")
	require.NoError(t, err)
	require.False(t, ok, "a strong passphrase is not on the denylist")

	// Substrings of strong passphrases must not match (exact matching only).
	ok, err = deny.PasswordBreached(t.Context(), "my-password123-is-long-and-unique")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestDenylistCheckerLoadsFile(t *testing.T) {
	file := t.TempDir() + "/denylist.txt"
	require.NoError(t, os.WriteFile(file, []byte("# comment line\n\nkenyan-bank-phrase\nPASSWORD-FILE-ENTRY\n"), 0o600))

	deny := NewDenylistChecker()
	require.NoError(t, deny.LoadFile(file))

	hit, err := deny.PasswordBreached(t.Context(), "Kenyan-Bank-Phrase")
	require.NoError(t, err)
	require.True(t, hit)
	hit, err = deny.PasswordBreached(t.Context(), "Password-File-Entry")
	require.NoError(t, err)
	require.True(t, hit, "matching is exact but case-insensitive")

	require.Error(t, deny.LoadFile(t.TempDir()+"/missing.txt"), "an unreadable denylist file is a boot failure")
}

func TestHIBPCheckerAgainstTestServer(t *testing.T) {
	breached := "some-breached-password"
	sum := sha1.Sum([]byte(breached))
	digest := strings.ToUpper(hex.EncodeToString(sum[:]))

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		// The response carries the matching suffix FIRST so a naive
		// "first line wins" parser would miss; plus decoy lines.
		_, _ = w.Write([]byte("AAAAA0000000000000000000000000000000:12\n" +
			digest[5:] + ":47\nBBBBB1111111111111111111111111111111:1\n"))
	}))
	defer srv.Close()

	checker := NewHIBPChecker(srv.URL, 2*time.Second)
	hit, err := checker.PasswordBreached(t.Context(), breached)
	require.NoError(t, err)
	require.True(t, hit, "the password whose SHA-1 suffix is listed with count>0 is breached")
	require.Equal(t, "/range/"+digest[:5], gotPath, "k-anonymity: only the 5-char SHA-1 prefix is requested")

	hit, err = checker.PasswordBreached(t.Context(), "a-strong-unique-passphrase")
	require.NoError(t, err)
	require.False(t, hit, "a password whose suffix is absent from the range is not breached")
}

func TestHIBPCheckerZeroCountIsNotBreached(t *testing.T) {
	sum := sha1.Sum([]byte("count-zero-password"))
	digest := strings.ToUpper(hex.EncodeToString(sum[:]))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(digest[5:] + ":0\n"))
	}))
	defer srv.Close()

	hit, err := NewHIBPChecker(srv.URL, time.Second).PasswordBreached(t.Context(), "count-zero-password")
	require.NoError(t, err)
	require.False(t, hit, "a listed suffix with count 0 is not a breach hit")
}

func TestHIBPCheckerErrors(t *testing.T) {
	t.Run("upstream 500", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		defer srv.Close()
		_, err := NewHIBPChecker(srv.URL, time.Second).PasswordBreached(t.Context(), "anything-here")
		require.Error(t, err)
	})

	t.Run("timeout", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(200 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		_, err := NewHIBPChecker(srv.URL, 20*time.Millisecond).PasswordBreached(t.Context(), "anything-here")
		require.Error(t, err, "the configured timeout bounds the lookup")
	})

	t.Run("malformed count for own suffix", func(t *testing.T) {
		sum := sha1.Sum([]byte("malformed-count-password"))
		digest := strings.ToUpper(hex.EncodeToString(sum[:]))
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(digest[5:] + ":not-a-number\n"))
		}))
		defer srv.Close()
		_, err := NewHIBPChecker(srv.URL, time.Second).PasswordBreached(t.Context(), "malformed-count-password")
		require.Error(t, err, "an unreadable answer for our suffix is not a silent not-breached")
	})
}

func TestChainBreacherShortCircuitsAndPropagatesErrors(t *testing.T) {
	denyHit := NewDenylistChecker()

	// Denylist hit: the HIBP server is never contacted.
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte(""))
	}))
	defer srv.Close()

	chain := &ChainBreacher{checkers: []PasswordBreacher{denyHit, NewHIBPChecker(srv.URL, time.Second)}}
	hit, err := chain.PasswordBreached(t.Context(), "qwerty123")
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, 0, hits, "a local denylist hit avoids the network round trip")

	// Denylist miss + HIBP error: the error propagates for the policy
	// layer. A reachable endpoint answering 200 is a valid "not breached",
	// so the failing case needs a genuinely erroring upstream.
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer broken.Close()

	chain = &ChainBreacher{checkers: []PasswordBreacher{denyHit, NewHIBPChecker(broken.URL, time.Second)}}
	_, err = chain.PasswordBreached(t.Context(), "strong-unique-passphrase")
	require.Error(t, err, "the failing HIBP lookup must stay visible, not degrade silently")

	// Denylist miss + reachable HIBP answering 200: a clean not-breached.
	chain = &ChainBreacher{checkers: []PasswordBreacher{denyHit, NewHIBPChecker(srv.URL, time.Second)}}
	hit, err = chain.PasswordBreached(t.Context(), "strong-unique-passphrase")
	require.NoError(t, err)
	require.False(t, hit)
	require.Equal(t, 1, hits, "the denylist miss reached HIBP exactly once")
}

// TestChainViaNewBreacher pins the composition the service binary wires.
func TestChainViaNewBreacher(t *testing.T) {
	// HIBP disabled: denylist only, no error even with network broken.
	breacher, err := NewBreacher(BreachConfig{})
	require.NoError(t, err)
	hit, err := breacher.PasswordBreached(t.Context(), "password1")
	require.NoError(t, err)
	require.True(t, hit)
	hit, err = breacher.PasswordBreached(t.Context(), "correct-horse-9")
	require.NoError(t, err)
	require.False(t, hit)

	// Denylist file is loaded through the config path.
	file := t.TempDir() + "/extra.txt"
	require.NoError(t, os.WriteFile(file, []byte("operator-added-secret\n"), 0o600))
	breacher, err = NewBreacher(BreachConfig{DenylistFile: file})
	require.NoError(t, err)
	hit, err = breacher.PasswordBreached(t.Context(), "Operator-Added-Secret")
	require.NoError(t, err)
	require.True(t, hit)
}

func TestChangePasswordFlow(t *testing.T) {
	svc, store := newTestService()
	registered, err := svc.Register(t.Context(), "changer@example.com", "", "original-pass-1", "Changer", "", "")
	require.NoError(t, err)
	user, err := store.GetUserByEmail(t.Context(), "changer@example.com")
	require.NoError(t, err)

	// Wrong current password is refused.
	err = svc.ChangePassword(t.Context(), user.ID, "wrong-current-1", "replacement-pass-1")
	require.Equal(t, 401, appErr(t, err).Status)
	ok, err := VerifyPassword("original-pass-1", mustHash(t, store, user.ID))
	require.NoError(t, err)
	require.True(t, ok, "the password is unchanged after a wrong current password")

	// Successful change revokes every live session of the user.
	require.NoError(t, svc.ChangePassword(t.Context(), user.ID, "original-pass-1", "replacement-pass-1"))
	_, err = svc.RefreshTokens(t.Context(), registered.RefreshToken)
	requireUnauthorized(t, err, "the password change sweeps the user's sessions")
	ok, err = VerifyPassword("replacement-pass-1", mustHash(t, store, user.ID))
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, store.auditsByAction(ActionPasswordChanged), 1)
	require.Equal(t, 1, store.auditsByAction(ActionPasswordChanged)[0].Metadata["sessions_revoked"])

	// The new password logs in; the old one does not.
	_, err = svc.Login(t.Context(), "changer@example.com", "replacement-pass-1", "", "")
	require.NoError(t, err)
	_, err = svc.Login(t.Context(), "changer@example.com", "original-pass-1", "", "")
	requireUnauthorized(t, err)
}

func TestChangePasswordRejectsBreachedNewPassword(t *testing.T) {
	// Only the NEW password is reported breached, so the registration
	// itself succeeds.
	stub := &stubBreacher{breachPassword: knownBreachedSample}
	svc, store := newBreachTestService(stub, false)
	registered, err := svc.Register(t.Context(), "changer2@example.com", "", "original-pass-1", "Changer", "", "")
	require.NoError(t, err)
	user, err := store.GetUserByEmail(t.Context(), "changer2@example.com")
	require.NoError(t, err)

	err = svc.ChangePassword(t.Context(), user.ID, "original-pass-1", knownBreachedSample)
	require.Equal(t, 422, appErr(t, err).Status, "the new password is breach-checked before hashing")
	ok, err := VerifyPassword("original-pass-1", mustHash(t, store, user.ID))
	require.NoError(t, err)
	require.True(t, ok, "no change persisted")

	// The session survived the rejected change (revocation happens after
	// every check passed).
	_, err = svc.RefreshTokens(t.Context(), registered.RefreshToken)
	require.NoError(t, err)
}

// mustHash loads a stored password hash (test probe).
func mustHash(t *testing.T, store *fakeStore, id uuid.UUID) string {
	t.Helper()
	u, err := store.GetUser(t.Context(), id)
	require.NoError(t, err)
	return u.PasswordHash
}

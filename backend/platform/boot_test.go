package platform

// Degraded-boot contract tests (issue #122, ADR-0008): the process must
// always serve /healthz; configuration and dependency failures surface as
// /readyz 503 with machine-readable, coarse reasons; domain routes answer
// 503 problem+json while not ready and never bypass authentication; and a
// recovered dependency flips /readyz back to 200 without a restart.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newTestBootServer wires a Server with degraded boot and a short wiring
// backoff so retry passes resolve inside test deadlines.
func newTestBootServer(t *testing.T) (*Server, *Boot) {
	t.Helper()
	cfg := LoadForTest()
	cfg.App.Name = "test-svc"
	logger := NewLogger(cfg.App)
	boot := NewBoot(cfg, logger)
	boot.wireBackoffBase = time.Millisecond
	boot.wireBackoffMax = 2 * time.Millisecond
	srv := NewServer(cfg, logger, WithBoot(boot))
	return srv, boot
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body
}

func TestHealthzNeverDependsOnDependencies(t *testing.T) {
	// The liveness contract: /healthz answers 200 even with a latched
	// configuration blocker, a failing live database probe, and no wired
	// domain router (the exact Vercel state that used to os.Exit(1) and
	// return FUNCTION_INVOCATION_FAILED, issue #122).
	srv, boot := newTestBootServer(t)
	boot.BlockPermanent("config", "MOTIVRA_DATABASE_URL is required")
	srv.AddChecker("database", func(context.Context) error { return errors.New("connection refused") })

	w := get(t, srv, "/healthz")
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"status":"ok"`)
}

func TestConfigInvalidBootHealthzUpReadyzDown(t *testing.T) {
	srv, boot := newTestBootServer(t)

	// Table-driven: every failure mode must keep liveness green while
	// readiness reports the blocker names — and only the names.
	t.Run("config blocker", func(t *testing.T) {
		boot.BlockPermanent("config", "MOTIVRA_JWT_JWKS_URL or MOTIVRA_JWT_PUBLIC_KEY_FILE required in production")
		w := get(t, srv, "/readyz")
		require.Equal(t, http.StatusServiceUnavailable, w.Code)
		require.Equal(t, "application/problem+json", w.Header().Get("Content-Type"))
		body := decodeBody(t, w)
		require.Equal(t, []any{"config"}, body["failed"])
		// Info-leak decision: full reasons are log/admin-only.
		require.NotContains(t, w.Body.String(), "MOTIVRA_JWT_JWKS_URL")
	})

	t.Run("dependency checker failure", func(t *testing.T) {
		srv.AddChecker("database", func(context.Context) error {
			return errors.New("dial tcp 10.0.0.5:5432: connect refused")
		})
		w := get(t, srv, "/readyz")
		require.Equal(t, http.StatusServiceUnavailable, w.Code)
		body := decodeBody(t, w)
		failed := map[string]bool{}
		for _, name := range body["failed"].([]any) {
			failed[name.(string)] = true
		}
		require.True(t, failed["config"], "latched config blocker stays reported")
		require.True(t, failed["database"], "live checker failure is reported")
		// The unauthenticated body must not leak the dependency detail (#81).
		require.NotContains(t, w.Body.String(), "10.0.0.5")
	})

	t.Run("liveness unaffected", func(t *testing.T) {
		require.Equal(t, http.StatusOK, get(t, srv, "/healthz").Code)
	})
}

func TestReadyzFlipsReadyWhenDependencyRecovers(t *testing.T) {
	srv, boot := newTestBootServer(t)

	var dbUp atomic.Bool
	boot.Wire(context.Background(),
		BootStep{Name: "database", Run: func(context.Context) error {
			if dbUp.Load() {
				return nil
			}
			return errors.New("connection refused")
		}},
		BootStep{Name: "routes", Run: func(context.Context) error {
			if boot.DomainHandler() != nil {
				return nil
			}
			domain := http.NewServeMux()
			domain.HandleFunc("/v1/ping", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			boot.SetDomainHandler(domain)
			return nil
		}},
	)

	// While the dependency is down: readyz 503, domain route 503 problem+json.
	require.Eventually(t, func() bool {
		return get(t, srv, "/readyz").Code == http.StatusServiceUnavailable
	}, 2*time.Second, 5*time.Millisecond)
	w := get(t, srv, "/v1/ping")
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Equal(t, "application/problem+json", w.Header().Get("Content-Type"))
	require.Contains(t, w.Body.String(), `"code":"not_ready"`)
	failed := decodeBody(t, w)["failed"].([]any)
	require.Equal(t, []any{"database"}, failed)

	// Recovery (dependency comes back, no restart): readyz flips 200 and the
	// domain route serves — the degraded-boot transition, issue #122.
	dbUp.Store(true)
	require.Eventually(t, func() bool {
		return get(t, srv, "/readyz").Code == http.StatusOK
	}, 2*time.Second, 5*time.Millisecond)
	require.Equal(t, http.StatusOK, get(t, srv, "/healthz").Code)
	require.Equal(t, http.StatusOK, get(t, srv, "/v1/ping").Code)
	require.True(t, boot.Ready())
}

func TestWireRetriesFailedStepsAndRecordsBlockers(t *testing.T) {
	_, boot := newTestBootServer(t)

	var attempts atomic.Int64
	boot.Wire(context.Background(),
		BootStep{Name: "always-fails", Run: func(context.Context) error {
			attempts.Add(1)
			return errors.New("still down")
		}},
	)
	require.Eventually(t, func() bool {
		return attempts.Load() >= 3
	}, 2*time.Second, 5*time.Millisecond, "the runner must retry with backoff, not give up")
	require.False(t, boot.Ready())
	require.Equal(t, []string{"always-fails"}, boot.PublicReasons())
	require.Contains(t, boot.Reasons()["always-fails"], "still down")
}

func TestWirePermanentStepRecordsOnceAndLaterStepsStillRun(t *testing.T) {
	_, boot := newTestBootServer(t)

	var laterAttempts atomic.Int64
	boot.Wire(context.Background(),
		BootStep{Name: "config", Permanent: true, Run: func(context.Context) error {
			return errors.New("MOTIVRA_DATABASE_URL is required")
		}},
		BootStep{Name: "database", Run: func(context.Context) error {
			laterAttempts.Add(1)
			return nil
		}},
		BootStep{Name: "routes", Run: func(context.Context) error {
			boot.SetDomainHandler(http.NewServeMux())
			return nil
		}},
	)
	require.Eventually(t, func() bool { return laterAttempts.Load() >= 1 },
		2*time.Second, 5*time.Millisecond,
		"retryable steps run on the pass after the permanent blocker is latched")
	require.False(t, boot.Ready())
	require.Equal(t, []string{"config"}, boot.PublicReasons(), "only the permanent blocker is latched")
	require.Len(t, boot.Reasons(), 1)
}

func TestDegradedDomainRoutesServeAfterWiring(t *testing.T) {
	srv, boot := newTestBootServer(t)
	boot.Wire(context.Background(),
		BootStep{Name: "routes", Run: func(context.Context) error {
			if boot.DomainHandler() != nil {
				return nil
			}
			domain := http.NewServeMux()
			domain.HandleFunc("/v1/ping", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			boot.SetDomainHandler(domain)
			return nil
		}},
	)
	require.Eventually(t, boot.Ready, 2*time.Second, 5*time.Millisecond)
	require.Equal(t, http.StatusOK, get(t, srv, "/v1/ping").Code)

	// Once wired, unknown paths fall back to the standard 404 (not 503):
	require.Equal(t, http.StatusNotFound, get(t, srv, "/v1/nope").Code)

	// Method gate: a known health path with the wrong method is 405 when
	// ready, 503 while degraded (covered below in the degraded test).
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("POST", "/healthz", nil))
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
	require.Contains(t, w.Body.String(), "method_not_allowed")
}

func TestDegradedMethodGateReturns503(t *testing.T) {
	srv, boot := newTestBootServer(t)
	boot.Block("database", "connection refused")

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("POST", "/healthz", nil))
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), `"code":"not_ready"`)
}

func TestRunShutdownHooksReverseOrderAndFirstError(t *testing.T) {
	t.Run("reverse order, first error returned", func(t *testing.T) {
		_, boot := newTestBootServer(t)
		var order []string
		boot.OnShutdown(func(context.Context) error { order = append(order, "first"); return errors.New("boom") })
		boot.OnShutdown(func(context.Context) error { order = append(order, "second"); return nil })

		err := boot.RunShutdownHooks(context.Background())
		require.Error(t, err)
		require.Contains(t, err.Error(), "boom")
		require.Equal(t, []string{"second", "first"}, order, "hooks run in reverse registration order")
	})

	t.Run("nil hooks are tolerated", func(t *testing.T) {
		_, boot := newTestBootServer(t)
		boot.OnShutdown(nil)
		require.NoError(t, boot.RunShutdownHooks(context.Background()))
	})
}

func TestDegradationsDoNotBlockReadiness(t *testing.T) {
	srv, boot := newTestBootServer(t)
	boot.Degraded("tracing", "collector unreachable")

	w := get(t, srv, "/readyz")
	require.Equal(t, http.StatusOK, w.Code)
	body := decodeBody(t, w)
	require.Equal(t, "ready", body["status"])
	require.Equal(t, []any{"tracing"}, body["degraded"])
}

func TestReadyzReasonDisclosureByRole(t *testing.T) {
	srv, boot := newTestBootServer(t)
	boot.Block("database", "dial tcp db.internal:5432: connect refused")

	cases := []struct {
		name        string
		role        string
		wantReasons bool
	}{
		{"anonymous sees names only", "", false},
		{"customer sees names only", "CUSTOMER", false},
		{"technician sees names only", "TECHNICIAN", false},
		{"admin sees full reasons", "ADMIN", true},
		{"super admin sees full reasons", "SUPER_ADMIN", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/readyz", nil)
			if tc.role != "" {
				r = r.WithContext(context.WithValue(r.Context(), ctxKeyClaims, Claims{Role: tc.role}))
			}
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, r)
			require.Equal(t, http.StatusServiceUnavailable, w.Code)
			body := decodeBody(t, w)
			_, hasReasons := body["reasons"]
			require.Equal(t, tc.wantReasons, hasReasons)
			if tc.wantReasons {
				require.Contains(t, w.Body.String(), "db.internal")
			} else {
				require.NotContains(t, w.Body.String(), "db.internal")
			}
		})
	}
}

func TestValidateProblemsMatchesValidate(t *testing.T) {
	cfg := LoadForTest()
	cfg.App.Env = EnvProduction
	cfg.Database.URL = ""
	cfg.JWT.Secret = "short"

	err := cfg.Validate()
	require.Error(t, err)

	problems := cfg.ValidateProblems()
	require.NotEmpty(t, problems)
	require.Contains(t, err.Error(), strings.Join(problems, "; "))
}

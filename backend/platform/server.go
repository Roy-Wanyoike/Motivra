package platform

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Checker reports whether a dependency is ready. Name it after the
// dependency (database, redis, nats); failures surface on /readyz.
type Checker func(ctx context.Context) error

// operatorRoles are the identity-service RBAC roles (identity RBAC matrix,
// SUPER_ADMIN > ADMIN) allowed to see full readiness reasons. Declared as
// literals here because the platform package must not import domain
// packages (ADR-0001).
var operatorRoles = map[string]struct{}{
	"ADMIN":       {},
	"SUPER_ADMIN": {},
}

// Server is the Motivra HTTP server conventions applied to every service:
// request IDs, real-IP resolution, tracing, structured request logs, panic
// recovery, timeouts, health endpoints and optional metrics exposition.
// With degraded boot (WithBoot, issue #122) it also gates every non-health
// route behind the Boot's readiness: domain paths answer 503 problem+json
// until the wiring loop completes, instead of the process exiting at boot.
type Server struct {
	Router chi.Router

	checkMu  sync.RWMutex
	checkers map[string]Checker
	boot     *Boot
	logger   *slog.Logger
	cfg      Config
}

// ServerOption customises the server at construction.
type ServerOption func(*Server)

// WithChecker registers a readiness check.
func WithChecker(name string, c Checker) ServerOption {
	return func(s *Server) {
		s.checkers[name] = c
	}
}

// WithBoot enables the degraded-boot contract (issue #122, ADR-0008):
//
//   - /healthz stays dependency-free liveness and always answers 200;
//   - /readyz merges the Boot's latched blockers with the live checkers,
//     exposing full reasons only to authenticated operators (ADMIN /
//     SUPER_ADMIN bearer) and check NAMES to everyone;
//   - every path that is not a registered route (which, before the wiring
//     loop installs the domain router, is every domain path) answers 503
//     problem+json while the boot is not ready, and the wired domain router
//     (or chi's standard 404) afterwards.
func WithBoot(b *Boot) ServerOption {
	return func(s *Server) {
		s.boot = b
		s.Router.NotFound(s.serveDomainGate)
		s.Router.MethodNotAllowed(s.serveMethodGate)
	}
}

// WithMount attaches an external handler at pattern (for example the
// /metrics exposition).
func WithMount(pattern string, h http.Handler) ServerOption {
	return func(s *Server) {
		s.Router.Mount(pattern, h)
	}
}

// WithMiddleware appends a chi middleware to the chain. It must be applied
// through NewServer options — middleware registered after routes panic.
func WithMiddleware(mw func(http.Handler) http.Handler) ServerOption {
	return func(s *Server) {
		s.Router.Use(mw)
	}
}

// NewServer builds the router with the Motivra middleware chain and
// service options first, then mounts /healthz (liveness) and /readyz
// (readiness) last so callers never fight route-registration ordering.
// X-Forwarded-For is honoured only when cfg.TrustForwarded is set: without
// a trusted proxy the header is client-controlled and spoofable (audit
// IPs and rate-limit keys must be attacker-unchoosable).
func NewServer(cfg Config, logger *slog.Logger, opts ...ServerOption) *Server {
	installPropagators()
	s := &Server{
		Router:   chi.NewRouter(),
		checkers: map[string]Checker{},
		logger:   logger,
		cfg:      cfg,
	}
	s.Router.Use(middleware.RequestID)
	if cfg.TrustForwarded {
		s.Router.Use(middleware.RealIP)
	}
	s.Router.Use(s.traceMiddleware)
	s.Router.Use(s.requestLogger)
	s.Router.Use(s.recoverer)
	s.Router.Use(middleware.Timeout(30 * time.Second))
	for _, opt := range opts {
		opt(s)
	}
	s.Router.Get("/healthz", s.handleLiveness)
	s.Router.Get("/readyz", s.handleReadiness)
	return s
}

// AddChecker registers a readiness check after construction. Degraded boot
// needs this: the wiring loop discovers dependencies (pool, redis client)
// after the server is already serving, and their live probes must join
// /readyz. Safe for concurrent use with readiness handling.
func (s *Server) AddChecker(name string, c Checker) {
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	s.checkers[name] = c
}

// handleLiveness is dependency-free by contract: it MUST answer 200 even
// when every dependency and the configuration itself are broken (issue
// #122) — orchestration kills/restarts the process based on it.
func (s *Server) handleLiveness(w http.ResponseWriter, _ *http.Request) {
	writeJSONOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleReadiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	s.checkMu.RLock()
	checks := make(map[string]Checker, len(s.checkers))
	for name, check := range s.checkers {
		checks[name] = check
	}
	s.checkMu.RUnlock()

	// name -> full failure detail. Detail is operator-facing: it goes to the
	// log always, into the response body only for authenticated operators.
	failures := map[string]string{}
	for name, check := range checks {
		if err := check(ctx); err != nil {
			failures[name] = err.Error()
		}
	}
	if s.boot != nil {
		// Latched wiring/config blockers merge with the live probes: a
		// config problem or a pending migration has no per-request probe.
		for name, reason := range s.boot.Reasons() {
			failures[name] = reason
		}
	}
	if len(failures) > 0 {
		// Dependency error details go to the log only: /readyz is reachable
		// without authentication, and pgx/NATS error strings can carry
		// internal hostnames and DSN fragments (hardening issue #81).
		// Authenticated operators (ADMIN/SUPER_ADMIN) receive the full map;
		// everyone else gets check names (issue #122 security review).
		details := make([]any, 0, len(failures)*2)
		for name, msg := range failures {
			details = append(details, name, msg)
		}
		s.logger.Error("readiness check failed", details...)
		names := make([]string, 0, len(failures))
		for name := range failures {
			names = append(names, name)
		}
		sort.Strings(names)
		body := map[string]any{
			"code":   "not_ready",
			"title":  "Service is not ready",
			"failed": names,
		}
		if isOperator(r) {
			body["reasons"] = failures
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(LogJSON(body)))
		return
	}
	if s.boot != nil {
		if degraded := s.boot.Degradations(); len(degraded) > 0 {
			// Serving, with named reduced capabilities (e.g. no trace
			// export). Honest 200: availability first, visibility second.
			writeJSONOK(w, map[string]any{"status": "ready", "degraded": degraded})
			return
		}
	}
	writeJSONOK(w, map[string]string{"status": "ready"})
}

// isOperator reports whether the request carries validated claims for one
// of the operator roles (identity RBAC matrix). AuthMiddleware on the
// server chain populates the claims; unauthenticated or non-operator roles
// get coarse readiness only.
func isOperator(r *http.Request) bool {
	claims, ok := ClaimsFromContext(r.Context())
	if !ok {
		return false
	}
	_, ok = operatorRoles[claims.Role]
	return ok
}

// serveDomainGate is the NotFound target under degraded boot. Before the
// wiring loop installs the domain router every domain path lands here —
// exactly the traffic that must NOT be served (no store, no validator, no
// auth material) — so it answers 503 problem+json with check names. After
// wiring AND readiness, unknown paths fall through to chi's standard 404.
// Readiness is checked first: a latched permanent blocker (invalid
// configuration) keeps every domain path on 503 even when a router is
// wired — /readyz 503 and serving domain traffic must never disagree.
func (s *Server) serveDomainGate(w http.ResponseWriter, r *http.Request) {
	if !s.boot.Ready() {
		s.boot.writeNotReady(w)
		return
	}
	if h := s.boot.DomainHandler(); h != nil {
		h.ServeHTTP(w, r)
		return
	}
	http.NotFound(w, r)
}

// serveMethodGate is the MethodNotAllowed target under degraded boot: a
// known health path hit with the wrong method (or any method before wiring
// completes) answers 503 while not ready and the standard 405 afterwards.
func (s *Server) serveMethodGate(w http.ResponseWriter, _ *http.Request) {
	if !s.boot.Ready() {
		s.boot.writeNotReady(w)
		return
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusMethodNotAllowed)
	_, _ = w.Write([]byte(LogJSON(map[string]any{
		"code":  "method_not_allowed",
		"title": "Method not allowed",
	})))
}

// ServeHTTP satisfies http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Router.ServeHTTP(w, r)
}

func writeJSONOK(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(LogJSON(body)))
}

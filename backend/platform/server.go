package platform

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Checker reports whether a dependency is ready. Name it after the
// dependency (database, redis, nats); failures surface on /readyz.
type Checker func(ctx context.Context) error

// Server is the Motivra HTTP server conventions applied to every service:
// request IDs, real-IP resolution, tracing, structured request logs, panic
// recovery, timeouts, health endpoints and optional metrics exposition.
type Server struct {
	Router   chi.Router
	checkers map[string]Checker
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

func (s *Server) handleLiveness(w http.ResponseWriter, _ *http.Request) {
	writeJSONOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleReadiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	failures := map[string]string{}
	for name, check := range s.checkers {
		if err := check(ctx); err != nil {
			failures[name] = err.Error()
		}
	}
	if len(failures) > 0 {
		// Dependency error details go to the log only: /readyz is reachable
		// without authentication, and pgx/NATS error strings can carry
		// internal hostnames and DSN fragments (hardening issue #81).
		details := make([]any, 0, len(failures)*2)
		for name, msg := range failures {
			details = append(details, name, msg)
		}
		s.logger.Error("readiness check failed", details...)
		names := make([]string, 0, len(failures))
		for name := range failures {
			names = append(names, name)
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusServiceUnavailable)
		body := map[string]any{
			"code":   "not_ready",
			"title":  "Service is not ready",
			"failed": names,
		}
		_, _ = w.Write([]byte(LogJSON(body)))
		return
	}
	writeJSONOK(w, map[string]string{"status": "ready"})
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

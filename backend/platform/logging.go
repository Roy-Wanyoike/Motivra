package platform

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
)

// Context keys attached by middleware so any layer can enrich log output.
type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota + 1
	ctxKeyTenantID
	ctxKeyTraceID
)

// requestLogMeta is the per-request mutable holder for the request-scoped
// logger. It exists because downstream middleware (auth) enriches the
// logger AFTER requestLogger built it, while requestLogger needs the
// enriched logger for its final log line — the same goroutine sequences
// the writes, so plain mutation is safe. All log fields set through it
// (request_id, trace_id, tenant_id) ride on every line the request emits,
// including the closing http_request record (ADR-0005 correlation,
// observability issue #84).
type requestLogMeta struct {
	logger *slog.Logger
}

const ctxKeyLogMeta ctxKey = 101

// NewLogger returns the service logger: JSON to stdout, Debug in
// non-production environments and Info otherwise.
func NewLogger(cfg AppConfig) *slog.Logger {
	level := slog.LevelInfo
	if cfg.Env != EnvProduction {
		level = slog.LevelDebug
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(handler).With("service", cfg.Name, "env", cfg.Env)
}

// IntoContext stores logger as the ambient logger for ctx.
func IntoContext(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKeyLogger, logger)
}

// FromContext returns the ambient logger for ctx, defaulting to the global
// logger. Middleware installs a request-scoped logger (via requestLogMeta)
// so handlers and repositories inherit request_id, tenant_id and trace_id
// automatically.
func FromContext(ctx context.Context) *slog.Logger {
	if meta, ok := ctx.Value(ctxKeyLogMeta).(*requestLogMeta); ok && meta != nil && meta.logger != nil {
		return meta.logger
	}
	if l, ok := ctx.Value(ctxKeyLogger).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}

// EnrichRequestLogger annotates the request-scoped logger installed by
// requestLogger with the given key/value. It is a no-op when no
// request-scoped logger exists (background work outside the HTTP chain).
func EnrichRequestLogger(ctx context.Context, key, value string) {
	if meta, ok := ctx.Value(ctxKeyLogMeta).(*requestLogMeta); ok && meta != nil && meta.logger != nil {
		meta.logger = meta.logger.With(key, value)
	}
}

const ctxKeyLogger ctxKey = 100

// WithRequestID returns a logger annotated with the request identifier.
func WithRequestID(l *slog.Logger, requestID string) *slog.Logger {
	if requestID == "" {
		return l
	}
	return l.With("request_id", requestID)
}

// WithTenantID returns a logger annotated with the tenant identifier.
func WithTenantID(l *slog.Logger, tenantID string) *slog.Logger {
	if tenantID == "" {
		return l
	}
	return l.With("tenant_id", tenantID)
}

// WithTraceID returns a logger annotated with the trace identifier.
func WithTraceID(l *slog.Logger, traceID string) *slog.Logger {
	if traceID == "" {
		return l
	}
	return l.With("trace_id", traceID)
}

// LogJSON marshals v for structured fields; on failure it returns a compact
// fallback so logging never panics.
func LogJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"error":"marshal_failure"}`
	}
	return string(b)
}

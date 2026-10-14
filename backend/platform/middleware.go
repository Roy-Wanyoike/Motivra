package platform

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// requestLogger emits one structured log line per request with method,
// path, status, duration, remote address, request ID and — when a span is
// active — the W3C trace ID, so log lines join traces (ADR-0005
// correlation requirement, observability issue #84). AuthMiddleware later
// enriches the same logger with tenant_id once claims are validated.
func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		reqID := middleware.GetReqID(r.Context())

		// Seed from the SERVER logger, never FromContext: the request
		// context has no logger yet at this point, and the previous
		// FromContext-based seeding silently degraded every request log
		// line to slog.Default()'s text output (the JSON-logging promise
		// was broken for the closing http_request record).
		reqLogger := WithRequestID(s.logger, reqID)
		if sc := trace.SpanContextFromContext(r.Context()); sc.IsValid() {
			reqLogger = WithTraceID(reqLogger, sc.TraceID().String())
		}
		meta := &requestLogMeta{logger: reqLogger}
		r = r.WithContext(context.WithValue(r.Context(), ctxKeyLogMeta, meta))
		next.ServeHTTP(ww, r)

		// The meta holder carries any enrichment downstream middleware
		// applied (tenant_id from auth claims), so the closing line joins
		// every attribute the request accumulated.
		meta.logger.Info("http_request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
			"bytes", ww.BytesWritten(),
			"remote", r.RemoteAddr,
			"request_id", reqID,
		)
	})
}

// recoverer converts handler panics into 500 problem+json responses and
// logs the stack; a panic must never take the process down.
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				FromContext(r.Context()).Error("panic_recovered", "panic", rec)
				WriteError(w, ErrInternal())
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// traceMiddleware starts a server span per request, records the status
// and stamps the route template onto the span (kept as the template, not
// the raw path, so span cardinality stays bounded by the route table).
func (s *Server) traceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tracer := otel.Tracer("motivra/platform")
		prop := otel.GetTextMapPropagator()
		ctx := prop.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		routePattern := r.URL.Path
		spanName := "HTTP " + r.Method + " " + routePattern
		sctx, span := tracer.Start(ctx, spanName,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.method", r.Method),
				attribute.String("http.route", routePattern),
				attribute.String("http.client_ip", s.clientIP(r)),
			),
		)
		defer span.End()

		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r.WithContext(sctx))
		span.SetAttributes(attribute.Int("http.status_code", ww.Status()))
	})
}

// clientIP resolves the client IP for span attributes. X-Forwarded-For is
// honoured only when the server was configured with TrustForwarded — the
// same posture as audit rows and rate-limit keys (issue #81).
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustForwarded {
		if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); fwd != "" {
			if idx := strings.IndexByte(fwd, ','); idx > 0 {
				return strings.TrimSpace(fwd[:idx])
			}
			return fwd
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

package platform

import (
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
// path, status, duration, remote address and request ID, and installs a
// request-scoped logger into the request context.
func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		reqID := middleware.GetReqID(r.Context())

		base := FromContext(r.Context())
		reqLogger := WithRequestID(base, reqID)
		next.ServeHTTP(ww, r.WithContext(IntoContext(r.Context(), reqLogger)))

		svc := FromContext(r.Context())
		svc.Info("http_request",
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

// traceMiddleware starts a server span per request and records the status.
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
				attribute.String("http.client_ip", clientIP(r)),
			),
		)
		defer span.End()

		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r.WithContext(sctx))
		span.SetAttributes(attribute.Int("http.status_code", ww.Status()))
	})
}

func clientIP(r *http.Request) string {
	if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); fwd != "" {
		if idx := strings.IndexByte(fwd, ','); idx > 0 {
			return strings.TrimSpace(fwd[:idx])
		}
		return fwd
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

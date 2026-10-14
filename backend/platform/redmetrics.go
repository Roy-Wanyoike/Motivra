package platform

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
)

// HTTPMetrics is the RED (rate/error/duration) instrumentation for service
// HTTP traffic (observability issue #84, ADR-0005 SLO commitments): a
// request counter and a duration histogram, both labelled by method and
// low-cardinality route template. Health endpoints are excluded — they are
// probes, not traffic — and unmatched requests collapse into a single
// "unmatched" route so 404 scans cannot explode label cardinality.
type HTTPMetrics struct {
	service       string
	requestsTotal *prometheus.CounterVec
	duration      *prometheus.HistogramVec
}

// httpMetricsSkip lists paths excluded from RED metrics.
var httpMetricsSkip = map[string]struct{}{
	"/healthz": {},
	"/readyz":  {},
	"/metrics": {},
}

// NewHTTPMetrics registers the RED instruments on reg, namespaced by
// service. Register once per process (usually in the service main via
// SetupMetrics) and wire the Middleware through WithMiddleware.
func NewHTTPMetrics(reg *prometheus.Registry, service string) *HTTPMetrics {
	m := &HTTPMetrics{
		service: service,
		requestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "http_requests_total",
				Help: "HTTP requests processed, by service, method, route template and status class.",
			},
			[]string{"service", "method", "route", "status"},
		),
		duration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "http_request_duration_seconds",
				Help:    "HTTP request duration in seconds, by service, method and route template.",
				Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
			},
			[]string{"service", "method", "route"},
		),
	}
	reg.MustRegister(m.requestsTotal, m.duration)
	return m
}

// routeLabel resolves the chi route template (e.g. "/v1/vehicles/{vehicleId}")
// so cardinality is bounded by the route table, not by request paths.
func routeLabel(r *http.Request) string {
	if _, skip := httpMetricsSkip[r.URL.Path]; skip {
		return ""
	}
	if rtc := chi.RouteContext(r.Context()); rtc != nil && rtc.RoutePattern() != "" {
		return rtc.RoutePattern()
	}
	return "unmatched"
}

// Middleware instruments the wrapped handler. Metrics for skipped paths
// (health probes) pass through unmeasured.
func (m *HTTPMetrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if routeLabel(r) == "" {
			next.ServeHTTP(w, r)
			return
		}
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()

		next.ServeHTTP(ww, r)

		// Resolve the route template AFTER routing: chi populates the
		// route context during dispatch, so a Use-registered middleware
		// only sees the matched pattern once the handler returned.
		route := routeLabel(r)
		m.duration.WithLabelValues(m.service, r.Method, route).Observe(time.Since(start).Seconds())
		m.requestsTotal.WithLabelValues(m.service, r.Method, route, statusClass(ww.Status())).Inc()
	})
}

// statusClass buckets the status code to keep the status label bounded
// (2xx, 3xx, 4xx, 5xx, "x" for anything exotic such as a 1xx or 0).
func statusClass(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 300 && status < 400:
		return "3xx"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500 && status < 600:
		return "5xx"
	default:
		return "x"
	}
}

// ShutdownTick satisfies the cleanup-function shape some mains expect;
// metrics need no shutdown work. Provided for symmetry with Stop handles.
func (m *HTTPMetrics) ShutdownTick(context.Context) error { return nil }

package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// bufferLogger returns a JSON logger writing into buf (test helper for
// log-content assertions).
func bufferLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// TestHTTPMetricsRED proves the RED contract: per-route counters and
// durations exported with bounded labels, health endpoints excluded and
// unmatched paths collapsed.
func TestHTTPMetricsRED(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := NewHTTPMetrics(reg, "identity-test")

	r := chi.NewRouter()
	r.Use(m.Middleware)
	r.Get("/v1/vehicles/{vehicleID}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r.Get("/v1/boom", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/v1/vehicles/ABC123", http.StatusOK},
		{"/v1/boom", http.StatusInternalServerError},
		{"/nope", http.StatusNotFound},
		{"/healthz", http.StatusOK},
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		require.Equal(t, tc.want, rec.Code, tc.path)
	}

	families, err := reg.Gather()
	require.NoError(t, err)
	dump := metricFamiliesText(families)

	require.Contains(t, dump, "route=/v1/vehicles/{vehicleID}", "route templates, not raw paths")
	require.Contains(t, dump, "route=unmatched", "unmatched paths collapse to one label")
	require.NotContains(t, dump, "ABC123", "raw request paths must never appear")
	require.Contains(t, dump, "service=identity-test")
	require.Contains(t, dump, "status=5xx")
	// The health probe was served but must not be counted.
	require.NotContains(t, dump, `route="/healthz"`)
}

// TestRequestLogCarriesTraceID proves the ADR-0005 correlation rule: a
// request log line contains the active span's trace ID.
func TestRequestLogCarriesTraceID(t *testing.T) {
	var buf bytes.Buffer
	cfg := LoadForTest()
	s := NewServer(cfg, bufferLogger(&buf))

	s.Router.Get("/correlated", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/correlated", nil)
	// Install a real tracer provider so the request span carries the
	// traceparent-derived trace ID (a noop tracer yields an empty
	// SpanContext, which would skip the trace_id field).
	prev := otel.GetTracerProvider()
	tp := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		_ = tp.Shutdown(context.Background())
	})

	// Seed a traceparent header so the propagator establishes a known
	// trace for the request span.
	req.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-00f067aa0ba902b7-01")
	s.ServeHTTP(httptest.NewRecorder(), req)

	fields := lastLogFields(t, &buf)
	require.Equal(t, "0af7651916cd43dd8448eb211c80319c", fields["trace_id"],
		"the request log line must carry the W3C trace ID")
	require.NotEmpty(t, fields["request_id"])
}

// TestAuthMiddlewareEnrichesTenantLog proves authenticated requests log
// with tenant_id (issue #84 correlation scope) while unauthenticated ones
// do not.
func TestAuthMiddlewareEnrichesTenantLog(t *testing.T) {
	var buf bytes.Buffer
	cfg := LoadForTest()
	secret := cfg.JWT.Secret

	validator := NewJWTValidator(secret, cfg.JWT.Issuer, cfg.JWT.Audience)
	s := NewServer(cfg, bufferLogger(&buf), WithMiddleware(AuthMiddleware(validator)))
	s.Router.Get("/who", func(w http.ResponseWriter, r *http.Request) {
		if claims, ok := ClaimsFromContext(r.Context()); !ok || claims.Role != "TECHNICIAN" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	// Without a token: no tenant enrichment.
	s.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/who", nil))
	require.NotContains(t, buf.String(), "tenant_id")
	buf.Reset()

	// With a tenant-scoped token: the log line carries tenant_id.
	token, err := signTestToken(secret, cfg.JWT.Issuer, cfg.JWT.Audience, "user-1", "tenant-9", "TECHNICIAN")
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodGet, "/who", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	fields := lastLogFields(t, &buf)
	require.Equal(t, "tenant-9", fields["tenant_id"])
}

// signTestToken mints a minimal valid HS256 access token for middleware
// tests (the platform validates but never issues in production code).
func signTestToken(secret, iss, aud, sub, tenant, role string) (string, error) {
	claims := jwt.MapClaims{
		"iss": iss, "aud": aud, "sub": sub,
		"tenant_id": tenant, "role": role,
		"iat": time.Now().Unix(), "exp": time.Now().Add(15 * time.Minute).Unix(),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// lastLogFields parses the final JSON log line from buf.
func lastLogFields(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.NotEmpty(t, lines)
	var rec map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &rec), "log line must be JSON")
	return rec
}

// metricFamiliesText renders gathered metric families into the exposition
// text format so label assertions read like a real scrape.
func metricFamiliesText(families []*dto.MetricFamily) string {
	var sb strings.Builder
	for _, mf := range families {
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				sb.WriteString(lp.GetName() + "=" + lp.GetValue() + "\n")
			}
		}
	}
	return sb.String()
}

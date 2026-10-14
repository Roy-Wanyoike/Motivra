package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// spanRecorder is a minimal in-memory sdktrace.SpanExporter used to prove
// the panic-recovery middleware's OpenTelemetry contract (issue #88 error
// tracking) without shipping anything over the network.
type spanRecorder struct {
	mu    sync.Mutex
	spans []sdktrace.ReadOnlySpan
}

func (r *spanRecorder) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.spans = append(r.spans, spans...)
	return nil
}

func (r *spanRecorder) Shutdown(context.Context) error { return nil }

func (r *spanRecorder) recorded() []sdktrace.ReadOnlySpan {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sdktrace.ReadOnlySpan(nil), r.spans...)
}

// TestRecovererRecordsOTelExceptionEvent proves AC3 of issue #88: a
// recovered handler panic is visible as an OTel exception event on the
// request's server span — exception.type, exception.message and
// exception.stacktrace per the OTel semconv — with the span marked Error,
// which is exactly what the Grafana/Tempo trace view renders when spans
// reach the observability stack (RUNBOOK §5).
//
// The test swaps the global tracer provider for one exporting to memory;
// it must therefore never run in parallel with other tests.
func TestRecovererRecordsOTelExceptionEvent(t *testing.T) {
	recorder := &spanRecorder{}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = tp.Shutdown(context.Background())
	})

	s := newTestServer(t)
	s.Router.Get("/boom", func(http.ResponseWriter, *http.Request) {
		panic("kaboom")
	})

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/boom", nil))
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "internal_error")

	spans := recorder.recorded()
	require.NotEmpty(t, spans, "the request span must be exported")

	var serverSpan sdktrace.ReadOnlySpan
	for _, sp := range spans {
		if strings.Contains(sp.Name(), "GET /boom") {
			serverSpan = sp
			break
		}
	}
	require.NotNil(t, serverSpan, "the HTTP GET /boom server span must be among the exported spans")

	var exception sdktrace.Event
	for _, ev := range serverSpan.Events() {
		if ev.Name == "exception" {
			exception = ev
			break
		}
	}
	require.NotNil(t, exception, "the server span must carry an exception event")

	attrs := map[string]string{}
	for _, kv := range exception.Attributes {
		attrs[string(kv.Key)] = kv.Value.Emit()
	}
	assert.Equal(t, "string", attrs["exception.type"], "the panic value's concrete type")
	assert.Equal(t, "kaboom", attrs["exception.message"], "the panic value itself")
	require.Contains(t, attrs, "exception.stacktrace", "the stack trace must ride the event")
	assert.Contains(t, attrs["exception.stacktrace"], "runtime/debug.Stack", "stack traces start at the recovery point")

	require.Equal(t, codes.Error, serverSpan.Status().Code, "the span must be marked Error")
	assert.Contains(t, serverSpan.Status().Description, "panic")
}

// TestRecovererWithoutRecordingSpanStaysSafe proves the no-tracing posture
// is unchanged: with a provider that never exports (and by extension a
// disabled OTel config, whose spans are noop) the recoverer still answers
// the identical 500 — the exception-event path is strictly additive.
func TestRecovererWithoutRecordingSpanStaysSafe(t *testing.T) {
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider()) // no exporter: spans record but never export
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	// Also exercises the truly-noop path: a disabled OTel config yields noop
	// spans (IsRecording false) through the same late-bound global provider.
	s := newTestServer(t)
	s.Router.Get("/boom", func(http.ResponseWriter, *http.Request) {
		panic("still safe")
	})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/boom", nil))
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "internal_error")
}

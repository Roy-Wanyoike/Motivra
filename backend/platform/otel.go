package platform

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	promreg "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// SetupTracing configures the global tracer provider with an OTLP gRPC
// exporter when cfg.Enabled, installs the W3C propagators and returns a
// shutdown function. When disabled it still installs the propagators so
// trace context flows through even without export.
func SetupTracing(ctx context.Context, cfg OTelConfig, serviceName, environment string) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	if !cfg.Enabled {
		return func(context.Context) error { return nil }, nil
	}

	endpoint := cfg.Endpoint
	insecure := true
	if idx := strings.Index(endpoint, "://"); idx >= 0 {
		scheme := endpoint[:idx]
		endpoint = endpoint[idx+3:]
		insecure = scheme != "https"
	}

	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithTimeout(5 * time.Second),
	}
	if insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	exporter, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("otel exporter: %w", err)
	}
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			attribute.String("deployment.environment.name", environment),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(5*time.Second)),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// SetupMetrics builds the Prometheus metrics exposition: a dedicated
// registry plus its HTTP handler (mount at /metrics via WithMount).
// Services register client_golang collectors for their counters, gauges
// and histograms; the registry is scrape-ready by construction.
func SetupMetrics(serviceName, environment string) (http.Handler, func(context.Context) error, error) {
	registry := promreg.NewRegistry()
	registry.MustRegister(promreg.NewProcessCollector(promreg.ProcessCollectorOpts{}))
	registry.MustRegister(promreg.NewGoCollector())

	handler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	noop := func(context.Context) error { return nil }
	return handler, noop, nil
}

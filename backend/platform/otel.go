package platform

import (
        "context"
        "fmt"
        "time"

        "go.opentelemetry.io/otel"
        "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
        "go.opentelemetry.io/otel/exporters/prometheus"
        "go.opentelemetry.io/otel/propagation"
        otelmetric "go.opentelemetry.io/otel/metric"
        "go.opentelemetry.io/otel/sdk/metric"
        "go.opentelemetry.io/otel/sdk/resource"
        sdktrace "go.opentelemetry.io/otel/sdk/trace"
        semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
        promreg "github.com/prometheus/client_golang/prometheus"
        "github.com/prometheus/client_golang/prometheus/promhttp"
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

        exporter, err := otlptracegrpc.New(ctx,
                otlptracegrpc.WithEndpoint(endpoint),
                otlptracegrpc.WithInsecure(insecure),
                otlptracegrpc.WithTimeout(5*time.Second),
        )
        if err != nil {
                return nil, fmt.Errorf("otel exporter: %w", err)
        }
        res, err := resource.New(ctx,
                resource.WithAttributes(
                        semconv.ServiceName(serviceName),
                        semconv.DeploymentEnvironmentName(environment),
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

// SetupMetrics builds an OTel meter provider bridged to a Prometheus
// registry and returns the exposition handler plus a shutdown function.
// Mount the handler at /metrics via WithMount.
func SetupMetrics(serviceName, environment string) (http.Handler, func(context.Context) error, error) {
        exporter, err := prometheus.New()
        if err != nil {
                return nil, nil, fmt.Errorf("prometheus exporter: %w", err)
        }
        provider := metric.NewMeterProvider(
                metric.WithReader(exporter),
                metric.WithResource(resource.NewSchemaless(
                        semconv.ServiceName(serviceName),
                        semconv.DeploymentEnvironmentName(environment),
                )),
        )
        otel.SetMeterProvider(provider)

        registry := promreg.NewRegistry()
        if err := registry.Register(exporter); err != nil {
                return nil, nil, fmt.Errorf("register prometheus collector: %w", err)
        }
        handler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
        return handler, provider.Shutdown, nil
}

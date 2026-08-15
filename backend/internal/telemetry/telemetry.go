package telemetry

import (
    "context"
    "fmt"
    "log/slog"
    "os"

    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
    "go.opentelemetry.io/otel/propagation"
    "go.opentelemetry.io/otel/sdk/resource"
    sdktrace "go.opentelemetry.io/otel/sdk/trace"
    semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// ServiceName is the resource name reported on every span and log line.
// OTEL_SERVICE_NAME overrides it.
func ServiceName() string {
    if name := os.Getenv("OTEL_SERVICE_NAME"); name != "" {
        return name
    }
    return "dockerledger-backend"
}

// InitTracer wires up the OTLP/gRPC trace exporter and installs it as the
// global tracer provider. It returns a shutdown function that flushes pending
// spans.
//
// The collector endpoint comes from OTEL_EXPORTER_OTLP_ENDPOINT (read by the
// SDK itself, so the standard URL form "http://otel-collector:4317" works); if
// unset we fall back to the Compose service address. The exporter dials lazily,
// so a collector that is not up yet does not block or fail startup.
func InitTracer(ctx context.Context) (func(context.Context) error, error) {
    serviceName := ServiceName()
    endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")

    opts := []otlptracegrpc.Option{otlptracegrpc.WithInsecure()}
    if endpoint == "" {
        endpoint = "otel-collector:4317"
        opts = append(opts, otlptracegrpc.WithEndpoint(endpoint))
    }

    slog.Info("initializing tracer", "service", serviceName, "collector", endpoint)

    exporter, err := otlptracegrpc.New(ctx, opts...)
    if err != nil {
        return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
    }

    res, err := resource.New(ctx,
        resource.WithAttributes(
            semconv.ServiceNameKey.String(serviceName),
            attribute.String("environment", getEnvOr("ENV", "dev")),
        ),
    )
    if err != nil {
        return nil, fmt.Errorf("create resource: %w", err)
    }

    tp := sdktrace.NewTracerProvider(
        sdktrace.WithBatcher(exporter),
        sdktrace.WithResource(res),
    )
    otel.SetTracerProvider(tp)

    // Accept and forward W3C trace context so spans join across services.
    otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
        propagation.TraceContext{},
        propagation.Baggage{},
    ))

    return tp.Shutdown, nil
}

func getEnvOr(key, fallback string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return fallback
}

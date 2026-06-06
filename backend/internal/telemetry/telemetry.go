package telemetry

import (
    "context"
    "log"
    "os"

    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
    "go.opentelemetry.io/otel/sdk/resource"
    sdktrace "go.opentelemetry.io/otel/sdk/trace"
    semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

var serviceName = os.Getenv("OTEL_SERVICE_NAME")
var collectorURL = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")

func InitTracer() func(context.Context) error {
    if serviceName == "" {
        serviceName = "dockerledger-backend"
    }
    if collectorURL == "" {
        collectorURL = "otel-collector:4317" // Default for local setup
    }

    log.Printf("Initializing tracer with service: %s, collector: %s", serviceName, collectorURL)

    // Configure a new OTLP exporter using gRPC
    ctx := context.Background()
    exporter, err := otlptracegrpc.New(ctx,
        otlptracegrpc.WithInsecure(),
        otlptracegrpc.WithEndpoint(collectorURL),
    )
    if err != nil {
        log.Fatalf("Failed to create OTLP trace exporter: %v", err)
    }

    // Define resource attributes that describe your service
    res, err := resource.New(ctx,
        resource.WithAttributes(
            semconv.ServiceNameKey.String(serviceName),
            attribute.String("environment", os.Getenv("ENV")),
        ),
    )
    if err != nil {
        log.Fatalf("Failed to create resource: %v", err)
    }

    // Create a new tracer provider with a batch span processor
    tp := sdktrace.NewTracerProvider(
        sdktrace.WithBatcher(exporter),
        sdktrace.WithResource(res),
    )
    otel.SetTracerProvider(tp)

    // Return a shutdown function to flush spans on application exit
    return func(ctx context.Context) error {
        return tp.Shutdown(ctx)
    }
}
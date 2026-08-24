package telemetry_test

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/thirzq/dockerledger/internal/telemetry"
	"github.com/thirzq/dockerledger/internal/telemetry/logtest"
)

// TestWithRequestIDAddsRequestID is the contract every HTTP-facing module
// relies on: a request_id put into the context by the middleware ends up on
// every line logged for that request.
func TestWithRequestIDAddsRequestID(t *testing.T) {
	rec := logtest.Capture(t)

	ctx := context.WithValue(context.Background(), telemetry.RequestIDKey, "req-abc123")
	telemetry.WithRequestID(ctx).Info("handled something", "extra", "value")

	rec.RequireAttrs("handled something", map[string]any{
		"request_id": "req-abc123",
		"extra":      "value",
		"service":    telemetry.ServiceName(),
	})
}

func TestWithRequestIDWithoutRequestID(t *testing.T) {
	rec := logtest.Capture(t)

	telemetry.WithRequestID(context.Background()).Info("no request id here")

	e := rec.Require("no request id here")
	if _, ok := e.Attr("request_id"); ok {
		t.Errorf("request_id should be absent when the context carries none: %s", e)
	}
}

// An empty request_id must not be logged as an empty attribute — Loki would
// index a useless label and the Grafana pivot would break.
func TestWithRequestIDIgnoresEmptyID(t *testing.T) {
	rec := logtest.Capture(t)

	ctx := context.WithValue(context.Background(), telemetry.RequestIDKey, "")
	telemetry.WithRequestID(ctx).Info("empty request id")

	e := rec.Require("empty request id")
	if _, ok := e.Attr("request_id"); ok {
		t.Errorf("empty request_id should not be attached: %s", e)
	}
}

func TestWithRequestIDNilContext(t *testing.T) {
	rec := logtest.Capture(t)

	// A nil context must not panic; the logger falls back to the base logger.
	telemetry.WithRequestID(nil).Info("nil context")

	rec.RequireLevel("INFO", "nil context")
}

// TestWithRequestIDAddsTraceIDs covers the log-to-trace pivot: when the context
// carries a recording span, trace_id and span_id must appear on the log line.
// A plain SDK provider is used rather than telemetry.InitTracer, which would
// try to dial the OTLP collector.
func TestWithRequestIDAddsTraceIDs(t *testing.T) {
	rec := logtest.Capture(t)

	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	ctx, span := tp.Tracer("logtest").Start(context.Background(), "unit-test-span")
	defer span.End()

	telemetry.WithRequestID(ctx).Error("something failed", "error", "boom")

	e := rec.RequireLevel("ERROR", "something failed")
	traceID, ok := e.Attr("trace_id")
	if !ok {
		t.Fatalf("trace_id missing from log line: %s", e)
	}
	if want := span.SpanContext().TraceID().String(); traceID != want {
		t.Errorf("trace_id = %v, want %v", traceID, want)
	}
	spanID, ok := e.Attr("span_id")
	if !ok {
		t.Fatalf("span_id missing from log line: %s", e)
	}
	if want := span.SpanContext().SpanID().String(); spanID != want {
		t.Errorf("span_id = %v, want %v", spanID, want)
	}
}

// Both correlation IDs on one line is the combination Grafana needs.
func TestWithRequestIDCombinesRequestAndTraceIDs(t *testing.T) {
	rec := logtest.Capture(t)

	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	ctx := context.WithValue(context.Background(), telemetry.RequestIDKey, "req-combined")
	ctx, span := tp.Tracer("logtest").Start(ctx, "combined")
	defer span.End()

	telemetry.WithRequestID(ctx).Warn("combined line")

	rec.RequireAttrs("combined line", map[string]any{
		"request_id": "req-combined",
		"trace_id":   span.SpanContext().TraceID().String(),
		"span_id":    span.SpanContext().SpanID().String(),
	})
}

// Without a recording span there is nothing to correlate to, so no empty
// trace_id should be emitted.
func TestWithRequestIDNoSpanNoTraceID(t *testing.T) {
	rec := logtest.Capture(t)

	telemetry.WithRequestID(context.Background()).Info("no span")

	e := rec.Require("no span")
	if _, ok := e.Attr("trace_id"); ok {
		t.Errorf("trace_id should be absent without a span: %s", e)
	}
}

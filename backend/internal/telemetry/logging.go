package telemetry

import (
	"context"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel/trace"
)

// contextKey is unexported to prevent key collisions.
type contextKey string

const RequestIDKey contextKey = "request_id"

var Logger *slog.Logger

func init() {
	Logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel(),
	})).
		With("service", ServiceName())
	slog.SetDefault(Logger)
}

// logLevel reads LOG_LEVEL (debug|info|warn|error), defaulting to info.
func logLevel() slog.Level {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(os.Getenv("LOG_LEVEL"))); err != nil {
		return slog.LevelInfo
	}
	return lvl
}

// WithRequestID returns an slog.Logger annotated with the request_id from the
// context and, when the context carries a recording span, the trace_id and
// span_id. Emitting the trace IDs on every log line is what lets Grafana pivot
// from a log line to the trace it belongs to.
func WithRequestID(ctx context.Context) *slog.Logger {
	logger := Logger
	if ctx == nil {
		return logger
	}

	if id, ok := ctx.Value(RequestIDKey).(string); ok && id != "" {
		logger = logger.With("request_id", id)
	}

	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		logger = logger.With(
			"trace_id", sc.TraceID().String(),
			"span_id", sc.SpanID().String(),
		)
	}

	return logger
}

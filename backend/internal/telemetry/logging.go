package telemetry

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

// contextKey is unexported to prevent key collisions.
type contextKey string

const RequestIDKey contextKey = "request_id"

var Logger *slog.Logger

// levelVar backs the active handler, so the level can still be changed after
// the logger is built. That matters because this package's init() runs before
// config.Load() reads the .env file: without a dynamic level, a LOG_LEVEL set
// in .env would be read too late and silently ignored.
var levelVar = new(slog.LevelVar)

func init() {
	levelVar.Set(logLevel())
	Logger = newLogger(os.Stdout)
	slog.SetDefault(Logger)
}

// newLogger builds the process logger. Output is JSON by default because the
// OTel collector scrapes the container's stdout and parses each line as JSON to
// promote level, service and the correlation ids into Loki labels. Setting
// LOG_FORMAT=text switches to the human-readable handler for local runs; it
// must not be used in Compose, where it would break that pipeline.
func newLogger(w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: levelVar}

	var handler slog.Handler
	if strings.EqualFold(os.Getenv("LOG_FORMAT"), "text") {
		handler = slog.NewTextHandler(w, opts)
	} else {
		handler = slog.NewJSONHandler(w, opts)
	}

	return slog.New(handler).With("service", ServiceName())
}

// logLevel reads LOG_LEVEL (debug|info|warn|error), defaulting to info.
func logLevel() slog.Level {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(os.Getenv("LOG_LEVEL"))); err != nil {
		return slog.LevelInfo
	}
	return lvl
}

// ApplyEnv re-reads LOG_LEVEL and applies it to the running logger. Call it
// once the .env file has been loaded, so a level configured there takes effect.
// It only moves the threshold — the handler and its destination stay the same,
// so any logger a caller already holds keeps working.
func ApplyEnv() {
	levelVar.Set(logLevel())
}

// SetLevel changes the active log level at runtime.
func SetLevel(level slog.Level) {
	levelVar.Set(level)
}

// Level reports the active log level.
func Level() slog.Level {
	return levelVar.Level()
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

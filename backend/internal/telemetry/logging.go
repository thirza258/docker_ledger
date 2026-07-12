package telemetry

import (
	"context"
	"log/slog"
	"os"
)

// contextKey is unexported to prevent key collisions.
type contextKey string

const RequestIDKey contextKey = "request_id"

var Logger *slog.Logger

func init() {
	Logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})).
		With("service", "dockerledger-backend")
	slog.SetDefault(Logger)
}

// WithRequestID returns an slog.Logger that includes the request_id from the
// context, if one is set.
func WithRequestID(ctx context.Context) *slog.Logger {
	if id, ok := ctx.Value(RequestIDKey).(string); ok && id != "" {
		return Logger.With("request_id", id)
	}
	return Logger
}
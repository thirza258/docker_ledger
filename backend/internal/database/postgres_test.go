package database

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/thirzq/dockerledger/internal/telemetry"
	"github.com/thirzq/dockerledger/internal/telemetry/logtest"
)

// The GORM adapter is a plain struct, so every branch is reachable without a
// database connection.

func TestGormLoggerInfo(t *testing.T) {
	rec := logtest.Capture(t)

	(&slogGormLogger{}).Info(context.Background(), "migrating table", "containers")

	// The source is tagged with a component attribute rather than smuggled
	// into the message text, so Loki can filter on it.
	rec.RequireLevel("INFO", "migrating table containers")
	rec.RequireAttrs("migrating table containers", map[string]any{"component": "gorm"})
}

// GORM's logger interface is printf-shaped: the message is a format string and
// the variadic data are its arguments. They have to be interpolated, not
// concatenated.
func TestGormLoggerFormatsPrintfMessages(t *testing.T) {
	rec := logtest.Capture(t)

	(&slogGormLogger{}).Info(context.Background(), "replacing callback %s from %s", "gorm:update", "callbacks.go:42")

	rec.Require("replacing callback gorm:update from callbacks.go:42")
}

// GORM's own messages end with a newline, which has no place in a structured
// log line.
func TestGormLoggerTrimsTrailingNewline(t *testing.T) {
	rec := logtest.Capture(t)

	(&slogGormLogger{}).Info(context.Background(), "a message with a newline\n")

	rec.Require("a message with a newline")
}

func TestGormLoggerWarn(t *testing.T) {
	rec := logtest.Capture(t)

	(&slogGormLogger{}).Warn(context.Background(), "slow connection", 42)

	rec.RequireLevel("WARN", "slow connection 42")
}

// Database errors have to be logged at ERROR: the Grafana error-rate panel and
// its alert count lines by level, so an error logged at INFO is invisible.
func TestGormLoggerErrorIsLoggedAtErrorLevel(t *testing.T) {
	rec := logtest.Capture(t)

	(&slogGormLogger{}).Error(context.Background(), "connection lost", errors.New("boom"))

	rec.RequireLevel("ERROR", "connection lost boom")
}

// Record-not-found is expected on first-time repository lookups and must not
// produce noise in the log stream.
func TestGormLoggerSuppressesRecordNotFound(t *testing.T) {
	rec := logtest.Capture(t)

	(&slogGormLogger{}).Error(context.Background(), "record lookup", gorm.ErrRecordNotFound)

	if entries := rec.Entries(); len(entries) != 0 {
		t.Errorf("gorm.ErrRecordNotFound should be suppressed, got %d line(s): %s", len(entries), rec.Entries()[0])
	}
}

// A wrapped ErrRecordNotFound must be suppressed too. The check used to call
// errors.Is with its arguments reversed, so the unwrap never happened and every
// wrapped not-found reached the log.
func TestGormLoggerSuppressesWrappedRecordNotFound(t *testing.T) {
	rec := logtest.Capture(t)

	wrapped := fmt.Errorf("looking up container: %w", gorm.ErrRecordNotFound)
	(&slogGormLogger{}).Error(context.Background(), "record lookup", wrapped)

	if n := len(rec.Entries()); n != 0 {
		t.Errorf("wrapped gorm.ErrRecordNotFound should be suppressed, got %d line(s): %s", n, rec.Entries()[0])
	}
}

func TestGormLoggerErrorWithoutErrorValue(t *testing.T) {
	rec := logtest.Capture(t)

	(&slogGormLogger{}).Error(context.Background(), "plain message", "no error value here")

	rec.RequireLevel("ERROR", "plain message no error value here")
}

// Trace is the per-statement log line. On success it is a debug line carrying
// the SQL, the row count and the latency the dashboard charts.
func TestGormTraceSuccessLogsQueryAtDebug(t *testing.T) {
	rec := logtest.Capture(t)

	begin := time.Now().Add(-25 * time.Millisecond)
	(&slogGormLogger{}).Trace(context.Background(), begin, func() (string, int64) {
		return "SELECT * FROM log_entries LIMIT 10", 10
	}, nil)

	e := rec.RequireLevel("DEBUG", "gorm query")
	rec.RequireAttrs("gorm query", map[string]any{
		"sql":        "SELECT * FROM log_entries LIMIT 10",
		"rows":       10,
		"latency_ms": nil,
	})
	if _, ok := e.Attr("error"); ok {
		t.Errorf("successful query should not carry an error attribute: %s", e)
	}
}

func TestGormTraceErrorLogsAtWarn(t *testing.T) {
	rec := logtest.Capture(t)

	(&slogGormLogger{}).Trace(context.Background(), time.Now(), func() (string, int64) {
		return "SELECT * FROM missing_table", 0
	}, errors.New("relation \"missing_table\" does not exist"))

	// A failed query must NOT reuse the "gorm query" message: the OTel
	// collector filters that string out, which would hide every SQL error.
	rec.RequireLevel("WARN", "gorm query failed")
	rec.RequireAttrs("gorm query failed", map[string]any{
		"sql":   "SELECT * FROM missing_table",
		"rows":  0,
		"error": "relation \"missing_table\" does not exist",
	})
	rec.RequireAbsent("gorm query")
}

// Trace() does unwrap correctly (errors.Is(err, gorm.ErrRecordNotFound)), so a
// wrapped not-found is demoted to debug rather than logged as a warning.
func TestGormTraceRecordNotFoundIsDemotedToDebug(t *testing.T) {
	rec := logtest.Capture(t)

	l := &slogGormLogger{}
	l.Trace(context.Background(), time.Now(), func() (string, int64) {
		return "SELECT * FROM containers WHERE name = 'nope'", 0
	}, gorm.ErrRecordNotFound)
	l.Trace(context.Background(), time.Now(), func() (string, int64) {
		return "SELECT * FROM containers WHERE name = 'nope'", 0
	}, fmt.Errorf("first: %w", gorm.ErrRecordNotFound))

	for _, e := range rec.Entries() {
		if e.Level != "DEBUG" {
			t.Errorf("record-not-found query logged at %s, want DEBUG: %s", e.Level, e)
		}
	}
	if got := rec.Count("gorm query"); got != 2 {
		t.Errorf("got %d gorm query lines, want 2", got)
	}
}

// Database log lines must carry the request_id and trace ids of the HTTP
// request that triggered the query — that is what links a slow query back to
// the request in Grafana.
func TestGormLoggerPropagatesRequestAndTraceIDs(t *testing.T) {
	rec := logtest.Capture(t)

	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	ctx := context.WithValue(context.Background(), telemetry.RequestIDKey, "req-db-1")
	ctx, span := tp.Tracer("dbtest").Start(ctx, "query")
	defer span.End()

	(&slogGormLogger{}).Trace(ctx, time.Now(), func() (string, int64) {
		return "SELECT 1", 1
	}, nil)

	rec.RequireAttrs("gorm query", map[string]any{
		"request_id": "req-db-1",
		"trace_id":   span.SpanContext().TraceID().String(),
		"span_id":    span.SpanContext().SpanID().String(),
	})
}

func TestGormLoggerLogModeReturnsItself(t *testing.T) {
	l := &slogGormLogger{}
	// Levels are handled by slog via LOG_LEVEL, so LogMode is a passthrough.
	for _, lvl := range []int{1, 2, 3, 4} {
		if got := l.LogMode(gormlogger.LogLevel(lvl)); got != l {
			t.Errorf("LogMode(%d) returned a different logger; level handling belongs to slog", lvl)
		}
	}
}

func TestErrFromData(t *testing.T) {
	sentinel := errors.New("found me")
	tests := []struct {
		name string
		data []interface{}
		want error
	}{
		{"no data", nil, nil},
		{"no error in data", []interface{}{"a", 1, true}, nil},
		{"error is returned", []interface{}{"a", sentinel}, sentinel},
		{"first error wins", []interface{}{sentinel, errors.New("second")}, sentinel},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := errFromData(tc.data); !errors.Is(got, tc.want) {
				t.Errorf("errFromData(%v) = %v, want %v", tc.data, got, tc.want)
			}
		})
	}
}

// A slow statement is worth seeing without switching the whole service to
// debug logging, so its trace line is promoted to warn.
func TestGormTraceSlowQueryIsPromotedToWarn(t *testing.T) {
	rec := logtest.Capture(t)

	begin := time.Now().Add(-2 * slowQueryThreshold)
	(&slogGormLogger{}).Trace(context.Background(), begin, func() (string, int64) {
		return "SELECT * FROM log_entries", 100000
	}, nil)

	// Same reasoning as the failure case: a distinct message so the collector
	// filter does not drop it.
	rec.RequireLevel("WARN", "gorm slow query")
	rec.RequireAttrs("gorm slow query", map[string]any{
		"sql":          "SELECT * FROM log_entries",
		"latency_ms":   nil,
		"threshold_ms": slowQueryThreshold.Milliseconds(),
	})
	rec.RequireAbsent("gorm query")
}

// A fast query stays at debug, where the collector's filter drops it.
func TestGormTraceFastQueryStaysAtDebug(t *testing.T) {
	rec := logtest.Capture(t)

	(&slogGormLogger{}).Trace(context.Background(), time.Now(), func() (string, int64) {
		return "SELECT 1", 1
	}, nil)

	rec.RequireLevel("DEBUG", "gorm query")
	rec.RequireAbsent("gorm slow query")
}

// The message must stay exactly "gorm query": the OTel collector drops these
// lines by matching on that string, so renaming it would flood Loki.
func TestGormTraceMessageIsStableForCollectorFilter(t *testing.T) {
	rec := logtest.Capture(t)

	(&slogGormLogger{}).Trace(context.Background(), time.Now(), func() (string, int64) {
		return "SELECT 1", 1
	}, nil)

	if _, ok := rec.Find("gorm query"); !ok {
		t.Error(`the trace message must remain "gorm query"; config/otel-collector-config.yaml filters on it`)
	}
}

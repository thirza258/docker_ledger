package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// TestLogLevel covers the LOG_LEVEL parsing that decides which lines are
// emitted at all. A wrong default here silently drops every debug line in the
// WebSocket and GORM tracer paths.
func TestLogLevel(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want slog.Level
	}{
		{"unset defaults to info", "", slog.LevelInfo},
		{"debug", "debug", slog.LevelDebug},
		{"info", "info", slog.LevelInfo},
		{"warn", "warn", slog.LevelWarn},
		{"error", "error", slog.LevelError},
		{"uppercase is accepted", "DEBUG", slog.LevelDebug},
		{"garbage falls back to info", "not-a-level", slog.LevelInfo},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LOG_LEVEL", tc.env)
			if got := logLevel(); got != tc.want {
				t.Errorf("logLevel() with LOG_LEVEL=%q = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

func TestServiceName(t *testing.T) {
	t.Run("defaults to dockerledger-backend", func(t *testing.T) {
		t.Setenv("OTEL_SERVICE_NAME", "")
		if got := ServiceName(); got != "dockerledger-backend" {
			t.Errorf("ServiceName() = %q, want %q", got, "dockerledger-backend")
		}
	})

	t.Run("OTEL_SERVICE_NAME overrides", func(t *testing.T) {
		t.Setenv("OTEL_SERVICE_NAME", "custom-service")
		if got := ServiceName(); got != "custom-service" {
			t.Errorf("ServiceName() = %q, want %q", got, "custom-service")
		}
	})
}

// TestPackageLoggerIsJSONWithService checks the logger built by init(): every
// line must be JSON (Promtail/Loki parse it as such) and carry the service
// label the Grafana dashboards filter on.
func TestPackageLoggerIsJSONWithService(t *testing.T) {
	if Logger == nil {
		t.Fatal("telemetry.Logger is nil after package init")
	}
	if slog.Default() == nil {
		t.Fatal("slog default logger is nil after package init")
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})).
		With("service", ServiceName())
	logger.Info("probe", "key", "value")

	line := map[string]any{}
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log output is not JSON: %v (%s)", err, buf.String())
	}
	for _, key := range []string{"time", "level", "msg", "service", "key"} {
		if _, ok := line[key]; !ok {
			t.Errorf("log line missing %q key: %s", key, buf.String())
		}
	}
	if line["service"] != ServiceName() {
		t.Errorf("service = %v, want %q", line["service"], ServiceName())
	}
}

// restoreLevel puts the process log level back after a test changes it.
func restoreLevel(t *testing.T) {
	t.Helper()
	previous := Level()
	t.Cleanup(func() { SetLevel(previous) })
}

// The logger is built in init(), before config.Load() reads the .env file. A
// LOG_LEVEL set there used to be ignored entirely; ApplyEnv is what makes it
// take effect.
func TestApplyEnvUpdatesLevel(t *testing.T) {
	restoreLevel(t)

	SetLevel(slog.LevelInfo)
	if Logger.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("debug should be disabled at info level")
	}

	t.Setenv("LOG_LEVEL", "debug")
	ApplyEnv()

	if got := Level(); got != slog.LevelDebug {
		t.Errorf("Level() = %v, want %v", got, slog.LevelDebug)
	}
	// The change must reach the logger that was already built, not just the
	// stored value — callers hold references to it.
	if !Logger.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("debug is still disabled on the existing logger after ApplyEnv")
	}
}

func TestApplyEnvFallsBackToInfo(t *testing.T) {
	restoreLevel(t)

	SetLevel(slog.LevelError)
	t.Setenv("LOG_LEVEL", "nonsense")
	ApplyEnv()

	if got := Level(); got != slog.LevelInfo {
		t.Errorf("Level() = %v, want %v for an unparseable LOG_LEVEL", got, slog.LevelInfo)
	}
}

func TestSetLevel(t *testing.T) {
	restoreLevel(t)

	for _, want := range []slog.Level{slog.LevelDebug, slog.LevelWarn, slog.LevelError, slog.LevelInfo} {
		SetLevel(want)
		if got := Level(); got != want {
			t.Errorf("Level() = %v, want %v", got, want)
		}
	}
}

// JSON is the default because the OTel collector parses each stdout line as
// JSON to promote level, service and the correlation ids into Loki labels.
func TestDefaultFormatIsJSON(t *testing.T) {
	t.Setenv("LOG_FORMAT", "")

	var buf bytes.Buffer
	newLogger(&buf).Info("hello", "key", "value")

	line := map[string]any{}
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("default output is not JSON: %v (%s)", err, buf.String())
	}
	if line["msg"] != "hello" || line["key"] != "value" {
		t.Errorf("unexpected line: %s", buf.String())
	}
}

// LOG_FORMAT=text is for reading logs in a terminal during local development.
func TestTextFormat(t *testing.T) {
	t.Setenv("LOG_FORMAT", "text")

	var buf bytes.Buffer
	newLogger(&buf).Info("hello", "key", "value")

	out := buf.String()
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("LOG_FORMAT=text still produced JSON: %s", out)
	}
	for _, want := range []string{"msg=hello", "key=value", "service="} {
		if !strings.Contains(out, want) {
			t.Errorf("text output %q is missing %q", out, want)
		}
	}
}

func TestTextFormatIsCaseInsensitive(t *testing.T) {
	t.Setenv("LOG_FORMAT", "TEXT")

	var buf bytes.Buffer
	newLogger(&buf).Info("hello")

	if strings.HasPrefix(strings.TrimSpace(buf.String()), "{") {
		t.Errorf("LOG_FORMAT=TEXT should be honoured too, got %s", buf.String())
	}
}

// An unrecognised format must fall back to JSON rather than to something the
// log pipeline cannot parse.
func TestUnknownFormatFallsBackToJSON(t *testing.T) {
	t.Setenv("LOG_FORMAT", "yaml")

	var buf bytes.Buffer
	newLogger(&buf).Info("hello")

	if !strings.HasPrefix(strings.TrimSpace(buf.String()), "{") {
		t.Errorf("unknown LOG_FORMAT should fall back to JSON, got %s", buf.String())
	}
}

// Level filtering must actually drop lines below the threshold.
func TestLevelFiltersOutput(t *testing.T) {
	restoreLevel(t)
	t.Setenv("LOG_FORMAT", "")

	var buf bytes.Buffer
	logger := newLogger(&buf)

	SetLevel(slog.LevelWarn)
	logger.Debug("dropped")
	logger.Info("also dropped")
	logger.Warn("kept")

	out := buf.String()
	if strings.Contains(out, "dropped") {
		t.Errorf("lines below the threshold were emitted: %s", out)
	}
	if !strings.Contains(out, "kept") {
		t.Errorf("the warn line was dropped: %s", out)
	}
}

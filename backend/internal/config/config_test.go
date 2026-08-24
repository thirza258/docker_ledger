package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thirzq/dockerledger/internal/telemetry"
	"github.com/thirzq/dockerledger/internal/telemetry/logtest"
)

// Load warns when no .env is present — the one log line this module emits, and
// a useful signal when a container starts with missing configuration.
func TestLoadWarnsWithoutEnvFile(t *testing.T) {
	rec := logtest.Capture(t)
	t.Chdir(t.TempDir())

	Load()

	rec.RequireLevel("WARN", "no .env file found, relying on environment variables")
}

func TestLoadDoesNotWarnWithEnvFile(t *testing.T) {
	rec := logtest.Capture(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("DL_UNUSED_TEST_KEY=1\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Chdir(dir)

	Load()

	rec.RequireAbsent("no .env file found, relying on environment variables")
}

// The config carrying wrong values would misdirect the logs themselves (wrong
// DB, wrong port), so the defaults and overrides are covered too.
func TestLoadDefaults(t *testing.T) {
	logtest.Capture(t)
	t.Chdir(t.TempDir())

	for _, key := range []string{"DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME", "DB_SSLMODE", "SERVER_PORT", "DOCKER_HOST", "SHUTDOWN_TIMEOUT", "OPENROUTER_API_KEY", "OPENROUTER_MODEL"} {
		t.Setenv(key, "")
	}

	cfg := Load()

	checks := map[string]struct{ got, want string }{
		"DBHost":     {cfg.DBHost, "localhost"},
		"DBPort":     {cfg.DBPort, "5432"},
		"DBUser":     {cfg.DBUser, "postgres"},
		"DBName":     {cfg.DBName, "dockerledger"},
		"DBSSLMode":  {cfg.DBSSLMode, "disable"},
		"ServerPort": {cfg.ServerPort, "8080"},
		"DockerHost": {cfg.DockerHost, "unix:///var/run/docker.sock"},
	}
	for name, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", name, c.got, c.want)
		}
	}
	if cfg.ShutdownTimeout != 5*time.Second {
		t.Errorf("ShutdownTimeout = %v, want %v", cfg.ShutdownTimeout, 5*time.Second)
	}
}

func TestLoadReadsEnvironment(t *testing.T) {
	logtest.Capture(t)
	t.Chdir(t.TempDir())

	t.Setenv("DB_HOST", "db.internal")
	t.Setenv("SERVER_PORT", "9999")
	t.Setenv("SHUTDOWN_TIMEOUT", "30s")

	cfg := Load()

	if cfg.DBHost != "db.internal" {
		t.Errorf("DBHost = %q, want %q", cfg.DBHost, "db.internal")
	}
	if cfg.ServerPort != "9999" {
		t.Errorf("ServerPort = %q, want %q", cfg.ServerPort, "9999")
	}
	if cfg.ShutdownTimeout != 30*time.Second {
		t.Errorf("ShutdownTimeout = %v, want %v", cfg.ShutdownTimeout, 30*time.Second)
	}
}

func TestGetEnvAsDurationFallsBackOnGarbage(t *testing.T) {
	t.Setenv("DL_TEST_DURATION", "not-a-duration")
	if got := getEnvAsDuration("DL_TEST_DURATION", 7*time.Second); got != 7*time.Second {
		t.Errorf("getEnvAsDuration with garbage = %v, want the fallback %v", got, 7*time.Second)
	}
}

// The logger is built before Load() reads the .env file, so Load has to
// re-apply the level afterwards — otherwise LOG_LEVEL in .env does nothing.
func TestLoadAppliesLogLevelFromEnvFile(t *testing.T) {
	previous := telemetry.Level()
	t.Cleanup(func() { telemetry.SetLevel(previous) })

	telemetry.SetLevel(slog.LevelInfo)
	// Register the restore with t.Setenv, then clear it for real: godotenv
	// never overrides a variable that is already set, empty or not.
	t.Setenv("LOG_LEVEL", "info")
	os.Unsetenv("LOG_LEVEL")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("LOG_LEVEL=debug\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Chdir(dir)

	Load()

	if got := telemetry.Level(); got != slog.LevelDebug {
		t.Errorf("log level after Load() = %v, want %v from the .env file", got, slog.LevelDebug)
	}
}

// A real environment variable still wins, and still reaches the logger.
func TestLoadAppliesLogLevelFromEnvironment(t *testing.T) {
	previous := telemetry.Level()
	t.Cleanup(func() { telemetry.SetLevel(previous) })

	telemetry.SetLevel(slog.LevelInfo)
	t.Chdir(t.TempDir())
	t.Setenv("LOG_LEVEL", "error")

	Load()

	if got := telemetry.Level(); got != slog.LevelError {
		t.Errorf("log level after Load() = %v, want %v", got, slog.LevelError)
	}
}

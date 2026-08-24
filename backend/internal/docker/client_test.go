package docker

import (
	"context"
	"os"
	"testing"

	"github.com/thirzq/dockerledger/internal/telemetry/logtest"
)

// TestMain pins DOCKER_HOST to a closed port before any test can fire the
// sync.Once in GetClient. Without this the suite would behave differently
// depending on whether the developer's Docker daemon happens to be running —
// and on this project it usually is.
func TestMain(m *testing.M) {
	os.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	os.Unsetenv("DOCKER_CONTEXT")
	os.Exit(m.Run())
}

// TestGetClientLogsInitialization must run first: "Docker client initialized"
// is emitted inside a sync.Once, so it is logged only on the very first
// GetClient call in the process. Keep this the first test in the file.
func TestGetClientLogsInitialization(t *testing.T) {
	rec := logtest.Capture(t)

	cli, err := GetClient()
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	if cli == nil {
		t.Fatal("GetClient returned a nil client")
	}

	rec.RequireLevel("INFO", "Docker client initialized")

	// The line belongs to the once-only initialisation, so a second call must
	// not repeat it.
	rec.Reset()
	if _, err := GetClient(); err != nil {
		t.Fatalf("second GetClient: %v", err)
	}
	rec.RequireAbsent("Docker client initialized")
}

// Ping against a dead daemon must return an error and stay silent — the
// callers (health endpoint, wakeproxy manager) decide how to report it.
func TestPingAgainstDeadDaemon(t *testing.T) {
	rec := logtest.Capture(t)

	if err := Ping(context.Background()); err == nil {
		t.Fatal("expected Ping to fail against 127.0.0.1:1")
	}
	if n := len(rec.Entries()); n != 0 {
		t.Errorf("Ping logged %d line(s); the failure is reported through its error return", n)
	}
}

func TestCloseIsSafe(t *testing.T) {
	if _, err := GetClient(); err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	if err := Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

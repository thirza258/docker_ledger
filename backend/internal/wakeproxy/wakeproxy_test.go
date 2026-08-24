package wakeproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/thirzq/dockerledger/internal/telemetry/logtest"
)

// newTestManager builds a Manager by struct literal so the wakeproxy log paths
// can be exercised without a Docker daemon. Its cli field stays nil, so tests
// must only drive branches that log and return before any Docker call.
func newTestManager(t *testing.T, cfgs ...ServiceConfig) *Manager {
	t.Helper()
	m := &Manager{
		cfg:         &Config{IdleTimeout: time.Hour},
		idleTimeout: time.Hour,
		services:    make(map[string]*ManagedService),
		hostIndex:   make(map[string]string),
	}
	for _, cfg := range cfgs {
		m.services[cfg.Name] = NewManagedService(cfg, m)
		m.hostIndex[cfg.Host] = cfg.Name
	}
	return m
}

func TestProxyLogsUnknownHost(t *testing.T) {
	rec := logtest.Capture(t)

	h := NewProxyHandler(newTestManager(t))
	req := httptest.NewRequest(http.MethodGet, "http://nothing.example.com/", nil)
	req.Host = "nothing.example.com"
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
	rec.RequireLevel("WARN", "wakeproxy: unknown service host")
	rec.RequireAttrs("wakeproxy: unknown service host", map[string]any{
		"host":  "nothing.example.com",
		"error": "no service for host nothing.example.com",
	})
}

func TestProxyLogsDeactivatedService(t *testing.T) {
	rec := logtest.Capture(t)

	m := newTestManager(t, ServiceConfig{Name: "app", Host: "app.example.com", Container: "app-container", Port: 8080})
	m.services["app"].SetActive(false)

	h := NewProxyHandler(m)
	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	req.Host = "app.example.com"
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	rec.RequireLevel("INFO", "wakeproxy: service deactivated")
	rec.RequireAttrs("wakeproxy: service deactivated", map[string]any{
		"service": "app",
		"host":    "app.example.com",
	})
}

// A request that gives up while the container is still starting logs the
// start failure with the service and host attached.
func TestProxyLogsStartFailure(t *testing.T) {
	rec := logtest.Capture(t)

	m := newTestManager(t, ServiceConfig{Name: "slow", Host: "slow.example.com", Container: "slow-container", Port: 80})
	svc := m.services["slow"]
	// Pretend a startup is already in flight so AwaitReady waits instead of
	// launching one (which would need Docker).
	svc.mu.Lock()
	svc.state = StateStarting
	svc.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the client hung up

	h := NewProxyHandler(m)
	req := httptest.NewRequest(http.MethodGet, "http://slow.example.com/", nil).WithContext(ctx)
	req.Host = "slow.example.com"
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	rec.RequireLevel("ERROR", "wakeproxy: failed to start service")
	rec.RequireAttrs("wakeproxy: failed to start service", map[string]any{
		"service": "slow",
		"host":    "slow.example.com",
		"error":   "context canceled",
	})
}

// getProxy falls back to a dead address when a running service has no target
// URL; that fallback must be loud, since every request to it will fail.
func TestProxyLogsNilTargetURL(t *testing.T) {
	rec := logtest.Capture(t)

	m := newTestManager(t, ServiceConfig{Name: "notarget", Host: "notarget.example.com", Container: "c", Port: 80})
	h := NewProxyHandler(m)

	if proxy := h.getProxy(m.services["notarget"]); proxy == nil {
		t.Fatal("getProxy returned nil")
	}
	rec.RequireLevel("ERROR", "wakeproxy: nil target URL for service")
	rec.RequireAttrs("wakeproxy: nil target URL for service", map[string]any{"service": "notarget"})
}

func TestManagerLogsDynamicallyAddedService(t *testing.T) {
	rec := logtest.Capture(t)

	m := newTestManager(t)
	if err := m.AddService(ServiceConfig{Name: "new", Host: "new.example.com", Container: "new-container", Port: 3000}); err != nil {
		t.Fatalf("AddService: %v", err)
	}

	rec.RequireLevel("INFO", "wakeproxy: dynamically added service")
	rec.RequireAttrs("wakeproxy: dynamically added service", map[string]any{
		"name":      "new",
		"host":      "new.example.com",
		"container": "new-container",
	})
}

// Rejected registrations must not log a success line.
func TestManagerDoesNotLogRejectedService(t *testing.T) {
	rec := logtest.Capture(t)

	m := newTestManager(t, ServiceConfig{Name: "dup", Host: "dup.example.com", Container: "c"})
	if err := m.AddService(ServiceConfig{Name: "dup", Host: "other.example.com", Container: "c"}); err == nil {
		t.Fatal("expected a duplicate-name error")
	}
	if err := m.AddService(ServiceConfig{Name: "other", Host: "dup.example.com", Container: "c"}); err == nil {
		t.Fatal("expected a duplicate-host error")
	}
	if err := m.AddService(ServiceConfig{Container: "c"}); err == nil {
		t.Fatal("expected a missing-host error")
	}
	if err := m.AddService(ServiceConfig{Host: "x.example.com"}); err == nil {
		t.Fatal("expected a missing-container error")
	}

	rec.RequireAbsent("wakeproxy: dynamically added service")
}

func TestStopServiceWithoutContainerIsSilent(t *testing.T) {
	rec := logtest.Capture(t)

	m := newTestManager(t, ServiceConfig{Name: "idle", Host: "idle.example.com", Container: "c"})
	m.StopService(m.services["idle"]) // no container id yet

	rec.RequireAbsent("wakeproxy: stopping service due to idle timeout")
}

// --- admin API ---

func newAdmin(t *testing.T, cfgs ...ServiceConfig) (*AdminServer, *http.ServeMux, *Manager) {
	t.Helper()
	m := newTestManager(t, cfgs...)
	a := NewAdminServer(m)
	mux := http.NewServeMux()
	a.RegisterHandlers(mux)
	return a, mux, m
}

func TestAdminLogsInvalidJSON(t *testing.T) {
	rec := logtest.Capture(t)

	_, mux, _ := newAdmin(t)
	req := httptest.NewRequest(http.MethodPost, "/admin/services", strings.NewReader("{not json"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	e := rec.RequireLevel("WARN", "wakeproxy admin: invalid JSON for createService")
	if _, ok := e.Attr("error"); !ok {
		t.Errorf("invalid-JSON log line should carry the decode error: %s", e)
	}
}

func TestAdminLogsCreateFailure(t *testing.T) {
	rec := logtest.Capture(t)

	_, mux, _ := newAdmin(t, ServiceConfig{Name: "taken", Host: "taken.example.com", Container: "c"})
	body, _ := json.Marshal(ServiceConfig{Name: "taken", Host: "another.example.com", Container: "c"})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin/services", strings.NewReader(string(body))))

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d", w.Code, http.StatusConflict)
	}
	rec.RequireLevel("ERROR", "wakeproxy admin: failed to create service")
	rec.RequireAttrs("wakeproxy admin: failed to create service", map[string]any{
		"error": `service "taken" already exists`,
	})
}

func TestAdminLogsActivationAndDeactivation(t *testing.T) {
	rec := logtest.Capture(t)

	_, mux, m := newAdmin(t, ServiceConfig{Name: "svc", Host: "svc.example.com", Container: "c"})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin/services/svc/deactivate", nil))
	if w.Code != http.StatusOK {
		t.Errorf("deactivate status = %d, want %d", w.Code, http.StatusOK)
	}
	rec.RequireLevel("INFO", "wakeproxy admin: service deactivated")
	rec.RequireAttrs("wakeproxy admin: service deactivated", map[string]any{"name": "svc"})
	if m.services["svc"].IsActive() {
		t.Error("service should be inactive after deactivate")
	}

	rec.Reset()

	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin/services/svc/activate", nil))
	if w.Code != http.StatusOK {
		t.Errorf("activate status = %d, want %d", w.Code, http.StatusOK)
	}
	rec.RequireLevel("INFO", "wakeproxy admin: service activated")
	rec.RequireAttrs("wakeproxy admin: service activated", map[string]any{"name": "svc"})
	if !m.services["svc"].IsActive() {
		t.Error("service should be active after activate")
	}
}

func TestAdminLogsUnknownServiceOnActivate(t *testing.T) {
	rec := logtest.Capture(t)

	_, mux, _ := newAdmin(t)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin/services/ghost/activate", nil))

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
	rec.RequireLevel("WARN", "wakeproxy admin: service not found for activation")
	rec.RequireAttrs("wakeproxy admin: service not found for activation", map[string]any{"name": "ghost"})
}

func TestAdminLogsUnknownServiceOnDeactivate(t *testing.T) {
	rec := logtest.Capture(t)

	_, mux, _ := newAdmin(t)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin/services/ghost/deactivate", nil))

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
	rec.RequireLevel("WARN", "wakeproxy admin: service not found for deactivation")
	rec.RequireAttrs("wakeproxy admin: service not found for deactivation", map[string]any{"name": "ghost"})
}

// Listing services is a read-only path and should stay silent.
func TestAdminListServicesIsSilent(t *testing.T) {
	rec := logtest.Capture(t)

	_, mux, _ := newAdmin(t, ServiceConfig{Name: "svc", Host: "svc.example.com", Container: "c", Port: 80})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/services", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var services []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&services); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(services) != 1 || services[0]["name"] != "svc" {
		t.Errorf("unexpected service list: %v", services)
	}
	if n := len(rec.Entries()); n != 0 {
		t.Errorf("listing services logged %d line(s), want none", n)
	}
}

// RecordActivity is called on every proxied request once a service is running.
// It takes s.mu and then calls resetIdleTimer, which takes s.mu again — a
// non-reentrant mutex, so the request goroutine blocks forever.
func TestRecordActivityDoesNotDeadlock(t *testing.T) {
	m := newTestManager(t, ServiceConfig{Name: "running", Host: "running.example.com", Container: "c", Port: 80})
	svc := m.services["running"]
	svc.mu.Lock()
	svc.state = StateRunning
	svc.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.RecordActivity()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RecordActivity deadlocked: it holds s.mu and calls resetIdleTimer, which locks s.mu again")
	}
}

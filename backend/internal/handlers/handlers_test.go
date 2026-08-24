package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/thirzq/dockerledger/internal/config"
	"github.com/thirzq/dockerledger/internal/services"
	"github.com/thirzq/dockerledger/internal/storage"
	"github.com/thirzq/dockerledger/internal/telemetry"
	"github.com/thirzq/dockerledger/internal/telemetry/logtest"
	"github.com/thirzq/dockerledger/internal/testsupport"
)

// fakeDaemon stands in for Docker. Every container call answers the way a real
// daemon answers for a missing container, which is what drives the handlers
// down their error branches deterministically — with or without Docker
// installed on the machine running the tests.
func fakeDaemon() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/_ping") {
			w.Header().Set("Api-Version", "1.51")
			w.Header().Set("Ostype", "linux")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "No such container: " + r.URL.Path})
	}))
}

// TestMain points DOCKER_HOST at the fake daemon before anything can fire the
// sync.Once inside docker.GetClient.
func TestMain(m *testing.M) {
	srv := fakeDaemon()
	u, err := url.Parse(srv.URL)
	if err != nil {
		panic(err)
	}
	os.Setenv("DOCKER_HOST", "tcp://"+u.Host)
	os.Unsetenv("DOCKER_CONTEXT")

	code := m.Run()
	srv.Close()
	os.Exit(code)
}

func newContainerHandler(t *testing.T) *ContainerHandler {
	t.Helper()
	return NewContainerHandler(services.NewContainerService(), storage.NewLogRepository(testsupport.DeadDB(t)))
}

// requestWithID builds a request carrying a request_id, the way the middleware
// does, so each assertion also proves the id reaches the handler's log line.
func requestWithID(method, target, id string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	return req.WithContext(context.WithValue(req.Context(), telemetry.RequestIDKey, id))
}

// --- container handler ---

func TestListContainersLogsFailure(t *testing.T) {
	rec := logtest.Capture(t)
	h := newContainerHandler(t)

	w := httptest.NewRecorder()
	h.ListContainers(w, requestWithID(http.MethodGet, "/containers", "req-list"))

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
	rec.RequireLevel("ERROR", "failed to list containers")
	rec.RequireAttrs("failed to list containers", map[string]any{
		"request_id": "req-list",
		"error":      nil,
	})
}

func TestGetContainerLogsFailure(t *testing.T) {
	rec := logtest.Capture(t)
	h := newContainerHandler(t)

	w := httptest.NewRecorder()
	h.GetContainer(w, requestWithID(http.MethodGet, "/containers/abc123", "req-get"))

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
	rec.RequireLevel("ERROR", "failed to get container")
	rec.RequireAttrs("failed to get container", map[string]any{
		"container_id": "abc123",
		"request_id":   "req-get",
	})
}

// A rejected request is answered from the handler without touching Docker, so
// it must not produce an error line.
func TestGetContainerMissingIDIsNotLogged(t *testing.T) {
	rec := logtest.Capture(t)
	h := newContainerHandler(t)

	w := httptest.NewRecorder()
	h.GetContainer(w, httptest.NewRequest(http.MethodGet, "/containers/", nil))

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if n := len(rec.Entries()); n != 0 {
		t.Errorf("a client-side error logged %d line(s), want none: %v", n, rec.Entries())
	}
}

// The health check answers from the daemon ping and reports the result in the
// body; it is not a log call site, and must stay silent either way.
func TestDockerHealthCheckIsSilent(t *testing.T) {
	rec := logtest.Capture(t)
	h := newContainerHandler(t)

	w := httptest.NewRecorder()
	h.DockerHealthCheck(w, httptest.NewRequest(http.MethodGet, "/health/docker", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var resp map[string]bool
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The fake daemon answers /_ping, so the ping succeeds.
	if !resp["connected"] {
		t.Error("connected = false although the daemon answered the ping")
	}
	if n := len(rec.Entries()); n != 0 {
		t.Errorf("health check logged %d line(s); the result belongs in the response body: %v", n, rec.Entries())
	}
}

// --- logs handler ---

// A missing container is the user's mistake, not a server fault: it must log
// at WARN and answer 404, so it does not trip error-rate alerts.
func TestGetContainerLogsWarnsOnMissingContainer(t *testing.T) {
	rec := logtest.Capture(t)
	h := newContainerHandler(t)

	w := httptest.NewRecorder()
	h.GetContainerLogs(w, requestWithID(http.MethodGet, "/containers/ghost/logs", "req-logs"))

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
	rec.RequireLevel("WARN", "container not found for logs")
	rec.RequireAttrs("container not found for logs", map[string]any{
		"container_id": "ghost",
		"request_id":   "req-logs",
		"error":        nil,
	})
	rec.RequireAbsent("failed to retrieve container logs")
}

func TestGetContainerLogsBadPathsAreNotLogged(t *testing.T) {
	h := newContainerHandler(t)

	cases := []struct {
		name   string
		target string
		status int
	}{
		{"missing logs suffix", "/containers/abc", http.StatusBadRequest},
		{"empty container id", "/containers//logs", http.StatusBadRequest},
		{"non numeric tail", "/containers/abc/logs?tail=abc", http.StatusBadRequest},
		{"zero tail", "/containers/abc/logs?tail=0", http.StatusBadRequest},
		{"negative tail", "/containers/abc/logs?tail=-5", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := logtest.Capture(t)
			w := httptest.NewRecorder()
			h.GetContainerLogs(w, httptest.NewRequest(http.MethodGet, tc.target, nil))

			if w.Code != tc.status {
				t.Errorf("status = %d, want %d", w.Code, tc.status)
			}
			if n := len(rec.Entries()); n != 0 {
				t.Errorf("client-side rejection logged %d line(s), want none: %v", n, rec.Entries())
			}
		})
	}
}

// --- search handler ---

func TestSearchLogsLogsFailure(t *testing.T) {
	rec := logtest.Capture(t)
	h := newContainerHandler(t)

	w := httptest.NewRecorder()
	h.SearchLogs(w, requestWithID(http.MethodGet, "/logs/search?q=panic&container=api", "req-search"))

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
	rec.RequireLevel("ERROR", "log search failed")
	rec.RequireAttrs("log search failed", map[string]any{
		"query":      "panic",
		"container":  "api",
		"request_id": "req-search",
		"error":      nil,
	})
}

func TestSearchLogsRejectsBadInputWithoutLogging(t *testing.T) {
	h := newContainerHandler(t)

	cases := []struct{ name, target string }{
		{"missing query", "/logs/search"},
		{"bad from", "/logs/search?q=x&from=yesterday"},
		{"bad to", "/logs/search?q=x&to=tomorrow"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := logtest.Capture(t)
			w := httptest.NewRecorder()
			h.SearchLogs(w, httptest.NewRequest(http.MethodGet, tc.target, nil))

			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
			}
			if n := len(rec.Entries()); n != 0 {
				t.Errorf("client-side rejection logged %d line(s), want none: %v", n, rec.Entries())
			}
		})
	}
}

// --- AI summary handler ---

func newAIHandler(t *testing.T) *AISummaryHandler {
	t.Helper()
	cfg := &config.Config{OpenRouterAPIKey: "test-key", OpenRouterModel: "test-model"}
	return NewAISummaryHandler(services.NewAISummaryService(cfg, storage.NewLogRepository(testsupport.DeadDB(t))))
}

func TestGenerateSummaryLogsFailure(t *testing.T) {
	rec := logtest.Capture(t)
	h := newAIHandler(t)

	w := httptest.NewRecorder()
	h.GenerateSummary(w, requestWithID(http.MethodGet, "/ai/summary?hours_back=1&limit=10", "req-ai"))

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
	rec.RequireLevel("ERROR", "failed to generate AI summary")
	rec.RequireAttrs("failed to generate AI summary", map[string]any{
		"request_id": "req-ai",
		"error":      nil,
	})
}

func TestGenerateContainerSummaryLogsFailure(t *testing.T) {
	rec := logtest.Capture(t)
	h := newAIHandler(t)

	w := httptest.NewRecorder()
	h.GenerateContainerSummary(w, requestWithID(http.MethodGet, "/ai/summary/container?container_name=api", "req-ai-container"))

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
	rec.RequireLevel("ERROR", "failed to generate container AI summary")
	rec.RequireAttrs("failed to generate container AI summary", map[string]any{
		"container_name": "api",
		"request_id":     "req-ai-container",
	})
}

func TestGenerateContainerSummaryRejectsMissingNameWithoutLogging(t *testing.T) {
	rec := logtest.Capture(t)
	h := newAIHandler(t)

	w := httptest.NewRecorder()
	h.GenerateContainerSummary(w, httptest.NewRequest(http.MethodGet, "/ai/summary/container", nil))

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if n := len(rec.Entries()); n != 0 {
		t.Errorf("missing container_name logged %d line(s), want none: %v", n, rec.Entries())
	}
}

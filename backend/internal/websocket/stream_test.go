package websocket

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	gorilla "github.com/gorilla/websocket"

	"github.com/thirzq/dockerledger/internal/services"
	"github.com/thirzq/dockerledger/internal/telemetry/logtest"
)

// These tests drive a real WebSocket upgrade against a fake Docker daemon, so
// the live-streaming log lines — the ones that matter most in a log viewer —
// are covered end to end.

func frame(stream byte, payload string) []byte {
	var buf bytes.Buffer
	header := make([]byte, 8)
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
	buf.Write(header)
	buf.WriteString(payload)
	return buf.Bytes()
}

// fakeDaemon serves a short multiplexed log stream and a two-container list.
// Requests for the container id "ghost" are answered the way Docker answers
// for a container that does not exist.
func fakeDaemon() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("Api-Version", "1.51")
			w.Header().Set("Ostype", "linux")
			w.WriteHeader(http.StatusOK)

		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"Id": "abc123", "Names": []string{"/web"}, "State": "running", "Image": "nginx", "Status": "Up"},
				{"Id": "def456", "Names": []string{"/api"}, "State": "running", "Image": "go", "Status": "Up"},
			})

		case strings.HasSuffix(r.URL.Path, "/logs") && !strings.Contains(r.URL.Path, "/ghost/"):
			w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(frame(1, "first line\n"))
			_, _ = w.Write(frame(2, "second line\n"))
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			// Returning here ends the stream, which is what makes the handler
			// take its EOF branch.

		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "No such container"})
		}
	}))
}

// TestMain points DOCKER_HOST at the fake daemon before the sync.Once inside
// docker.GetClient can fire.
func TestMain(m *testing.M) {
	srv := fakeDaemon()
	u, _ := url.Parse(srv.URL)
	os.Setenv("DOCKER_HOST", "tcp://"+u.Host)
	os.Unsetenv("DOCKER_CONTEXT")

	code := m.Run()
	srv.Close()
	os.Exit(code)
}

func wsURL(base, path string) string {
	return "ws" + strings.TrimPrefix(base, "http") + path
}

// The full happy path: upgrade succeeds, the decoded log lines reach the
// client, and the end of the stream is logged rather than spun on.
func TestLogStreamLogsSuccessfulStream(t *testing.T) {
	rec := logtest.Capture(t)

	srv := httptest.NewServer(NewLogStreamHandler(services.NewContainerService()))
	defer srv.Close()

	conn, resp, err := gorilla.DefaultDialer.Dial(wsURL(srv.URL, "/containers/abc123/logs/live"), nil)
	if err != nil {
		t.Fatalf("dial: %v (status %v)", err, resp.Status)
	}
	defer conn.Close()

	var received []string
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		typ, msg, err := conn.ReadMessage()
		if err != nil {
			break // the server closed the stream
		}
		if typ == gorilla.TextMessage {
			received = append(received, string(msg))
		}
	}

	joined := strings.Join(received, "")
	for _, want := range []string{"first line", "second line"} {
		if !strings.Contains(joined, want) {
			t.Errorf("client never received %q; got %q", want, joined)
		}
	}

	rec.RequireLevel("INFO", "ws upgrade success")
	rec.RequireAttrs("ws upgrade success", map[string]any{"container_id": "abc123"})
	rec.RequireLevel("INFO", "ws stream ended")
	rec.RequireAttrs("ws stream ended", map[string]any{"container_id": "abc123"})
	rec.RequireAbsent("ws upgrade failed")
	rec.RequireAbsent("ws stream not found")
}

// The end of a stream must be logged exactly once. Looping on EOF used to
// write two lines a second per connection, which is how a log viewer floods
// its own logs.
func TestLogStreamDoesNotSpinOnStreamEnd(t *testing.T) {
	rec := logtest.Capture(t)

	srv := httptest.NewServer(NewLogStreamHandler(services.NewContainerService()))
	defer srv.Close()

	conn, _, err := gorilla.DefaultDialer.Dial(wsURL(srv.URL, "/containers/abc123/logs/live"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
	conn.Close()

	time.Sleep(200 * time.Millisecond) // a spinning handler would pile lines up here

	if got := rec.Count("ws stream ended"); got != 1 {
		t.Errorf("%q logged %d times, want exactly 1", "ws stream ended", got)
	}
}

// When the upgrade succeeds but the container does not exist, the failure has
// to be reported over the already-open socket and logged.
func TestLogStreamLogsMissingContainerAfterUpgrade(t *testing.T) {
	rec := logtest.Capture(t)

	srv := httptest.NewServer(NewLogStreamHandler(services.NewContainerService()))
	defer srv.Close()

	conn, _, err := gorilla.DefaultDialer.Dial(wsURL(srv.URL, "/containers/ghost/logs/live"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, readErr := conn.ReadMessage()
	if readErr == nil {
		t.Error("expected the server to close the connection for an unknown container")
	}

	rec.RequireLevel("INFO", "ws upgrade success")
	rec.RequireLevel("ERROR", "ws stream not found")
	rec.RequireAttrs("ws stream not found", map[string]any{
		"container_id": "ghost",
		"error":        nil,
	})
	rec.RequireAbsent("ws stream ended")
}

// The multi-container stream logs how many containers it attached to — the
// line that tells an operator the aggregate view is actually wired up.
func TestMultiLogLogsConnectedContainers(t *testing.T) {
	rec := logtest.Capture(t)

	srv := httptest.NewServer(NewMultiLogStreamHandler(services.NewContainerService()))
	defer srv.Close()

	conn, _, err := gorilla.DefaultDialer.Dial(wsURL(srv.URL, "/logs/live"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	var received []map[string]string
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var entry map[string]string
		if err := json.Unmarshal(msg, &entry); err != nil {
			t.Errorf("multi-log message is not JSON: %v (%s)", err, msg)
			continue
		}
		received = append(received, entry)
	}

	rec.RequireLevel("INFO", "ws multi-log connected")
	rec.RequireAttrs("ws multi-log connected", map[string]any{"containers": 2})
	rec.RequireAbsent("ws multi-log upgrade failed")
	rec.RequireAbsent("ws multi-log failed to list containers")

	if len(received) == 0 {
		t.Error("multi-log stream delivered no messages")
	}
	for _, entry := range received {
		if entry["container"] != "web" && entry["container"] != "api" {
			t.Errorf("message tagged with unexpected container %q", entry["container"])
		}
	}
}

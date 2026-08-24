package websocket

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/thirzq/dockerledger/internal/services"
	"github.com/thirzq/dockerledger/internal/telemetry"
	"github.com/thirzq/dockerledger/internal/telemetry/logtest"
)

// All the branches asserted here fire before the handler talks to Docker, so
// they need no daemon.

func newLogStreamHandler() *LogStreamHandler {
	return NewLogStreamHandler(services.NewContainerService())
}

func TestLogStreamLogsInvalidPath(t *testing.T) {
	rec := logtest.Capture(t)

	w := httptest.NewRecorder()
	newLogStreamHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/containers/abc/logs", nil))

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	rec.RequireLevel("WARN", "ws invalid path format")
}

func TestLogStreamLogsMissingContainerID(t *testing.T) {
	rec := logtest.Capture(t)

	w := httptest.NewRecorder()
	newLogStreamHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/containers//logs/live", nil))

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	rec.RequireLevel("WARN", "ws missing container ID")
}

// The request parse is how a failing upgrade is diagnosed, and it is one debug
// line carrying both the path and the parsed parts — not the four separate
// lines per request it used to be.
func TestLogStreamEmitsOneDebugLinePerRequest(t *testing.T) {
	rec := logtest.Capture(t)

	w := httptest.NewRecorder()
	newLogStreamHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/containers/abc123/logs/live", nil))

	e := rec.RequireAttrs("ws request", map[string]any{"path": "/containers/abc123/logs/live"})
	if e.Level != "DEBUG" {
		t.Errorf("%q logged at %s, want DEBUG", e.Msg, e.Level)
	}
	parts, ok := e.Attr("parts")
	if !ok {
		t.Fatalf("the request line should carry the parsed path parts: %s", e)
	}
	if got, want := len(parts.([]any)), 3; got != want {
		t.Errorf("parts has %d elements, want %d", got, want)
	}

	// The lines this replaced must be gone, or the noise is still there.
	for _, msg := range []string{"ws trimmed path", "ws path parts", "ws upgrade request"} {
		rec.RequireAbsent(msg)
	}
	if got := rec.Count("ws request"); got != 1 {
		t.Errorf("%q logged %d times per request, want 1", "ws request", got)
	}
}

// A plain HTTP request on the live-log route cannot be upgraded; the failure
// must be logged as an error rather than swallowed.
func TestLogStreamLogsUpgradeFailure(t *testing.T) {
	rec := logtest.Capture(t)

	w := httptest.NewRecorder()
	newLogStreamHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/containers/abc123/logs/live", nil))

	e := rec.RequireLevel("ERROR", "ws upgrade failed")
	if _, ok := e.Attr("error"); !ok {
		t.Errorf("upgrade failure should carry the error: %s", e)
	}
	rec.RequireAbsent("ws upgrade success")
}

// WebSocket log lines must carry the request_id the middleware assigned, so a
// stream can be traced from the access log into the streaming logs.
func TestLogStreamLogsCarryRequestID(t *testing.T) {
	rec := logtest.Capture(t)

	ctx := context.WithValue(context.Background(), telemetry.RequestIDKey, "ws-req-1")
	req := httptest.NewRequest(http.MethodGet, "/containers/abc/logs", nil).WithContext(ctx)
	newLogStreamHandler().ServeHTTP(httptest.NewRecorder(), req)

	rec.RequireAttrs("ws invalid path format", map[string]any{"request_id": "ws-req-1"})
}

func TestMultiLogLogsUpgradeFailure(t *testing.T) {
	rec := logtest.Capture(t)

	h := NewMultiLogStreamHandler(services.NewContainerService())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/logs/live", nil))

	e := rec.RequireLevel("ERROR", "ws multi-log upgrade failed")
	if _, ok := e.Attr("error"); !ok {
		t.Errorf("multi-log upgrade failure should carry the error: %s", e)
	}
	// The upgrader already wrote the HTTP error; the handler must not write a
	// second response.
	rec.RequireAbsent("ws multi-log connected")
}

func TestMultiLogUpgradeFailureCarriesRequestID(t *testing.T) {
	rec := logtest.Capture(t)

	ctx := context.WithValue(context.Background(), telemetry.RequestIDKey, "ws-multi-1")
	req := httptest.NewRequest(http.MethodGet, "/logs/live", nil).WithContext(ctx)
	NewMultiLogStreamHandler(services.NewContainerService()).ServeHTTP(httptest.NewRecorder(), req)

	rec.RequireAttrs("ws multi-log upgrade failed", map[string]any{"request_id": "ws-multi-1"})
}

// decodeLogChunk turns Docker's multiplexed stream into the text pushed to the
// browser; if it mangles frames the live log view shows garbage.
func TestDecodeLogChunk(t *testing.T) {
	frame := func(stream byte, payload string) []byte {
		n := len(payload)
		h := []byte{stream, 0, 0, 0, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
		return append(h, []byte(payload)...)
	}

	tests := []struct {
		name string
		in   []byte
		want string
	}{
		{"stdout frame", frame(1, "hello from stdout"), "hello from stdout"},
		// A single Read from the Docker stream routinely returns several
		// frames at once. All of their payloads must survive, or the live log
		// view silently drops lines. Compare services.decodeDockerLogs, which
		// loops over every frame in its input.
		{
			name: "several frames in one chunk",
			in:   append(frame(1, "line one\n"), frame(2, "line two\n")...),
			want: "line one\nline two\n",
		},
		{"stderr frame", frame(2, "error line"), "error line"},
		{"short chunk passes through", []byte("tiny"), "tiny"},
		{"plain text without header passes through", []byte("plain text, definitely longer than 8"), "plain text, definitely longer than 8"},
		// A frame split across two reads: emit the bytes that arrived rather
		// than handing the browser the raw header along with them.
		{"partial trailing frame yields its payload", append([]byte{1, 0, 0, 0, 0, 0, 0, 100}, []byte("short")...), "short"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeLogChunk(tc.in); got != tc.want {
				t.Errorf("decodeLogChunk() = %q, want %q", got, tc.want)
			}
		})
	}
}

package middleware

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thirzq/dockerledger/internal/telemetry"
	"github.com/thirzq/dockerledger/internal/telemetry/logtest"
)

// TestAccessLogEmitsRequestLine is the core access-log contract: one JSON line
// per request carrying the fields the Grafana dashboard and alert rules query.
func TestAccessLogEmitsRequestLine(t *testing.T) {
	rec := logtest.Capture(t)

	handler := AccessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))

	req := httptest.NewRequest(http.MethodPost, "/containers/abc/logs", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	rec.RequireLevel("INFO", "http request")
	rec.RequireAttrs("http request", map[string]any{
		"method":     http.MethodPost,
		"path":       "/containers/abc/logs",
		"status":     http.StatusCreated,
		"latency_ms": nil, // varies per run; presence is what matters
		"service":    telemetry.ServiceName(),
	})
}

// A handler that never calls WriteHeader still returns 200, and the access log
// has to say so rather than logging a zero status.
func TestAccessLogDefaultsToStatus200(t *testing.T) {
	rec := logtest.Capture(t)

	handler := AccessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))

	rec.RequireAttrs("http request", map[string]any{"status": http.StatusOK})
}

func TestAccessLogRecordsErrorStatus(t *testing.T) {
	rec := logtest.Capture(t)

	handler := AccessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))

	rec.RequireAttrs("http request", map[string]any{"status": http.StatusInternalServerError})
}

func TestAccessLogOneLinePerRequest(t *testing.T) {
	rec := logtest.Capture(t)

	handler := AccessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	for range 3 {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	}

	if got := rec.Count("http request"); got != 3 {
		t.Errorf("access log emitted %d lines for 3 requests, want 3", got)
	}
}

func TestRequestIDGeneratedWhenHeaderAbsent(t *testing.T) {
	var gotID string
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := r.Context().Value(telemetry.RequestIDKey).(string)
		gotID = id
	}))

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	if gotID == "" {
		t.Fatal("no request_id placed in the request context")
	}
	if len(gotID) != 12 { // 6 random bytes, hex encoded
		t.Errorf("generated request id %q has length %d, want 12", gotID, len(gotID))
	}
	if header := w.Header().Get("X-Request-ID"); header != gotID {
		t.Errorf("X-Request-ID response header = %q, want %q", header, gotID)
	}
}

func TestRequestIDPreservesIncomingHeader(t *testing.T) {
	var gotID string
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID, _ = r.Context().Value(telemetry.RequestIDKey).(string)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "caller-supplied-id")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if gotID != "caller-supplied-id" {
		t.Errorf("request id in context = %q, want the incoming header value", gotID)
	}
	if header := w.Header().Get("X-Request-ID"); header != "caller-supplied-id" {
		t.Errorf("X-Request-ID response header = %q, want it echoed back", header)
	}
}

func TestRequestIDsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := r.Context().Value(telemetry.RequestIDKey).(string)
		seen[id] = true
	}))

	const n = 50
	for range n {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}
	if len(seen) != n {
		t.Errorf("got %d unique request ids across %d requests, want %d", len(seen), n, n)
	}
}

// The chain as main.go wires it: RequestID(AccessLog(mux)). The id on the
// access-log line must be the same one returned to the caller, otherwise a
// user cannot look their own request up in Loki.
func TestRequestIDFlowsIntoAccessLog(t *testing.T) {
	rec := logtest.Capture(t)

	handler := RequestID(AccessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	req := httptest.NewRequest(http.MethodGet, "/containers", nil)
	req.Header.Set("X-Request-ID", "trace-me")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	rec.RequireAttrs("http request", map[string]any{"request_id": "trace-me"})
	if got := w.Header().Get("X-Request-ID"); got != "trace-me" {
		t.Errorf("response X-Request-ID = %q, want %q", got, "trace-me")
	}
}

func TestAccessLogWithGeneratedRequestID(t *testing.T) {
	rec := logtest.Capture(t)

	handler := RequestID(AccessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/containers", nil))

	e := rec.Require("http request")
	logged, ok := e.Attr("request_id")
	if !ok {
		t.Fatalf("access log line has no request_id: %s", e)
	}
	if logged != w.Header().Get("X-Request-ID") {
		t.Errorf("logged request_id %v does not match response header %q", logged, w.Header().Get("X-Request-ID"))
	}
}

// hijackableRecorder is an httptest.ResponseRecorder that also implements
// http.Hijacker and http.Flusher, standing in for the real server connection.
type hijackableRecorder struct {
	*httptest.ResponseRecorder
	hijacked bool
	flushed  bool
}

func (h *hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked = true
	server, client := net.Pipe()
	_ = client.Close()
	return server, bufio.NewReadWriter(bufio.NewReader(server), bufio.NewWriter(server)), nil
}

func (h *hijackableRecorder) Flush() { h.flushed = true }

// Live log streaming upgrades to WebSocket, which needs http.Hijacker to
// survive the AccessLog wrapper. If statusWriter stops forwarding Hijack, every
// live log stream fails with a 500 — so this is asserted directly.
func TestStatusWriterForwardsHijack(t *testing.T) {
	rec := logtest.Capture(t)

	var hijackErr error
	handler := AccessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("wrapped ResponseWriter does not implement http.Hijacker; WebSocket upgrades would fail")
			return
		}
		conn, _, err := hijacker.Hijack()
		hijackErr = err
		if conn != nil {
			_ = conn.Close()
		}
	}))

	hr := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder()}
	handler.ServeHTTP(hr, httptest.NewRequest(http.MethodGet, "/containers/abc/logs/live", nil))

	if hijackErr != nil {
		t.Fatalf("Hijack() returned an error: %v", hijackErr)
	}
	if !hr.hijacked {
		t.Error("Hijack was not forwarded to the underlying ResponseWriter")
	}
	// A hijacked connection writes its own response, so the access log records
	// the 101 the upgrade handshake returns.
	rec.RequireAttrs("http request", map[string]any{"status": http.StatusSwitchingProtocols})
}

// Without an underlying Hijacker the wrapper must report that clearly rather
// than panicking.
func TestStatusWriterHijackWithoutSupport(t *testing.T) {
	handler := AccessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("statusWriter should always advertise http.Hijacker")
		}
		_, _, err := hijacker.Hijack()
		if err == nil {
			t.Error("expected an error when the underlying writer cannot hijack")
			return
		}
		if !strings.Contains(err.Error(), "http.Hijacker") {
			t.Errorf("error %q should mention http.Hijacker", err)
		}
	}))

	// httptest.ResponseRecorder does not implement http.Hijacker.
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

// Streamed responses must not be buffered until the handler returns.
func TestStatusWriterForwardsFlush(t *testing.T) {
	handler := AccessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("wrapped ResponseWriter does not implement http.Flusher")
			return
		}
		flusher.Flush()
	}))

	hr := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder()}
	handler.ServeHTTP(hr, httptest.NewRequest(http.MethodGet, "/", nil))

	if !hr.flushed {
		t.Error("Flush was not forwarded to the underlying ResponseWriter")
	}
}

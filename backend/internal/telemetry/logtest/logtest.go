// Package logtest captures the structured log lines a module emits so tests can
// assert on them.
//
// Two sinks have to be redirected to see everything the codebase writes:
// modules that log through telemetry.WithRequestID / telemetry.Logger read the
// telemetry package variable at call time, while modules that call slog.Info
// and friends read slog.Default(). Capture swaps both and restores them when
// the test ends. Both are process-global, so a test using Capture must not call
// t.Parallel().
package logtest

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/thirzq/dockerledger/internal/telemetry"
)

// Entry is one decoded JSON log line.
type Entry struct {
	Level string
	Msg   string
	// Attrs holds every field of the line, including "time", "level", "msg"
	// and "service", so tests can assert on the envelope as well as on the
	// attributes the call site passed.
	Attrs map[string]any
}

// Attr returns the named attribute and whether it was present.
func (e Entry) Attr(key string) (any, bool) {
	v, ok := e.Attrs[key]
	return v, ok
}

// String renders the entry for test failure messages.
func (e Entry) String() string {
	b, _ := json.Marshal(e.Attrs)
	return string(b)
}

// Recorder collects log lines emitted while it is installed.
type Recorder struct {
	t   *testing.T
	mu  sync.Mutex
	buf bytes.Buffer
}

// Capture installs a JSON log handler at debug level over both the telemetry
// logger and the slog default, and restores the originals via t.Cleanup.
//
// The level is debug on purpose: several call sites under test (the WebSocket
// path handling and the GORM query tracer) log at debug, and the production
// default of info would silently drop them and read as "never emitted".
func Capture(t *testing.T) *Recorder {
	t.Helper()

	r := &Recorder{t: t}
	handler := slog.NewJSONHandler(&syncWriter{r: r}, &slog.HandlerOptions{Level: slog.LevelDebug})

	// Mirror telemetry's init(): the service attribute is part of every line
	// in production, and tests should see the same envelope.
	logger := slog.New(handler).With("service", telemetry.ServiceName())

	prevTelemetry := telemetry.Logger
	prevDefault := slog.Default()
	telemetry.Logger = logger
	slog.SetDefault(logger)

	t.Cleanup(func() {
		telemetry.Logger = prevTelemetry
		slog.SetDefault(prevDefault)
	})

	return r
}

type syncWriter struct{ r *Recorder }

func (w *syncWriter) Write(p []byte) (int, error) {
	w.r.mu.Lock()
	defer w.r.mu.Unlock()
	return w.r.buf.Write(p)
}

var _ io.Writer = (*syncWriter)(nil)

// Entries decodes every line captured so far.
func (r *Recorder) Entries() []Entry {
	r.mu.Lock()
	raw := r.buf.String()
	r.mu.Unlock()

	var entries []Entry
	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		attrs := map[string]any{}
		if err := json.Unmarshal([]byte(line), &attrs); err != nil {
			r.t.Fatalf("log line is not valid JSON: %v\nline: %s", err, line)
		}
		level, _ := attrs["level"].(string)
		msg, _ := attrs["msg"].(string)
		entries = append(entries, Entry{Level: level, Msg: msg, Attrs: attrs})
	}
	return entries
}

// Find returns the first entry whose msg equals msg.
func (r *Recorder) Find(msg string) (Entry, bool) {
	for _, e := range r.Entries() {
		if e.Msg == msg {
			return e, true
		}
	}
	return Entry{}, false
}

// Require returns the entry with the given msg, failing the test if the line
// was never emitted.
func (r *Recorder) Require(msg string) Entry {
	r.t.Helper()
	e, ok := r.Find(msg)
	if !ok {
		r.t.Fatalf("expected a log line with msg %q, got:\n%s", msg, r.dump())
	}
	return e
}

// RequireLevel asserts the line was emitted at the given level and returns it.
func (r *Recorder) RequireLevel(level, msg string) Entry {
	r.t.Helper()
	e := r.Require(msg)
	if e.Level != level {
		r.t.Errorf("log %q: level = %q, want %q", msg, e.Level, level)
	}
	return e
}

// RequireAttrs asserts the named attributes are present on the line with the
// given msg, and that any non-nil want value matches. A nil want only asserts
// presence, which is what you use for values that vary per run (latency, IDs).
func (r *Recorder) RequireAttrs(msg string, want map[string]any) Entry {
	r.t.Helper()
	e := r.Require(msg)
	for key, wantVal := range want {
		got, ok := e.Attrs[key]
		if !ok {
			r.t.Errorf("log %q: missing attribute %q; line: %s", msg, key, e)
			continue
		}
		if wantVal == nil {
			continue
		}
		if !equalJSON(got, wantVal) {
			r.t.Errorf("log %q: attribute %q = %#v, want %#v", msg, key, got, wantVal)
		}
	}
	return e
}

// RequireAbsent fails if any line with the given msg was emitted. Used for the
// paths that deliberately suppress a log line.
func (r *Recorder) RequireAbsent(msg string) {
	r.t.Helper()
	if e, ok := r.Find(msg); ok {
		r.t.Errorf("expected no log line with msg %q, got: %s", msg, e)
	}
}

// Count returns how many lines carry the given msg.
func (r *Recorder) Count(msg string) int {
	n := 0
	for _, e := range r.Entries() {
		if e.Msg == msg {
			n++
		}
	}
	return n
}

// Reset drops everything captured so far, so one test can assert on several
// independent phases.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf.Reset()
}

func (r *Recorder) dump() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.buf.Len() == 0 {
		return "  (no log lines captured)"
	}
	return r.buf.String()
}

// equalJSON compares a decoded JSON value against an expected Go value.
// Numbers decode as float64, so ints in test expectations are normalised.
func equalJSON(got, want any) bool {
	switch w := want.(type) {
	case int:
		g, ok := got.(float64)
		return ok && g == float64(w)
	case int64:
		g, ok := got.(float64)
		return ok && g == float64(w)
	case float64:
		g, ok := got.(float64)
		return ok && g == w
	case string:
		g, ok := got.(string)
		return ok && g == w
	case bool:
		g, ok := got.(bool)
		return ok && g == w
	default:
		return false
	}
}

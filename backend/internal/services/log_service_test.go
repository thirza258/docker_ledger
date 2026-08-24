package services

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thirzq/dockerledger/internal/models"
)

// This package has no log call sites of its own — it is the pipeline the log
// lines travel through. The tests here check that Docker's multiplexed stream
// is decoded into the exact text the API, the WebSocket stream and the
// collector go on to store and display.

// frame builds one Docker stream frame: an 8-byte header (stream type, then a
// big-endian payload length) followed by the payload.
func frame(stream byte, payload string) []byte {
	var buf bytes.Buffer
	header := make([]byte, 8)
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
	buf.Write(header)
	buf.WriteString(payload)
	return buf.Bytes()
}

const (
	streamStdout = 1
	streamStderr = 2
)

// fakeDaemon serves the handful of Docker endpoints these tests exercise.
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
				{"Id": "abc123", "Names": []string{"/web"}, "State": "running", "Image": "nginx", "Status": "Up 2 hours"},
				{"Id": "def456", "Names": []string{"/api"}, "State": "running", "Image": "go", "Status": "Up 1 hour"},
			})

		case strings.HasSuffix(r.URL.Path, "/logs") && !strings.Contains(r.URL.Path, "/ghost/"):
			w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
			w.WriteHeader(http.StatusOK)
			for _, f := range [][]byte{
				frame(streamStdout, "first line\n"),
				frame(streamStderr, "second line is an error\n"),
				frame(streamStdout, "third line\n"),
			} {
				_, _ = w.Write(f)
			}
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}

		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "No such container"})
		}
	}))
}

func TestMain(m *testing.M) {
	srv := fakeDaemon()
	u, _ := url.Parse(srv.URL)
	os.Setenv("DOCKER_HOST", "tcp://"+u.Host)
	os.Unsetenv("DOCKER_CONTEXT")

	code := m.Run()
	srv.Close()
	os.Exit(code)
}

// decodeDockerLogs backs the REST endpoint that returns a container's recent
// logs; a decoding slip shows the operator binary garbage instead of the log.
func TestDecodeDockerLogs(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{"empty input", nil, ""},
		{"single stdout frame", frame(streamStdout, "hello\n"), "hello\n"},
		{"stderr frame", frame(streamStderr, "boom\n"), "boom\n"},
		{
			name: "frames are concatenated in order",
			raw:  append(append(frame(streamStdout, "one\n"), frame(streamStderr, "two\n")...), frame(streamStdout, "three\n")...),
			want: "one\ntwo\nthree\n",
		},
		{"header without payload is dropped", frame(streamStdout, ""), ""},
		{"truncated header is dropped", []byte{1, 0, 0}, ""},
		{
			name: "truncated payload is dropped but earlier frames survive",
			raw:  append(frame(streamStdout, "kept\n"), append([]byte{1, 0, 0, 0, 0, 0, 0, 50}, []byte("short")...)...),
			want: "kept\n",
		},
		{"multi-byte utf8 survives", frame(streamStdout, "héllo → wörld\n"), "héllo → wörld\n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeDockerLogs(tc.raw); got != tc.want {
				t.Errorf("decodeDockerLogs() = %q, want %q", got, tc.want)
			}
		})
	}
}

// multiplexedDecoder feeds the live WebSocket stream and the collector.
func TestMultiplexedDecoder(t *testing.T) {
	raw := append(append(frame(streamStdout, "alpha\n"), frame(streamStderr, "beta\n")...), frame(streamStdout, "gamma\n")...)

	dec := newMultiplexedDecoder(bytes.NewReader(raw))
	got, err := readAll(dec)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if want := "alpha\nbeta\ngamma\n"; got != want {
		t.Errorf("decoded stream = %q, want %q", got, want)
	}
}

// A zero-length frame is a keepalive; it must be skipped rather than ending
// the stream, or a quiet container's logs would stop early.
func TestMultiplexedDecoderSkipsEmptyFrames(t *testing.T) {
	raw := append(frame(streamStdout, ""), frame(streamStdout, "after empty\n")...)

	dec := newMultiplexedDecoder(bytes.NewReader(raw))
	got, err := readAll(dec)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if want := "after empty\n"; got != want {
		t.Errorf("decoded stream = %q, want %q", got, want)
	}
}

// A payload larger than the caller's buffer must be handed over across several
// reads without losing or duplicating bytes.
func TestMultiplexedDecoderHandlesSmallBuffers(t *testing.T) {
	payload := strings.Repeat("x", 5000) + "\n"
	dec := newMultiplexedDecoder(bytes.NewReader(frame(streamStdout, payload)))

	var out bytes.Buffer
	buf := make([]byte, 64)
	for {
		n, err := dec.Read(buf)
		out.Write(buf[:n])
		if err != nil {
			break
		}
	}
	if out.String() != payload {
		t.Errorf("decoded %d bytes, want %d", out.Len(), len(payload))
	}
}

func readAll(dec *multiplexedDecoder) (string, error) {
	var out bytes.Buffer
	buf := make([]byte, 1024)
	for {
		n, err := dec.Read(buf)
		out.Write(buf[:n])
		if err != nil {
			if err.Error() == "EOF" || strings.Contains(err.Error(), "EOF") {
				return out.String(), nil
			}
			return out.String(), err
		}
	}
}

// --- against the fake daemon ---

func TestGetContainerLogsReturnsDecodedText(t *testing.T) {
	svc := NewContainerService()

	logs, err := svc.GetContainerLogs(context.Background(), "abc123", 100)
	if err != nil {
		t.Fatalf("GetContainerLogs: %v", err)
	}
	want := "first line\nsecond line is an error\nthird line\n"
	if logs != want {
		t.Errorf("logs = %q, want %q", logs, want)
	}
}

func TestGetAllRunningContainers(t *testing.T) {
	svc := NewContainerService()

	infos, err := svc.GetAllRunningContainers(context.Background())
	if err != nil {
		t.Fatalf("GetAllRunningContainers: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("got %d containers, want 2", len(infos))
	}
	// The leading slash Docker puts on names must be stripped, since the name
	// is used as the container label on every stored log line.
	if infos[0].Name != "web" || infos[1].Name != "api" {
		t.Errorf("names = %q/%q, want web/api without the leading slash", infos[0].Name, infos[1].Name)
	}
}

// FollowContainerLogs is what the collector and the multi-container WebSocket
// stream consume: one JSON object per line, tagging each message with its
// container.
func TestFollowContainerLogsEmitsJSONPerLine(t *testing.T) {
	svc := NewContainerService()

	ch, cancel, err := svc.FollowContainerLogs(context.Background(), "abc123", "web")
	if err != nil {
		t.Fatalf("FollowContainerLogs: %v", err)
	}
	defer cancel()

	var got []map[string]string
	timeout := time.After(5 * time.Second)
	for len(got) < 3 {
		select {
		case msg, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed after %d messages, want 3", len(got))
			}
			var entry map[string]string
			if err := json.Unmarshal([]byte(msg), &entry); err != nil {
				t.Fatalf("message is not JSON: %v (%s)", err, msg)
			}
			got = append(got, entry)
		case <-timeout:
			t.Fatalf("timed out after %d messages, want 3", len(got))
		}
	}

	want := []string{"first line", "second line is an error", "third line"}
	for i, entry := range got {
		if entry["container"] != "web" {
			t.Errorf("message %d container = %q, want %q", i, entry["container"], "web")
		}
		if entry["message"] != want[i] {
			t.Errorf("message %d = %q, want %q", i, entry["message"], want[i])
		}
	}
}

func TestStreamContainerLogsReturnsReadableStream(t *testing.T) {
	svc := NewContainerService()

	stream, err := svc.StreamContainerLogs(context.Background(), "abc123", 10)
	if err != nil {
		t.Fatalf("StreamContainerLogs: %v", err)
	}
	defer stream.Close()

	buf := make([]byte, 8+len("first line\n"))
	if _, err := stream.Read(buf); err != nil {
		t.Fatalf("read from stream: %v", err)
	}
	if !bytes.Contains(buf, []byte("first line")) {
		t.Errorf("first read = %q, want it to contain %q", buf, "first line")
	}
}

func TestGetContainerLogsPropagatesDockerErrors(t *testing.T) {
	svc := NewContainerService()

	_, err := svc.GetContainerLogs(context.Background(), "ghost", 10)
	if err == nil {
		t.Fatal("expected an error for a container the daemon does not know")
	}
	if !strings.Contains(err.Error(), "No such container") {
		t.Errorf("error = %v, want it to mention the missing container so the handler can map it to 404", err)
	}
}

// --- AI prompt building over log lines ---

// entry is a terse way to spell a models.LogEntry in these tables.
type entry struct{ container, message string }

func makeEntries(items ...entry) []models.LogEntry {
	out := make([]models.LogEntry, 0, len(items))
	for _, it := range items {
		out = append(out, models.LogEntry{ContainerName: it.container, Message: it.message})
	}
	return out
}

func TestPrepareLogsForPromptRanksBySeverityThenCount(t *testing.T) {
	entries := makeEntries(
		entry{"api", "just an info line"},
		entry{"api", "just an info line"},
		entry{"api", "just an info line"},
		entry{"db", "WARN connection pool nearly full"},
		entry{"api", "ERROR failed to reach upstream"},
		entry{"worker", "FATAL panic: nil map write"},
	)

	got := prepareLogsForPrompt(entries)
	if len(got) == 0 {
		t.Fatal("prepareLogsForPrompt returned nothing")
	}
	if !strings.Contains(got[0].Message, "FATAL") {
		t.Errorf("first entry = %q, want the fatal line first", got[0].Message)
	}
	if !strings.Contains(got[1].Message, "ERROR") {
		t.Errorf("second entry = %q, want the error line second", got[1].Message)
	}
	// Duplicates collapse into one group, so the repeated info line appears once.
	infoCount := 0
	for _, e := range got {
		if strings.Contains(e.Message, "just an info line") {
			infoCount++
		}
	}
	if infoCount != 1 {
		t.Errorf("the repeated info line appears %d times, want it collapsed into 1", infoCount)
	}
}

func TestPrepareLogsForPromptTruncatesWithMarker(t *testing.T) {
	var entries []entry
	for i := range 500 {
		entries = append(entries, entry{"api", fmt.Sprintf("unique info line number %d padded %s", i, strings.Repeat("y", 60))})
	}

	got := prepareLogsForPrompt(makeEntries(entries...))

	last := got[len(got)-1]
	if last.ContainerName != "SYSTEM" || !strings.Contains(last.Message, "Truncated") {
		t.Errorf("last entry = %+v, want a SYSTEM truncation marker so the model knows lines were dropped", last)
	}
	if len(got) >= 500 {
		t.Errorf("kept %d entries; the prompt should be capped well below the input size", len(got))
	}
}

func TestPrepareLogsForPromptEmpty(t *testing.T) {
	if got := prepareLogsForPrompt(nil); len(got) != 0 {
		t.Errorf("prepareLogsForPrompt(nil) = %v, want empty", got)
	}
}

func TestBuildPromptIncludesContainerAndMessage(t *testing.T) {
	prompt := buildPrompt(makeEntries(entry{"api", "ERROR upstream timeout"}))

	for _, want := range []string{"[api] ERROR upstream timeout", "top_errors", "most_failing_containers", "suggested_causes"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt is missing %q", want)
		}
	}
}

func TestBuildContainerPromptCapsAtHundredLines(t *testing.T) {
	var entries []entry
	for i := range 150 {
		entries = append(entries, entry{"api", fmt.Sprintf("line %d", i)})
	}

	prompt := buildContainerPrompt("api", makeEntries(entries...))

	if !strings.Contains(prompt, `container "api"`) {
		t.Error("prompt should name the container it is about")
	}
	if strings.Contains(prompt, "line 100") {
		t.Error("prompt includes line 100; it should stop at the first 100 lines")
	}
	if !strings.Contains(prompt, "line 99") {
		t.Error("prompt is missing line 99; the first 100 lines should be kept")
	}
}

func TestParseAIResponse(t *testing.T) {
	t.Run("extracts embedded JSON", func(t *testing.T) {
		raw := "Sure! Here is the analysis:\n```json\n" +
			`{"top_errors":["upstream timeout"],"most_failing_containers":["api"],"suggested_causes":["network"]}` +
			"\n```"
		got := parseAIResponse(raw)
		if len(got.TopErrors) != 1 || got.TopErrors[0] != "upstream timeout" {
			t.Errorf("top_errors = %v, want [upstream timeout]", got.TopErrors)
		}
		if len(got.MostFailingContainers) != 1 || got.MostFailingContainers[0] != "api" {
			t.Errorf("most_failing_containers = %v, want [api]", got.MostFailingContainers)
		}
	})

	t.Run("falls back to raw text", func(t *testing.T) {
		got := parseAIResponse("the model rambled without any JSON")
		if len(got.TopErrors) != 1 || got.TopErrors[0] != "Could not parse AI output" {
			t.Errorf("top_errors = %v, want the parse-failure marker", got.TopErrors)
		}
		if len(got.SuggestedCauses) != 1 || got.SuggestedCauses[0] != "the model rambled without any JSON" {
			t.Errorf("suggested_causes = %v, want the raw text preserved", got.SuggestedCauses)
		}
	})
}

package collector

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/moby/moby/api/types/events"

	"github.com/thirzq/dockerledger/internal/models"
	"github.com/thirzq/dockerledger/internal/services"
	"github.com/thirzq/dockerledger/internal/storage"
	"github.com/thirzq/dockerledger/internal/telemetry/logtest"
	"github.com/thirzq/dockerledger/internal/testsupport"
)

// TestMain pins DOCKER_HOST to a closed port before the sync.Once inside
// docker.GetClient can fire, so the collector's failure branches behave the
// same whether or not the developer has a Docker daemon running.
func TestMain(m *testing.M) {
	os.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	os.Unsetenv("DOCKER_CONTEXT")
	os.Exit(m.Run())
}

// newTestCollector wires a collector to repositories backed by an unreachable
// database, so every persistence call fails and its log line is emitted.
func newTestCollector(t *testing.T) *LogCollector {
	t.Helper()
	db := testsupport.DeadDB(t)
	return NewLogCollector(services.NewContainerService(), storage.NewContainerRepository(db), storage.NewLogRepository(db))
}

// A dropped batch is silent data loss unless it is logged — this is the most
// important error line in the collector.
func TestBatchWriterLogsInsertFailure(t *testing.T) {
	rec := logtest.Capture(t)
	c := newTestCollector(t)

	ctx, cancel := context.WithCancel(context.Background())
	go c.batchWriter(ctx)

	c.batchCh <- models.LogEntry{ContainerID: "c1", Message: "line one", Stream: "stdout", Timestamp: time.Now()}
	time.Sleep(50 * time.Millisecond) // let the writer pick the entry up
	cancel()                          // shutdown flushes synchronously

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := c.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	rec.RequireLevel("ERROR", "batch insert failed")
	e := rec.Require("batch insert failed")
	if _, ok := e.Attr("error"); !ok {
		t.Errorf("batch insert failure must carry the error: %s", e)
	}
}

// With nothing buffered there is nothing to insert, so the failure line must
// not appear on an idle shutdown.
func TestBatchWriterSilentWhenEmpty(t *testing.T) {
	rec := logtest.Capture(t)
	c := newTestCollector(t)

	ctx, cancel := context.WithCancel(context.Background())
	go c.batchWriter(ctx)
	time.Sleep(20 * time.Millisecond)
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := c.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	rec.RequireAbsent("batch insert failed")
}

func TestShutdownRespectsDeadline(t *testing.T) {
	c := newTestCollector(t) // batchWriter never started, so batchDone never closes

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if err := c.Shutdown(ctx); err == nil {
		t.Error("Shutdown should return the context error when the writer never finishes")
	}
}

// The writer can reach its exit path from two branches; closing batchDone
// twice would panic and take the process down during shutdown.
func TestSignalDoneIsIdempotent(t *testing.T) {
	c := newTestCollector(t)
	c.signalDone()
	c.signalDone() // must not panic

	if err := c.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown after signalDone: %v", err)
	}
}

// Retention failures are logged; a silent failure would let the log table grow
// until the disk fills.
func TestRunRetentionLogsFailure(t *testing.T) {
	rec := logtest.Capture(t)
	c := newTestCollector(t)

	c.runRetention(7 * 24 * time.Hour)

	rec.RequireLevel("ERROR", "log retention cleanup failed")
	e := rec.Require("log retention cleanup failed")
	if _, ok := e.Attr("error"); !ok {
		t.Errorf("retention failure must carry the error: %s", e)
	}
}

func TestRetentionLoopLogsOnStop(t *testing.T) {
	rec := logtest.Capture(t)
	c := newTestCollector(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.retentionLoop(ctx)
	}()

	time.Sleep(100 * time.Millisecond) // it runs one cleanup at startup
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("retentionLoop did not exit after its context was cancelled")
	}

	rec.RequireLevel("INFO", "retention loop stopped")
}

// With no reachable daemon the collector must say so instead of attaching to
// nothing in silence.
func TestAttachToAllRunningLogsListFailure(t *testing.T) {
	rec := logtest.Capture(t)
	c := newTestCollector(t)

	c.attachToAllRunning(context.Background())

	rec.RequireLevel("ERROR", "failed to list containers")
	e := rec.Require("failed to list containers")
	if _, ok := e.Attr("error"); !ok {
		t.Errorf("list failure must carry the error: %s", e)
	}
}

// attachToContainer has two failure points, and both must name the container
// so the operator knows which stream was lost.
func TestAttachToContainerLogsUpsertAndFollowFailures(t *testing.T) {
	rec := logtest.Capture(t)
	c := newTestCollector(t)

	c.attachToContainer(context.Background(), "deadbeef123", "my-container")

	rec.RequireLevel("ERROR", "failed to upsert container")
	rec.RequireAttrs("failed to upsert container", map[string]any{
		"container_id": "deadbeef123",
		"error":        nil,
	})

	rec.RequireLevel("ERROR", "failed to follow logs")
	rec.RequireAttrs("failed to follow logs", map[string]any{
		"container": "my-container",
		"error":     nil,
	})
}

func TestWatchEventsLogsFailure(t *testing.T) {
	rec := logtest.Capture(t)
	c := newTestCollector(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.watchEvents(ctx)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("watchEvents did not return")
	}

	// Against a dead daemon the event stream reports an error; either the
	// client could not be created or the stream itself failed.
	if _, ok := rec.Find("event error"); !ok {
		if _, ok := rec.Find("cannot create Docker client"); !ok {
			t.Errorf("watchEvents against a dead daemon logged neither %q nor %q: %v",
				"event error", "cannot create Docker client", rec.Entries())
		}
	}
}

func TestContainerNameFromEvent(t *testing.T) {
	tests := []struct {
		name  string
		event events.Message
		want  string
	}{
		{
			name:  "name attribute present",
			event: events.Message{Actor: events.Actor{Attributes: map[string]string{"name": "web"}}},
			want:  "web",
		},
		{
			name:  "no attributes",
			event: events.Message{Actor: events.Actor{Attributes: map[string]string{}}},
			want:  "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := containerNameFromEvent(tc.event); got != tc.want {
				t.Errorf("containerNameFromEvent() = %q, want %q", got, tc.want)
			}
		})
	}
}

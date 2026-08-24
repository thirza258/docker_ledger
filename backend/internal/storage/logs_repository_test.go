package storage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/thirzq/dockerledger/internal/models"
	"github.com/thirzq/dockerledger/internal/telemetry/logtest"
	"github.com/thirzq/dockerledger/internal/testsupport"
)

// --- failure paths: no database server needed ---

// An empty batch must be a no-op rather than an error; the collector calls it
// on every flush tick, including idle ones.
func TestBatchInsertEmptyIsNoOp(t *testing.T) {
	rec := logtest.Capture(t)
	repo := NewLogRepository(testsupport.DeadDB(t))

	if err := repo.BatchInsertLogs(context.Background(), nil); err != nil {
		t.Errorf("BatchInsertLogs(nil) = %v, want nil", err)
	}
	if n := len(rec.Entries()); n != 0 {
		t.Errorf("empty batch logged %d line(s), want none", n)
	}
}

// The repository reports failures through error returns; the caller decides
// what to log. This pins that contract so a future change does not start
// double-logging every failed insert.
func TestRepositoryFailuresAreReturnedNotLogged(t *testing.T) {
	rec := logtest.Capture(t)
	repo := NewLogRepository(testsupport.DeadDB(t))
	ctx := context.Background()

	entries := []models.LogEntry{{ContainerID: "c1", Message: "hello", Stream: "stdout", Timestamp: time.Now()}}

	if err := repo.BatchInsertLogs(ctx, entries); err == nil {
		t.Error("BatchInsertLogs against an unreachable database should fail")
	}
	if err := repo.InsertLog(ctx, "c1", "hello", "stdout"); err == nil {
		t.Error("InsertLog against an unreachable database should fail")
	}
	if _, err := repo.SearchLogs(ctx, "error", "", nil, nil, 10); err == nil {
		t.Error("SearchLogs against an unreachable database should fail")
	}
	if _, err := repo.GetLogsSince(ctx, time.Now().Add(-time.Hour), 10); err == nil {
		t.Error("GetLogsSince against an unreachable database should fail")
	}
	if _, err := repo.GetRecentLogs(ctx, "c1", 10); err == nil {
		t.Error("GetRecentLogs against an unreachable database should fail")
	}

	if n := len(rec.Entries()); n != 0 {
		t.Errorf("repository logged %d line(s) directly; failures should surface as errors: %v", n, rec.Entries())
	}
}

// A failed cleanup must not claim it deleted anything.
func TestDeleteLogsBeforeFailureLogsNothing(t *testing.T) {
	rec := logtest.Capture(t)
	repo := NewLogRepository(testsupport.DeadDB(t))

	n, err := repo.DeleteLogsBefore(context.Background(), time.Now())
	if err == nil {
		t.Error("DeleteLogsBefore against an unreachable database should fail")
	}
	if n != 0 {
		t.Errorf("deleted count = %d, want 0 on failure", n)
	}
	rec.RequireAbsent("log retention cleanup")
}

// --- success paths: need a live Postgres (DL_TEST_DSN) ---

// TestDeleteLogsBeforeLogsRetentionCleanup covers the only success-path log
// line in this package: the retention summary the dashboard uses to confirm
// the cleanup loop is alive.
func TestDeleteLogsBeforeLogsRetentionCleanup(t *testing.T) {
	rec := logtest.Capture(t)
	db := testsupport.LiveDB(t)

	containerID := fmt.Sprintf("retention-test-%d", time.Now().UnixNano())
	containerRepo := NewContainerRepository(db)
	if err := containerRepo.Upsert(context.Background(), containerID, containerID); err != nil {
		t.Fatalf("seed container: %v", err)
	}
	t.Cleanup(func() {
		db.Where("container_id = ?", containerID).Delete(&models.LogEntry{})
		db.Where("id = ?", containerID).Delete(&models.Container{})
	})

	logRepo := NewLogRepository(db)
	old := time.Now().Add(-48 * time.Hour)
	entries := []models.LogEntry{
		{ContainerID: containerID, Message: "old line 1", Stream: "stdout", Timestamp: old},
		{ContainerID: containerID, Message: "old line 2", Stream: "stderr", Timestamp: old},
	}
	if err := logRepo.BatchInsertLogs(context.Background(), entries); err != nil {
		t.Fatalf("seed logs: %v", err)
	}

	cutoff := time.Now().Add(-24 * time.Hour)
	deleted, err := logRepo.DeleteLogsBefore(context.Background(), cutoff)
	if err != nil {
		t.Fatalf("DeleteLogsBefore: %v", err)
	}
	if deleted < 2 {
		t.Fatalf("deleted %d rows, want at least the 2 seeded", deleted)
	}

	rec.RequireLevel("INFO", "log retention cleanup")
	e := rec.RequireAttrs("log retention cleanup", map[string]any{
		"deleted": float64(deleted),
		"before":  cutoff.Format(time.RFC3339),
	})
	if _, ok := e.Attr("deleted"); !ok {
		t.Errorf("retention line must report how many rows went: %s", e)
	}
}

// Deleting nothing must stay silent, or the hourly loop would log a useless
// line on every tick forever.
func TestDeleteLogsBeforeSilentWhenNothingDeleted(t *testing.T) {
	rec := logtest.Capture(t)
	db := testsupport.LiveDB(t)

	logRepo := NewLogRepository(db)
	// Far enough in the past that no row can match.
	if _, err := logRepo.DeleteLogsBefore(context.Background(), time.Unix(0, 0)); err != nil {
		t.Fatalf("DeleteLogsBefore: %v", err)
	}

	rec.RequireAbsent("log retention cleanup")
}

// TestSearchLogsRoundTrip checks that a stored log line comes back through the
// search endpoint's query with its container name resolved — the path behind
// the log viewer's search box.
func TestSearchLogsRoundTrip(t *testing.T) {
	db := testsupport.LiveDB(t)

	containerID := fmt.Sprintf("search-test-%d", time.Now().UnixNano())
	containerName := containerID
	if err := NewContainerRepository(db).Upsert(context.Background(), containerID, containerName); err != nil {
		t.Fatalf("seed container: %v", err)
	}
	t.Cleanup(func() {
		db.Where("container_id = ?", containerID).Delete(&models.LogEntry{})
		db.Where("id = ?", containerID).Delete(&models.Container{})
	})

	repo := NewLogRepository(db)
	now := time.Now()
	needle := fmt.Sprintf("UNIQUE-NEEDLE-%d", now.UnixNano())
	if err := repo.BatchInsertLogs(context.Background(), []models.LogEntry{
		{ContainerID: containerID, Message: needle + " something failed", Stream: "stderr", Timestamp: now},
		{ContainerID: containerID, Message: "unrelated line", Stream: "stdout", Timestamp: now},
	}); err != nil {
		t.Fatalf("seed logs: %v", err)
	}

	found, err := repo.SearchLogs(context.Background(), needle, containerName, nil, nil, 10)
	if err != nil {
		t.Fatalf("SearchLogs: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("SearchLogs returned %d rows, want 1", len(found))
	}
	if found[0].ContainerName != containerName {
		t.Errorf("container_name = %q, want %q (the join must resolve it)", found[0].ContainerName, containerName)
	}

	since, err := repo.GetLogsSince(context.Background(), now.Add(-time.Minute), 100)
	if err != nil {
		t.Fatalf("GetLogsSince: %v", err)
	}
	if len(since) == 0 {
		t.Error("GetLogsSince returned nothing for logs written a moment ago")
	}

	byContainer, err := repo.GetLogsByContainerSince(context.Background(), containerName, now.Add(-time.Minute), 100)
	if err != nil {
		t.Fatalf("GetLogsByContainerSince: %v", err)
	}
	if len(byContainer) != 2 {
		t.Fatalf("GetLogsByContainerSince returned %d rows, want 2", len(byContainer))
	}
	for _, l := range byContainer {
		if l.ContainerName != containerName {
			t.Errorf("container_name = %q, want %q (filled in from the looked-up container)", l.ContainerName, containerName)
		}
	}
}

// Timestamps must be set on write; a zero value writes year 1 and hides the
// row from every time-range query the UI makes.
func TestInsertLogSetsTimestamp(t *testing.T) {
	db := testsupport.LiveDB(t)

	containerID := fmt.Sprintf("ts-test-%d", time.Now().UnixNano())
	if err := NewContainerRepository(db).Upsert(context.Background(), containerID, containerID); err != nil {
		t.Fatalf("seed container: %v", err)
	}
	t.Cleanup(func() {
		db.Where("container_id = ?", containerID).Delete(&models.LogEntry{})
		db.Where("id = ?", containerID).Delete(&models.Container{})
	})

	repo := NewLogRepository(db)
	if err := repo.InsertLog(context.Background(), containerID, "timestamped", "stdout"); err != nil {
		t.Fatalf("InsertLog: %v", err)
	}

	logs, err := repo.GetRecentLogs(context.Background(), containerID, 1)
	if err != nil {
		t.Fatalf("GetRecentLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("got %d rows, want 1", len(logs))
	}
	if logs[0].Timestamp.Year() < 2000 {
		t.Errorf("timestamp = %v; it must be set on write, not left zero", logs[0].Timestamp)
	}
}

package collector

import (
    "context"
    "encoding/json"
    "log"
    "sync"
    "time"

	"github.com/moby/moby/client"
	"github.com/moby/moby/api/types/events"

	"github.com/thirzq/dockerledger/internal/docker"
    "github.com/thirzq/dockerledger/internal/models"
	"github.com/thirzq/dockerledger/internal/storage"
    "github.com/thirzq/dockerledger/internal/services"

)

type LogCollector struct {
    dockerService  *services.ContainerService
    containerRepo  *storage.ContainerRepository
    logRepo        *storage.LogRepository
    cancelFuncs    map[string]context.CancelFunc
    mu             sync.RWMutex
    batchCh        chan models.LogEntry
    batchDone      chan struct{}
}

func NewLogCollector(
    dockerService *services.ContainerService,
    containerRepo *storage.ContainerRepository,
    logRepo *storage.LogRepository,
) *LogCollector {
    return &LogCollector{
        dockerService: dockerService,
        containerRepo: containerRepo,
        logRepo:       logRepo,
        cancelFuncs:   make(map[string]context.CancelFunc),
        batchCh:       make(chan models.LogEntry, 10000), // buffered
        batchDone:     make(chan struct{}),
    }
}

// Start begins collecting logs and the batch writer.
func (c *LogCollector) Start(ctx context.Context) {
    go c.batchWriter(ctx)
    c.attachToAllRunning(ctx)
    go c.watchEvents(ctx)
}

// batchWriter flushes logs every 5 seconds or when batchCh is closed.
func (c *LogCollector) batchWriter(ctx context.Context) {
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()
    batch := make([]models.LogEntry, 0, 1000)

    flush := func() {
        if len(batch) == 0 {
            return
        }
        // Create a copy to send to DB
        toInsert := make([]models.LogEntry, len(batch))
        copy(toInsert, batch)
        // Clear batch immediately
        batch = batch[:0]

        // Insert in background with short timeout
        go func(entries []models.LogEntry) {
            ctxTimeout, cancel := context.WithTimeout(context.Background(), 10*time.Second)
            defer cancel()
            if err := c.logRepo.BatchInsertLogs(ctxTimeout, entries); err != nil {
                log.Printf("Batch insert failed: %v", err)
                // Optionally, you could re-queue them, but for simplicity we drop.
            }
        }(toInsert)
    }

    for {
        select {
        case entry, ok := <-c.batchCh:
            if !ok {
                // Channel closed - flush remaining and exit
                flush()
                close(c.batchDone)
                return
            }
            batch = append(batch, entry)
            if len(batch) >= 1000 {
                flush()
                ticker.Reset(5 * time.Second)
            }
        case <-ticker.C:
            flush()
        case <-ctx.Done():
            // Context cancelled - flush remaining and exit
            flush()
            close(c.batchDone)
            return
        }
    }
}

// attachToContainer now sends logs to batchCh.
func (c *LogCollector) attachToContainer(parentCtx context.Context, containerID, containerName string) {
    ctx, cancel := context.WithCancel(parentCtx)

    c.mu.Lock()
    c.cancelFuncs[containerID] = cancel
    c.mu.Unlock()

    // Upsert container into DB
    if err := c.containerRepo.Upsert(ctx, containerID, containerName); err != nil {
        log.Printf("Failed to upsert container %s: %v", containerID, err)
    }

    // Get log stream (from service)
    logChan, _, err := c.dockerService.FollowContainerLogs(ctx, containerID, containerName)
    if err != nil {
        log.Printf("Failed to follow logs for %s: %v", containerName, err)
        return
    }

    go func() {
        for logJSON := range logChan {
            var entry struct {
                Container string `json:"container"`
                Message   string `json:"message"`
            }
            if err := json.Unmarshal([]byte(logJSON), &entry); err != nil {
                log.Printf("Invalid log message: %v", err)
                continue
            }
            // Create LogEntry model and send to batch channel
            logEntry := models.LogEntry{
                ContainerID: containerID,
                Message:     entry.Message,
                Stream:      "stdout", // can differentiate by stream if needed
                Timestamp:   time.Now(),
            }
            // Non-blocking send (with select to avoid deadlock on shutdown)
            select {
            case c.batchCh <- logEntry:
            case <-ctx.Done():
                return
            }
        }
        // Remove cancel func when stream ends
        c.mu.Lock()
        delete(c.cancelFuncs, containerID)
        c.mu.Unlock()
        log.Printf("Log stream ended for container %s", containerName)
    }()
}

func (c *LogCollector) attachToAllRunning(ctx context.Context) {
    containers, err := c.dockerService.GetAllRunningContainers(ctx)
    if err != nil {
        log.Printf("Failed to list containers: %v", err)
        return
    }
    for _, container := range containers {
        c.attachToContainer(ctx, container.ID, container.Name)
    }
}

func (c *LogCollector) watchEvents(ctx context.Context) {
    cli, err := docker.GetClient()
    if err != nil {
        log.Fatalf("Cannot create Docker client: %v", err)
    }

    result := cli.Events(ctx, client.EventsListOptions{})
    if err != nil {
        log.Printf("Failed to get events: %v", err)
        return
    }

    for {
        select {
        case event := <-result.Messages:
            if event.Type == events.ContainerEventType && event.Action == "start" {
                containerID := event.Actor.ID
                c.mu.RLock()
                _, exists := c.cancelFuncs[containerID]
                c.mu.RUnlock()
                if !exists {
                    name := event.Actor.Attributes["name"]
                    if name == "" {
                        // fallback to inspect
                        inspect, _ := cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
                        if inspect.Container.Name != "" {
                            name = inspect.Container.Name[1:]
                        }
                    }
                    if name != "" {
                        log.Printf("Detected new container: %s (%s)", name, containerID)
                        c.attachToContainer(ctx, containerID, name)
                    }
                }
            }
        case err := <-result.Err:
            if err != nil {
                log.Printf("Event error: %v", err)
                // optionally break and restart
                return
            }
        case <-ctx.Done():
            return
        }
    }
}

func containerNameFromEvent(event events.Message) string {
    if name, ok := event.Actor.Attributes["name"]; ok {
        return name
    }
    return ""
}

func (c *LogCollector) Shutdown(ctx context.Context) error {
    close(c.batchCh) // signal batch writer to stop
    select {
    case <-c.batchDone:
        return nil
    case <-ctx.Done():
        return ctx.Err()
    }
}
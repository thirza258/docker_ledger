package websocket

import (
    "context"
    "net/http"
    "sync"

    "github.com/gorilla/websocket"
    "github.com/thirzq/dockerledger/internal/services"
)



type MultiLogStreamHandler struct {
    service *services.ContainerService
}

func NewMultiLogStreamHandler(service *services.ContainerService) *MultiLogStreamHandler {
    return &MultiLogStreamHandler{service: service}
}

func (h *MultiLogStreamHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    conn, err := upgrader.Upgrade(w, r, nil)
    if err != nil {
        http.Error(w, "WebSocket upgrade failed", http.StatusInternalServerError)
        return
    }
    defer conn.Close()

    ctx, cancel := context.WithCancel(r.Context())
    defer cancel()

    // Get all running containers
    containers, err := h.service.GetAllRunningContainers(ctx)
    if err != nil {
        conn.WriteMessage(websocket.CloseMessage, []byte("Failed to list containers: "+err.Error()))
        return
    }

    var wg sync.WaitGroup
    merged := make(chan string, 200)

    // Start a follower for each container
    for _, c := range containers {
        logChan, cancelFunc, err := h.service.FollowContainerLogs(ctx, c.ID, c.Name)
        if err != nil {
            continue
        }
        defer cancelFunc()
        wg.Add(1)
        go func(ch <-chan string) {
            defer wg.Done()
            for msg := range ch {
                select {
                case merged <- msg:
                case <-ctx.Done():
                    return
                }
            }
        }(logChan)
    }

    // Close merged when all followers are done
    go func() {
        wg.Wait()
        close(merged)
    }()

    // Send each log message to the WebSocket client
    for msg := range merged {
        if err := conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
            break
        }
    }
}
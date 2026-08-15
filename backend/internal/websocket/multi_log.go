package websocket

import (
	"context"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/thirzq/dockerledger/internal/services"
	"github.com/thirzq/dockerledger/internal/telemetry"
)

type MultiLogStreamHandler struct {
	service *services.ContainerService
}

func NewMultiLogStreamHandler(service *services.ContainerService) *MultiLogStreamHandler {
	return &MultiLogStreamHandler{service: service}
}

func (h *MultiLogStreamHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log := telemetry.WithRequestID(r.Context())

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote an HTTP error response; writing another one
		// here only produces a "superfluous WriteHeader" warning.
		log.Error("ws multi-log upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Get all running containers
	containers, err := h.service.GetAllRunningContainers(ctx)
	if err != nil {
		log.Error("ws multi-log failed to list containers", "error", err)
		writeClose(conn, websocket.CloseInternalServerErr, "Failed to list containers")
		return
	}
	log.Info("ws multi-log connected", "containers", len(containers))

	var wg sync.WaitGroup
	merged := make(chan string, 200)

	// Start a follower for each container
	for _, c := range containers {
		logChan, cancelFunc, err := h.service.FollowContainerLogs(ctx, c.ID, c.Name)
		if err != nil {
			log.Warn("ws multi-log failed to follow container", "container", c.Name, "container_id", c.ID, "error", err)
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

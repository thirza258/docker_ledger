package websocket

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/thirzq/dockerledger/internal/services"
	"github.com/thirzq/dockerledger/internal/telemetry"
)

var upgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true }, // Allow all origins for development
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

type LogStreamHandler struct {
	service *services.ContainerService
}

func NewLogStreamHandler(service *services.ContainerService) *LogStreamHandler {
	return &LogStreamHandler{service: service}
}

// ServeHTTP upgrades to WebSocket and streams Docker logs.
func (h *LogStreamHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log := telemetry.WithRequestID(r.Context())
	log.Debug("ws request", "path", r.URL.Path)

	path := strings.TrimPrefix(r.URL.Path, "/containers/")
	parts := strings.Split(path, "/")
	log.Debug("ws trimmed path", "path", path)
	log.Debug("ws path parts", "parts", parts, "len", len(parts))

	if len(parts) != 3 || parts[1] != "logs" || parts[2] != "live" {
		log.Warn("ws invalid path format")
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	containerID := parts[0]
	if containerID == "" {
		log.Warn("ws missing container ID")
		http.Error(w, "Container ID required", http.StatusBadRequest)
		return
	}

	// Optional: tail query parameter
	tail := 100 // default
	if tailStr := r.URL.Query().Get("tail"); tailStr != "" {
		// parse integer
		if t, err := strconv.Atoi(tailStr); err == nil && t > 0 {
			tail = t
		}
	}
	log.Debug("ws upgrade request", "path", r.URL.Path)

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error("ws upgrade failed", "error", err)
		return
	}
	log.Info("ws upgrade success", "container_id", containerID)
	defer conn.Close()

	// Get log stream from Docker
	stream, err := h.service.StreamContainerLogs(r.Context(), containerID, tail)
	if err != nil {
		log.Error("ws stream not found", "container_id", containerID, "error", err)
		conn.WriteMessage(websocket.CloseMessage, []byte("Container not found or error"))
		return
	}
	defer stream.Close()

	// Buffer for reading chunks
	buf := make([]byte, 4096)
	for {
		n, err := stream.Read(buf)
		if n > 0 {

			decoded := decodeLogChunk(buf[:n])
			if decoded != "" {
				if err := conn.WriteMessage(websocket.TextMessage, []byte(decoded)); err != nil {
					return // client disconnected
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				conn.WriteMessage(websocket.CloseMessage, []byte("Log stream ended"))
			}
			log.Warn("ws stream error", "error", err)
			time.Sleep(500 * time.Millisecond)
			continue
		}
	}
}

func decodeLogChunk(data []byte) string {
	// If data has at least 8 bytes and starts with STREAM_TYPE byte,
	// we can try to parse header. But to keep it working, we'll simply
	// strip the 8-byte header if present.
	if len(data) < 8 {
		return string(data) // assume plain text
	}
	// Check if first byte is 0x01 (stdout) or 0x02 (stderr)
	if data[0] == 1 || data[0] == 2 {
		msgLen := int(data[7]) | int(data[6])<<8 | int(data[5])<<16 | int(data[4])<<24
		if 8+msgLen <= len(data) {
			return string(data[8 : 8+msgLen])
		}
	}
	return string(data)
}

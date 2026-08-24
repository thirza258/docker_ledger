package websocket

import (
	"encoding/binary"
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

	path := strings.TrimPrefix(r.URL.Path, "/containers/")
	parts := strings.Split(path, "/")
	// One line for the whole parse. This used to be four separate debug lines
	// per request, which buried everything else in the stream.
	log.Debug("ws request", "path", r.URL.Path, "parts", parts)

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
		writeClose(conn, websocket.CloseInternalServerErr, "Container not found or error")
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
		// Any read error terminates the stream. Looping here (the previous
		// behaviour) spun forever on EOF, holding the Docker stream and the
		// goroutine open and writing two log lines a second per connection.
		if err != nil {
			if err == io.EOF {
				log.Info("ws stream ended", "container_id", containerID)
				writeClose(conn, websocket.CloseNormalClosure, "Log stream ended")
			} else {
				log.Warn("ws stream error", "container_id", containerID, "error", err)
				writeClose(conn, websocket.CloseInternalServerErr, "Log stream error")
			}
			return
		}
	}
}

// writeClose sends a well-formed close frame. A CloseMessage whose payload is
// raw text is not a valid close frame — the first two bytes must be the status
// code — and browsers drop it.
func writeClose(conn *websocket.Conn, code int, reason string) {
	_ = conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason),
		time.Now().Add(time.Second),
	)
}

func decodeLogChunk(data []byte) string {
	// Docker multiplexes stdout and stderr into frames of an 8-byte header
	// (stream type, then a big-endian payload length) followed by the payload.
	// A single read routinely returns several frames, so every one of them has
	// to be decoded — decoding only the first silently dropped log lines from
	// the live view.
	var out strings.Builder
	for i := 0; i+8 <= len(data); {
		if data[i] != 1 && data[i] != 2 {
			// Not a framed stream (Docker sends raw text for TTY containers);
			// hand back whatever is left as-is.
			out.Write(data[i:])
			return out.String()
		}

		msgLen := int(binary.BigEndian.Uint32(data[i+4 : i+8]))
		i += 8
		if msgLen == 0 {
			continue // keepalive frame
		}
		if i+msgLen > len(data) {
			// Partial frame at the end of the chunk: emit what arrived rather
			// than dropping it.
			out.Write(data[i:])
			break
		}
		out.Write(data[i : i+msgLen])
		i += msgLen
	}

	if out.Len() == 0 {
		// Too short to be a frame at all — assume plain text.
		return string(data)
	}
	return out.String()
}

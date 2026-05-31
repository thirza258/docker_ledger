package handlers

import (
	"net/http"
	"strings"
	"fmt"
	"strconv"
    "time"
    "encoding/json"
)

func (h *ContainerHandler) GetContainerLogs(w http.ResponseWriter, r *http.Request) {
    // Extract container ID from path (e.g., /containers/abc123/logs)
    path := strings.TrimPrefix(r.URL.Path, "/containers/")
    parts := strings.Split(path, "/")
    if len(parts) != 2 || parts[1] != "logs" {
        http.Error(w, "Invalid request path", http.StatusBadRequest)
        return
    }
    containerID := parts[0]
    if containerID == "" {
        http.Error(w, "Container ID required", http.StatusBadRequest)
        return
    }

    // Parse tail query parameter
    tailStr := r.URL.Query().Get("tail")
    tail := 100 // default
    if tailStr != "" {
        parsed, err := strconv.Atoi(tailStr)
        if err == nil && parsed > 0 {
            tail = parsed
        } else if err == nil && parsed <= 0 {
            http.Error(w, "tail must be a positive integer", http.StatusBadRequest)
            return
        } else {
            http.Error(w, "invalid tail value", http.StatusBadRequest)
            return
        }
    }

    logs, err := h.service.GetContainerLogs(r.Context(), containerID, tail)
    if err != nil {
        // Handle container not found
        if strings.Contains(err.Error(), "No such container") || strings.Contains(err.Error(), "not found") {
            http.Error(w, "Container not found", http.StatusNotFound)
            return
        }
        http.Error(w, fmt.Sprintf("Failed to retrieve logs: %v", err), http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "text/plain; charset=utf-8")
    w.WriteHeader(http.StatusOK)
    w.Write([]byte(logs))
}

func (h *ContainerHandler) SearchLogs(w http.ResponseWriter, r *http.Request) {
    query := r.URL.Query().Get("q")
    if query == "" {
        http.Error(w, "Missing query parameter 'q'", http.StatusBadRequest)
        return
    }

    containerName := r.URL.Query().Get("container")
    limitStr := r.URL.Query().Get("limit")
    limit := 100
    if limitStr != "" {
        if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 1000 {
            limit = l
        }
    }

    // Parse time range
    var fromTime, toTime *time.Time
    if fromStr := r.URL.Query().Get("from"); fromStr != "" {
        t, err := time.Parse(time.RFC3339, fromStr)
        if err != nil {
            http.Error(w, "Invalid 'from' format, use RFC3339: 2006-01-02T15:04:05Z", http.StatusBadRequest)
            return
        }
        fromTime = &t
    }
    if toStr := r.URL.Query().Get("to"); toStr != "" {
        t, err := time.Parse(time.RFC3339, toStr)
        if err != nil {
            http.Error(w, "Invalid 'to' format, use RFC3339: 2006-01-02T15:04:05Z", http.StatusBadRequest)
            return
        }
        toTime = &t
    }

    logs, err := h.logRepo.SearchLogs(r.Context(), query, containerName, fromTime, toTime, limit)
    if err != nil {
        http.Error(w, "Search failed", http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    json.NewEncoder(w).Encode(logs)
}
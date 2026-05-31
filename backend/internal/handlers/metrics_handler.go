package handlers

import (
	"strings"
	"net/http"
	"encoding/json"
)

func (h *ContainerHandler) GetContainerStats(w http.ResponseWriter, r *http.Request) {
   path := strings.TrimPrefix(r.URL.Path, "/containers/")
    parts := strings.Split(path, "/")
    if len(parts) != 2 || parts[1] != "stats" {
        http.Error(w, "Invalid request path", http.StatusBadRequest)
        return
    }
    containerID := parts[0]
    if containerID == "" {
        http.Error(w, "Container ID required", http.StatusBadRequest)
        return
    }

    stats, err := h.service.GetContainerStats(r.Context(), containerID)
    if err != nil {
        // handle not found, etc.
        http.Error(w, "Failed to retrieve stats", http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(stats)
}
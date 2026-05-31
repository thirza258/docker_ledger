package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/thirzq/dockerledger/internal/models"
	"github.com/thirzq/dockerledger/internal/services"
	"github.com/thirzq/dockerledger/internal/storage"
)

type ContainerHandler struct {
    service  *services.ContainerService
    logRepo  *storage.LogRepository 
}

func NewContainerHandler(service *services.ContainerService, logRepo *storage.LogRepository) *ContainerHandler {
    return &ContainerHandler{
        service: service,
        logRepo: logRepo,
    }
}
func (h *ContainerHandler) DockerHealthCheck(w http.ResponseWriter, r *http.Request) {
	connected := h.service.IsDockerConnected(r.Context())
	resp := models.DockerHealthResponse{Connected: connected}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

func (h *ContainerHandler) ListContainers(w http.ResponseWriter, r *http.Request) {
	containers, err := h.service.ListContainers(r.Context())
	if err != nil {
		http.Error(w, "Failed to list containers", http.StatusInternalServerError)
		return
	}

	response := models.DockerListAllContainer{Containers: containers}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

func (h *ContainerHandler) GetContainer(w http.ResponseWriter, r *http.Request) {
    // Extract container ID from URL path: /containers/{id}
    id := strings.TrimPrefix(r.URL.Path, "/containers/")
    if id == "" {
        http.Error(w, "Container ID required", http.StatusBadRequest)
        return
    }

    containerData, err := h.service.GetContainerByID(r.Context(), id)
    if err != nil {
        http.Error(w, "Failed to get container details", http.StatusInternalServerError)
		return
    }

    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    json.NewEncoder(w).Encode(containerData)
}
// Future handlers: ListContainers, etc.
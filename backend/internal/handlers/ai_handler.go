package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/thirzq/dockerledger/internal/services"
	"github.com/thirzq/dockerledger/internal/telemetry"
)

type AISummaryHandler struct {
	aiService *services.AISummaryService
}

func NewAISummaryHandler(aiService *services.AISummaryService) *AISummaryHandler {
	return &AISummaryHandler{aiService: aiService}
}

func (h *AISummaryHandler) GenerateSummary(w http.ResponseWriter, r *http.Request) {
	hoursBack, _ := strconv.Atoi(r.URL.Query().Get("hours_back"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	req := services.SummaryRequest{
		HoursBack: hoursBack,
		Limit:     limit,
	}

	summary, err := h.aiService.GenerateSummary(r.Context(), req)
	if err != nil {
		telemetry.WithRequestID(r.Context()).Error("failed to generate AI summary", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(summary)
}

func (h *AISummaryHandler) GenerateContainerSummary(w http.ResponseWriter, r *http.Request) {
	containerName := r.URL.Query().Get("container_name")
	if containerName == "" {
		http.Error(w, "missing container_name query parameter", http.StatusBadRequest)
		return
	}

	hoursBack, _ := strconv.Atoi(r.URL.Query().Get("hours_back"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	req := services.SummaryRequest{
		HoursBack: hoursBack,
		Limit:     limit,
	}

	summary, err := h.aiService.GenerateContainerSummary(r.Context(), containerName, req)
	if err != nil {
		telemetry.WithRequestID(r.Context()).Error("failed to generate container AI summary", "container_name", containerName, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(summary)
}

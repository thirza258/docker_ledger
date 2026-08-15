package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/thirzq/dockerledger/internal/services"
	"github.com/thirzq/dockerledger/internal/telemetry"
)

// summaryTimeout bounds a summary request end to end. The server itself has no
// WriteTimeout (it also serves hijacked WebSocket streams), so slow paths carry
// their own deadline. It sits above the 30s OpenRouter client timeout so the
// upstream error surfaces instead of a bare cancellation.
const summaryTimeout = 45 * time.Second

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

	ctx, cancel := context.WithTimeout(r.Context(), summaryTimeout)
	defer cancel()

	summary, err := h.aiService.GenerateSummary(ctx, req)
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

	ctx, cancel := context.WithTimeout(r.Context(), summaryTimeout)
	defer cancel()

	summary, err := h.aiService.GenerateContainerSummary(ctx, containerName, req)
	if err != nil {
		telemetry.WithRequestID(r.Context()).Error("failed to generate container AI summary", "container_name", containerName, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(summary)
}

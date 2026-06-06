package handlers

import (
    "encoding/json"
    "net/http"
    "strconv"

    "github.com/thirzq/dockerledger/internal/services"
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
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(summary)
}
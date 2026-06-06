package services

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "net/http"
	"strings"
    "time"

    "github.com/thirzq/dockerledger/internal/config"
    "github.com/thirzq/dockerledger/internal/models"
    "github.com/thirzq/dockerledger/internal/storage"
)

type AISummaryService struct {
    cfg      *config.Config
    logRepo  *storage.LogRepository
    httpClient *http.Client
}

func NewAISummaryService(cfg *config.Config, logRepo *storage.LogRepository) *AISummaryService {
    return &AISummaryService{
        cfg:      cfg,
        logRepo:  logRepo,
        httpClient: &http.Client{Timeout: 30 * time.Second},
    }
}

// SummaryRequest defines parameters for log analysis.
type SummaryRequest struct {
    HoursBack int    `json:"hours_back"` // analyze logs from last N hours
    Limit     int    `json:"limit"`      // max number of log lines to send
}

// SummaryResponse is the structured output.
type SummaryResponse struct {
    TopErrors          []string `json:"top_errors"`
    MostFailingContainers []string `json:"most_failing_containers"`
    SuggestedCauses    []string `json:"suggested_causes"`
    RawResponse        string   `json:"raw_response,omitempty"`
}

// GenerateSummary fetches logs, calls OpenRouter, parses answer.
func (s *AISummaryService) GenerateSummary(ctx context.Context, req SummaryRequest) (*SummaryResponse, error) {
    // Defaults
    if req.HoursBack == 0 {
        req.HoursBack = 24
    }
    if req.Limit == 0 || req.Limit > 5000 {
        req.Limit = 2000
    }

    // Fetch logs from DB (last N hours, error logs only? We'll fetch all but emphasize errors)
    since := time.Now().Add(-time.Duration(req.HoursBack) * time.Hour)
    logs, err := s.logRepo.GetLogsSince(ctx, since, req.Limit)
    if err != nil {
        return nil, fmt.Errorf("failed to fetch logs: %w", err)
    }

    if len(logs) == 0 {
        return &SummaryResponse{
            TopErrors:          []string{"No logs in the specified time range."},
            MostFailingContainers: []string{},
            SuggestedCauses:    []string{},
        }, nil
    }

    // Build prompt
    prompt := buildPrompt(logs)

    // Call OpenRouter
    raw, err := s.callOpenRouter(ctx, prompt)
    if err != nil {
        return nil, err
    }

    // Parse AI response into structured format
    summary := parseAIResponse(raw)
    summary.RawResponse = raw
    return summary, nil
}

func buildPrompt(logs []models.LogEntry) string {
    // Limit to reasonable size (e.g., first 1000 lines)
    if len(logs) > 1000 {
        logs = logs[:1000]
    }

    prompt := `You are a DevOps assistant. Analyze the following Docker container logs and provide:
1. Top 3 error messages (most frequent or severe)
2. The containers that are failing most often
3. Suggested causes for the failures

Be concise. Return the answer as JSON with keys: "top_errors" (array), "most_failing_containers" (array), "suggested_causes" (array).

Logs (format: [container_name] message):
`
    for _, l := range logs {
        prompt += fmt.Sprintf("[%s] %s\n", l.ContainerName, l.Message)
    }
    return prompt
}

// callOpenRouter sends request to OpenRouter API.
func (s *AISummaryService) callOpenRouter(ctx context.Context, prompt string) (string, error) {
    apiKey := s.cfg.OpenRouterAPIKey
    if apiKey == "" {
        return "", fmt.Errorf("OPENROUTER_API_KEY not set")
    }

    reqBody := map[string]interface{}{
        "model": s.cfg.OpenRouterModel,
        "messages": []map[string]string{
            {
                "role":    "user",
                "content": prompt,
            },
        },
        "reasoning": map[string]bool{"enabled": true},
    }
    jsonBody, _ := json.Marshal(reqBody)

    req, err := http.NewRequestWithContext(ctx, "POST", "https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(jsonBody))
    if err != nil {
        return "", err
    }
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("Authorization", "Bearer "+apiKey)

    resp, err := s.httpClient.Do(req)
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return "", fmt.Errorf("OpenRouter API error: %s", resp.Status)
    }

    var result struct {
        Choices []struct {
            Message struct {
                Content string `json:"content"`
            } `json:"message"`
        } `json:"choices"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return "", err
    }
    if len(result.Choices) == 0 {
        return "", fmt.Errorf("no choices in response")
    }
    return result.Choices[0].Message.Content, nil
}

// parseAIResponse attempts to extract JSON from AI text.
func parseAIResponse(raw string) *SummaryResponse {
    // Try to find JSON object in the response
    start := strings.Index(raw, "{")
    end := strings.LastIndex(raw, "}")
    if start != -1 && end != -1 && end > start {
        jsonStr := raw[start : end+1]
        var resp SummaryResponse
        if err := json.Unmarshal([]byte(jsonStr), &resp); err == nil {
            return &resp
        }
    }
    // Fallback: put entire text into SuggestedCauses
    return &SummaryResponse{
        TopErrors:          []string{"Could not parse AI output"},
        MostFailingContainers: []string{},
        SuggestedCauses:    []string{raw},
    }
}
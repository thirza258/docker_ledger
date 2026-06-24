package services

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "net/http"
	"strings"
    "sort"
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

type SummaryRequest struct {
    HoursBack int    `json:"hours_back"` // analyze logs from last N hours
    Limit     int    `json:"limit"`      // max number of log lines to send
}

type SummaryResponse struct {
    TopErrors          []string `json:"top_errors"`
    MostFailingContainers []string `json:"most_failing_containers"`
    SuggestedCauses    []string `json:"suggested_causes"`
    RawResponse        string   `json:"raw_response,omitempty"`
}

type LogSummary struct {
    Container string
    Message   string
    Count     int
    Severity  int 
}

const (
    maxLogSectionChars = 8000 
)

// GenerateSummary fetches logs, calls OpenRouter, parses answer.
func (s *AISummaryService) GenerateSummary(ctx context.Context, req SummaryRequest) (*SummaryResponse, error) {
    // Defaults
    if req.HoursBack == 0 {
        req.HoursBack = 24
    }
    if req.Limit == 0 || req.Limit > 5000 {
        req.Limit = 2000
    }

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
    // Limit to reasonable size (e.g., first 100 lines)
    logs = prepareLogsForPrompt(logs)

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

func prepareLogsForPrompt(logs []models.LogEntry) []models.LogEntry {
    if len(logs) == 0 {
        return logs
    }

    summaryMap := make(map[string]*LogSummary)
    for _, log := range logs {
        key := fmt.Sprintf("%s|%s", log.ContainerName, log.Message)
        if _, exists := summaryMap[key]; !exists {
            severity := 0
            lowerMsg := strings.ToLower(log.Message)
            if strings.Contains(lowerMsg, "fatal") || strings.Contains(lowerMsg, "panic") {
                severity = 4
            } else if strings.Contains(lowerMsg, "error") || strings.Contains(lowerMsg, "exception") || strings.Contains(lowerMsg, "failed")  {
                severity = 3
            } else if strings.Contains(lowerMsg, "warn") {
                severity = 2
            } else {
                severity = 1
            }
            summaryMap[key] = &LogSummary{
                Container: log.ContainerName,
                Message:   log.Message, 
                Count:     0,
                Severity:  severity,
            }
        }
        summaryMap[key].Count++

    }
    
    summaries := make([]*LogSummary, 0, len(summaryMap))
    for _, summary := range summaryMap {
        summaries = append(summaries, summary)
    }

    sort.Slice(summaries, func(i, j int) bool {
        if summaries[i].Severity != summaries[j].Severity {
            return summaries[i].Severity > summaries[j].Severity
        }
        return summaries[i].Count > summaries[j].Count
    })

    var result []models.LogEntry
    totalChars := 0

    for _, summary := range summaries {
        var line string
        if summary.Count > 1 {
            line = fmt.Sprintf("[%s] %s (repeated %d times)\n", summary.Container, summary.Message, summary.Count)
        } else {
            line = fmt.Sprintf("[%s] %s\n", summary.Container, summary.Message)
        }

        lineLen := len(line)

        if totalChars+lineLen > maxLogSectionChars {
            if len(result) == 0 && summary.Severity >= 3 {
                result = append(result, models.LogEntry{ContainerName: summary.Container, Message: summary.Message})
                break
            }
            break
        }
        result = append(result, models.LogEntry{ContainerName: summary.Container, Message: summary.Message})
        totalChars += lineLen
    }   

    if len(summaries) > len(result) {
        result = append(result, models.LogEntry{
            ContainerName: "SYSTEM",
            Message:       fmt.Sprintf("(Truncated: %d additional low-severity or duplicate log groups omitted to save context)", len(summaries)-len(result)),
        })
    }

    return result
}

func (s *AISummaryService) GenerateContainerSummary(ctx context.Context, containerName string, req SummaryRequest) (*SummaryResponse, error) {
    // Defaults
    if req.HoursBack == 0 {
        req.HoursBack = 24
    }
    if req.Limit == 0 || req.Limit > 5000 {
        req.Limit = 2000
    }

    since := time.Now().Add(-time.Duration(req.HoursBack) * time.Hour)

    // Fetch logs only for the given container
    logs, err := s.logRepo.GetLogsByContainerSince(ctx, containerName, since, req.Limit)
    if err != nil {
        return nil, fmt.Errorf("failed to fetch logs for container %s: %w", containerName, err)
    }

    if len(logs) == 0 {
        return &SummaryResponse{
            TopErrors:          []string{fmt.Sprintf("No logs for container '%s' in the last %d hours.", containerName, req.HoursBack)},
            MostFailingContainers: []string{containerName},
            SuggestedCauses:    []string{},
        }, nil
    }

    // Build prompt that focuses on this container
    prompt := buildContainerPrompt(containerName, logs)

    raw, err := s.callOpenRouter(ctx, prompt)
    if err != nil {
        return nil, err
    }

    summary := parseAIResponse(raw)
    summary.RawResponse = raw
    return summary, nil
}

// buildContainerPrompt creates a tailored prompt for a single container.
func buildContainerPrompt(containerName string, logs []models.LogEntry) string {
    if len(logs) > 100 {
        logs = logs[:100]
    }

    prompt := fmt.Sprintf(`You are a DevOps assistant. Analyze the following Docker logs for container "%s". Provide:
1. Top 3 error messages (most frequent or severe)
2. Suggested causes for these failures (be specific to this container)
3. Any patterns or recurring warnings

Be concise. Return the answer as JSON with keys: "top_errors" (array), "suggested_causes" (array), "patterns" (array).

Logs:
`, containerName)

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
        // "reasoning": map[string]bool{"enabled": true},
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
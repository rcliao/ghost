package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ── E2E Benchmark: Ghost retrieval + LLM answering ────────────────
//
// Replicates how shell uses Ghost:
//   1. Ingest sessions into Ghost
//   2. Retrieve relevant memories via Search()
//   3. Format as "[Relevant memories]..." prefix (like shell's InjectContext)
//   4. Call LLM with context + question
//   5. Score answer against ground truth
//
// Three modes compared:
//   - no-memory: LLM answers with no context (baseline)
//   - ghost: LLM answers with Ghost-retrieved memories
//   - oracle: LLM answers with ground-truth evidence sessions (upper bound)

// LLMClient abstracts the LLM call for testability.
type LLMClient interface {
	Generate(ctx context.Context, systemPrompt, userMessage string) (string, error)
}

// E2EConfig controls the end-to-end benchmark.
type E2EConfig struct {
	DatasetPath  string
	Limit        int
	PerTypeLimit int
	TopK         int    // number of memories to retrieve (default 5)
	NS           string
	LLM          LLMClient
	Modes        []string // subset of: "no-memory", "ghost", "oracle"
	ProgressFunc func(done, total int)
}

// E2EResult holds results for one question across modes.
type E2EResult struct {
	QuestionID   string             `json:"question_id"`
	QuestionType string             `json:"question_type"`
	Question     string             `json:"question"`
	GoldAnswer   string             `json:"gold_answer"`
	Answers      map[string]string  `json:"answers"`
	TokenF1      map[string]float64 `json:"token_f1"`
}

// E2EReport holds aggregate results.
type E2EReport struct {
	Timestamp time.Time                     `json:"timestamp"`
	Dataset   string                        `json:"dataset"`
	Total     int                           `json:"total"`
	ByType    map[string]*E2ETypeAgg        `json:"by_type"`
	Overall   map[string]map[string]float64 `json:"overall"` // mode → metric → value
	Results   []E2EResult                   `json:"results,omitempty"`
}

// E2ETypeAgg aggregates per-type.
type E2ETypeAgg struct {
	Count   int                           `json:"count"`
	Metrics map[string]map[string]float64 `json:"metrics"` // mode → metric → value
}

// tokenF1 computes token-level F1 between prediction and reference.
func tokenF1(prediction, reference string) float64 {
	predTokens := strings.Fields(strings.ToLower(prediction))
	refTokens := strings.Fields(strings.ToLower(reference))
	if len(predTokens) == 0 || len(refTokens) == 0 {
		return 0
	}
	refSet := make(map[string]int)
	for _, t := range refTokens {
		refSet[t]++
	}
	common := 0
	for _, t := range predTokens {
		if refSet[t] > 0 {
			common++
			refSet[t]--
		}
	}
	if common == 0 {
		return 0
	}
	precision := float64(common) / float64(len(predTokens))
	recall := float64(common) / float64(len(refTokens))
	return 2 * precision * recall / (precision + recall)
}

// formatMemoryContext formats retrieved memories like shell's InjectContext.
func formatMemoryContext(contents []string) string {
	if len(contents) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("[Relevant memories from previous conversations]\n")
	for _, c := range contents {
		if len(c) > 500 {
			c = c[:500] + "..."
		}
		sb.WriteString("- ")
		sb.WriteString(c)
		sb.WriteString("\n")
	}
	sb.WriteString("[End of memories]\n\n")
	return sb.String()
}

const e2eSystemPrompt = `You are a helpful assistant with access to memories from previous conversations. Answer the user's question based on the provided memories. If the memories don't contain enough information, say you don't know. Be concise — answer in 1-2 sentences only.`

// RunE2ELongMemEval runs the end-to-end benchmark on LongMemEval.
func RunE2ELongMemEval(cfg E2EConfig, newStore func() (*SQLiteStore, func(), error)) (*E2EReport, error) {
	entries, err := LoadLongMemEval(cfg.DatasetPath)
	if err != nil {
		return nil, err
	}

	if cfg.PerTypeLimit > 0 {
		typeCounts := make(map[string]int)
		var sampled []LongMemEvalEntry
		for _, e := range entries {
			if typeCounts[e.QuestionType] < cfg.PerTypeLimit {
				sampled = append(sampled, e)
				typeCounts[e.QuestionType]++
			}
		}
		entries = sampled
	}
	if cfg.Limit > 0 && cfg.Limit < len(entries) {
		entries = entries[:cfg.Limit]
	}
	if cfg.NS == "" {
		cfg.NS = "bench:e2e"
	}
	if cfg.TopK == 0 {
		cfg.TopK = 5
	}
	if len(cfg.Modes) == 0 {
		cfg.Modes = []string{"no-memory", "ghost", "oracle"}
	}

	report := &E2EReport{
		Timestamp: time.Now(),
		Dataset:   filepath.Base(cfg.DatasetPath),
		ByType:    make(map[string]*E2ETypeAgg),
		Overall:   make(map[string]map[string]float64),
	}
	for _, mode := range cfg.Modes {
		report.Overall[mode] = make(map[string]float64)
	}

	ctx := context.Background()
	evalTotal := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.QuestionID, "_abs") {
			evalTotal++
		}
	}
	evalDone := 0

	for i, entry := range entries {
		if strings.HasSuffix(entry.QuestionID, "_abs") {
			continue
		}

		sessionContents := make(map[string]string)
		store, cleanup, err := newStore()
		if err != nil {
			return nil, fmt.Errorf("create store q%d: %w", i, err)
		}

		for j, session := range entry.HaystackSessions {
			sessionID := fmt.Sprintf("session-%d", j)
			if j < len(entry.HaystackIDs) {
				sessionID = entry.HaystackIDs[j]
			}
			content := sessionContent(session)
			if content == "" {
				continue
			}
			sessionContents[sessionID] = content

			var sessionTime time.Time
			if j < len(entry.HaystackDates) && entry.HaystackDates[j] != "" {
				t, _ := time.Parse("2006-01-02 15:04:05", entry.HaystackDates[j])
				sessionTime = t
			}
			store.BenchInsert(ctx, cfg.NS, sessionID, content, sessionTime)
		}

		result := E2EResult{
			QuestionID:   entry.QuestionID,
			QuestionType: entry.QuestionType,
			Question:     entry.Question,
			GoldAnswer:   entry.Answer,
			Answers:      make(map[string]string),
			TokenF1:      make(map[string]float64),
		}

		for _, mode := range cfg.Modes {
			var userMsg string
			switch mode {
			case "no-memory":
				userMsg = entry.Question
			case "ghost":
				results, _ := store.Search(ctx, SearchParams{
					NS: cfg.NS, Query: entry.Question,
					Limit: cfg.TopK, IncludeAll: true,
				})
				var contents []string
				for _, r := range results {
					contents = append(contents, r.Content)
				}
				userMsg = formatMemoryContext(contents) + entry.Question
			case "oracle":
				var contents []string
				for _, sid := range entry.AnswerSessionIDs {
					if c, ok := sessionContents[sid]; ok {
						contents = append(contents, c)
					}
				}
				userMsg = formatMemoryContext(contents) + entry.Question
			}

			answer, err := cfg.LLM.Generate(ctx, e2eSystemPrompt, userMsg)
			if err != nil {
				cleanup()
				return nil, fmt.Errorf("llm %s q%d: %w", mode, i, err)
			}

			result.Answers[mode] = answer
			result.TokenF1[mode] = tokenF1(answer, entry.Answer)
		}

		cleanup()
		report.Results = append(report.Results, result)

		// Aggregate
		qt := entry.QuestionType
		if _, ok := report.ByType[qt]; !ok {
			report.ByType[qt] = &E2ETypeAgg{
				Metrics: make(map[string]map[string]float64),
			}
			for _, mode := range cfg.Modes {
				report.ByType[qt].Metrics[mode] = make(map[string]float64)
			}
		}
		report.ByType[qt].Count++
		report.Total++

		for _, mode := range cfg.Modes {
			report.ByType[qt].Metrics[mode]["token_f1"] += result.TokenF1[mode]
			report.Overall[mode]["token_f1"] += result.TokenF1[mode]
		}

		evalDone++
		if cfg.ProgressFunc != nil && (evalDone%5 == 0 || evalDone == evalTotal) {
			cfg.ProgressFunc(evalDone, evalTotal)
		}
	}

	// Average
	if report.Total > 0 {
		for _, mode := range cfg.Modes {
			for metric := range report.Overall[mode] {
				report.Overall[mode][metric] /= float64(report.Total)
			}
		}
	}
	for _, agg := range report.ByType {
		if agg.Count > 0 {
			for _, mode := range cfg.Modes {
				for metric := range agg.Metrics[mode] {
					agg.Metrics[mode][metric] /= float64(agg.Count)
				}
			}
		}
	}

	return report, nil
}

// ── Anthropic HTTP Client (no SDK dependency) ─────────────────────

// AnthropicClient calls the Anthropic Messages API via HTTP.
type AnthropicClient struct {
	APIKey string
	Model  string
	client *http.Client
}

// NewAnthropicClient creates a client. Defaults to Haiku for cost efficiency.
func NewAnthropicClient(model string) *AnthropicClient {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if model == "" {
		model = "claude-haiku-4-5-20251001"
	}
	return &AnthropicClient{
		APIKey: apiKey,
		Model:  model,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

type anthropicReqMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicReq struct {
	Model     string            `json:"model"`
	MaxTokens int               `json:"max_tokens"`
	System    string            `json:"system,omitempty"`
	Messages  []anthropicReqMsg `json:"messages"`
}

type anthropicResp struct {
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (c *AnthropicClient) Generate(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	if c.APIKey == "" {
		return "", fmt.Errorf("ANTHROPIC_API_KEY not set")
	}

	reqBody, _ := json.Marshal(anthropicReq{
		Model:     c.Model,
		MaxTokens: 256,
		System:    systemPrompt,
		Messages:  []anthropicReqMsg{{Role: "user", Content: userMessage}},
	})

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("anthropic request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("anthropic %d: %s", resp.StatusCode, string(body))
	}

	var result anthropicResp
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}
	if result.Error != nil {
		return "", fmt.Errorf("anthropic error: %s", result.Error.Message)
	}
	if len(result.Content) == 0 {
		return "", fmt.Errorf("empty response")
	}
	return result.Content[0].Text, nil
}

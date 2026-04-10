package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/rcliao/ghost/internal/model"
)

// ── E2E Benchmark: Ghost retrieval + LLM answering ────────────────
//
// Three modes compared:
//   - no-memory: LLM answers with no context (baseline)
//   - ghost: LLM answers with Ghost-retrieved memories (best config)
//   - oracle: LLM answers with ground-truth evidence sessions (upper bound)

// LLMClient abstracts the LLM call for testability.
type LLMClient interface {
	Generate(ctx context.Context, systemPrompt, userMessage string) (string, error)
	Name() string
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
	Scores       map[string]float64 `json:"scores"` // combined score per mode
}

// E2EReport holds aggregate results.
type E2EReport struct {
	Timestamp time.Time                     `json:"timestamp"`
	Dataset   string                        `json:"dataset"`
	LLM       string                        `json:"llm"`
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

// ── Smarter Scoring (#2) ──────────────────────────────────────────

// scoreAnswer computes multiple objective metrics between LLM answer and gold.
// Returns a map of metric name → score.
func scoreAnswer(answer, gold string) map[string]float64 {
	scores := make(map[string]float64)

	ansLower := normalize(answer)
	goldLower := normalize(gold)

	// 1. Exact containment: does the answer contain the gold answer?
	scores["contains"] = 0
	if strings.Contains(ansLower, goldLower) {
		scores["contains"] = 1
	}

	// 2. Flexible containment: check each gold "answer phrase"
	// Handle gold answers like "3" or "45 minutes each way"
	scores["flexible_contains"] = flexibleContains(ansLower, goldLower)

	// 3. Token recall: what fraction of gold tokens appear in the answer?
	scores["token_recall"] = tokenRecall(ansLower, goldLower)

	// 4. Token F1
	scores["token_f1"] = tokenF1(answer, gold)

	// 5. Combined score: take the max of multiple signals.
	// Different question types are best measured by different metrics:
	// - Short fact answers ("Target", "3"): flexible_contains is best
	// - Long rubric answers: token_recall captures partial credit
	// - General: token F1 balances precision and recall
	contains := scores["flexible_contains"]
	recall := scores["token_recall"]
	f1 := scores["token_f1"]

	// Use the best signal as the primary score
	best := contains
	if recall > best {
		best = recall
	}
	if f1 > best {
		best = f1
	}
	scores["score"] = best

	return scores
}

// normalize cleans text for comparison: lowercase, strip punctuation, collapse whitespace.
func normalize(s string) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) {
			return r
		}
		return ' '
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// flexibleContains checks if the answer contains the gold answer or key parts of it.
// Handles numeric answers ("3"), short phrases ("Target"), multi-part answers,
// and long rubric-style gold answers.
func flexibleContains(ansNorm, goldNorm string) float64 {
	// Direct containment
	if strings.Contains(ansNorm, goldNorm) {
		return 1.0
	}

	// Check for numeric match (e.g., gold="3", answer contains "3" or "three")
	numWords := map[string]string{
		"0": "zero", "1": "one", "2": "two", "3": "three", "4": "four",
		"5": "five", "6": "six", "7": "seven", "8": "eight", "9": "nine",
		"10": "ten", "11": "eleven", "12": "twelve",
	}
	for num, word := range numWords {
		if goldNorm == num || goldNorm == word {
			if strings.Contains(ansNorm, num) || strings.Contains(ansNorm, word) {
				return 1.0
			}
		}
	}

	// Short gold answer (1-3 words): check word-level containment
	goldWords := strings.Fields(goldNorm)
	if len(goldWords) <= 3 {
		found := 0
		for _, w := range goldWords {
			if len(w) >= 1 && strings.Contains(ansNorm, w) {
				found++
			}
		}
		if len(goldWords) > 0 {
			return float64(found) / float64(len(goldWords))
		}
	}

	// Long gold answer (rubric-style, >10 words): extract key phrases and check.
	// For gold answers like "The user would prefer resources tailored to Adobe Premiere Pro",
	// check if key non-stopword phrases from gold appear in the answer.
	if len(goldWords) > 10 {
		// Extract significant words from gold (non-stopwords, length >= 4)
		var keyTerms []string
		for _, w := range goldWords {
			if len(w) >= 4 && !isStopWord(w) {
				keyTerms = append(keyTerms, w)
			}
		}
		if len(keyTerms) > 0 {
			found := 0
			for _, kt := range keyTerms {
				if strings.Contains(ansNorm, kt) {
					found++
				}
			}
			return float64(found) / float64(len(keyTerms))
		}
	}

	return 0
}

// tokenRecall computes what fraction of gold answer tokens appear in the prediction.
func tokenRecall(predNorm, goldNorm string) float64 {
	goldTokens := strings.Fields(goldNorm)
	if len(goldTokens) == 0 {
		return 0
	}
	predSet := make(map[string]bool)
	for _, t := range strings.Fields(predNorm) {
		predSet[t] = true
	}
	hits := 0
	for _, t := range goldTokens {
		if len(t) >= 2 && predSet[t] {
			hits++
		}
	}
	return float64(hits) / float64(len(goldTokens))
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

// ── Optimized Prompt Formatting (#5) ───────────────────────────────

// formatMemoryForLLM formats retrieved memories for LLM consumption.
// Highlights the most query-relevant sentences to help the LLM find answers
// in long conversation transcripts.
func formatMemoryForLLM(query string, memories []SearchResult, maxTotal int) string {
	if len(memories) == 0 {
		return ""
	}
	if maxTotal <= 0 {
		maxTotal = 30000
	}

	queryTerms := extractQueryTerms(query)

	var sb strings.Builder
	sb.WriteString("[Memories from previous conversations]\n\n")

	budget := maxTotal
	for i, m := range memories {
		if budget <= 100 {
			break
		}

		content := m.Content

		// If content fits in budget, highlight relevant lines
		if len(content) <= budget-100 {
			content = highlightRelevantLines(content, queryTerms)
		} else {
			// Excerpt the most relevant part
			content = extractRelevantExcerpt(query, content, budget-100)
			content = highlightRelevantLines(content, queryTerms)
		}

		header := fmt.Sprintf("Memory %d", i+1)
		if m.CreatedAt.Year() > 2000 {
			header += fmt.Sprintf(" (%s)", m.CreatedAt.Format("2006-01-02"))
		}
		entry := fmt.Sprintf("### %s\n%s\n\n", header, content)

		sb.WriteString(entry)
		budget -= len(entry)
	}

	sb.WriteString("[End of memories]\n\n")
	return sb.String()
}

// highlightRelevantLines marks lines that contain query-relevant terms with >>> prefix.
// This draws the LLM's attention to the most important parts of long sessions.
func highlightRelevantLines(content string, queryTerms []string) string {
	if len(queryTerms) == 0 {
		return content
	}

	lines := strings.Split(content, "\n")
	var result []string
	for _, line := range lines {
		lineLower := strings.ToLower(line)
		hits := 0
		for _, qt := range queryTerms {
			if strings.Contains(lineLower, qt) {
				hits++
			}
		}
		if hits >= 1 && len(strings.TrimSpace(line)) > 10 {
			result = append(result, ">>> "+line)
		} else {
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}

// extractRelevantExcerpt finds the most query-relevant passage in content.
// Returns up to maxLen chars centered on the best-matching paragraph.
func extractRelevantExcerpt(query, content string, maxLen int) string {
	if len(content) <= maxLen {
		return content
	}

	// Split into paragraphs (double newline or speaker turns)
	paragraphs := splitParagraphs(content)
	if len(paragraphs) == 0 {
		return content[:maxLen]
	}

	// Score each paragraph by query term overlap
	queryTerms := extractQueryTerms(query)
	bestIdx := 0
	bestScore := -1.0
	for i, p := range paragraphs {
		score := 0.0
		pLower := strings.ToLower(p)
		for _, qt := range queryTerms {
			if strings.Contains(pLower, qt) {
				score += 1.0
			}
		}
		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}

	// Build excerpt centered on best paragraph, expanding to fill maxLen
	var parts []string
	totalLen := 0
	parts = append(parts, paragraphs[bestIdx])
	totalLen += len(paragraphs[bestIdx])

	// Expand outward from best paragraph
	lo, hi := bestIdx-1, bestIdx+1
	for totalLen < maxLen && (lo >= 0 || hi < len(paragraphs)) {
		if hi < len(paragraphs) && totalLen+len(paragraphs[hi]) <= maxLen {
			parts = append(parts, paragraphs[hi])
			totalLen += len(paragraphs[hi])
			hi++
		} else if lo >= 0 && totalLen+len(paragraphs[lo]) <= maxLen {
			parts = append([]string{paragraphs[lo]}, parts...)
			totalLen += len(paragraphs[lo])
			lo--
		} else {
			break
		}
	}

	return strings.Join(parts, "\n")
}

func splitParagraphs(text string) []string {
	// Split on double newlines or speaker turn boundaries (Name: ...)
	lines := strings.Split(text, "\n")
	var result []string
	var current []string

	flush := func() {
		if len(current) > 0 {
			p := strings.TrimSpace(strings.Join(current, "\n"))
			if len(p) > 20 {
				result = append(result, p)
			}
			current = nil
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flush()
			continue
		}
		// Split on speaker turns (e.g., "user: " or "Caroline: ")
		if idx := strings.Index(trimmed, ": "); idx > 0 && idx < 25 {
			prefix := trimmed[:idx]
			if !strings.Contains(prefix, " ") || len(strings.Fields(prefix)) <= 2 {
				flush()
			}
		}
		current = append(current, line)
	}
	flush()
	return result
}

func extractQueryTerms(query string) []string {
	var terms []string
	for _, w := range strings.Fields(strings.ToLower(query)) {
		w = strings.Trim(w, "?.,!\"'")
		if len(w) >= 3 && !isStopWord(w) {
			terms = append(terms, w)
		}
	}
	return terms
}

var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "are": true, "was": true,
	"were": true, "been": true, "have": true, "has": true, "had": true,
	"does": true, "did": true, "will": true, "would": true, "could": true,
	"should": true, "can": true, "may": true, "might": true, "what": true,
	"which": true, "who": true, "whom": true, "how": true, "when": true,
	"where": true, "why": true, "that": true, "this": true, "with": true,
	"from": true, "into": true, "about": true, "you": true, "your": true,
	"our": true, "their": true, "his": true, "her": true, "its": true,
}

func isStopWord(w string) bool { return stopWords[w] }

// ── System Prompt (#5) ────────────────────────────────────────────

const e2eSystemPrompt = `You are a personal assistant answering questions from a user's conversation history. Memories from previous conversations are provided below.

Lines marked with >>> are the most relevant to the question — pay extra attention to them and their surrounding context.

CRITICAL INSTRUCTIONS:
1. Read ALL provided memories carefully — the answer is almost always in there
2. Lines marked >>> are highlights — the answer is often in or near these lines
3. Look for SPECIFIC details: names, numbers, places, dates, even if mentioned briefly or indirectly
4. Give a DIRECT answer — just the fact, name, number, or place
5. Keep answers SHORT — one sentence maximum
6. Do NOT say "I don't have that information" unless you have genuinely read every memory and the answer is truly absent
7. Do NOT ask follow-up questions
8. If the answer is a number, respond with just the number
9. If you're unsure between options, pick the most likely one based on context`

// ── E2E Runner ────────────────────────────────────────────────────

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
		cfg.TopK = 10
	}
	if len(cfg.Modes) == 0 {
		cfg.Modes = []string{"no-memory", "ghost", "oracle"}
	}

	report := &E2EReport{
		Timestamp: time.Now(),
		Dataset:   filepath.Base(cfg.DatasetPath),
		LLM:       cfg.LLM.Name(),
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
			Scores:       make(map[string]float64),
		}

		for _, mode := range cfg.Modes {
			var userMsg string
			switch mode {
			case "no-memory":
				userMsg = entry.Question
			case "ghost":
				// Use best config: retrieve top candidates, show top-3 in full
				results, _ := store.Search(ctx, SearchParams{
					NS: cfg.NS, Query: entry.Question,
					Limit: cfg.TopK, IncludeAll: true,
				})
				// Show fewer memories but with more content each
				showN := 3
				if showN > len(results) {
					showN = len(results)
				}
				userMsg = formatMemoryForLLM(entry.Question, results[:showN], 30000) + entry.Question
			case "oracle":
				var oracleResults []SearchResult
				for _, sid := range entry.AnswerSessionIDs {
					if c, ok := sessionContents[sid]; ok {
						oracleResults = append(oracleResults, SearchResult{
							Memory: model.Memory{Content: c},
						})
					}
				}
				userMsg = formatMemoryForLLM(entry.Question, oracleResults, 30000) + entry.Question
			}

			answer, err := cfg.LLM.Generate(ctx, e2eSystemPrompt, userMsg)
			if err != nil {
				cleanup()
				return nil, fmt.Errorf("llm %s q%d: %w", mode, i, err)
			}

			result.Answers[mode] = answer
			answerScores := scoreAnswer(answer, entry.Answer)
			result.Scores[mode] = answerScores["score"]

			// Aggregate all metrics
			qt := entry.QuestionType
			if _, ok := report.ByType[qt]; !ok {
				report.ByType[qt] = &E2ETypeAgg{
					Metrics: make(map[string]map[string]float64),
				}
				for _, m := range cfg.Modes {
					report.ByType[qt].Metrics[m] = make(map[string]float64)
				}
			}
			for metric, val := range answerScores {
				report.ByType[qt].Metrics[mode][metric] += val
				report.Overall[mode][metric] += val
			}
		}

		cleanup()
		report.Results = append(report.Results, result)

		qt := entry.QuestionType
		report.ByType[qt].Count++
		report.Total++

		evalDone++
		if cfg.ProgressFunc != nil && (evalDone%5 == 0 || evalDone == evalTotal) {
			cfg.ProgressFunc(evalDone, evalTotal)
		}
	}

	// Average all metrics
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

// ── LLM Client implementations ────────────────────────────────────

// ClaudeCLIClient calls `claude -p` for LLM generation.
type ClaudeCLIClient struct {
	Model string
}

func NewClaudeCLIClient(model string) *ClaudeCLIClient {
	return &ClaudeCLIClient{Model: model}
}

func (c *ClaudeCLIClient) Name() string {
	if c.Model != "" {
		return "claude-cli:" + c.Model
	}
	return "claude-cli"
}

func (c *ClaudeCLIClient) Generate(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	args := []string{"-p", userMessage, "--system-prompt", systemPrompt}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("claude -p failed: %s", string(exitErr.Stderr))
		}
		return "", fmt.Errorf("claude -p: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// AnthropicClient calls the Anthropic Messages API via HTTP.
type AnthropicClient struct {
	APIKey string
	Model  string
	client *http.Client
}

func NewAnthropicClient(model string) *AnthropicClient {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if model == "" {
		model = "claude-haiku-4-5-20251001"
	}
	return &AnthropicClient{
		APIKey: apiKey, Model: model,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *AnthropicClient) Name() string { return "api:" + c.Model }

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
		Model: c.Model, MaxTokens: 256,
		System:   systemPrompt,
		Messages: []anthropicReqMsg{{Role: "user", Content: userMessage}},
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
		return "", fmt.Errorf("anthropic: %s", result.Error.Message)
	}
	if len(result.Content) == 0 {
		return "", fmt.Errorf("empty response")
	}
	return result.Content[0].Text, nil
}

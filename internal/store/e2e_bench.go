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
	Modes        []string // subset of: "no-memory", "ghost", "ghost-hyde", "ghost-rewrite", "oracle"
	LLMJudge     bool     // if true, use LLM-as-judge scoring (correct=1, partial=0.5, wrong=0)
	Judge        LLMClient // judge LLM (defaults to LLM if unset)
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

// estimateTokensFromChars approximates tokens as chars/4 (standard rule of thumb).
func estimateTokensFromChars(s string) int {
	return (len(s) + 3) / 4
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

// ── LLM-as-judge scorer ───────────────────────────────────────────
//
// Alternative to token-F1 scoring: an LLM judges whether the prediction
// is correct/partial/wrong given the gold answer. Scoring rule from
// LoCoMo-Plus: correct=1.0, partial=0.5, wrong=0.0.
//
// Opt-in via cfg.LLMJudge = true. Uses the same LLM client as generation
// but with a deterministic, evaluation-focused system prompt.

const llmJudgeSystemPrompt = `You are an evaluator judging whether a prediction answers a question correctly, given the gold answer. Reply with ONE word only:
- "correct" if the prediction fully contains the gold answer or its equivalent meaning
- "partial" if the prediction contains some but not all of the gold information, or is close but imprecise
- "wrong" if the prediction is incorrect, refuses to answer, or omits the gold fact entirely

Output ONLY one word: correct, partial, or wrong.`

// cognitiveJudgeSystemPrompt judges whether an LLM's response demonstrates
// awareness of the latent constraint revealed by an earlier cue dialogue
// (LoCoMo-Plus cognitive memory evaluation).
const cognitiveJudgeSystemPrompt = `You are evaluating whether an assistant's response reflects awareness of a latent constraint the user expressed earlier in their conversation history.

You will see:
- A CUE: An earlier exchange revealing the user's value, goal, state, or causal link
- A TRIGGER: A later message from the user that seems to conflict with or build on the cue
- A RESPONSE: The assistant's response to the trigger

Classify the response as:
- "correct" if it explicitly connects to the cue, references the earlier context, or shows memory awareness
- "partial" if it addresses the trigger appropriately but doesn't reference the cue
- "wrong" if it ignores both the cue and the user's situation, or gives a generic answer unrelated to context

Output ONE word: correct, partial, or wrong.`

// cognitiveJudge scores whether a response reflects awareness of the cue.
func cognitiveJudge(ctx context.Context, judge LLMClient, cue, trigger, response string) float64 {
	msg := fmt.Sprintf("CUE:\n%s\n\nTRIGGER:\n%s\n\nRESPONSE:\n%s\n\nLabel:", cue, trigger, response)
	out, err := judge.Generate(ctx, cognitiveJudgeSystemPrompt, msg)
	if err != nil {
		return -1
	}
	out = strings.ToLower(strings.TrimSpace(out))
	switch {
	case strings.HasPrefix(out, "correct"):
		return 1.0
	case strings.HasPrefix(out, "partial"):
		return 0.5
	case strings.HasPrefix(out, "wrong"):
		return 0.0
	}
	if strings.Contains(out, "correct") {
		return 1.0
	}
	if strings.Contains(out, "partial") {
		return 0.5
	}
	if strings.Contains(out, "wrong") {
		return 0.0
	}
	return -1
}

// llmJudgeScore asks the LLM to classify prediction vs gold as correct/partial/wrong.
// Returns 1.0 / 0.5 / 0.0 respectively. On error, returns -1 (caller falls back).
func llmJudgeScore(ctx context.Context, llm LLMClient, question, prediction, gold string) float64 {
	msg := fmt.Sprintf("Question: %s\n\nGold answer: %s\n\nPrediction: %s\n\nLabel:", question, gold, prediction)
	out, err := llm.Generate(ctx, llmJudgeSystemPrompt, msg)
	if err != nil {
		return -1
	}
	out = strings.ToLower(strings.TrimSpace(out))
	// Be lenient with punctuation/leading words
	switch {
	case strings.HasPrefix(out, "correct"):
		return 1.0
	case strings.HasPrefix(out, "partial"):
		return 0.5
	case strings.HasPrefix(out, "wrong"):
		return 0.0
	}
	// Scan for keyword anywhere
	if strings.Contains(out, "correct") {
		return 1.0
	}
	if strings.Contains(out, "partial") {
		return 0.5
	}
	if strings.Contains(out, "wrong") {
		return 0.0
	}
	return -1
}

// ── LLM-assisted retrieval helpers ───────────────────────────────
//
// These helpers use an LLM *outside* Ghost to transform queries before
// searching. Ghost itself stays LLM-free — it's the caller's orchestration
// layer that optionally invokes the LLM. This lets us benchmark integration
// patterns (HyDE, query rewriting) without coupling Ghost to any LLM.

const hydeSystemPrompt = `You are a helpful assistant generating a hypothetical response to a user's question. The response will be used as a search query to retrieve relevant memories. Write a 2-3 sentence direct answer as if you already knew the answer. Use specific details, names, and concepts. Do NOT say "I don't know" — invent plausible specifics. Output ONLY the hypothetical answer, no preamble.`

const rewriteSystemPrompt = `You are a helpful assistant rewriting a user's question to maximize retrieval from a memory system. Expand the query with synonyms, related concepts, and likely topics the answer would discuss. Keep important names and entities. Output a single line of 20-40 words. No preamble, no bullet points.`

// hydeQuery calls the LLM to generate a hypothetical answer for retrieval.
// Falls back to the original query on error.
func hydeQuery(ctx context.Context, llm LLMClient, query string) string {
	out, err := llm.Generate(ctx, hydeSystemPrompt, query)
	if err != nil || len(strings.TrimSpace(out)) < 10 {
		return query
	}
	// Combine original query with hypothetical for best of both worlds
	return query + " " + strings.TrimSpace(out)
}

// rewriteQuery calls the LLM to expand the query with related terms.
func rewriteQuery(ctx context.Context, llm LLMClient, query string) string {
	out, err := llm.Generate(ctx, rewriteSystemPrompt, query)
	if err != nil || len(strings.TrimSpace(out)) < 10 {
		return query
	}
	return strings.TrimSpace(out)
}

// compressSystemPrompt asks the LLM to extract the minimal set of facts
// from retrieved memories that are relevant to the query. Outputs a compact
// query-focused summary for the answering call to consume.
//
// Purpose: raw session chunks contain much off-topic conversation. Compressing
// to query-relevant facts reduces context tokens and focuses the answerer.
const compressSystemPrompt = `You are a helpful assistant extracting the most relevant facts from conversation memories for a specific question.

You will see:
- A user's QUESTION
- Several conversation MEMORIES retrieved from past sessions

Extract every fact that could plausibly inform the question, including:
- Stated preferences, values, or constraints the user has voiced
- Prior decisions, commitments, or goals the user has declared
- Emotional reactions or patterns (e.g., "felt stressed when...", "learned to say no")
- Recent life states (job, location, health, schedule) that shape current choices

**Preserve distinct situations separately.** If multiple memories describe different incidents (e.g., a landlord dispute AND a work-boundaries conflict), keep each as its own bullet with concrete anchors (who/what/when). Do not blend them into a single abstract theme — the answering model will pick the right one.

Preserve specific details: names, dates, places, numbers, exact phrasing of preferences. Omit unrelated chit-chat.

Output format: a compact bulleted summary (1-8 bullets, 10-30 words each). Each bullet should begin with a concrete anchor (e.g., "Landlord was unresponsive → started apartment hunt" not "Advocated for self in housing"). If no memory is relevant, output "No relevant facts found."

Do NOT answer the question yourself — just extract facts the memories contain.`

// compressContext asks the LLM to extract query-relevant facts from memories.
// Returns compressed text, or the original on LLM error.
func compressContext(ctx context.Context, llm LLMClient, query string, memories []SearchResult) string {
	if len(memories) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("QUESTION: ")
	sb.WriteString(query)
	sb.WriteString("\n\nMEMORIES:\n")
	for i, m := range memories {
		content := m.Content
		if len(content) > 3000 {
			content = content[:3000] + "..."
		}
		fmt.Fprintf(&sb, "\n### Memory %d\n%s\n", i+1, content)
	}

	compressed, err := llm.Generate(ctx, compressSystemPrompt, sb.String())
	if err != nil || len(strings.TrimSpace(compressed)) < 10 {
		// Fallback to default formatting
		return formatMemoryForLLM(query, memories, 30000)
	}
	// Wrap in the expected format
	return "[Memories from previous conversations — compressed]\n\n" + strings.TrimSpace(compressed) + "\n\n[End of memories]\n\n"
}

// agentSearchSystemPrompt asks the LLM to inspect retrieval results and decide
// whether to search again with a refined query, or stop. Enables iterative
// multi-hop retrieval where each round builds on what was found.
const agentSearchSystemPrompt = `You are a search agent inspecting retrieved memories and deciding whether more retrieval is needed.

Given:
- The user's question
- Memories retrieved so far (possibly incomplete)

Output ONE of these two lines:
1. "STOP" if the memories contain enough information to answer the question
2. "SEARCH: <refined query>" if more memories are needed (refined query should use different terms/concepts than the original)

Do not explain. Output exactly one line.`

// agentSearch runs up to maxRounds of iterative retrieval with LLM-driven
// query refinement. Returns the combined deduplicated result set.
//
// Each round: LLM sees retrieved-so-far and decides STOP or refines the query.
// Ghost's Search stays LLM-free — LLM only guides the orchestrator.
func agentSearch(ctx context.Context, store *SQLiteStore, llm LLMClient, ns, initialQuery string, topK, maxRounds int) []SearchResult {
	if maxRounds <= 0 {
		maxRounds = 3
	}
	seen := make(map[string]bool)
	var combined []SearchResult
	query := initialQuery

	for round := 0; round < maxRounds; round++ {
		results, err := store.Search(ctx, SearchParams{
			NS: ns, Query: query, Limit: topK, IncludeAll: true,
		})
		if err != nil {
			break
		}
		// Merge results deduplicated by key
		for _, r := range results {
			if !seen[r.Key] {
				seen[r.Key] = true
				combined = append(combined, r)
			}
		}

		// Last round: no need to ask LLM if we should stop
		if round == maxRounds-1 {
			break
		}

		// Build prompt summarizing retrieval state
		var sb strings.Builder
		sb.WriteString("Question: ")
		sb.WriteString(initialQuery)
		sb.WriteString("\n\nMemories retrieved so far:\n")
		showN := 3
		if showN > len(combined) {
			showN = len(combined)
		}
		for i := 0; i < showN; i++ {
			snippet := combined[i].Content
			if len(snippet) > 300 {
				snippet = snippet[:300] + "..."
			}
			fmt.Fprintf(&sb, "[%d] %s\n", i+1, snippet)
		}
		decision, err := llm.Generate(ctx, agentSearchSystemPrompt, sb.String())
		if err != nil {
			break
		}
		decision = strings.TrimSpace(decision)
		if strings.HasPrefix(strings.ToUpper(decision), "STOP") {
			break
		}
		if strings.HasPrefix(strings.ToUpper(decision), "SEARCH:") {
			refined := strings.TrimSpace(decision[len("SEARCH:"):])
			if len(refined) < 5 {
				break
			}
			query = refined
			continue
		}
		// If LLM output doesn't match format, stop
		break
	}

	return combined
}

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

		var batchSessions []BenchSession
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
			batchSessions = append(batchSessions, BenchSession{
				Key: sessionID, Content: content, CreatedAt: sessionTime,
			})
		}
		store.BatchBenchInsert(ctx, cfg.NS, batchSessions)

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
				// Use best config: retrieve candidates, show top-5 with highlights
				results, _ := store.Search(ctx, SearchParams{
					NS: cfg.NS, Query: entry.Question,
					Limit: cfg.TopK, IncludeAll: true,
				})
				showN := 5
				if showN > len(results) {
					showN = len(results)
				}
				userMsg = formatMemoryForLLM(entry.Question, results[:showN], 50000) + entry.Question
			case "ghost-hyde":
				// LLM generates hypothetical answer, Ghost searches with it
				searchQuery := hydeQuery(ctx, cfg.LLM, entry.Question)
				results, _ := store.Search(ctx, SearchParams{
					NS: cfg.NS, Query: searchQuery,
					Limit: cfg.TopK, IncludeAll: true,
				})
				showN := 5
				if showN > len(results) {
					showN = len(results)
				}
				userMsg = formatMemoryForLLM(entry.Question, results[:showN], 50000) + entry.Question
			case "ghost-rewrite":
				// LLM rewrites query with synonyms/concepts, Ghost searches
				searchQuery := rewriteQuery(ctx, cfg.LLM, entry.Question)
				results, _ := store.Search(ctx, SearchParams{
					NS: cfg.NS, Query: searchQuery,
					Limit: cfg.TopK, IncludeAll: true,
				})
				showN := 5
				if showN > len(results) {
					showN = len(results)
				}
				userMsg = formatMemoryForLLM(entry.Question, results[:showN], 50000) + entry.Question
			case "ghost-agent":
				// Iterative LLM-driven retrieval (up to 3 rounds)
				results := agentSearch(ctx, store, cfg.LLM, cfg.NS, entry.Question, cfg.TopK, 3)
				showN := 5
				if showN > len(results) {
					showN = len(results)
				}
				userMsg = formatMemoryForLLM(entry.Question, results[:showN], 50000) + entry.Question
			case "ghost-compress":
				// Ghost retrieves → LLM compresses to query-focused summary → LLM answers
				results, _ := store.Search(ctx, SearchParams{
					NS: cfg.NS, Query: entry.Question,
					Limit: cfg.TopK, IncludeAll: true,
				})
				showN := 5
				if showN > len(results) {
					showN = len(results)
				}
				userMsg = compressContext(ctx, cfg.LLM, entry.Question, results[:showN]) + entry.Question
			case "ghost-compress-wide":
				// Retrieve wider (top-15) → compress noise out → answer
				wideLimit := cfg.TopK * 3
				if wideLimit < 15 {
					wideLimit = 15
				}
				results, _ := store.Search(ctx, SearchParams{
					NS: cfg.NS, Query: entry.Question,
					Limit: wideLimit, IncludeAll: true,
				})
				userMsg = compressContext(ctx, cfg.LLM, entry.Question, results) + entry.Question
			case "oracle":
				var oracleResults []SearchResult
				for _, sid := range entry.AnswerSessionIDs {
					if c, ok := sessionContents[sid]; ok {
						oracleResults = append(oracleResults, SearchResult{
							Memory: model.Memory{Content: c},
						})
					}
				}
				userMsg = formatMemoryForLLM(entry.Question, oracleResults, 50000) + entry.Question
			}

			answerStart := time.Now()
			answer, err := cfg.LLM.Generate(ctx, e2eSystemPrompt, userMsg)
			answerLatency := time.Since(answerStart).Seconds()
			if err != nil {
				result.Answers[mode] = fmt.Sprintf("[ERROR: %v]", err)
				result.Scores[mode] = 0
				continue
			}

			result.Answers[mode] = answer
			answerScores := scoreAnswer(answer, entry.Answer)
			// Cost-quality-latency instrumentation
			answerScores["input_tokens"] = float64(estimateTokensFromChars(userMsg) + estimateTokensFromChars(e2eSystemPrompt))
			answerScores["output_tokens"] = float64(estimateTokensFromChars(answer))
			answerScores["latency_sec"] = answerLatency
			if cfg.LLMJudge {
				judge := cfg.Judge
				if judge == nil {
					judge = cfg.LLM
				}
				if js := llmJudgeScore(ctx, judge, entry.Question, answer, entry.Answer); js >= 0 {
					answerScores["llm_judge"] = js
					answerScores["score"] = js // judge takes precedence
				}
			}
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

// ── LoCoMo E2E Runner ────────────────────────────────────────────

// RunE2ELoCoMo runs the end-to-end benchmark on LoCoMo.
// Same three modes as LongMemEval: no-memory, ghost, oracle.
func RunE2ELoCoMo(cfg E2EConfig, newStore func() (*SQLiteStore, func(), error)) (*E2EReport, error) {
	entries, err := LoadLoCoMo(cfg.DatasetPath)
	if err != nil {
		return nil, err
	}

	if cfg.NS == "" {
		cfg.NS = "bench:e2e-locomo"
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

	// Count evaluable QA pairs
	evalTotal := 0
	catCounts := make(map[int]int)
	for _, entry := range entries {
		for _, qa := range entry.QA {
			if qa.Category == 5 { // skip adversarial
				continue
			}
			if cfg.PerTypeLimit > 0 {
				cat := qa.Category
				if catCounts[cat] >= cfg.PerTypeLimit {
					continue
				}
				catCounts[cat]++
			}
			evalTotal++
		}
	}
	// Reset counts for actual eval
	catCounts = make(map[int]int)
	evalDone := 0

	for _, entry := range entries {
		sessions := parseLoCoMoSessions(entry.Conversation)
		if len(sessions) == 0 {
			continue
		}

		// Build session content map for oracle mode
		sessionContents := make(map[string]string)
		store, cleanup, err := newStore()
		if err != nil {
			return nil, fmt.Errorf("create store for %s: %w", entry.SampleID, err)
		}

		var batchSessions []BenchSession
		for _, sess := range sessions {
			sessionContents[sess.key] = sess.content
			batchSessions = append(batchSessions, BenchSession{
				Key: sess.key, Content: sess.content,
			})
		}
		store.BatchBenchInsert(ctx, cfg.NS, batchSessions)

		for _, qa := range entry.QA {
			if qa.Category == 5 {
				continue
			}
			if cfg.PerTypeLimit > 0 && catCounts[qa.Category] >= cfg.PerTypeLimit {
				continue
			}

			catName := categoryName(qa.Category)
			result := E2EResult{
				QuestionID:   fmt.Sprintf("%s_cat%d", entry.SampleID, qa.Category),
				QuestionType: catName,
				Question:     qa.Question,
				GoldAnswer:   qa.Answer,
				Answers:      make(map[string]string),
				Scores:       make(map[string]float64),
			}

			for _, mode := range cfg.Modes {
				var userMsg string
				switch mode {
				case "no-memory":
					userMsg = qa.Question
				case "ghost":
					results, _ := store.Search(ctx, SearchParams{
						NS: cfg.NS, Query: qa.Question,
						Limit: cfg.TopK, IncludeAll: true,
					})
					showN := 5
					if showN > len(results) {
						showN = len(results)
					}
					userMsg = formatMemoryForLLM(qa.Question, results[:showN], 50000) + qa.Question
				case "ghost-hyde":
					searchQuery := hydeQuery(ctx, cfg.LLM, qa.Question)
					results, _ := store.Search(ctx, SearchParams{
						NS: cfg.NS, Query: searchQuery,
						Limit: cfg.TopK, IncludeAll: true,
					})
					showN := 5
					if showN > len(results) {
						showN = len(results)
					}
					userMsg = formatMemoryForLLM(qa.Question, results[:showN], 50000) + qa.Question
				case "ghost-rewrite":
					searchQuery := rewriteQuery(ctx, cfg.LLM, qa.Question)
					results, _ := store.Search(ctx, SearchParams{
						NS: cfg.NS, Query: searchQuery,
						Limit: cfg.TopK, IncludeAll: true,
					})
					showN := 5
					if showN > len(results) {
						showN = len(results)
					}
					userMsg = formatMemoryForLLM(qa.Question, results[:showN], 50000) + qa.Question
				case "ghost-agent":
					results := agentSearch(ctx, store, cfg.LLM, cfg.NS, qa.Question, cfg.TopK, 3)
					showN := 5
					if showN > len(results) {
						showN = len(results)
					}
					userMsg = formatMemoryForLLM(qa.Question, results[:showN], 50000) + qa.Question
				case "ghost-compress":
					results, _ := store.Search(ctx, SearchParams{
						NS: cfg.NS, Query: qa.Question,
						Limit: cfg.TopK, IncludeAll: true,
					})
					showN := 5
					if showN > len(results) {
						showN = len(results)
					}
					userMsg = compressContext(ctx, cfg.LLM, qa.Question, results[:showN]) + qa.Question
				case "ghost-compress-wide":
					wideLimit := cfg.TopK * 3
					if wideLimit < 15 {
						wideLimit = 15
					}
					results, _ := store.Search(ctx, SearchParams{
						NS: cfg.NS, Query: qa.Question,
						Limit: wideLimit, IncludeAll: true,
					})
					userMsg = compressContext(ctx, cfg.LLM, qa.Question, results) + qa.Question
				case "oracle":
					evidenceSessions := evidenceToSessions(qa.Evidence)
					var oracleResults []SearchResult
					for _, sid := range evidenceSessions {
						if c, ok := sessionContents[sid]; ok {
							oracleResults = append(oracleResults, SearchResult{
								Memory: model.Memory{Content: c},
							})
						}
					}
					userMsg = formatMemoryForLLM(qa.Question, oracleResults, 50000) + qa.Question
				}

				answerStart := time.Now()
				answer, err := cfg.LLM.Generate(ctx, e2eSystemPrompt, userMsg)
				answerLatency := time.Since(answerStart).Seconds()
				if err != nil {
					result.Answers[mode] = fmt.Sprintf("[ERROR: %v]", err)
					result.Scores[mode] = 0
					continue
				}

				result.Answers[mode] = answer
				answerScores := scoreAnswer(answer, qa.Answer)
				answerScores["input_tokens"] = float64(estimateTokensFromChars(userMsg) + estimateTokensFromChars(e2eSystemPrompt))
				answerScores["output_tokens"] = float64(estimateTokensFromChars(answer))
				answerScores["latency_sec"] = answerLatency
				if cfg.LLMJudge {
					judge := cfg.Judge
					if judge == nil {
						judge = cfg.LLM
					}
					if js := llmJudgeScore(ctx, judge, qa.Question, answer, qa.Answer); js >= 0 {
						answerScores["llm_judge"] = js
						answerScores["score"] = js
					}
				}
				result.Scores[mode] = answerScores["score"]

				// Aggregate metrics
				if _, ok := report.ByType[catName]; !ok {
					report.ByType[catName] = &E2ETypeAgg{
						Metrics: make(map[string]map[string]float64),
					}
					for _, m := range cfg.Modes {
						report.ByType[catName].Metrics[m] = make(map[string]float64)
					}
				}
				for metric, val := range answerScores {
					report.ByType[catName].Metrics[mode][metric] += val
					report.Overall[mode][metric] += val
				}
			}

			report.Results = append(report.Results, result)
			report.ByType[catName].Count++
			catCounts[qa.Category]++
			report.Total++
			evalDone++

			if cfg.ProgressFunc != nil && (evalDone%5 == 0 || evalDone == evalTotal) {
				cfg.ProgressFunc(evalDone, evalTotal)
			}
		}

		cleanup()
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

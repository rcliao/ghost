package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rcliao/ghost/internal/embedding"
)

// ── LongMemEval dataset types ──────────────────────────────────────

// LongMemEvalEntry represents one evaluation instance from the LongMemEval dataset.
// Each entry contains a question, expected answer, timestamped chat sessions (haystack),
// and ground-truth session IDs that contain evidence for the answer.
type LongMemEvalEntry struct {
	QuestionID       string           `json:"question_id"`
	QuestionType     string           `json:"question_type"`
	Question         string           `json:"question"`
	RawAnswer        json.RawMessage  `json:"answer"`
	Answer           string           `json:"-"` // populated after unmarshal
	QuestionDate     string           `json:"question_date"`
	HaystackIDs      []string         `json:"haystack_session_ids"`
	HaystackDates    []string         `json:"haystack_dates"`
	HaystackSessions [][]LMETurn      `json:"haystack_sessions"`
	AnswerSessionIDs []string         `json:"answer_session_ids"`
}

// LMETurn represents a single turn in a chat session.
type LMETurn struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	HasAnswer bool   `json:"has_answer,omitempty"`
}

// ── Benchmark runner ───────────────────────────────────────────────

// LongMemEvalConfig controls the benchmark run.
type LongMemEvalConfig struct {
	DatasetPath    string // path to the JSON file
	Limit          int    // max questions to evaluate (0 = all)
	PerTypeLimit   int    // max questions per type for stratified sampling (0 = no limit)
	TopK           []int  // K values for metrics (default: [5, 10])
	NS             string // namespace for memories (default: "bench:longmemeval")
	EmbedCachePath string // path to embedding cache file (speeds up repeated runs)
	ProgressFunc   func(done, total int) // optional progress callback
}

// LongMemEvalResult holds results for one question.
type LongMemEvalResult struct {
	QuestionID   string             `json:"question_id"`
	QuestionType string             `json:"question_type"`
	NumSessions  int                `json:"num_sessions"`
	NumEvidence  int                `json:"num_evidence"`
	Retrieved    []string           `json:"retrieved"`
	Relevant     []string           `json:"relevant"`
	Metrics      map[string]float64 `json:"metrics"`
}

// LongMemEvalReport holds the aggregate benchmark results.
type LongMemEvalReport struct {
	Timestamp   time.Time                       `json:"timestamp"`
	Dataset     string                          `json:"dataset"`
	Total       int                             `json:"total"`
	ByType      map[string]*LongMemEvalTypeAgg  `json:"by_type"`
	Overall     map[string]float64              `json:"overall"`
	Results     []LongMemEvalResult             `json:"results"`
}

// LongMemEvalTypeAgg aggregates metrics for a question type.
type LongMemEvalTypeAgg struct {
	Count   int                `json:"count"`
	Metrics map[string]float64 `json:"metrics"` // mean of per-question metrics
}

// LoadLongMemEval reads the dataset JSON from the given path.
func LoadLongMemEval(path string) ([]LongMemEvalEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read dataset: %w", err)
	}
	var entries []LongMemEvalEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse dataset: %w", err)
	}
	// Normalize answer field (can be string or number in the dataset)
	for i := range entries {
		if len(entries[i].RawAnswer) > 0 {
			var s string
			if err := json.Unmarshal(entries[i].RawAnswer, &s); err == nil {
				entries[i].Answer = s
			} else {
				// Fallback: use raw JSON representation (handles numbers, etc.)
				entries[i].Answer = strings.Trim(string(entries[i].RawAnswer), "\"")
			}
		}
	}
	return entries, nil
}

// sessionContent concatenates all turns in a session into a single string.
func sessionContent(turns []LMETurn) string {
	var sb strings.Builder
	for _, t := range turns {
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(t.Role)
		sb.WriteString(": ")
		sb.WriteString(t.Content)
	}
	return sb.String()
}

// BuildEmbedCache pre-computes embeddings for all unique session texts in the dataset
// and saves them to a cache file. This is a one-time cost that makes subsequent
// benchmark runs much faster (cache hits skip ONNX inference entirely).
func BuildEmbedCache(datasetPath, cachePath string, embedder embedding.Embedder, progressFunc func(done, total int)) error {
	entries, err := LoadLongMemEval(datasetPath)
	if err != nil {
		return err
	}

	// Collect unique session texts and question texts.
	// Sessions are embedded as whole texts (not chunked) for benchmark speed.
	unique := make(map[string]string) // content hash → text
	addText := func(text string) {
		h := embedding.ContentHash(text)
		if _, ok := unique[h]; !ok {
			unique[h] = text
		}
	}

	for _, entry := range entries {
		for _, session := range entry.HaystackSessions {
			content := sessionContent(session)
			if content != "" {
				addText(content)
			}
		}
		if entry.Question != "" {
			addText(entry.Question)
		}
	}

	cached := embedding.NewCachedEmbedder(embedder, cachePath)
	total := len(unique)
	done := 0

	// Embed in batches of 32 for efficiency
	const batchSize = 32
	texts := make([]string, 0, batchSize)
	for _, text := range unique {
		texts = append(texts, text)
		if len(texts) >= batchSize {
			if _, err := cached.EmbedBatch(context.Background(), texts); err != nil {
				return fmt.Errorf("embed batch: %w", err)
			}
			done += len(texts)
			if progressFunc != nil {
				progressFunc(done, total)
			}
			texts = texts[:0]
		}
	}
	// Final batch
	if len(texts) > 0 {
		if _, err := cached.EmbedBatch(context.Background(), texts); err != nil {
			return fmt.Errorf("embed final batch: %w", err)
		}
		done += len(texts)
		if progressFunc != nil {
			progressFunc(done, total)
		}
	}

	return cached.Save()
}

// RunLongMemEval executes the benchmark and returns the report.
// For each question, it creates a fresh store, ingests all haystack sessions,
// runs a search query, and measures retrieval metrics against ground truth.
func RunLongMemEval(cfg LongMemEvalConfig, newStore func() (*SQLiteStore, func(), error)) (*LongMemEvalReport, error) {
	entries, err := LoadLongMemEval(cfg.DatasetPath)
	if err != nil {
		return nil, err
	}

	// Apply stratified sampling if PerTypeLimit is set
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
		cfg.NS = "bench:longmemeval"
	}
	if len(cfg.TopK) == 0 {
		cfg.TopK = []int{5, 10}
	}

	report := &LongMemEvalReport{
		Timestamp: time.Now(),
		Dataset:   filepath.Base(cfg.DatasetPath),
		Total:     len(entries),
		ByType:    make(map[string]*LongMemEvalTypeAgg),
		Overall:   make(map[string]float64),
		Results:   make([]LongMemEvalResult, 0, len(entries)),
	}

	ctx := context.Background()
	maxK := 0
	for _, k := range cfg.TopK {
		if k > maxK {
			maxK = k
		}
	}

	// Count non-abstention questions for progress reporting
	evalTotal := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.QuestionID, "_abs") {
			evalTotal++
		}
	}
	evalDone := 0

	for i, entry := range entries {
		// Skip abstention questions for retrieval eval — they have no evidence sessions
		if strings.HasSuffix(entry.QuestionID, "_abs") {
			continue
		}

		store, cleanup, err := newStore()
		if err != nil {
			return nil, fmt.Errorf("create store for q%d: %w", i, err)
		}

		// Ingest haystack sessions using fast bulk insert (single chunk per session,
		// no chunking, no auto-linking — optimized for retrieval benchmarking)
		for j, session := range entry.HaystackSessions {
			sessionID := fmt.Sprintf("session-%d", j)
			if j < len(entry.HaystackIDs) {
				sessionID = entry.HaystackIDs[j]
			}

			content := sessionContent(session)
			if content == "" {
				continue
			}

			// Parse session date for temporal-aware retrieval
			var sessionTime time.Time
			if j < len(entry.HaystackDates) && entry.HaystackDates[j] != "" {
				if t, err := time.Parse("2006-01-02 15:04:05", entry.HaystackDates[j]); err == nil {
					sessionTime = t
				} else if t, err := time.Parse("2006-01-02", entry.HaystackDates[j]); err == nil {
					sessionTime = t
				}
			}

			if err := store.BenchInsert(ctx, cfg.NS, sessionID, content, sessionTime); err != nil {
				cleanup()
				return nil, fmt.Errorf("ingest session %s for q%d: %w", sessionID, i, err)
			}
		}

		// Search with the question
		searchLimit := maxK
		if searchLimit < 50 {
			searchLimit = 50 // get enough candidates
		}
		// Parse question date for temporal-aware scoring
		var questionTime time.Time
		if entry.QuestionDate != "" {
			if t, err := time.Parse("2006-01-02 15:04:05", entry.QuestionDate); err == nil {
				questionTime = t
			} else if t, err := time.Parse("2006-01-02", entry.QuestionDate); err == nil {
				questionTime = t
			}
		}
		results, err := store.Search(ctx, SearchParams{
			NS:            cfg.NS,
			Query:         entry.Question,
			Limit:         searchLimit,
			IncludeAll:    true,
			ReferenceTime: questionTime,
		})
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("search q%d: %w", i, err)
		}

		// Extract retrieved session keys
		retrieved := make([]string, len(results))
		for ri, r := range results {
			retrieved[ri] = r.Key
		}

		// Compute metrics
		result := LongMemEvalResult{
			QuestionID:   entry.QuestionID,
			QuestionType: entry.QuestionType,
			NumSessions:  len(entry.HaystackSessions),
			NumEvidence:  len(entry.AnswerSessionIDs),
			Retrieved:    retrieved,
			Relevant:     entry.AnswerSessionIDs,
			Metrics:      make(map[string]float64),
		}

		for _, k := range cfg.TopK {
			result.Metrics[fmt.Sprintf("recall@%d", k)] = RecallAtK(retrieved, entry.AnswerSessionIDs, k)
			result.Metrics[fmt.Sprintf("ndcg@%d", k)] = NDCGAtK(retrieved, entry.AnswerSessionIDs, k)
			result.Metrics[fmt.Sprintf("precision@%d", k)] = PrecisionAtK(retrieved, entry.AnswerSessionIDs, k)
		}

		// MRR
		relSet := make(map[string]bool, len(entry.AnswerSessionIDs))
		for _, sid := range entry.AnswerSessionIDs {
			relSet[sid] = true
		}
		result.Metrics["mrr"] = MRR(retrieved, relSet)

		report.Results = append(report.Results, result)
		cleanup()

		evalDone++
		if cfg.ProgressFunc != nil {
			cfg.ProgressFunc(evalDone, evalTotal)
		}
	}

	// Aggregate by type and overall
	typeSums := make(map[string]map[string]float64)
	typeCounts := make(map[string]int)
	overallSums := make(map[string]float64)
	overallCount := 0

	for _, r := range report.Results {
		qt := r.QuestionType
		if _, ok := typeSums[qt]; !ok {
			typeSums[qt] = make(map[string]float64)
		}
		typeCounts[qt]++
		overallCount++

		for metric, val := range r.Metrics {
			typeSums[qt][metric] += val
			overallSums[metric] += val
		}
	}

	for qt, sums := range typeSums {
		agg := &LongMemEvalTypeAgg{
			Count:   typeCounts[qt],
			Metrics: make(map[string]float64),
		}
		for metric, sum := range sums {
			agg.Metrics[metric] = sum / float64(typeCounts[qt])
		}
		report.ByType[qt] = agg
	}

	if overallCount > 0 {
		for metric, sum := range overallSums {
			report.Overall[metric] = sum / float64(overallCount)
		}
	}

	return report, nil
}

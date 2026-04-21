package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/rcliao/ghost/internal/model"
)

// ── LoCoMo-Plus dataset (2026) ────────────────────────────────────
//
// LoCoMo-Plus adds a "Cognitive" memory test: 401 cue-trigger pairs across
// four relation types (causal, state, goal, value). Given a trigger message,
// the system must retrieve the semantically-disconnected cue that reveals
// a latent constraint (user's value, goal, state, or causal link).
//
// Paper: https://arxiv.org/abs/2602.10715v1
// Repo:  https://github.com/xjtuleeyf/Locomo-Plus
//
// Example (causal):
//   cue:     "A: After learning to say 'no', I've felt a lot less stressed."
//   trigger: "A: I ended up volunteering for that project, and now I'm overwhelmed."
//   → memory should surface the cue when querying the trigger.

// LoCoMoPlusEntry is one cue-trigger pair.
type LoCoMoPlusEntry struct {
	RelationType         string             `json:"relation_type"` // causal | state | goal | value
	CueDialogue          string             `json:"cue_dialogue"`
	TriggerQuery         string             `json:"trigger_query"`
	TimeGap              string             `json:"time_gap"`
	ModelName            string             `json:"model_name,omitempty"`
	Scores               map[string]float64 `json:"scores,omitempty"`
	Ranks                map[string]int     `json:"ranks,omitempty"`
	FinalSimilarityScore float64            `json:"final_similarity_score,omitempty"`
}

// LoCoMoPlusConfig controls the benchmark run.
type LoCoMoPlusConfig struct {
	DatasetPath    string
	Limit          int // max entries to evaluate (0 = all)
	PerTypeLimit   int // max per relation_type
	TopK           []int
	NS             string
	EmbedCachePath string
	ExpandEdges    bool
	MultiQuery     bool
	// LLM-assisted retrieval: transform trigger query via LLM before searching.
	// Ghost itself remains LLM-free — LLM runs in the benchmark orchestrator.
	LLMHyde    bool      // if true, LLM writes hypothetical answer as search query
	LLMRewrite bool      // if true, LLM rewrites query with synonyms/concepts
	LLM        LLMClient // client used when LLMHyde or LLMRewrite is true
	ProgressFunc func(done, total int)
}

// LoCoMoPlusReport holds aggregate results.
type LoCoMoPlusReport struct {
	Timestamp time.Time                      `json:"timestamp"`
	Dataset   string                         `json:"dataset"`
	Total     int                            `json:"total"`
	ByType    map[string]*LongMemEvalTypeAgg `json:"by_type"` // reuse type agg
	Overall   map[string]float64             `json:"overall"`
}

// LoadLoCoMoPlus reads the LoCoMo-Plus JSON dataset.
func LoadLoCoMoPlus(path string) ([]LoCoMoPlusEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read dataset: %w", err)
	}
	var entries []LoCoMoPlusEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse dataset: %w", err)
	}
	return entries, nil
}

// RunLoCoMoPlus runs retrieval benchmark on LoCoMo-Plus. Each cue_dialogue
// is ingested as a memory; each trigger_query is used to search. Relevance
// is exact match: the cue corresponding to the trigger is the gold result.
func RunLoCoMoPlus(cfg LoCoMoPlusConfig, newStore func() (*SQLiteStore, func(), error)) (*LoCoMoPlusReport, error) {
	entries, err := LoadLoCoMoPlus(cfg.DatasetPath)
	if err != nil {
		return nil, err
	}

	if cfg.NS == "" {
		cfg.NS = "bench:locomo-plus"
	}
	if len(cfg.TopK) == 0 {
		cfg.TopK = []int{5, 10}
	}

	// Apply per-type sampling if requested
	if cfg.PerTypeLimit > 0 {
		typeCounts := make(map[string]int)
		var sampled []LoCoMoPlusEntry
		for _, e := range entries {
			if typeCounts[e.RelationType] < cfg.PerTypeLimit {
				sampled = append(sampled, e)
				typeCounts[e.RelationType]++
			}
		}
		entries = sampled
	}
	if cfg.Limit > 0 && cfg.Limit < len(entries) {
		entries = entries[:cfg.Limit]
	}

	report := &LoCoMoPlusReport{
		Timestamp: time.Now(),
		Dataset:   filepath.Base(cfg.DatasetPath),
		Total:     len(entries),
		ByType:    make(map[string]*LongMemEvalTypeAgg),
		Overall:   make(map[string]float64),
	}

	ctx := context.Background()
	maxK := 0
	for _, k := range cfg.TopK {
		if k > maxK {
			maxK = k
		}
	}
	searchLimit := maxK
	if searchLimit < 50 {
		searchLimit = 50
	}

	// Single shared store: all 401 cues form the haystack; each trigger queries it.
	store, cleanup, err := newStore()
	if err != nil {
		return nil, fmt.Errorf("create store: %w", err)
	}
	defer cleanup()

	// Ingest all cues as memories (keys: cue-<index>)
	batchSessions := make([]BenchSession, 0, len(entries))
	for i, e := range entries {
		batchSessions = append(batchSessions, BenchSession{
			Key:     fmt.Sprintf("cue-%d", i),
			Content: e.CueDialogue,
		})
	}
	if err := store.BatchBenchInsert(ctx, cfg.NS, batchSessions); err != nil {
		return nil, fmt.Errorf("ingest cues: %w", err)
	}

	if cfg.ExpandEdges {
		store.BenchBuildEdges(ctx, cfg.NS)
	}

	// Run retrieval per trigger
	for i, e := range entries {
		expected := fmt.Sprintf("cue-%d", i)
		query := e.TriggerQuery
		// Optional LLM-assisted transformation (Ghost itself stays LLM-free)
		if cfg.LLM != nil {
			if cfg.LLMHyde {
				query = hydeQuery(ctx, cfg.LLM, e.TriggerQuery)
			} else if cfg.LLMRewrite {
				query = rewriteQuery(ctx, cfg.LLM, e.TriggerQuery)
			}
		}
		results, err := store.Search(ctx, SearchParams{
			NS:          cfg.NS,
			Query:       query,
			Limit:       searchLimit,
			IncludeAll:  true,
			ExpandEdges: cfg.ExpandEdges,
			MultiQuery:  cfg.MultiQuery,
		})
		if err != nil {
			return nil, fmt.Errorf("search entry %d: %w", i, err)
		}

		retrieved := make([]string, len(results))
		for ri, r := range results {
			retrieved[ri] = r.Key
		}
		relSet := map[string]bool{expected: true}

		typeName := e.RelationType
		if _, ok := report.ByType[typeName]; !ok {
			report.ByType[typeName] = &LongMemEvalTypeAgg{Metrics: make(map[string]float64)}
		}

		for _, k := range cfg.TopK {
			recall := RecallAtK(retrieved, []string{expected}, k)
			ndcg := NDCGAtK(retrieved, []string{expected}, k)
			precision := PrecisionAtK(retrieved, []string{expected}, k)
			report.ByType[typeName].Metrics[fmt.Sprintf("recall@%d", k)] += recall
			report.ByType[typeName].Metrics[fmt.Sprintf("ndcg@%d", k)] += ndcg
			report.ByType[typeName].Metrics[fmt.Sprintf("precision@%d", k)] += precision
			report.Overall[fmt.Sprintf("recall@%d", k)] += recall
			report.Overall[fmt.Sprintf("ndcg@%d", k)] += ndcg
			report.Overall[fmt.Sprintf("precision@%d", k)] += precision
		}

		mrr := MRR(retrieved, relSet)
		report.ByType[typeName].Metrics["mrr"] += mrr
		report.Overall["mrr"] += mrr
		report.ByType[typeName].Count++

		if cfg.ProgressFunc != nil && ((i+1)%50 == 0 || i+1 == len(entries)) {
			cfg.ProgressFunc(i+1, len(entries))
		}
	}

	// Average
	if report.Total > 0 {
		for k := range report.Overall {
			report.Overall[k] /= float64(report.Total)
		}
	}
	for _, agg := range report.ByType {
		if agg.Count > 0 {
			for k := range agg.Metrics {
				agg.Metrics[k] /= float64(agg.Count)
			}
		}
	}
	// Keep types sorted in iteration downstream (stable output)
	_ = sort.StringsAreSorted
	return report, nil
}

// ── LoCoMo-Plus E2E ──────────────────────────────────────────────
//
// Full pipeline: Ghost retrieves cue from trigger → LLM generates response →
// cognitive judge scores whether response reflects awareness of the latent
// constraint. Measures Ghost+LLM integration on cognitive memory, which is
// LoCoMo-Plus's novel contribution.

// LoCoMoPlusE2EReport holds E2E results.
type LoCoMoPlusE2EReport struct {
	Timestamp time.Time                     `json:"timestamp"`
	Dataset   string                        `json:"dataset"`
	LLM       string                        `json:"llm"`
	Total     int                           `json:"total"`
	ByType    map[string]*E2ETypeAgg        `json:"by_type"`
	Overall   map[string]map[string]float64 `json:"overall"` // mode → metric → value
	Results   []E2EResult                   `json:"results,omitempty"`
}

const cognitiveResponderPrompt = `You are a personal assistant with access to the user's conversation history. The user just sent you a message. Memories from earlier conversations are provided to help you respond with awareness of the user's context, values, and goals.

Respond to the user directly. If memories reveal something important about their situation (e.g., a stated value, ongoing struggle, or earlier goal), reference it naturally. Keep responses concise (2-3 sentences).`

// RunE2ELoCoMoPlus runs the full cognitive-memory E2E pipeline.
// Modes: "no-memory", "ghost", "ghost-hyde", "ghost-rewrite", "ghost-agent", "oracle"
func RunE2ELoCoMoPlus(cfg E2EConfig, newStore func() (*SQLiteStore, func(), error)) (*LoCoMoPlusE2EReport, error) {
	entries, err := LoadLoCoMoPlus(cfg.DatasetPath)
	if err != nil {
		return nil, err
	}

	if cfg.NS == "" {
		cfg.NS = "bench:e2e-locomo-plus"
	}
	if cfg.TopK == 0 {
		cfg.TopK = 5
	}
	if len(cfg.Modes) == 0 {
		cfg.Modes = []string{"no-memory", "ghost", "oracle"}
	}
	if cfg.PerTypeLimit > 0 {
		typeCounts := make(map[string]int)
		var sampled []LoCoMoPlusEntry
		for _, e := range entries {
			if typeCounts[e.RelationType] < cfg.PerTypeLimit {
				sampled = append(sampled, e)
				typeCounts[e.RelationType]++
			}
		}
		entries = sampled
	}
	if cfg.Limit > 0 && cfg.Limit < len(entries) {
		entries = entries[:cfg.Limit]
	}

	report := &LoCoMoPlusE2EReport{
		Timestamp: time.Now(),
		Dataset:   filepath.Base(cfg.DatasetPath),
		LLM:       cfg.LLM.Name(),
		ByType:    make(map[string]*E2ETypeAgg),
		Overall:   make(map[string]map[string]float64),
	}
	for _, mode := range cfg.Modes {
		report.Overall[mode] = make(map[string]float64)
	}

	// Resume support: if GHOST_BENCH_RESUME=1 and a matching checkpoint exists,
	// load accumulated metrics and skip ahead past completed questions.
	resumeFrom := 0
	if os.Getenv("GHOST_BENCH_RESUME") == "1" && os.Getenv("GHOST_BENCH_CHECKPOINT") != "" {
		if buf, err := os.ReadFile(os.Getenv("GHOST_BENCH_CHECKPOINT")); err == nil {
			var prev LoCoMoPlusE2EReport
			if err := json.Unmarshal(buf, &prev); err == nil && prev.Total > 0 {
				// Validate checkpoint matches current config — same modes and dataset
				same := prev.Dataset == report.Dataset && len(prev.Overall) == len(report.Overall)
				for _, m := range cfg.Modes {
					if _, ok := prev.Overall[m]; !ok {
						same = false
						break
					}
				}
				if same {
					report = &prev
					resumeFrom = prev.Total
					fmt.Fprintf(os.Stderr, "Resuming from checkpoint: %d questions already done\n", resumeFrom)
				}
			}
		}
	}

	ctx := context.Background()

	// Single shared store: all cues form the haystack
	store, cleanup, err := newStore()
	if err != nil {
		return nil, fmt.Errorf("create store: %w", err)
	}
	defer cleanup()

	batchSessions := make([]BenchSession, 0, len(entries))
	cueByKey := make(map[string]string, len(entries))
	for i, e := range entries {
		key := fmt.Sprintf("cue-%d", i)
		batchSessions = append(batchSessions, BenchSession{Key: key, Content: e.CueDialogue})
		cueByKey[key] = e.CueDialogue
	}
	if err := store.BatchBenchInsert(ctx, cfg.NS, batchSessions); err != nil {
		return nil, fmt.Errorf("ingest: %w", err)
	}

	// Build edges between cues so ghost-compress-edges has something to
	// expand. Uses semantic similarity + entity co-occurrence + topic overlap.
	needsEdges := false
	for _, m := range cfg.Modes {
		if m == "ghost-compress-edges" {
			needsEdges = true
			break
		}
	}
	if needsEdges {
		if _, err := store.BenchBuildEdges(ctx, cfg.NS); err != nil {
			return nil, fmt.Errorf("build edges: %w", err)
		}
	}

	judge := cfg.Judge
	if judge == nil {
		judge = cfg.LLM
	}

	for i, e := range entries {
		if i < resumeFrom {
			continue
		}
		result := E2EResult{
			QuestionID:   fmt.Sprintf("cog-%d", i),
			QuestionType: e.RelationType,
			Question:     e.TriggerQuery,
			GoldAnswer:   e.CueDialogue, // the cue is the "gold context" to reference
			Answers:      make(map[string]string),
			Scores:       make(map[string]float64),
		}

		for _, mode := range cfg.Modes {
			var userMsg string
			// Time the full pipeline including prep-call LLM (hyde/rewrite/compress/agent)
			// so multi-call modes reflect their true end-to-end latency.
			modeStart := time.Now()
			switch mode {
			case "no-memory":
				userMsg = e.TriggerQuery
			case "ghost":
				results, _ := store.Search(ctx, SearchParams{
					NS: cfg.NS, Query: e.TriggerQuery, Limit: cfg.TopK, IncludeAll: true,
				})
				userMsg = formatMemoryForLLM(e.TriggerQuery, capResults(results, 5), 30000) + e.TriggerQuery
			case "ghost-hyde":
				q := hydeQuery(ctx, cfg.LLM, e.TriggerQuery)
				results, _ := store.Search(ctx, SearchParams{
					NS: cfg.NS, Query: q, Limit: cfg.TopK, IncludeAll: true,
				})
				userMsg = formatMemoryForLLM(e.TriggerQuery, capResults(results, 5), 30000) + e.TriggerQuery
			case "ghost-rewrite":
				q := rewriteQuery(ctx, cfg.LLM, e.TriggerQuery)
				results, _ := store.Search(ctx, SearchParams{
					NS: cfg.NS, Query: q, Limit: cfg.TopK, IncludeAll: true,
				})
				userMsg = formatMemoryForLLM(e.TriggerQuery, capResults(results, 5), 30000) + e.TriggerQuery
			case "ghost-agent":
				results := agentSearch(ctx, store, cfg.LLM, cfg.NS, e.TriggerQuery, cfg.TopK, 3)
				userMsg = formatMemoryForLLM(e.TriggerQuery, capResults(results, 5), 30000) + e.TriggerQuery
			case "ghost-compress":
				results, _ := store.Search(ctx, SearchParams{
					NS: cfg.NS, Query: e.TriggerQuery, Limit: cfg.TopK, IncludeAll: true,
				})
				userMsg = compressContext(ctx, cfg.LLM, e.TriggerQuery, capResults(results, 5)) + e.TriggerQuery
			case "ghost-compress-wide":
				// Feed top-15 to the compressor so it has more candidates
				// to extract from. Compression filters noise, so wider recall
				// shouldn't pollute the answering prompt.
				wideLimit := cfg.TopK * 3
				if wideLimit < 15 {
					wideLimit = 15
				}
				results, _ := store.Search(ctx, SearchParams{
					NS: cfg.NS, Query: e.TriggerQuery, Limit: wideLimit, IncludeAll: true,
				})
				userMsg = compressContext(ctx, cfg.LLM, e.TriggerQuery, capResults(results, wideLimit)) + e.TriggerQuery
			case "ghost-rewrite-compress":
				// LLM rewrites the query, searches with it, then compresses.
				// Tests whether stacking rewrite + compress compounds gains.
				q := rewriteQuery(ctx, cfg.LLM, e.TriggerQuery)
				results, _ := store.Search(ctx, SearchParams{
					NS: cfg.NS, Query: q, Limit: cfg.TopK, IncludeAll: true,
				})
				userMsg = compressContext(ctx, cfg.LLM, e.TriggerQuery, capResults(results, 5)) + e.TriggerQuery
			case "ghost-hyde-compress":
				// HyDE generates a hypothetical cue (speculating what memory
				// might contain), searches with it, then compresses top-5.
				// Tests whether HyDE's theme-expansion helps the compressor.
				q := hydeQuery(ctx, cfg.LLM, e.TriggerQuery)
				results, _ := store.Search(ctx, SearchParams{
					NS: cfg.NS, Query: q, Limit: cfg.TopK, IncludeAll: true,
				})
				userMsg = compressContext(ctx, cfg.LLM, e.TriggerQuery, capResults(results, 5)) + e.TriggerQuery
			case "ghost-compress-edges":
				// Search with 1-hop edge expansion, then compress.
				// Tests whether spreading-activation recall helps the compressor
				// find latent cues that direct search missed.
				results, _ := store.Search(ctx, SearchParams{
					NS: cfg.NS, Query: e.TriggerQuery, Limit: cfg.TopK,
					IncludeAll: true, ExpandEdges: true,
				})
				userMsg = compressContext(ctx, cfg.LLM, e.TriggerQuery, capResults(results, 10)) + e.TriggerQuery
			case "oracle":
				oracleResults := []SearchResult{{Memory: model.Memory{Content: e.CueDialogue}}}
				userMsg = formatMemoryForLLM(e.TriggerQuery, oracleResults, 30000) + e.TriggerQuery
			}

			response, err := cfg.LLM.Generate(ctx, cognitiveResponderPrompt, userMsg)
			answerLatency := time.Since(modeStart).Seconds()
			if err != nil {
				result.Answers[mode] = fmt.Sprintf("[ERROR: %v]", err)
				result.Scores[mode] = 0
				continue
			}
			result.Answers[mode] = response

			// Cognitive judge: did response reflect awareness of the cue?
			score := cognitiveJudge(ctx, judge, e.CueDialogue, e.TriggerQuery, response)
			if score < 0 {
				score = 0
			}
			result.Scores[mode] = score

			if _, ok := report.ByType[e.RelationType]; !ok {
				report.ByType[e.RelationType] = &E2ETypeAgg{Metrics: make(map[string]map[string]float64)}
				for _, m := range cfg.Modes {
					report.ByType[e.RelationType].Metrics[m] = make(map[string]float64)
				}
			}
			report.ByType[e.RelationType].Metrics[mode]["score"] += score
			report.ByType[e.RelationType].Metrics[mode]["input_tokens"] += float64(estimateTokensFromChars(userMsg) + estimateTokensFromChars(cognitiveResponderPrompt))
			report.ByType[e.RelationType].Metrics[mode]["output_tokens"] += float64(estimateTokensFromChars(response))
			report.ByType[e.RelationType].Metrics[mode]["latency_sec"] += answerLatency
			report.Overall[mode]["score"] += score
			report.Overall[mode]["input_tokens"] += float64(estimateTokensFromChars(userMsg) + estimateTokensFromChars(cognitiveResponderPrompt))
			report.Overall[mode]["output_tokens"] += float64(estimateTokensFromChars(response))
			report.Overall[mode]["latency_sec"] += answerLatency
		}

		report.Results = append(report.Results, result)
		report.ByType[e.RelationType].Count++
		report.Total++

		if cfg.ProgressFunc != nil && (report.Total%10 == 0 || report.Total == len(entries)) {
			cfg.ProgressFunc(report.Total, len(entries))
		}

		// Checkpoint every 25 questions so long runs produce usable partial data
		if report.Total%25 == 0 && os.Getenv("GHOST_BENCH_CHECKPOINT") != "" {
			checkpointPath := os.Getenv("GHOST_BENCH_CHECKPOINT")
			if buf, err := json.MarshalIndent(report, "", "  "); err == nil {
				_ = os.WriteFile(checkpointPath, buf, 0644)
			}
		}
	}

	// Average metrics
	if report.Total > 0 {
		for _, mode := range cfg.Modes {
			for k := range report.Overall[mode] {
				report.Overall[mode][k] /= float64(report.Total)
			}
		}
	}
	for _, agg := range report.ByType {
		if agg.Count > 0 {
			for _, mode := range cfg.Modes {
				for k := range agg.Metrics[mode] {
					agg.Metrics[mode][k] /= float64(agg.Count)
				}
			}
		}
	}
	return report, nil
}

// capResults caps a slice to n without panicking.
func capResults(results []SearchResult, n int) []SearchResult {
	if n > len(results) {
		return results
	}
	return results[:n]
}

package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
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

package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/rcliao/ghost/internal/embedding"
)

// TestLoCoMoPlus runs the LoCoMo-Plus cue-trigger retrieval benchmark.
//
// Usage:
//
//	GHOST_EMBED_PROVIDER=local \
//	GHOST_BENCH_LOCOMO_PLUS=testdata/locomo/locomo_plus.json \
//	GHOST_BENCH_EMBED_CACHE=testdata/locomo/embed_cache_plus.json \
//	  go test ./internal/store/ -run TestLoCoMoPlus -v -timeout 30m
func TestLoCoMoPlus(t *testing.T) {
	datasetPath := os.Getenv("GHOST_BENCH_LOCOMO_PLUS")
	if datasetPath == "" {
		t.Skip("GHOST_BENCH_LOCOMO_PLUS not set — skipping LoCoMo-Plus benchmark")
	}
	datasetPath = resolveRepoPath(datasetPath)
	if _, err := os.Stat(datasetPath); os.IsNotExist(err) {
		t.Skipf("dataset not found at %s", datasetPath)
	}

	perTypeLimit := 0
	if s := os.Getenv("GHOST_BENCH_PER_TYPE"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			perTypeLimit = n
		}
	}

	cachePath := os.Getenv("GHOST_BENCH_EMBED_CACHE")
	if cachePath != "" {
		cachePath = resolveRepoPath(cachePath)
	}

	var embedder embedding.Embedder
	if base := embedding.NewFromEnv(); base != nil {
		if cachePath != "" {
			embedder = embedding.NewCachedEmbedder(base, cachePath)
			t.Logf("Using embed cache: %s (%d entries)", cachePath, embedder.(*embedding.CachedEmbedder).Len())
		} else {
			embedder = base
		}
	}

	expandEdges := os.Getenv("GHOST_BENCH_EXPAND_EDGES") == "1"
	multiQuery := os.Getenv("GHOST_BENCH_MULTI_QUERY") == "1"
	llmHyde := os.Getenv("GHOST_BENCH_LLM_HYDE") == "1"
	llmRewrite := os.Getenv("GHOST_BENCH_LLM_REWRITE") == "1"

	var llm LLMClient
	if llmHyde || llmRewrite {
		llmModel := os.Getenv("GHOST_BENCH_LLM_MODEL")
		if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
			llm = NewAnthropicClient(llmModel)
			t.Logf("Using LLM for query transformation: %s", llm.Name())
		} else {
			llm = NewClaudeCLIClient(llmModel)
			t.Logf("Using claude CLI for query transformation: %s", llm.Name())
		}
	}

	cfg := LoCoMoPlusConfig{
		DatasetPath:    datasetPath,
		PerTypeLimit:   perTypeLimit,
		TopK:           []int{1, 5, 10, 50},
		EmbedCachePath: cachePath,
		ExpandEdges:    expandEdges,
		MultiQuery:     multiQuery,
		LLMHyde:        llmHyde,
		LLMRewrite:     llmRewrite,
		LLM:            llm,
		ProgressFunc: func(done, total int) {
			t.Logf("Progress: %d/%d (%.0f%%)", done, total, float64(done)/float64(total)*100)
		},
	}

	report, err := RunLoCoMoPlus(cfg, func() (*SQLiteStore, func(), error) {
		dir := t.TempDir()
		s, err := NewSQLiteStore(filepath.Join(dir, "bench.db"))
		if err != nil {
			return nil, nil, err
		}
		if embedder != nil {
			s.SetEmbedder(embedder)
		}
		return s, func() { s.Close() }, nil
	})
	if err != nil {
		t.Fatalf("benchmark failed: %v", err)
	}

	t.Logf("\n=== LoCoMo-Plus Results ===")
	t.Logf("Dataset: %s", report.Dataset)
	t.Logf("Entries: %d", report.Total)
	t.Logf("")

	t.Logf("── Overall ──")
	printMetrics(t, report.Overall)

	types := make([]string, 0, len(report.ByType))
	for c := range report.ByType {
		types = append(types, c)
	}
	sort.Strings(types)
	for _, typ := range types {
		agg := report.ByType[typ]
		t.Logf("")
		t.Logf("── %s (n=%d) ──", typ, agg.Count)
		printMetrics(t, agg.Metrics)
	}

	jsonReport, _ := json.MarshalIndent(report, "", "  ")
	t.Logf("\nLOCOMO_PLUS_REPORT:%s", string(jsonReport))
}

// TestE2ELoCoMoPlus runs the full cognitive-memory E2E pipeline on LoCoMo-Plus.
//
// Usage:
//
//	GHOST_EMBED_PROVIDER=local \
//	GHOST_BENCH_LOCOMO_PLUS=testdata/locomo/locomo_plus.json \
//	GHOST_BENCH_PER_TYPE=5 \
//	GHOST_BENCH_LLM_MODEL=haiku \
//	GHOST_BENCH_MODES=no-memory,ghost,ghost-rewrite,oracle \
//	  go test ./internal/store/ -run TestE2ELoCoMoPlus -v -timeout 60m
func TestE2ELoCoMoPlus(t *testing.T) {
	datasetPath := os.Getenv("GHOST_BENCH_LOCOMO_PLUS")
	if datasetPath == "" {
		t.Skip("GHOST_BENCH_LOCOMO_PLUS not set")
	}
	datasetPath = resolveRepoPath(datasetPath)

	perTypeLimit := 0
	if s := os.Getenv("GHOST_BENCH_PER_TYPE"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			perTypeLimit = n
		}
	}

	cachePath := os.Getenv("GHOST_BENCH_EMBED_CACHE")
	if cachePath != "" {
		cachePath = resolveRepoPath(cachePath)
	}

	var embedder embedding.Embedder
	if base := embedding.NewFromEnv(); base != nil {
		if cachePath != "" {
			embedder = embedding.NewCachedEmbedder(base, cachePath)
		} else {
			embedder = base
		}
	}

	llmModel := os.Getenv("GHOST_BENCH_LLM_MODEL")
	var llm LLMClient
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		llm = NewAnthropicClient(llmModel)
	} else {
		llm = NewClaudeCLIClient(llmModel)
	}
	t.Logf("Using LLM: %s", llm.Name())

	modes := []string{"no-memory", "ghost", "ghost-rewrite", "oracle"}
	if s := os.Getenv("GHOST_BENCH_MODES"); s != "" {
		modes = splitCSV(s)
	}

	cfg := E2EConfig{
		DatasetPath:  datasetPath,
		PerTypeLimit: perTypeLimit,
		TopK:         5,
		LLM:          llm,
		Modes:        modes,
		ProgressFunc: func(done, total int) {
			t.Logf("Progress: %d/%d (%.0f%%)", done, total, float64(done)/float64(total)*100)
		},
	}

	report, err := RunE2ELoCoMoPlus(cfg, func() (*SQLiteStore, func(), error) {
		dir := t.TempDir()
		s, err := NewSQLiteStore(filepath.Join(dir, "bench.db"))
		if err != nil {
			return nil, nil, err
		}
		if embedder != nil {
			s.SetEmbedder(embedder)
		}
		return s, func() { s.Close() }, nil
	})
	if err != nil {
		t.Fatalf("E2E LoCoMo-Plus failed: %v", err)
	}

	t.Logf("\n=== E2E LoCoMo-Plus Results (LLM: %s) ===", report.LLM)
	t.Logf("Entries: %d  (cognitive judge: correct=1.0, partial=0.5, wrong=0.0)", report.Total)
	t.Logf("")
	t.Logf("── Overall ──")
	t.Logf("  %-15s %6s %10s %10s %10s", "Mode", "Score", "In-Tok", "Out-Tok", "Latency")
	for _, mode := range modes {
		if m, ok := report.Overall[mode]; ok {
			t.Logf("  %-15s %6.3f %10.0f %10.0f %10.2fs",
				mode, m["score"], m["input_tokens"], m["output_tokens"], m["latency_sec"])
		}
	}
	types := []string{"causal", "state", "goal", "value"}
	for _, typ := range types {
		agg, ok := report.ByType[typ]
		if !ok {
			continue
		}
		t.Logf("")
		t.Logf("── %s (n=%d) ──", typ, agg.Count)
		for _, mode := range modes {
			if m, ok := agg.Metrics[mode]; ok {
				t.Logf("  %-14s score=%.3f", mode, m["score"])
			}
		}
	}

	jsonReport, _ := json.MarshalIndent(report, "", "  ")
	t.Logf("\nLOCOMO_PLUS_E2E_REPORT:%s", string(jsonReport))
}

func splitCSV(s string) []string {
	parts := []string{}
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			parts = append(parts, t)
		}
	}
	return parts
}

// TestLoCoMoPlusBuildCache pre-computes embeddings for all cues + triggers.
func TestLoCoMoPlusBuildCache(t *testing.T) {
	datasetPath := os.Getenv("GHOST_BENCH_LOCOMO_PLUS")
	cachePath := os.Getenv("GHOST_BENCH_EMBED_CACHE")
	if datasetPath == "" || cachePath == "" {
		t.Skip("GHOST_BENCH_LOCOMO_PLUS and GHOST_BENCH_EMBED_CACHE required")
	}
	datasetPath = resolveRepoPath(datasetPath)
	cachePath = resolveRepoPath(cachePath)

	embedder := embedding.NewFromEnv()
	if embedder == nil {
		t.Skip("no embedder configured")
	}

	entries, err := LoadLoCoMoPlus(datasetPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	cached := embedding.NewCachedEmbedder(embedder, cachePath)

	var allTexts []string
	seen := make(map[string]bool)
	add := func(s string) {
		h := embedding.ContentHash(s)
		if !seen[h] {
			seen[h] = true
			allTexts = append(allTexts, s)
		}
	}
	for _, e := range entries {
		add(e.CueDialogue)
		add(e.TriggerQuery)
		// Include windowed speaker chunks so BatchBenchInsert gets cache hits
		for _, chunks := range extractSpeakerTurns(e.CueDialogue) {
			for _, chunk := range chunks {
				add(chunk)
			}
		}
	}
	t.Logf("Unique texts to embed: %d", len(allTexts))

	const batchSize = 32
	for i := 0; i < len(allTexts); i += batchSize {
		end := i + batchSize
		if end > len(allTexts) {
			end = len(allTexts)
		}
		if _, err := cached.EmbedBatch(t.Context(), allTexts[i:end]); err != nil {
			t.Fatalf("embed batch: %v", err)
		}
		if (i+batchSize)%200 == 0 {
			t.Logf("Progress: %d/%d", i+batchSize, len(allTexts))
		}
	}
	if err := cached.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	t.Logf("Cache built: %d entries at %s", cached.Len(), cachePath)
}

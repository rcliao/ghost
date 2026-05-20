package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/rcliao/ghost/internal/embedding"
)

// TestHaluMemRetrieval runs the QA-retrieval portion of HaluMem.
// Skipped unless GHOST_BENCH_HALUMEM is set.
//
// Usage:
//
//	GHOST_EMBED_PROVIDER=local \
//	GHOST_BENCH_HALUMEM=testdata/halumem/HaluMem-Medium.jsonl \
//	  go test ./internal/store/ -run TestHaluMemRetrieval -v -timeout 30m
func TestHaluMemRetrieval(t *testing.T) {
	datasetPath := os.Getenv("GHOST_BENCH_HALUMEM")
	if datasetPath == "" {
		t.Skip("GHOST_BENCH_HALUMEM not set — skipping HaluMem benchmark")
	}
	datasetPath = resolveRepoPath(datasetPath)
	if _, err := os.Stat(datasetPath); os.IsNotExist(err) {
		t.Skipf("dataset not found at %s", datasetPath)
	}

	userLimit := 0
	if s := os.Getenv("GHOST_BENCH_USER_LIMIT"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			userLimit = n
		}
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

	// Optional LLM-judge E2E pass for Accuracy / Hallucination / Omission.
	var llm LLMClient
	if llmModel := os.Getenv("GHOST_BENCH_LLM_MODEL"); llmModel != "" {
		if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
			llm = NewAnthropicClient(llmModel)
			t.Logf("Using Anthropic API for E2E judge: %s", llm.Name())
		} else {
			llm = NewClaudeCLIClient(llmModel)
			t.Logf("Using Claude CLI for E2E judge: %s", llm.Name())
		}
	}
	judgeTopK := 5
	if s := os.Getenv("GHOST_BENCH_JUDGE_TOPK"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			judgeTopK = n
		}
	}

	cfg := HaluMemConfig{
		DatasetPath:    datasetPath,
		UserLimit:      userLimit,
		PerTypeLimit:   perTypeLimit,
		TopK:           []int{5, 10},
		EmbedCachePath: cachePath,
		SkipBoundary:   true, // retrieval recall isn't meaningful for abstention
		LLM:            llm,
		Judge:          llm,
		JudgeTopK:      judgeTopK,
		ProgressFunc: func(done, total int) {
			t.Logf("Progress: %d/%d (%.0f%%)", done, total, float64(done)/float64(total)*100)
		},
	}

	report, err := RunHaluMemRetrieval(cfg, func() (*SQLiteStore, func(), error) {
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

	t.Logf("\n=== HaluMem Retrieval Results ===")
	t.Logf("Dataset: %s", report.Dataset)
	t.Logf("Questions evaluated: %d", report.Total)

	t.Logf("\n── Overall ──")
	printMetrics(t, report.Overall)

	types := make([]string, 0, len(report.ByType))
	for qt := range report.ByType {
		types = append(types, qt)
	}
	sort.Strings(types)
	for _, qt := range types {
		agg := report.ByType[qt]
		t.Logf("\n── %s (n=%d) ──", qt, agg.Count)
		printMetrics(t, agg.Metrics)
	}

	jsonReport, _ := json.MarshalIndent(report, "", "  ")
	t.Logf("\nHALUMEM_REPORT:%s", string(jsonReport))
}

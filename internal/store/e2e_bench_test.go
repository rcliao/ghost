package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/rcliao/ghost/internal/embedding"
)

// TestE2ELongMemEval runs the end-to-end benchmark: Ghost retrieval + Claude answering.
//
// Requires: ANTHROPIC_API_KEY + GHOST_BENCH_LONGMEMEVAL
//
// Usage (cheap: 3 per type with Haiku ≈ $0.10):
//
//	ANTHROPIC_API_KEY=sk-... \
//	GHOST_EMBED_PROVIDER=local \
//	GHOST_BENCH_LONGMEMEVAL=testdata/longmemeval/longmemeval_s_cleaned.json \
//	GHOST_BENCH_EMBED_CACHE=testdata/longmemeval/embed_cache_s.json \
//	GHOST_BENCH_PER_TYPE=3 \
//	  go test ./internal/store/ -run TestE2ELongMemEval -v -timeout 30m
func TestE2ELongMemEval(t *testing.T) {
	datasetPath := os.Getenv("GHOST_BENCH_LONGMEMEVAL")
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if datasetPath == "" {
		t.Skip("GHOST_BENCH_LONGMEMEVAL not set")
	}
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set — skipping E2E benchmark")
	}
	datasetPath = resolveRepoPath(datasetPath)

	limit := 0
	if s := os.Getenv("GHOST_BENCH_LIMIT"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			limit = n
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
		} else {
			embedder = base
		}
	}

	model := os.Getenv("GHOST_BENCH_LLM_MODEL")
	llm := NewAnthropicClient(model)

	cfg := E2EConfig{
		DatasetPath:  datasetPath,
		Limit:        limit,
		PerTypeLimit: perTypeLimit,
		TopK:         5,
		LLM:          llm,
		Modes:        []string{"no-memory", "ghost", "oracle"},
		ProgressFunc: func(done, total int) {
			t.Logf("Progress: %d/%d (%.0f%%)", done, total, float64(done)/float64(total)*100)
		},
	}

	report, err := RunE2ELongMemEval(cfg, func() (*SQLiteStore, func(), error) {
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
		t.Fatalf("E2E benchmark failed: %v", err)
	}

	// Print summary
	t.Logf("\n=== E2E Benchmark Results (LLM: %s) ===", llm.Model)
	t.Logf("Dataset: %s, Questions: %d", report.Dataset, report.Total)
	t.Logf("")

	// Overall
	t.Logf("── Overall Token F1 ──")
	modes := []string{"no-memory", "ghost", "oracle"}
	for _, mode := range modes {
		if m, ok := report.Overall[mode]; ok {
			t.Logf("  %-12s %.4f", mode, m["token_f1"])
		}
	}

	// By type
	types := make([]string, 0, len(report.ByType))
	for qt := range report.ByType {
		types = append(types, qt)
	}
	sort.Strings(types)

	for _, qt := range types {
		agg := report.ByType[qt]
		t.Logf("")
		t.Logf("── %s (n=%d) ──", qt, agg.Count)
		for _, mode := range modes {
			if m, ok := agg.Metrics[mode]; ok {
				t.Logf("  %-12s F1=%.4f", mode, m["token_f1"])
			}
		}
	}

	// Sample results
	t.Logf("\n── Sample Results ──")
	for i, r := range report.Results {
		if i >= 5 {
			break
		}
		t.Logf("Q: %s", r.Question)
		t.Logf("  Gold: %s", r.GoldAnswer)
		for _, mode := range modes {
			answer := r.Answers[mode]
			if len(answer) > 100 {
				answer = answer[:100] + "..."
			}
			t.Logf("  %-12s (F1=%.2f) %s", mode, r.TokenF1[mode], answer)
		}
		t.Logf("")
	}

	jsonReport, _ := json.MarshalIndent(report, "", "  ")
	fmt.Fprintf(os.Stdout, "E2E_REPORT:%s\n", string(jsonReport))
}

func TestTokenF1(t *testing.T) {
	tests := []struct {
		pred, ref string
		wantMin   float64
		wantMax   float64
	}{
		{"Business Administration", "Business Administration", 0.99, 1.01},
		{"The answer is Business Administration degree", "Business Administration", 0.5, 0.9},
		{"something completely different", "Business Administration", 0, 0.1},
		{"", "Business Administration", 0, 0.01},
	}
	for _, tt := range tests {
		got := tokenF1(tt.pred, tt.ref)
		if got < tt.wantMin || got > tt.wantMax {
			t.Errorf("tokenF1(%q, %q) = %.3f, want [%.2f, %.2f]", tt.pred, tt.ref, got, tt.wantMin, tt.wantMax)
		}
	}
}

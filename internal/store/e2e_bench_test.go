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
	"github.com/rcliao/ghost/internal/model"
)

// TestE2ELongMemEval runs the end-to-end benchmark: Ghost retrieval + Claude answering.
//
// Uses `claude -p` by default. Set ANTHROPIC_API_KEY for API mode.
//
// Usage:
//
//	GHOST_EMBED_PROVIDER=local \
//	GHOST_BENCH_LONGMEMEVAL=testdata/longmemeval/longmemeval_s_cleaned.json \
//	GHOST_BENCH_EMBED_CACHE=testdata/longmemeval/embed_cache_s.json \
//	GHOST_BENCH_PER_TYPE=3 \
//	GHOST_BENCH_LLM_MODEL=haiku \
//	  go test ./internal/store/ -run TestE2ELongMemEval -v -timeout 30m
func TestE2ELongMemEval(t *testing.T) {
	datasetPath := os.Getenv("GHOST_BENCH_LONGMEMEVAL")
	if datasetPath == "" {
		t.Skip("GHOST_BENCH_LONGMEMEVAL not set")
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

	llmModel := os.Getenv("GHOST_BENCH_LLM_MODEL")
	var llm LLMClient
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		llm = NewAnthropicClient(llmModel)
		t.Logf("Using Anthropic API: %s", llm.Name())
	} else {
		llm = NewClaudeCLIClient(llmModel)
		t.Logf("Using Claude CLI: %s", llm.Name())
	}

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
	t.Logf("\n=== E2E Benchmark Results (LLM: %s) ===", report.LLM)
	t.Logf("Dataset: %s, Questions: %d", report.Dataset, report.Total)

	modes := []string{"no-memory", "ghost", "oracle"}

	// Overall
	t.Logf("")
	t.Logf("── Overall ──")
	t.Logf("  %-14s %8s %8s %8s %8s", "Mode", "Score", "Contains", "TokRecall", "TokF1")
	for _, mode := range modes {
		if m, ok := report.Overall[mode]; ok {
			t.Logf("  %-14s %8.3f %8.1f%% %8.3f %8.3f",
				mode, m["score"], m["flexible_contains"]*100, m["token_recall"], m["token_f1"])
		}
	}

	// Ghost value metrics
	if ghost, ok := report.Overall["ghost"]; ok {
		if nomem, ok2 := report.Overall["no-memory"]; ok2 {
			t.Logf("")
			t.Logf("── Ghost Value ──")
			t.Logf("  Ghost vs No-Memory: +%.1f%% score", (ghost["score"]-nomem["score"])*100)
		}
		if oracle, ok3 := report.Overall["oracle"]; ok3 {
			t.Logf("  Ghost vs Oracle:    %.1f%% of ceiling", ghost["score"]/oracle["score"]*100)
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
				t.Logf("  %-14s score=%.3f contains=%.0f%% recall=%.3f",
					mode, m["score"], m["flexible_contains"]*100, m["token_recall"])
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
			if len(answer) > 120 {
				answer = answer[:120] + "..."
			}
			t.Logf("  %-14s (%.2f) %s", mode, r.Scores[mode], answer)
		}
		t.Logf("")
	}

	jsonReport, _ := json.MarshalIndent(report, "", "  ")
	fmt.Fprintf(os.Stdout, "E2E_REPORT:%s\n", string(jsonReport))
}

func TestScoreAnswer(t *testing.T) {
	tests := []struct {
		answer, gold string
		wantMin      float64
	}{
		{"Business Administration", "Business Administration", 0.9},
		{"I graduated with a degree in Business Administration from State University.", "Business Administration", 0.7},
		{"The answer is 3 items.", "3", 0.7},
		{"Three items need to be picked up.", "3", 0.7},
		{"I bought it at Target last week.", "Target", 0.7},
		{"I don't have that information.", "Business Administration", 0.0},
		{"something completely unrelated", "Target", 0.0},
	}
	for _, tt := range tests {
		scores := scoreAnswer(tt.answer, tt.gold)
		if scores["score"] < tt.wantMin {
			t.Errorf("scoreAnswer(%q, %q) score=%.3f, want >= %.3f\n  details: %v",
				tt.answer, tt.gold, scores["score"], tt.wantMin, scores)
		}
	}
}

// Ensure model import is used (for oracle mode SearchResult construction)
var _ = model.Memory{}

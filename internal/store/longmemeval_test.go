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

// TestLongMemEval runs the LongMemEval retrieval benchmark against ghost's search.
//
// Requires: GHOST_BENCH_LONGMEMEVAL env var pointing to a dataset JSON file.
// Optional: GHOST_BENCH_LIMIT to cap the number of questions (default: all).
//
// Usage:
//
//	GHOST_BENCH_LONGMEMEVAL=testdata/longmemeval/longmemeval_oracle.json \
//	  go test ./internal/store/ -run TestLongMemEval -v -timeout 30m
func TestLongMemEval(t *testing.T) {
	datasetPath := os.Getenv("GHOST_BENCH_LONGMEMEVAL")
	if datasetPath == "" {
		t.Skip("GHOST_BENCH_LONGMEMEVAL not set — skipping LongMemEval benchmark")
	}

	datasetPath = resolveRepoPath(datasetPath)

	if _, err := os.Stat(datasetPath); os.IsNotExist(err) {
		t.Skipf("dataset not found at %s — see testdata/longmemeval/README.md", datasetPath)
	}

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
	if cachePath != "" && !filepath.IsAbs(cachePath) {
		cachePath = resolveRepoPath(cachePath)
	}

	useContext := os.Getenv("GHOST_BENCH_USE_CONTEXT") != ""

	cfg := LongMemEvalConfig{
		DatasetPath:    datasetPath,
		Limit:          limit,
		PerTypeLimit:   perTypeLimit,
		TopK:           []int{5, 10, 50},
		EmbedCachePath: cachePath,
		UseContext:     useContext,
		ProgressFunc: func(done, total int) {
			if done%10 == 0 || done == total {
				t.Logf("Progress: %d/%d (%.0f%%)", done, total, float64(done)/float64(total)*100)
			}
		},
	}

	// Build the embedder: use cached embedder if cache path is set
	var embedder embedding.Embedder
	if baseEmbedder := embedding.NewFromEnv(); baseEmbedder != nil {
		if cachePath != "" {
			embedder = embedding.NewCachedEmbedder(baseEmbedder, cachePath)
			t.Logf("Using embed cache: %s (%d entries)", cachePath, embedder.(*embedding.CachedEmbedder).Len())
		} else {
			embedder = baseEmbedder
		}
	}

	report, err := RunLongMemEval(cfg, func() (*SQLiteStore, func(), error) {
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

	// Print summary
	t.Logf("\n=== LongMemEval Benchmark Results ===")
	t.Logf("Dataset: %s", report.Dataset)
	t.Logf("Questions evaluated: %d", len(report.Results))
	t.Logf("")

	// Overall metrics
	t.Logf("── Overall ──")
	printMetrics(t, report.Overall)

	// By question type
	types := make([]string, 0, len(report.ByType))
	for qt := range report.ByType {
		types = append(types, qt)
	}
	sort.Strings(types)

	for _, qt := range types {
		agg := report.ByType[qt]
		t.Logf("")
		t.Logf("── %s (n=%d) ──", qt, agg.Count)
		printMetrics(t, agg.Metrics)
	}

	// Emit JSON report for programmatic consumption
	jsonReport, _ := json.MarshalIndent(report, "", "  ")
	t.Logf("\nLONGMEMEVAL_REPORT:%s", string(jsonReport))
}

// TestLongMemEvalLoad verifies dataset parsing without running the full benchmark.
func TestLongMemEvalLoad(t *testing.T) {
	datasetPath := os.Getenv("GHOST_BENCH_LONGMEMEVAL")
	if datasetPath == "" {
		t.Skip("GHOST_BENCH_LONGMEMEVAL not set")
	}
	datasetPath = resolveRepoPath(datasetPath)

	entries, err := LoadLongMemEval(datasetPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	t.Logf("Loaded %d entries", len(entries))

	// Count by type
	typeCounts := make(map[string]int)
	absCount := 0
	totalSessions := 0
	for _, e := range entries {
		typeCounts[e.QuestionType]++
		if len(e.AnswerSessionIDs) == 0 {
			absCount++
		}
		totalSessions += len(e.HaystackSessions)
	}

	t.Logf("Abstention questions: %d", absCount)
	t.Logf("Total haystack sessions across all questions: %d", totalSessions)
	t.Logf("Avg sessions per question: %.1f", float64(totalSessions)/float64(len(entries)))

	for qt, count := range typeCounts {
		t.Logf("  %s: %d", qt, count)
	}

	// Validate first entry structure
	if len(entries) > 0 {
		e := entries[0]
		t.Logf("\nFirst entry:")
		t.Logf("  ID: %s", e.QuestionID)
		t.Logf("  Type: %s", e.QuestionType)
		t.Logf("  Question: %.100s...", e.Question)
		t.Logf("  Answer: %.100s...", e.Answer)
		t.Logf("  Sessions: %d", len(e.HaystackSessions))
		t.Logf("  Evidence sessions: %v", e.AnswerSessionIDs)
	}
}

// resolveRepoPath resolves a relative path from the repo root (where go.mod lives).
func resolveRepoPath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, p)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return p
}

// TestLongMemEvalBuildCache pre-computes embeddings for all dataset sessions.
//
// Usage:
//
//	GHOST_EMBED_PROVIDER=local \
//	GHOST_BENCH_LONGMEMEVAL=testdata/longmemeval/longmemeval_s_cleaned.json \
//	GHOST_BENCH_EMBED_CACHE=testdata/longmemeval/embed_cache_s.json \
//	  go test ./internal/store/ -run TestLongMemEvalBuildCache -v -timeout 180m
func TestLongMemEvalBuildCache(t *testing.T) {
	datasetPath := os.Getenv("GHOST_BENCH_LONGMEMEVAL")
	cachePath := os.Getenv("GHOST_BENCH_EMBED_CACHE")
	if datasetPath == "" || cachePath == "" {
		t.Skip("GHOST_BENCH_LONGMEMEVAL and GHOST_BENCH_EMBED_CACHE required")
	}

	datasetPath = resolveRepoPath(datasetPath)
	cachePath = resolveRepoPath(cachePath)

	embedder := embedding.NewFromEnv()
	if embedder == nil {
		t.Skip("no embedder configured (set GHOST_EMBED_PROVIDER)")
	}

	t.Logf("Building embed cache: %s", cachePath)
	err := BuildEmbedCache(datasetPath, cachePath, embedder, func(done, total int) {
		if done%100 == 0 || done == total {
			t.Logf("Embedding progress: %d/%d (%.0f%%)", done, total, float64(done)/float64(total)*100)
		}
	})
	if err != nil {
		t.Fatalf("build cache: %v", err)
	}

	// Verify
	cached := embedding.NewCachedEmbedder(embedder, cachePath)
	t.Logf("Cache built: %d entries at %s", cached.Len(), cachePath)
}

func printMetrics(t *testing.T, metrics map[string]float64) {
	t.Helper()
	keys := make([]string, 0, len(metrics))
	for k := range metrics {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("  %-15s %.4f", k, metrics[k])
	}
}

// TestNDCGAtK verifies the NDCG computation.
func TestNDCGAtK(t *testing.T) {
	tests := []struct {
		name      string
		retrieved []string
		relevant  []string
		k         int
		wantMin   float64
		wantMax   float64
	}{
		{
			name:      "perfect ranking",
			retrieved: []string{"a", "b", "c", "d"},
			relevant:  []string{"a", "b"},
			k:         5,
			wantMin:   0.99,
			wantMax:   1.01,
		},
		{
			name:      "reversed ranking",
			retrieved: []string{"c", "d", "a", "b"},
			relevant:  []string{"a", "b"},
			k:         5,
			wantMin:   0.4,
			wantMax:   0.8,
		},
		{
			name:      "no relevant found",
			retrieved: []string{"x", "y", "z"},
			relevant:  []string{"a", "b"},
			k:         5,
			wantMin:   0,
			wantMax:   0.01,
		},
		{
			name:      "empty retrieved",
			retrieved: []string{},
			relevant:  []string{"a"},
			k:         5,
			wantMin:   0,
			wantMax:   0.01,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NDCGAtK(tt.retrieved, tt.relevant, tt.k)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("NDCGAtK() = %f, want [%f, %f]", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

// TestLongMemEvalSingleQuestion runs a single question end-to-end for debugging.
func TestLongMemEvalSingleQuestion(t *testing.T) {
	datasetPath := os.Getenv("GHOST_BENCH_LONGMEMEVAL")
	if datasetPath == "" {
		t.Skip("GHOST_BENCH_LONGMEMEVAL not set")
	}

	qIdx := 0
	if s := os.Getenv("GHOST_BENCH_QUESTION"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			qIdx = n
		}
	}

	datasetPath = resolveRepoPath(datasetPath)

	entries, err := LoadLongMemEval(datasetPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if qIdx >= len(entries) {
		t.Fatalf("question index %d out of range (max %d)", qIdx, len(entries)-1)
	}

	entry := entries[qIdx]
	t.Logf("Question %d: %s", qIdx, entry.QuestionID)
	t.Logf("Type: %s", entry.QuestionType)
	t.Logf("Q: %s", entry.Question)
	t.Logf("A: %s", entry.Answer)
	t.Logf("Evidence sessions: %v", entry.AnswerSessionIDs)
	t.Logf("Haystack sessions: %d", len(entry.HaystackSessions))

	// Create store and ingest
	dir := t.TempDir()
	s, err := NewSQLiteStore(filepath.Join(dir, "bench.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer s.Close()

	ctx := t.Context()
	ns := "bench:longmemeval"

	for j, session := range entry.HaystackSessions {
		sessionID := fmt.Sprintf("session-%d", j)
		if j < len(entry.HaystackIDs) {
			sessionID = entry.HaystackIDs[j]
		}
		content := sessionContent(session)
		if content == "" {
			continue
		}
		if _, err := s.Put(ctx, PutParams{
			NS:      ns,
			Key:     sessionID,
			Content: content,
			Kind:    "episodic",
			Tier:    "stm",
		}); err != nil {
			t.Fatalf("ingest %s: %v", sessionID, err)
		}
	}

	// Search
	results, err := s.Search(ctx, SearchParams{
		NS:         ns,
		Query:      entry.Question,
		Limit:      50,
		IncludeAll: true,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	t.Logf("\nTop 10 results:")
	retrieved := make([]string, len(results))
	for i, r := range results {
		retrieved[i] = r.Key
		isEvidence := ""
		for _, eid := range entry.AnswerSessionIDs {
			if r.Key == eid {
				isEvidence = " *** EVIDENCE ***"
				break
			}
		}
		if i < 10 {
			preview := r.Content
			if len(preview) > 120 {
				preview = preview[:120] + "..."
			}
			t.Logf("  [%d] %s (sim=%.3f)%s", i+1, r.Key, r.Similarity, isEvidence)
			t.Logf("      %s", preview)
		}
	}

	// Metrics
	t.Logf("\nMetrics:")
	for _, k := range []int{5, 10, 50} {
		t.Logf("  Recall@%d:    %.4f", k, RecallAtK(retrieved, entry.AnswerSessionIDs, k))
		t.Logf("  NDCG@%d:      %.4f", k, NDCGAtK(retrieved, entry.AnswerSessionIDs, k))
		t.Logf("  Precision@%d: %.4f", k, PrecisionAtK(retrieved, entry.AnswerSessionIDs, k))
	}
}

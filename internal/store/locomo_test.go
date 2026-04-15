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

// TestLoCoMo runs the LoCoMo retrieval benchmark against ghost's search.
//
// Requires: GHOST_BENCH_LOCOMO env var pointing to locomo10.json.
//
// Usage:
//
//	GHOST_BENCH_LOCOMO=testdata/locomo/locomo10.json \
//	  go test ./internal/store/ -run TestLoCoMo -v -timeout 30m
//
//	# With embeddings + cache:
//	GHOST_EMBED_PROVIDER=local \
//	GHOST_BENCH_LOCOMO=testdata/locomo/locomo10.json \
//	GHOST_BENCH_EMBED_CACHE=testdata/locomo/embed_cache.json \
//	  go test ./internal/store/ -run TestLoCoMo -v -timeout 30m
func TestLoCoMo(t *testing.T) {
	datasetPath := os.Getenv("GHOST_BENCH_LOCOMO")
	if datasetPath == "" {
		t.Skip("GHOST_BENCH_LOCOMO not set — skipping LoCoMo benchmark")
	}
	datasetPath = resolveRepoPath(datasetPath)
	if _, err := os.Stat(datasetPath); os.IsNotExist(err) {
		t.Skipf("dataset not found at %s", datasetPath)
	}

	perCatLimit := 0
	if s := os.Getenv("GHOST_BENCH_PER_CAT"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			perCatLimit = n
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
	prf := os.Getenv("GHOST_BENCH_PRF") == "1"
	mmr := os.Getenv("GHOST_BENCH_MMR") == "1"

	cfg := LoCoMoConfig{
		DatasetPath:    datasetPath,
		PerCatLimit:    perCatLimit,
		TopK:           []int{5, 10},
		EmbedCachePath: cachePath,
		ExpandEdges:    expandEdges,
		MultiQuery:     multiQuery,
		PRF:            prf,
		MMR:            mmr,
		ProgressFunc: func(done, total int) {
			t.Logf("Progress: %d/%d (%.0f%%)", done, total, float64(done)/float64(total)*100)
		},
	}

	report, err := RunLoCoMo(cfg, func() (*SQLiteStore, func(), error) {
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
	t.Logf("\n=== LoCoMo Benchmark Results ===")
	t.Logf("Dataset: %s", report.Dataset)
	t.Logf("QA pairs evaluated: %d", report.Total)
	t.Logf("")

	t.Logf("── Overall ──")
	printMetrics(t, report.Overall)

	cats := make([]string, 0, len(report.ByCat))
	for c := range report.ByCat {
		cats = append(cats, c)
	}
	sort.Strings(cats)

	for _, cat := range cats {
		agg := report.ByCat[cat]
		t.Logf("")
		t.Logf("── %s (n=%d) ──", cat, agg.Count)
		printMetrics(t, agg.Metrics)
	}

	jsonReport, _ := json.MarshalIndent(report, "", "  ")
	t.Logf("\nLOCOMO_REPORT:%s", string(jsonReport))
}

// TestLoCoMoLoad verifies dataset parsing.
func TestLoCoMoLoad(t *testing.T) {
	datasetPath := os.Getenv("GHOST_BENCH_LOCOMO")
	if datasetPath == "" {
		t.Skip("GHOST_BENCH_LOCOMO not set")
	}
	datasetPath = resolveRepoPath(datasetPath)

	entries, err := LoadLoCoMo(datasetPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	t.Logf("Loaded %d conversations", len(entries))
	totalQA := 0
	catCounts := make(map[int]int)
	for _, e := range entries {
		sessions := parseLoCoMoSessions(e.Conversation)
		t.Logf("  %s: %d sessions, %d QA pairs", e.SampleID, len(sessions), len(e.QA))
		totalQA += len(e.QA)
		for _, qa := range e.QA {
			catCounts[qa.Category]++
		}
	}
	t.Logf("Total QA: %d", totalQA)
	for cat, count := range catCounts {
		t.Logf("  Category %d (%s): %d", cat, categoryName(cat), count)
	}
}

// TestLoCoMoBuildCache pre-computes embeddings for all LoCoMo sessions.
func TestLoCoMoBuildCache(t *testing.T) {
	datasetPath := os.Getenv("GHOST_BENCH_LOCOMO")
	cachePath := os.Getenv("GHOST_BENCH_EMBED_CACHE")
	if datasetPath == "" || cachePath == "" {
		t.Skip("GHOST_BENCH_LOCOMO and GHOST_BENCH_EMBED_CACHE required")
	}
	datasetPath = resolveRepoPath(datasetPath)
	cachePath = resolveRepoPath(cachePath)

	embedder := embedding.NewFromEnv()
	if embedder == nil {
		t.Skip("no embedder configured")
	}

	entries, err := LoadLoCoMo(datasetPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	cached := embedding.NewCachedEmbedder(embedder, cachePath)
	total := 0
	done := 0

	// Collect all unique texts
	var allTexts []string
	seen := make(map[string]bool)
	for _, entry := range entries {
		sessions := parseLoCoMoSessions(entry.Conversation)
		for _, s := range sessions {
			h := embedding.ContentHash(s.content)
			if !seen[h] {
				seen[h] = true
				allTexts = append(allTexts, s.content)
			}
			// Also cache speaker-turn windowed chunks
			for _, chunks := range extractSpeakerTurns(s.content) {
				for _, chunk := range chunks {
					ch := embedding.ContentHash(chunk)
					if !seen[ch] {
						seen[ch] = true
						allTexts = append(allTexts, chunk)
					}
				}
			}
		}
		for _, qa := range entry.QA {
			h := embedding.ContentHash(qa.Question)
			if !seen[h] {
				seen[h] = true
				allTexts = append(allTexts, qa.Question)
			}
		}
	}
	total = len(allTexts)
	t.Logf("Unique texts to embed: %d", total)

	// Batch embed
	const batchSize = 32
	for i := 0; i < len(allTexts); i += batchSize {
		end := i + batchSize
		if end > len(allTexts) {
			end = len(allTexts)
		}
		if _, err := cached.EmbedBatch(t.Context(), allTexts[i:end]); err != nil {
			t.Fatalf("embed batch: %v", err)
		}
		done += end - i
		if done%100 == 0 || done == total {
			t.Logf("Progress: %d/%d (%.0f%%)", done, total, float64(done)/float64(total)*100)
		}
	}

	if err := cached.Save(); err != nil {
		t.Fatalf("save cache: %v", err)
	}
	t.Logf("Cache built: %d entries at %s", cached.Len(), cachePath)
}

func TestEvidenceToSessions(t *testing.T) {
	tests := []struct {
		evidence []string
		want     []string
	}{
		{[]string{"D1:3"}, []string{"session_1"}},
		{[]string{"D1:3", "D1:5"}, []string{"session_1"}},
		{[]string{"D1:3", "D15:7"}, []string{"session_1", "session_15"}},
		{[]string{}, nil},
	}
	for _, tt := range tests {
		got := evidenceToSessions(tt.evidence)
		sort.Strings(got)
		sort.Strings(tt.want)
		if len(got) != len(tt.want) {
			t.Errorf("evidenceToSessions(%v) = %v, want %v", tt.evidence, got, tt.want)
		}
	}
}

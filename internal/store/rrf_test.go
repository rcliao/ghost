package store

import (
	"context"
	"testing"
)

func TestRRFScore(t *testing.T) {
	// Single method, rank 1: 1/(60+1) ≈ 0.01639
	score := rrfScore([]int{1}, 60)
	if score < 0.016 || score > 0.017 {
		t.Errorf("expected ~0.01639, got %f", score)
	}

	// Two methods, both rank 1: 2/(60+1) ≈ 0.03279
	score2 := rrfScore([]int{1, 1}, 60)
	if score2 < 0.032 || score2 > 0.034 {
		t.Errorf("expected ~0.03279, got %f", score2)
	}

	// Result appearing in two methods ranked higher than one method
	oneMethod := rrfScore([]int{1}, 60)
	twoMethods := rrfScore([]int{5, 5}, 60)
	if twoMethods <= oneMethod {
		t.Error("result in two methods should score higher than one method rank 1")
	}

	// Higher rank = higher score within same method count
	highRank := rrfScore([]int{1}, 60)
	lowRank := rrfScore([]int{10}, 60)
	if highRank <= lowRank {
		t.Error("rank 1 should score higher than rank 10")
	}
}

func TestSearchRRFFusion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Create memories that match differently across FTS and LIKE
	s.Put(ctx, PutParams{NS: "test", Key: "exact-match", Content: "golang programming language"})
	s.Put(ctx, PutParams{NS: "test", Key: "partial-match", Content: "go is a compiled language"})
	s.Put(ctx, PutParams{NS: "test", Key: "no-match", Content: "python scripting"})

	results, err := s.Search(ctx, SearchParams{Query: "golang", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}

	if len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}

	// The exact match should be ranked first (appears in both FTS and LIKE)
	if results[0].Key != "exact-match" {
		t.Errorf("expected exact-match first, got %q", results[0].Key)
	}

	// All results should have a Similarity (RRF score) > 0
	for _, r := range results {
		if r.Similarity <= 0 {
			t.Errorf("expected positive RRF score for %q, got %f", r.Key, r.Similarity)
		}
	}
}

func TestSearchRRFMultiMethodBoost(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// A memory that matches both FTS and LIKE should rank higher than one
	// that only matches LIKE (e.g., key match only)
	s.Put(ctx, PutParams{NS: "test", Key: "content-match", Content: "database optimization techniques"})
	s.Put(ctx, PutParams{NS: "test", Key: "database-key", Content: "unrelated content here"})

	results, err := s.Search(ctx, SearchParams{Query: "database", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}

	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}

	// Content match appears in both FTS and LIKE → higher RRF score
	if results[0].Key != "content-match" {
		t.Errorf("expected content-match ranked first (multi-method boost), got %q", results[0].Key)
	}
}

func TestSearchRRFDeduplicates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "mem1", Content: "unique search term xyzzy"})

	results, err := s.Search(ctx, SearchParams{Query: "xyzzy", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}

	// Should only appear once even though it matches both FTS and LIKE
	count := 0
	for _, r := range results {
		if r.Key == "mem1" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected mem1 to appear once, appeared %d times", count)
	}
}

func TestSearchStopWordsStillWork(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "mem1", Content: "the quick brown fox"})

	// Query with all stop words should still return results via LIKE fallback
	results, err := s.Search(ctx, SearchParams{Query: "the", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}

	if len(results) == 0 {
		t.Error("expected results even for stop-word-only query")
	}
}

package store

import (
	"context"
	"testing"
	"time"
)

func TestPeekEmpty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	result, err := s.Peek(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.MemoryCounts) != 0 {
		t.Errorf("expected empty memory counts, got %v", result.MemoryCounts)
	}
	if len(result.HighImportance) != 0 {
		t.Errorf("expected empty high importance, got %v", result.HighImportance)
	}
	if len(result.RecentTopics) != 0 {
		t.Errorf("expected empty recent topics, got %v", result.RecentTopics)
	}
}

func TestPeekWithMemories(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Insert memories with different tiers
	s.Put(ctx, PutParams{NS: "test", Key: "stm1", Content: "short term memory", Tags: []string{"golang", "testing"}})
	s.Put(ctx, PutParams{NS: "test", Key: "stm2", Content: "another stm", Tags: []string{"debug"}})
	s.Put(ctx, PutParams{NS: "test", Key: "important", Content: "very important memory", Importance: 0.9})

	// Manually set a memory as pinned
	s.db.Exec(`UPDATE memories SET pinned = 1, content = 'I am a test agent' WHERE key = 'important'`)

	result, err := s.Peek(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}

	// Check tier counts
	if result.MemoryCounts["stm"] != 3 {
		t.Errorf("expected 3 stm memories, got %d", result.MemoryCounts["stm"])
	}

	// Check pinned summary
	if result.PinnedSummary == "" {
		t.Error("expected pinned summary")
	}

	// Check recent topics
	if len(result.RecentTopics) == 0 {
		t.Error("expected recent topics")
	}

	// Check high importance has entries
	if len(result.HighImportance) == 0 {
		t.Error("expected high importance entries")
	}
	if len(result.HighImportance) > 5 {
		t.Errorf("expected at most 5 high importance, got %d", len(result.HighImportance))
	}

	// Check summary truncation
	for _, stub := range result.HighImportance {
		if len(stub.Summary) > 83 { // 80 + "..."
			t.Errorf("summary too long: %d chars", len(stub.Summary))
		}
	}
}

func TestPeekNamespaceFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "proj:a", Key: "mem1", Content: "project a memory"})
	s.Put(ctx, PutParams{NS: "proj:b", Key: "mem2", Content: "project b memory"})

	result, err := s.Peek(ctx, "proj:a")
	if err != nil {
		t.Fatal(err)
	}

	total := 0
	for _, c := range result.MemoryCounts {
		total += c
	}
	if total != 1 {
		t.Errorf("expected 1 memory for proj:a, got %d", total)
	}
}

func TestPeekTokenTotals(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "mem1", Content: "hello world"})
	s.Put(ctx, PutParams{NS: "test", Key: "mem2", Content: "another memory with more content"})

	result, err := s.Peek(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}

	stmTokens := result.TotalEstTokens["stm"]
	if stmTokens <= 0 {
		t.Errorf("expected positive token count for stm, got %d", stmTokens)
	}
}

func TestContextTierAwarePinning(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Create memories in different tiers
	s.Put(ctx, PutParams{NS: "test", Key: "identity-mem", Content: "I am a helpful assistant", Importance: 0.95, Pinned: true})
	s.Put(ctx, PutParams{NS: "test", Key: "ltm-mem", Content: "user prefers dark mode", Importance: 0.8, Pinned: true})
	s.Put(ctx, PutParams{NS: "test", Key: "stm-mem", Content: "working on search feature today"})

	// Context loads pinned memories first
	result, err := s.Context(ctx, ContextParams{
		NS:     "test",
		Query:  "search",
		Budget: 4000,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Memories) == 0 {
		t.Fatal("expected at least some memories")
	}

	// First memories should be from pinned tiers (identity/ltm)
	foundPinned := false
	for _, m := range result.Memories {
		if m.Key == "identity-mem" || m.Key == "ltm-mem" {
			foundPinned = true
			break
		}
	}
	if !foundPinned {
		t.Error("expected pinned tier memories to be included")
	}
}

func TestContextNoPinTiersBackwardsCompat(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "mem1", Content: "test memory for search"})

	// Call without PinTiers — should behave exactly like before
	result, err := s.Context(ctx, ContextParams{
		NS:    "test",
		Query: "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Memories) != 1 {
		t.Errorf("expected 1 memory, got %d", len(result.Memories))
	}
}

func TestContextPinBudgetRespected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Create a large identity memory and a search-relevant memory
	largeContent := string(make([]byte, 2000)) // ~500 tokens
	s.Put(ctx, PutParams{NS: "test", Key: "big-identity", Content: largeContent, Importance: 0.9})
	s.db.Exec(`UPDATE memories SET tier = 'identity' WHERE key = 'big-identity'`)

	s.Put(ctx, PutParams{NS: "test", Key: "search-hit", Content: "relevant search result"})

	result, err := s.Context(ctx, ContextParams{
		NS:        "test",
		Query:     "relevant",
		Budget:    1000,
		PinTiers:  []string{"identity"},
		PinBudget: 100, // very small pin budget — large identity won't fit
	})
	if err != nil {
		t.Fatal(err)
	}

	// The large identity memory shouldn't be included due to pin budget
	for _, m := range result.Memories {
		if m.Key == "big-identity" {
			t.Error("large identity memory should not fit in small pin budget")
		}
	}
}

// Verify that we don't have defer-on-deferred-rows issue by running Peek with data
func TestPeekDoesNotUpdateAccess(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "mem1", Content: "peek test memory"})

	// Get initial access count
	var accessBefore int
	s.db.QueryRow(`SELECT access_count FROM memories WHERE key = 'mem1'`).Scan(&accessBefore)

	// Peek should not update access
	_, err := s.Peek(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}

	// Allow a moment for any async updates
	time.Sleep(10 * time.Millisecond)

	var accessAfter int
	s.db.QueryRow(`SELECT access_count FROM memories WHERE key = 'mem1'`).Scan(&accessAfter)

	if accessAfter != accessBefore {
		t.Errorf("peek should not update access count, was %d now %d", accessBefore, accessAfter)
	}
}

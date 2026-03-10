package store

import (
	"context"
	"testing"
)

func TestContextBasic(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	ctx := context.Background()

	// Put some memories
	s.Put(ctx, PutParams{NS: "test", Key: "go-lang", Content: "Go is a statically typed language"})
	s.Put(ctx, PutParams{NS: "test", Key: "rust-lang", Content: "Rust is a systems language with borrow checker"})
	s.Put(ctx, PutParams{NS: "test", Key: "python-lang", Content: "Python is a dynamic language popular for ML"})

	result, err := s.Context(ctx, ContextParams{
		NS:     "test",
		Query:  "language",
		Budget: 4000,
	})
	if err != nil {
		t.Fatalf("context: %v", err)
	}

	if len(result.Memories) == 0 {
		t.Fatal("expected at least one memory in context")
	}
	if result.Budget != 4000 {
		t.Errorf("expected budget 4000, got %d", result.Budget)
	}
}

func TestContextBudgetLimit(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	ctx := context.Background()

	// Put a large memory
	longContent := ""
	for i := 0; i < 100; i++ {
		longContent += "This is a line about programming languages and their features. "
	}
	s.Put(ctx, PutParams{NS: "test", Key: "big", Content: longContent})
	s.Put(ctx, PutParams{NS: "test", Key: "small", Content: "Go is great for programming"})

	// Very small budget
	result, err := s.Context(ctx, ContextParams{
		NS:     "test",
		Query:  "programming",
		Budget: 50, // ~200 chars
	})
	if err != nil {
		t.Fatalf("context: %v", err)
	}

	// Should still return something (excerpt or small memory)
	if len(result.Memories) == 0 {
		t.Fatal("expected at least one memory even with small budget")
	}
}

func TestContextEmpty(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	ctx := context.Background()

	result, err := s.Context(ctx, ContextParams{
		Query:  "nothing here",
		Budget: 4000,
	})
	if err != nil {
		t.Fatalf("context: %v", err)
	}

	if len(result.Memories) != 0 {
		t.Errorf("expected empty memories, got %d", len(result.Memories))
	}
}

func TestKindWeightsSum(t *testing.T) {
	// All kind weight vectors must sum to 1.0 (tier is now multiplicative, not additive)
	for _, kind := range []string{"episodic", "semantic", "procedural"} {
		w := kindWeights(kind)
		sum := w.relevance + w.recency + w.importance + w.access
		if sum < 0.99 || sum > 1.01 {
			t.Errorf("kindWeights(%q) sums to %f, want 1.0", kind, sum)
		}
	}
}

func TestKindWeightsEpisodicFavorsRecency(t *testing.T) {
	ep := kindWeights("episodic")
	sem := kindWeights("semantic")
	if ep.recency <= sem.recency {
		t.Errorf("episodic recency weight (%f) should exceed semantic (%f)", ep.recency, sem.recency)
	}
}

func TestKindWeightsProceduralFavorsAccess(t *testing.T) {
	proc := kindWeights("procedural")
	sem := kindWeights("semantic")
	if proc.access <= sem.access {
		t.Errorf("procedural access weight (%f) should exceed semantic (%f)", proc.access, sem.access)
	}
}

func TestTierMultiplierOrdering(t *testing.T) {
	ltm := tierMultiplier("ltm")
	stm := tierMultiplier("stm")
	dormant := tierMultiplier("dormant")
	sensory := tierMultiplier("sensory")

	if ltm <= stm {
		t.Errorf("ltm (%f) should be > stm (%f)", ltm, stm)
	}
	if stm <= dormant {
		t.Errorf("stm (%f) should be > dormant (%f)", stm, dormant)
	}
	if sensory >= dormant {
		t.Errorf("sensory (%f) should be < dormant (%f)", sensory, dormant)
	}
	// Dormant should meaningfully suppress: multiplier < 0.2
	if dormant >= 0.2 {
		t.Errorf("dormant multiplier (%f) should be < 0.2 to meaningfully suppress", dormant)
	}
}

func TestKindDefaultByTier(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	ctx := context.Background()

	// stm tier → episodic default
	stmMem, _ := s.Put(ctx, PutParams{NS: "test", Key: "stm-mem", Content: "stm observation"})
	if stmMem.Kind != "episodic" {
		t.Errorf("stm tier default kind: want episodic, got %s", stmMem.Kind)
	}

	// sensory tier → episodic default
	senMem, _ := s.Put(ctx, PutParams{NS: "test", Key: "sen-mem", Content: "sensory input", Tier: "sensory"})
	if senMem.Kind != "episodic" {
		t.Errorf("sensory tier default kind: want episodic, got %s", senMem.Kind)
	}

	// ltm tier → semantic default
	ltmMem, _ := s.Put(ctx, PutParams{NS: "test", Key: "ltm-mem", Content: "proven fact", Tier: "ltm"})
	if ltmMem.Kind != "semantic" {
		t.Errorf("ltm tier default kind: want semantic, got %s", ltmMem.Kind)
	}

	// identity tier → semantic default
	idMem, _ := s.Put(ctx, PutParams{NS: "test", Key: "id-mem", Content: "core truth", Tier: "identity"})
	if idMem.Kind != "semantic" {
		t.Errorf("identity tier default kind: want semantic, got %s", idMem.Kind)
	}

	// explicit kind always wins
	explMem, _ := s.Put(ctx, PutParams{NS: "test", Key: "expl-mem", Content: "how to do X", Kind: "procedural"})
	if explMem.Kind != "procedural" {
		t.Errorf("explicit kind should win: want procedural, got %s", explMem.Kind)
	}
}

func TestContextPriorityBoosting(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	ctx := context.Background()

	// Put memories with different priorities
	s.Put(ctx, PutParams{NS: "test", Key: "low-pri", Content: "low priority info about coding", Priority: "low"})
	s.Put(ctx, PutParams{NS: "test", Key: "critical-pri", Content: "critical info about coding", Priority: "critical"})

	result, err := s.Context(ctx, ContextParams{
		NS:     "test",
		Query:  "coding",
		Budget: 4000,
	})
	if err != nil {
		t.Fatalf("context: %v", err)
	}

	if len(result.Memories) < 2 {
		t.Fatalf("expected 2 memories, got %d", len(result.Memories))
	}

	// Critical should score higher
	if result.Memories[0].Key != "critical-pri" {
		t.Errorf("expected critical-pri first, got %s", result.Memories[0].Key)
	}
}

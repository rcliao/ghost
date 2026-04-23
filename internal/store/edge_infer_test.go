package store

import (
	"context"
	"strings"
	"testing"
)

// mockLLM returns a canned response for InferLLMClient.
type mockLLM struct {
	responses []string
	calls     int
}

func (m *mockLLM) Generate(ctx context.Context, _, _ string) (string, error) {
	if m.calls >= len(m.responses) {
		return `{"rel": "none", "reason": "exhausted"}`, nil
	}
	resp := m.responses[m.calls]
	m.calls++
	return resp, nil
}

func TestInferEdges(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "deadline", Content: "Big deadline Friday"})
	s.Put(ctx, PutParams{NS: "test", Key: "anxiety", Content: "Feeling anxious about work"})
	s.Put(ctx, PutParams{NS: "test", Key: "vegan", Content: "I am vegan"})
	s.Put(ctx, PutParams{NS: "test", Key: "cheese", Content: "I don't eat cheese"})

	// Seed relates_to edges so InferEdges has candidates
	s.CreateEdge(ctx, EdgeParams{FromNS: "test", FromKey: "anxiety", ToNS: "test", ToKey: "deadline", Rel: "relates_to"})
	s.CreateEdge(ctx, EdgeParams{FromNS: "test", FromKey: "vegan", ToNS: "test", ToKey: "cheese", Rel: "relates_to"})

	llm := &mockLLM{responses: []string{
		`{"rel": "caused_by", "reason": "deadline causes anxiety"}`,
		`{"rel": "implies", "reason": "vegan implies no cheese"}`,
	}}

	res, err := s.InferEdges(ctx, InferEdgesParams{NS: "test", LLM: llm, MaxPairs: 10})
	if err != nil {
		t.Fatalf("InferEdges: %v", err)
	}
	if res.PairsExamined != 2 {
		t.Errorf("examined=%d want 2", res.PairsExamined)
	}
	if res.EdgesCreated != 2 {
		t.Errorf("created=%d want 2", res.EdgesCreated)
	}

	// Verify the edges exist
	edges, _ := s.GetEdgesByNSKey(ctx, "test", "anxiety")
	found := false
	for _, e := range edges {
		if e.Rel == "caused_by" {
			found = true
		}
	}
	if !found {
		t.Error("expected caused_by edge from anxiety")
	}
}

func TestListReasoningCandidates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "deadline", Content: "Big deadline Friday"})
	s.Put(ctx, PutParams{NS: "test", Key: "anxiety", Content: "Feeling anxious about work"})
	s.Put(ctx, PutParams{NS: "test", Key: "vegan", Content: "I am vegan"})
	s.Put(ctx, PutParams{NS: "test", Key: "cheese", Content: "I don't eat cheese"})
	s.Put(ctx, PutParams{NS: "test", Key: "solo", Content: "Unconnected memory"})

	// 2 candidate relates_to pairs
	s.CreateEdge(ctx, EdgeParams{FromNS: "test", FromKey: "anxiety", ToNS: "test", ToKey: "deadline", Rel: "relates_to"})
	s.CreateEdge(ctx, EdgeParams{FromNS: "test", FromKey: "vegan", ToNS: "test", ToKey: "cheese", Rel: "relates_to"})

	res, err := s.ListReasoningCandidates(ctx, ReasoningCandidatesParams{NS: "test"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(res.Candidates) != 2 {
		t.Fatalf("candidates=%d want 2", len(res.Candidates))
	}
	if res.SkippedExisting != 0 {
		t.Errorf("skipped_existing=%d want 0", res.SkippedExisting)
	}

	// After adding a caused_by edge on one pair, that pair should be excluded
	s.CreateEdge(ctx, EdgeParams{FromNS: "test", FromKey: "anxiety", ToNS: "test", ToKey: "deadline", Rel: "caused_by"})
	res2, _ := s.ListReasoningCandidates(ctx, ReasoningCandidatesParams{NS: "test"})
	if len(res2.Candidates) != 1 {
		t.Errorf("candidates after reasoning edge=%d want 1", len(res2.Candidates))
	}
	if res2.SkippedExisting != 1 {
		t.Errorf("skipped_existing=%d want 1", res2.SkippedExisting)
	}

	// Seed filter
	res3, _ := s.ListReasoningCandidates(ctx, ReasoningCandidatesParams{NS: "test", Seed: []string{"vegan"}})
	if len(res3.Candidates) != 1 || res3.Candidates[0].FromKey != "vegan" {
		t.Errorf("seed filter failed: %+v", res3.Candidates)
	}
}

func TestInferEdgesIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "a", Content: "memory a"})
	s.Put(ctx, PutParams{NS: "test", Key: "b", Content: "memory b"})
	s.CreateEdge(ctx, EdgeParams{FromNS: "test", FromKey: "a", ToNS: "test", ToKey: "b", Rel: "relates_to"})
	s.CreateEdge(ctx, EdgeParams{FromNS: "test", FromKey: "a", ToNS: "test", ToKey: "b", Rel: "caused_by"})

	llm := &mockLLM{responses: []string{`{"rel": "implies", "reason": "would create duplicate"}`}}
	res, _ := s.InferEdges(ctx, InferEdgesParams{NS: "test", LLM: llm, MaxPairs: 10})

	if res.EdgesSkipped != 1 {
		t.Errorf("skipped=%d want 1 (existing reasoning edge)", res.EdgesSkipped)
	}
	if res.EdgesCreated != 0 {
		t.Errorf("created=%d want 0", res.EdgesCreated)
	}
}

func TestParseInferResponse(t *testing.T) {
	cases := []struct {
		raw, wantRel string
	}{
		{`{"rel": "caused_by", "reason": "foo"}`, "caused_by"},
		{`{"rel":"implies","reason":"bar"}`, "implies"},
		{`Analysis: this is a prevents relationship`, "prevents"},
		{`{"rel": "none"}`, "none"},
		{``, ""},
	}
	for _, c := range cases {
		got, _ := parseInferResponse(c.raw)
		if got != c.wantRel {
			t.Errorf("parseInferResponse(%q) rel=%q want %q", c.raw, got, c.wantRel)
		}
	}
}

// Ensure mockLLM and InferLLMClient are compatible
var _ InferLLMClient = (*mockLLM)(nil)

// quiet unused warnings when building
var _ = strings.TrimSpace

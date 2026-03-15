package store

import (
	"context"
	"testing"
)

func TestExpandListNodesEmpty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// No consolidation nodes — should return empty list
	result, err := s.Expand(ctx, ExpandParams{NS: "test"})
	if err != nil {
		t.Fatalf("expand list: %v", err)
	}
	if len(result.Nodes) != 0 {
		t.Errorf("expected 0 nodes, got %d", len(result.Nodes))
	}
}

func TestExpandListNodes(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Create source memories and a consolidation node
	s.Put(ctx, PutParams{NS: "test", Key: "detail-a", Content: "first detail memory"})
	s.Put(ctx, PutParams{NS: "test", Key: "detail-b", Content: "second detail memory"})
	s.Put(ctx, PutParams{NS: "test", Key: "detail-c", Content: "third detail memory"})

	_, err := s.Consolidate(ctx, ConsolidateParams{
		NS:         "test",
		SummaryKey: "summary-1",
		Content:    "Summary of details A and B",
		SourceKeys: []string{"detail-a", "detail-b"},
	})
	if err != nil {
		t.Fatalf("consolidate: %v", err)
	}

	// List consolidation nodes
	result, err := s.Expand(ctx, ExpandParams{NS: "test"})
	if err != nil {
		t.Fatalf("expand list: %v", err)
	}
	if len(result.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(result.Nodes))
	}
	if result.Nodes[0].Key != "summary-1" {
		t.Errorf("expected key summary-1, got %s", result.Nodes[0].Key)
	}
	if result.Nodes[0].Children != 2 {
		t.Errorf("expected 2 children, got %d", result.Nodes[0].Children)
	}
}

func TestExpandDrillDown(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "child-1", Content: "Auth uses JWT tokens", Kind: "semantic", Importance: 0.5})
	s.Put(ctx, PutParams{NS: "test", Key: "child-2", Content: "Tokens expire in 24h", Kind: "episodic", Importance: 0.6})

	_, err := s.Consolidate(ctx, ConsolidateParams{
		NS:         "test",
		SummaryKey: "auth-summary",
		Content:    "Auth overview: JWT with 24h expiry",
		SourceKeys: []string{"child-1", "child-2"},
	})
	if err != nil {
		t.Fatalf("consolidate: %v", err)
	}

	// Expand the consolidation node
	result, err := s.Expand(ctx, ExpandParams{NS: "test", Key: "auth-summary"})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}

	if result.Parent == nil {
		t.Fatal("expected parent to be set")
	}
	if result.Parent.Key != "auth-summary" {
		t.Errorf("expected parent key auth-summary, got %s", result.Parent.Key)
	}

	if len(result.Children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(result.Children))
	}

	childKeys := map[string]bool{}
	for _, c := range result.Children {
		childKeys[c.Key] = true
		if c.Content == "" {
			t.Errorf("child %s has empty content", c.Key)
		}
	}
	if !childKeys["child-1"] || !childKeys["child-2"] {
		t.Errorf("expected both children, got %v", childKeys)
	}
}

func TestExpandNonConsolidationNode(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// A regular memory with no contains edges
	s.Put(ctx, PutParams{NS: "test", Key: "plain", Content: "just a plain memory"})

	result, err := s.Expand(ctx, ExpandParams{NS: "test", Key: "plain"})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}

	if result.Parent == nil {
		t.Fatal("expected parent to be set")
	}
	if result.Parent.Key != "plain" {
		t.Errorf("expected parent key plain, got %s", result.Parent.Key)
	}
	if len(result.Children) != 0 {
		t.Errorf("expected 0 children for non-consolidation node, got %d", len(result.Children))
	}
}

func TestExpandNamespaceIsolation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Create consolidation in ns "alpha"
	s.Put(ctx, PutParams{NS: "alpha", Key: "a1", Content: "alpha detail 1"})
	s.Put(ctx, PutParams{NS: "alpha", Key: "a2", Content: "alpha detail 2"})
	s.Consolidate(ctx, ConsolidateParams{
		NS: "alpha", SummaryKey: "alpha-summary",
		Content: "Alpha summary", SourceKeys: []string{"a1", "a2"},
	})

	// Listing nodes in a different namespace should return nothing
	result, err := s.Expand(ctx, ExpandParams{NS: "beta"})
	if err != nil {
		t.Fatalf("expand list: %v", err)
	}
	if len(result.Nodes) != 0 {
		t.Errorf("expected 0 nodes in beta namespace, got %d", len(result.Nodes))
	}
}

func TestConsolidateBasic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "src-1", Content: "source memory one"})
	s.Put(ctx, PutParams{NS: "test", Key: "src-2", Content: "source memory two"})
	s.Put(ctx, PutParams{NS: "test", Key: "src-3", Content: "source memory three"})

	result, err := s.Consolidate(ctx, ConsolidateParams{
		NS:         "test",
		SummaryKey: "my-summary",
		Content:    "Summary of three source memories",
		SourceKeys: []string{"src-1", "src-2", "src-3"},
		Tags:       []string{"project:test"},
	})
	if err != nil {
		t.Fatalf("consolidate: %v", err)
	}

	if result.Summary == nil {
		t.Fatal("expected summary memory")
	}
	if result.Summary.Key != "my-summary" {
		t.Errorf("expected key my-summary, got %s", result.Summary.Key)
	}
	if result.Summary.Kind != "semantic" {
		t.Errorf("expected kind semantic, got %s", result.Summary.Kind)
	}
	if result.Summary.Importance != 0.7 {
		t.Errorf("expected importance 0.7, got %f", result.Summary.Importance)
	}
	if len(result.Edges) != 3 {
		t.Errorf("expected 3 edges, got %d", len(result.Edges))
	}
	for _, e := range result.Edges {
		if e.Rel != "contains" {
			t.Errorf("expected contains edge, got %s", e.Rel)
		}
	}
}

func TestConsolidateTooFewKeys(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "only-one", Content: "single memory"})

	_, err := s.Consolidate(ctx, ConsolidateParams{
		NS:         "test",
		SummaryKey: "bad-summary",
		Content:    "Cannot consolidate one memory",
		SourceKeys: []string{"only-one"},
	})
	if err == nil {
		t.Fatal("expected error for < 2 source keys")
	}
}

func TestConsolidateMissingSource(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "exists", Content: "I exist"})

	_, err := s.Consolidate(ctx, ConsolidateParams{
		NS:         "test",
		SummaryKey: "bad-summary",
		Content:    "One source is missing",
		SourceKeys: []string{"exists", "nonexistent"},
	})
	if err == nil {
		t.Fatal("expected error for missing source memory")
	}
}

func TestConsolidateCustomKindAndImportance(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "s1", Content: "source 1"})
	s.Put(ctx, PutParams{NS: "test", Key: "s2", Content: "source 2"})

	result, err := s.Consolidate(ctx, ConsolidateParams{
		NS:         "test",
		SummaryKey: "custom-summary",
		Content:    "Custom kind and importance",
		SourceKeys: []string{"s1", "s2"},
		Kind:       "procedural",
		Importance: 0.9,
	})
	if err != nil {
		t.Fatalf("consolidate: %v", err)
	}

	if result.Summary.Kind != "procedural" {
		t.Errorf("expected kind procedural, got %s", result.Summary.Kind)
	}
	if result.Summary.Importance != 0.9 {
		t.Errorf("expected importance 0.9, got %f", result.Summary.Importance)
	}
}

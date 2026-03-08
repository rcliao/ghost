package embedding

import (
	"context"
	"os"
	"testing"
)

func TestLocalEmbedder_Dims(t *testing.T) {
	e := NewLocalEmbedder()
	if e.Dims() != 384 {
		t.Errorf("expected 384 dims, got %d", e.Dims())
	}
}

// TestLocalEmbedder_Embed is an integration test that downloads the model.
// Skip in CI or short mode.
func TestLocalEmbedder_Embed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping local embedding test in short mode (requires model download)")
	}
	if os.Getenv("CI") != "" {
		t.Skip("skipping local embedding test in CI")
	}

	e := NewLocalEmbedder()
	defer e.Close()

	ctx := context.Background()
	vec, err := e.Embed(ctx, "Hello world")
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}
	if len(vec) != 384 {
		t.Errorf("expected 384 dims, got %d", len(vec))
	}

	// Verify similarity: same text should be identical
	vec2, err := e.Embed(ctx, "Hello world")
	if err != nil {
		t.Fatalf("second Embed failed: %v", err)
	}
	sim := CosineSimilarity(vec, vec2)
	if sim < 0.99 {
		t.Errorf("same text similarity = %f, expected ~1.0", sim)
	}

	// Different text should be less similar
	vec3, err := e.Embed(ctx, "Rust has a borrow checker for memory safety")
	if err != nil {
		t.Fatalf("third Embed failed: %v", err)
	}
	sim2 := CosineSimilarity(vec, vec3)
	if sim2 > 0.9 {
		t.Errorf("different text similarity = %f, expected < 0.9", sim2)
	}
}

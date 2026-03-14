package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// TestEdgeInfluenceEval measures how edge expansion changes context retrieval.
// It creates a realistic memory graph, then compares context results with and
// without edge expansion to quantify the influence of edges on retrieval.
func TestEdgeInfluenceEval(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Scenario: an agent working on an auth system.
	// Some memories are directly relevant to a query, others are only
	// reachable via edges (association, not content match).
	memories := []struct {
		key     string
		content string
	}{
		// Direct hits for "authentication" query
		{"auth-overview", "Authentication uses JWT tokens signed with RSA256 for API access"},
		{"auth-middleware", "The auth middleware validates tokens on every request and rejects expired ones"},

		// Reachable via edges (no "auth" keyword)
		{"jwt-rotation", "Token refresh rotation happens every 24 hours using httpOnly cookies"},
		{"rsa-key-mgmt", "RSA key pairs rotated quarterly, stored in AWS KMS, public keys cached in Redis"},
		{"session-store", "User sessions tracked in Redis with 30 minute sliding expiration"},

		// Contradicting memory
		{"auth-deprecated", "DEPRECATED: Authentication previously used session cookies with CSRF tokens"},

		// Unrelated memories (noise)
		{"db-schema", "Database uses PostgreSQL 15 with UUID primary keys and JSONB columns"},
		{"ci-pipeline", "CI pipeline runs on GitHub Actions with Go 1.22 and parallel test shards"},
		{"deploy-config", "Production deploys via ArgoCD to GKE autopilot with canary rollout"},
	}

	for _, m := range memories {
		s.Put(ctx, PutParams{NS: "eval", Key: m.key, Content: m.content})
	}

	// Create edges that represent real associations
	edges := []struct {
		from, to, rel string
		weight        float64
	}{
		{"auth-overview", "jwt-rotation", "depends_on", 0.8},
		{"auth-overview", "rsa-key-mgmt", "depends_on", 0.7},
		{"auth-overview", "auth-deprecated", "contradicts", 0.9},
		{"auth-middleware", "session-store", "relates_to", 0.6},
		{"jwt-rotation", "rsa-key-mgmt", "relates_to", 0.5},
		// Consolidation: summary contains details
		{"auth-overview", "auth-middleware", "contains", 0.6},
	}

	for _, e := range edges {
		fromMems, _ := s.Get(ctx, GetParams{NS: "eval", Key: e.from})
		toMems, _ := s.Get(ctx, GetParams{NS: "eval", Key: e.to})
		if len(fromMems) == 0 || len(toMems) == 0 {
			t.Fatalf("memory not found for edge %s -> %s", e.from, e.to)
		}
		s.db.ExecContext(ctx,
			`INSERT INTO memory_edges (from_id, to_id, rel, weight, access_count, created_at)
			 VALUES (?, ?, ?, ?, 0, datetime('now'))`,
			fromMems[0].ID, toMems[0].ID, e.rel, e.weight)
	}

	query := "how does authentication work"

	// Run context WITH edge expansion (default)
	resultWith, err := s.Context(ctx, ContextParams{
		NS:    "eval",
		Query: query,
		Budget: 10000,
	})
	if err != nil {
		t.Fatalf("context with edges: %v", err)
	}

	// Run context WITHOUT edge expansion
	disabled := EdgeExpansionConfig{Enabled: false}
	resultWithout, err := s.Context(ctx, ContextParams{
		NS:            "eval",
		Query:         query,
		Budget:        10000,
		EdgeExpansion: &disabled,
	})
	if err != nil {
		t.Fatalf("context without edges: %v", err)
	}

	// Collect keys from each result
	keysWith := map[string]float64{}
	for _, m := range resultWith.Memories {
		keysWith[m.Key] = m.Score
	}
	keysWithout := map[string]float64{}
	for _, m := range resultWithout.Memories {
		keysWithout[m.Key] = m.Score
	}

	// Report
	t.Logf("Query: %q", query)
	t.Logf("")
	t.Logf("=== WITHOUT edge expansion (%d memories) ===", len(resultWithout.Memories))
	for _, m := range resultWithout.Memories {
		t.Logf("  [%.2f] %s: %s", m.Score, m.Key, truncateStr(m.Content, 60))
	}
	t.Logf("")
	t.Logf("=== WITH edge expansion (%d memories) ===", len(resultWith.Memories))
	for _, m := range resultWith.Memories {
		marker := ""
		if _, ok := keysWithout[m.Key]; !ok {
			marker = " [EDGE-ADDED]"
		} else if m.Score > keysWithout[m.Key]+0.01 {
			marker = fmt.Sprintf(" [EDGE-BOOSTED +%.2f]", m.Score-keysWithout[m.Key])
		}
		t.Logf("  [%.2f] %s: %s%s", m.Score, m.Key, truncateStr(m.Content, 60), marker)
	}

	// Evaluate: did edges bring in the expected memories?
	t.Logf("")
	t.Logf("=== Edge Influence Analysis ===")

	// Memories that should be pulled in by edges
	expectedEdgeAdds := []string{"jwt-rotation", "rsa-key-mgmt", "session-store"}
	edgeAdded := 0
	for _, key := range expectedEdgeAdds {
		inWith := keysWith[key] > 0
		inWithout := keysWithout[key] > 0
		if inWith && !inWithout {
			t.Logf("  EDGE-ADDED: %s (score %.2f) — pulled in via edge, not found by search", key, keysWith[key])
			edgeAdded++
		} else if inWith && inWithout {
			boost := keysWith[key] - keysWithout[key]
			if boost > 0.01 {
				t.Logf("  EDGE-BOOSTED: %s (%.2f → %.2f, +%.2f)", key, keysWithout[key], keysWith[key], boost)
			} else {
				t.Logf("  SEARCH-FOUND: %s — found by search alone (score %.2f)", key, keysWithout[key])
			}
		} else if !inWith {
			t.Logf("  MISSED: %s — not in results even with edges", key)
		}
	}

	// Contradicts should appear
	if keysWith["auth-deprecated"] > 0 {
		t.Logf("  CONTRADICTS: auth-deprecated appeared (score %.2f) — conflict surfaced", keysWith["auth-deprecated"])
	} else {
		t.Logf("  CONTRADICTS-MISSED: auth-deprecated not in results")
	}

	// Containment suppression: auth-middleware should be suppressed if auth-overview is present
	if keysWith["auth-overview"] > 0 && keysWith["auth-middleware"] > 0 {
		t.Logf("  SUPPRESSION-FAILED: auth-middleware should be suppressed by auth-overview contains edge")
	} else if keysWith["auth-overview"] > 0 && keysWith["auth-middleware"] == 0 {
		t.Logf("  SUPPRESSION-OK: auth-middleware suppressed by auth-overview (contains edge)")
	}

	// Noise check: unrelated memories should NOT appear
	noise := []string{"db-schema", "ci-pipeline", "deploy-config"}
	for _, key := range noise {
		if keysWith[key] > 0 {
			t.Logf("  NOISE: %s appeared (score %.2f) — should not be in results", key, keysWith[key])
		}
	}

	t.Logf("")
	t.Logf("=== Summary ===")
	t.Logf("  Without edges: %d memories", len(resultWithout.Memories))
	t.Logf("  With edges:    %d memories", len(resultWith.Memories))
	t.Logf("  Edge-added:    %d (memories found only via edges)", edgeAdded)
}

// TestEdgeExpansionScoring verifies the scoring math for edge-propagated candidates.
func TestEdgeExpansionScoring(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Seed memory (will match search)
	seed, _ := s.Put(ctx, PutParams{NS: "eval", Key: "seed", Content: "authentication system overview"})
	// Neighbor (won't match search, only reachable via edge)
	neighbor, _ := s.Put(ctx, PutParams{NS: "eval", Key: "neighbor", Content: "completely unrelated content xyz"})

	// Create edge: seed → neighbor with weight 0.8
	s.db.ExecContext(ctx,
		`INSERT INTO memory_edges (from_id, to_id, rel, weight, access_count, created_at)
		 VALUES (?, ?, 'depends_on', 0.8, 0, datetime('now'))`,
		seed.ID, neighbor.ID)

	// Get context with edges
	result, _ := s.Context(ctx, ContextParams{
		NS:    "eval",
		Query: "authentication",
		Budget: 10000,
	})

	seedScore := 0.0
	neighborScore := 0.0
	for _, m := range result.Memories {
		if m.Key == "seed" {
			seedScore = m.Score
		}
		if m.Key == "neighbor" {
			neighborScore = m.Score
		}
	}

	if seedScore == 0 {
		t.Fatal("seed memory not found in results")
	}

	t.Logf("Seed score: %.4f", seedScore)
	t.Logf("Neighbor score: %.4f", neighborScore)

	if neighborScore > 0 {
		// Verify propagated score follows the formula:
		// propagated = seed_score × edge_weight × damping
		// But capped at 0.3 for edge-only candidates
		expectedMax := seedScore * 0.8 * 0.3 // weight=0.8, damping=0.3
		if expectedMax > 0.3 {
			expectedMax = 0.3
		}
		t.Logf("Expected max propagated: %.4f (seed %.4f × weight 0.8 × damping 0.3, capped at 0.3)", expectedMax, seedScore)
		t.Logf("Actual neighbor score: %.4f", neighborScore)

		if neighborScore > expectedMax+0.01 {
			t.Errorf("neighbor score %.4f exceeds expected max %.4f", neighborScore, expectedMax)
		}
	} else {
		t.Log("Neighbor not in results (edge expansion may not have reached it)")
	}
}

// TestContradictsScoring verifies contradicts edges produce high scores.
func TestContradictsScoring(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seed, _ := s.Put(ctx, PutParams{NS: "eval", Key: "current-approach", Content: "We use JWT authentication with RSA256"})
	contra, _ := s.Put(ctx, PutParams{NS: "eval", Key: "old-approach", Content: "Completely different topic about gardening"})

	s.db.ExecContext(ctx,
		`INSERT INTO memory_edges (from_id, to_id, rel, weight, access_count, created_at)
		 VALUES (?, ?, 'contradicts', 0.9, 0, datetime('now'))`,
		seed.ID, contra.ID)

	result, _ := s.Context(ctx, ContextParams{
		NS:    "eval",
		Query: "JWT authentication",
		Budget: 10000,
	})

	seedScore := 0.0
	contraScore := 0.0
	for _, m := range result.Memories {
		if m.Key == "current-approach" {
			seedScore = m.Score
		}
		if m.Key == "old-approach" {
			contraScore = m.Score
		}
	}

	t.Logf("Seed score: %.4f", seedScore)
	t.Logf("Contradicts score: %.4f", contraScore)

	if seedScore == 0 {
		t.Fatal("seed not in results")
	}

	if contraScore == 0 {
		t.Error("contradicting memory should appear in results")
	} else {
		// Contradicts should get at least 80% of seed's score
		minExpected := seedScore * 0.8
		if contraScore < minExpected-0.01 {
			t.Errorf("contradicts score %.4f < expected min %.4f (80%% of seed)", contraScore, minExpected)
		} else {
			t.Logf("Contradicts score %.4f >= %.4f (80%% of seed) — OK", contraScore, minExpected)
		}
	}
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Try to break at word boundary
	if idx := strings.LastIndex(s[:n], " "); idx > n/2 {
		return s[:idx] + "..."
	}
	return s[:n] + "..."
}

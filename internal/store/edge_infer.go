package store

import (
	"context"
	"fmt"
	"strings"
)

// ── Auto Reasoning Edge Inference ────────────────────────────────
//
// Scans pairs of related memories in a namespace and uses an LLM to classify
// whether a reasoning relationship exists (caused_by, prevents, implies).
// Creates typed edges when the LLM confirms with confidence.
//
// Design principle: Ghost itself remains LLM-free in the hot path (Search,
// Context). InferEdges is a background/offline operation that can be invoked
// explicitly (e.g., nightly) to enrich the edge graph with semantic reasoning
// information that embeddings alone can't capture.
//
// Cost: N pairs × 1 LLM call per pair. Scoped by MaxPairs config to bound cost.

// InferLLMClient is the minimal LLM interface needed for edge inference.
// Matches store.LLMClient to avoid import cycles.
type InferLLMClient interface {
	Generate(ctx context.Context, systemPrompt, userMessage string) (string, error)
}

// InferEdgesParams controls reasoning edge inference.
type InferEdgesParams struct {
	NS       string          // namespace to scan
	LLM      InferLLMClient  // LLM to classify relationships
	MaxPairs int             // max candidate pairs to examine (default 100)
	Seed     []string        // optional: only scan pairs involving these memory keys
	DryRun   bool            // if true, return what would be created without writing
}

// InferredEdge is a single edge proposed by the LLM.
type InferredEdge struct {
	FromKey string  `json:"from_key"`
	ToKey   string  `json:"to_key"`
	Rel     string  `json:"rel"`
	Reason  string  `json:"reason,omitempty"`
	Applied bool    `json:"applied"`
}

// InferResult summarizes an inference run.
type InferResult struct {
	PairsExamined int             `json:"pairs_examined"`
	EdgesCreated  int             `json:"edges_created"`
	EdgesSkipped  int             `json:"edges_skipped"` // already exist
	Inferences    []InferredEdge  `json:"inferences"`
}

// ReasoningCandidatesParams filters which relates_to pairs to surface for
// agent-driven classification.
type ReasoningCandidatesParams struct {
	NS       string   // namespace to scan (required)
	MaxPairs int      // max candidate pairs to return (default 50)
	Seed     []string // optional: only return pairs touching these memory keys
}

// ReasoningCandidate is a relates_to pair with content, surfaced to the caller
// so an LLM agent can classify whether a caused_by / prevents / implies edge
// should replace or augment the relates_to link.
type ReasoningCandidate struct {
	FromKey        string  `json:"from_key"`
	FromContent    string  `json:"from_content"`
	ToKey          string  `json:"to_key"`
	ToContent      string  `json:"to_content"`
	RelatesWeight  float64 `json:"relates_weight"`
}

// ReasoningCandidatesResult is returned by ListReasoningCandidates.
type ReasoningCandidatesResult struct {
	Candidates      []ReasoningCandidate `json:"candidates"`
	SkippedExisting int                  `json:"skipped_existing"` // pairs already with a reasoning edge
}

// ListReasoningCandidates returns relates_to pairs that do NOT yet have a
// typed reasoning edge (caused_by / prevents / implies). This is the
// agent-driven counterpart to InferEdges: Ghost does zero LLM work; the
// caller (which is itself an LLM) classifies each pair in its own reasoning
// and commits edges via CreateEdge.
//
// Ghost's hot path stays LLM-free AND this hygiene-time call also stays
// LLM-free — the LLM loop is owned entirely by the agent.
func (s *SQLiteStore) ListReasoningCandidates(ctx context.Context, p ReasoningCandidatesParams) (*ReasoningCandidatesResult, error) {
	if p.MaxPairs <= 0 {
		p.MaxPairs = 50
	}
	result := &ReasoningCandidatesResult{}

	// Fetch relates_to pairs, skipping those that already have a reasoning edge.
	// Using NOT EXISTS rather than post-filter to keep the database honest about
	// the max_pairs limit — we return up to MaxPairs NEW candidates.
	q := `
		SELECT e.from_id, e.to_id, m1.key, m1.content, m2.key, m2.content, e.weight
		FROM memory_edges e
		INNER JOIN memories m1 ON m1.id = e.from_id
		INNER JOIN memories m2 ON m2.id = e.to_id
		WHERE e.rel = 'relates_to'
		  AND m1.ns = ? AND m2.ns = ?
		  AND m1.deleted_at IS NULL AND m2.deleted_at IS NULL
		  AND NOT EXISTS (
		    SELECT 1 FROM memory_edges e2
		    WHERE e2.from_id = e.from_id AND e2.to_id = e.to_id
		      AND e2.rel IN ('caused_by','prevents','implies')
		  )
		ORDER BY e.weight DESC
		LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, p.NS, p.NS, p.MaxPairs)
	if err != nil {
		return nil, fmt.Errorf("scan pairs: %w", err)
	}
	defer rows.Close()

	seedSet := make(map[string]bool, len(p.Seed))
	for _, k := range p.Seed {
		seedSet[k] = true
	}

	for rows.Next() {
		var fromID, toID, fromKey, fromContent, toKey, toContent string
		var weight float64
		if err := rows.Scan(&fromID, &toID, &fromKey, &fromContent, &toKey, &toContent, &weight); err != nil {
			continue
		}
		if len(seedSet) > 0 && !seedSet[fromKey] && !seedSet[toKey] {
			continue
		}
		// Truncate long content so the caller's prompt budget is predictable.
		result.Candidates = append(result.Candidates, ReasoningCandidate{
			FromKey:       fromKey,
			FromContent:   truncStrN(fromContent, 1500),
			ToKey:         toKey,
			ToContent:     truncStrN(toContent, 1500),
			RelatesWeight: weight,
		})
	}
	rows.Close()

	// Count how many pairs were skipped because they already have a reasoning edge.
	countQ := `
		SELECT COUNT(*)
		FROM memory_edges e
		INNER JOIN memories m1 ON m1.id = e.from_id
		INNER JOIN memories m2 ON m2.id = e.to_id
		WHERE e.rel = 'relates_to'
		  AND m1.ns = ? AND m2.ns = ?
		  AND m1.deleted_at IS NULL AND m2.deleted_at IS NULL
		  AND EXISTS (
		    SELECT 1 FROM memory_edges e2
		    WHERE e2.from_id = e.from_id AND e2.to_id = e.to_id
		      AND e2.rel IN ('caused_by','prevents','implies')
		  )`
	_ = s.db.QueryRowContext(ctx, countQ, p.NS, p.NS).Scan(&result.SkippedExisting)

	return result, nil
}

const inferEdgeSystemPrompt = `You are an expert at identifying reasoning relationships between two pieces of text from a user's memory.

Given two memories (A and B), decide if one of these reasoning relationships holds:
- "caused_by": B is the cause of A (A happened because of B)
- "prevents":  B prevents A (B makes A less likely or impossible)
- "implies":   A logically implies B (if A is true, B must be true)
- "none":      No reasoning relationship (or only generic topical similarity)

Output a JSON object with:
{"rel": "caused_by|prevents|implies|none", "reason": "<one short sentence>"}

Be strict — only output a reasoning relation when the logical link is clear from the text. Generic topical similarity is NOT enough; that's what "relates_to" edges capture.`

// InferEdges runs LLM-assisted reasoning edge inference on memory pairs in a
// namespace. It uses existing relates_to edges as candidate pairs (the LLM only
// examines memories already known to be topically related).
//
// The caller is responsible for rate-limiting and cost. Idempotent: skips pairs
// that already have a reasoning edge.
func (s *SQLiteStore) InferEdges(ctx context.Context, p InferEdgesParams) (*InferResult, error) {
	if p.LLM == nil {
		return nil, fmt.Errorf("LLM client required")
	}
	if p.MaxPairs <= 0 {
		p.MaxPairs = 100
	}
	result := &InferResult{}

	// Find candidate pairs: existing relates_to edges in this namespace.
	// These are already known to be topically related, so a reasoning link
	// (if any) is most likely to be found here.
	q := `
		SELECT e.from_id, e.to_id, m1.key, m1.content, m2.key, m2.content
		FROM memory_edges e
		INNER JOIN memories m1 ON m1.id = e.from_id
		INNER JOIN memories m2 ON m2.id = e.to_id
		WHERE e.rel = 'relates_to'
		  AND m1.ns = ? AND m2.ns = ?
		  AND m1.deleted_at IS NULL AND m2.deleted_at IS NULL
		ORDER BY e.weight DESC
		LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, p.NS, p.NS, p.MaxPairs)
	if err != nil {
		return nil, fmt.Errorf("scan pairs: %w", err)
	}
	defer rows.Close()

	seedSet := make(map[string]bool, len(p.Seed))
	for _, k := range p.Seed {
		seedSet[k] = true
	}

	type pair struct {
		fromID, toID, fromKey, fromContent, toKey, toContent string
	}
	var pairs []pair
	for rows.Next() {
		var pp pair
		if err := rows.Scan(&pp.fromID, &pp.toID, &pp.fromKey, &pp.fromContent, &pp.toKey, &pp.toContent); err != nil {
			continue
		}
		// Apply seed filter if provided
		if len(seedSet) > 0 && !seedSet[pp.fromKey] && !seedSet[pp.toKey] {
			continue
		}
		pairs = append(pairs, pp)
	}
	rows.Close()

	// Check for existing reasoning edges (idempotence)
	existingRel := func(fromID, toID string) string {
		var rel string
		_ = s.db.QueryRowContext(ctx,
			`SELECT rel FROM memory_edges
			 WHERE from_id = ? AND to_id = ?
			   AND rel IN ('caused_by','prevents','implies')
			 LIMIT 1`, fromID, toID).Scan(&rel)
		return rel
	}

	for _, pp := range pairs {
		result.PairsExamined++
		if existingRel(pp.fromID, pp.toID) != "" {
			result.EdgesSkipped++
			continue
		}

		msg := fmt.Sprintf("Memory A (key: %s):\n%s\n\nMemory B (key: %s):\n%s",
			pp.fromKey, truncStrN(pp.fromContent, 1500),
			pp.toKey, truncStrN(pp.toContent, 1500))
		raw, err := p.LLM.Generate(ctx, inferEdgeSystemPrompt, msg)
		if err != nil {
			continue
		}
		rel, reason := parseInferResponse(raw)
		if rel == "none" || rel == "" {
			continue
		}

		inf := InferredEdge{FromKey: pp.fromKey, ToKey: pp.toKey, Rel: rel, Reason: reason}
		if !p.DryRun {
			_, err := s.CreateEdge(ctx, EdgeParams{
				FromNS: p.NS, FromKey: pp.fromKey,
				ToNS: p.NS, ToKey: pp.toKey,
				Rel: rel,
			})
			if err == nil {
				inf.Applied = true
				result.EdgesCreated++
			}
		}
		result.Inferences = append(result.Inferences, inf)
	}
	return result, nil
}

func truncStrN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// parseInferResponse extracts the relation and reason from the LLM output.
// Accepts JSON, but also falls back to keyword scanning for robustness.
func parseInferResponse(raw string) (rel, reason string) {
	raw = strings.TrimSpace(raw)
	lower := strings.ToLower(raw)

	// Keyword detection (ordered: most specific first)
	relCandidates := []string{"caused_by", "prevents", "implies", "none"}
	for _, cand := range relCandidates {
		if strings.Contains(lower, `"rel":"`+cand+`"`) ||
			strings.Contains(lower, `"rel": "`+cand+`"`) ||
			strings.Contains(lower, `'rel': '`+cand+`'`) {
			rel = cand
			break
		}
	}
	if rel == "" {
		// Fallback: first keyword in output
		for _, cand := range relCandidates {
			if strings.Contains(lower, cand) {
				rel = cand
				break
			}
		}
	}

	// Extract reason (best-effort)
	if idx := strings.Index(lower, `"reason"`); idx > 0 {
		// Skip to opening quote of value
		rest := raw[idx:]
		if q1 := strings.IndexByte(rest, ':'); q1 > 0 {
			rest = rest[q1+1:]
			rest = strings.TrimSpace(rest)
			rest = strings.TrimLeft(rest, `"'`)
			if q2 := strings.IndexAny(rest, `"'}`); q2 > 0 {
				reason = strings.TrimSpace(rest[:q2])
			}
		}
	}
	return rel, reason
}

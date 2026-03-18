package store

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/rcliao/ghost/internal/model"
)

// ContextParams holds parameters for context assembly.
type ContextParams struct {
	NS             string
	Query          string
	Kind           string
	Tags           []string
	Budget         int              // max tokens in output
	PinTiers       []string         // tiers always injected first (e.g. ["identity", "ltm"])
	PinBudget      int              // token budget reserved for pinned tiers (default: Budget/3)
	SearchBudget   int              // remaining budget for query-relevant search (default: Budget - PinBudget)
	EdgeExpansion  *EdgeExpansionConfig // edge expansion config; nil means use defaults
}

// ContextMemory is a scored memory for context output.
type ContextMemory struct {
	NS      string  `json:"ns"`
	Key     string  `json:"key"`
	Kind    string  `json:"kind"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
	Excerpt bool    `json:"excerpt,omitempty"`
}

// ContextResult is the assembled context response.
type ContextResult struct {
	Budget              int             `json:"budget"`
	Used                int             `json:"used"`
	Memories            []ContextMemory `json:"memories"`
	Skipped             int             `json:"skipped,omitempty"`
	CompactionSuggested bool            `json:"compaction_suggested,omitempty"`
}

// contextCandidate is a memory with its computed score for context ranking.
type contextCandidate struct {
	memory model.Memory
	score  float64
}

// Context assembles relevant memories within a token budget.
func (s *SQLiteStore) Context(ctx context.Context, p ContextParams) (*ContextResult, error) {
	budget := p.Budget
	if budget <= 0 {
		budget = 4000
	}

	result := &ContextResult{Budget: budget, Memories: []ContextMemory{}}
	usedTokens := 0
	seen := map[string]bool{} // track memory IDs to deduplicate

	// Phase 1: Load pinned memories first (chronically accessible)
	{
		pinBudget := p.PinBudget
		if pinBudget <= 0 {
			pinBudget = budget / 3
		}

		pinned, err := s.loadPinnedMemories(ctx, p.NS)
		if err != nil {
			return nil, fmt.Errorf("load pinned tiers: %w", err)
		}

		for _, m := range pinned {
			if usedTokens >= pinBudget {
				break
			}
			memTokens := m.EstTokens
			if memTokens <= 0 {
				memTokens = (len(m.Content) / 4) + 20
			}
			if usedTokens+memTokens <= pinBudget {
				result.Memories = append(result.Memories, ContextMemory{
					NS:      m.NS,
					Key:     m.Key,
					Kind:    m.Kind,
					Content: m.Content,
					Score:   m.Importance, // pinned memories use importance as score
				})
				usedTokens += memTokens
				seen[m.ID] = true
			}
		}
	}

	// Phase 2: Search-based candidates fill remaining budget
	searchBudget := p.SearchBudget
	if searchBudget <= 0 {
		searchBudget = budget - usedTokens
	}
	if searchBudget < 0 {
		searchBudget = 0
	}

	// Search for candidates (get more than we need for scoring)
	results, err := s.Search(ctx, SearchParams{
		NS:    p.NS,
		Query: p.Query,
		Kind:  p.Kind,
		Tags:  p.Tags,
		Limit: 50,
	})
	if err != nil {
		return nil, err
	}

	if len(results) == 0 && len(result.Memories) == 0 {
		return &ContextResult{Budget: budget, Used: 0, Memories: []ContextMemory{}}, nil
	}

	// Score each memory using kind-specific weights.
	// Cognitive rationale:
	//   Episodic (events): recency dominates — time-bound observations
	//   Semantic (facts): relevance + importance — timeless knowledge
	//   Procedural (skills): access frequency — strengthened by practice (testing effect)
	now := time.Now()

	// scoreMap tracks scores by memory ID for edge boost merging
	scoreMap := map[string]*contextCandidate{}

	for _, r := range results {
		if seen[r.ID] {
			continue // already included from pinned tiers
		}
		m := r.Memory
		score := computeContextScore(m, r.Similarity, now)
		scoreMap[m.ID] = &contextCandidate{memory: m, score: score}
	}

	// Phase 3: Edge expansion — spreading activation
	edgeCfg := DefaultEdgeExpansion()
	if p.EdgeExpansion != nil {
		edgeCfg = *p.EdgeExpansion
	}

	if edgeCfg.Enabled && len(scoreMap) > 0 {
		s.expandEdges(ctx, scoreMap, seen, edgeCfg, now)
	}

	// Collect and sort candidates
	var candidates []contextCandidate
	for _, sc := range scoreMap {
		candidates = append(candidates, *sc)
	}

	// Sort by score descending
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	// Build containment map before packing: for each candidate, find if it's
	// a child of another candidate (via 'contains' edges). If a parent summary
	// is in the candidate pool, its children should be suppressed.
	suppressed := map[string]bool{}
	{
		candidateIDs := map[string]bool{}
		for _, c := range candidates {
			candidateIDs[c.memory.ID] = true
		}
		for _, c := range candidates {
			children, err := s.getContainsChildren(ctx, c.memory.ID)
			if err == nil && len(children) > 0 {
				for _, childID := range children {
					if candidateIDs[childID] {
						suppressed[childID] = true
					}
				}
			}
		}
	}

	// Greedy packing into remaining budget with contains-suppression.
	pinnedCount := len(result.Memories)

	for _, c := range candidates {
		// Skip memories suppressed by a parent summary in the candidate pool
		if suppressed[c.memory.ID] {
			continue
		}

		memTokens := c.memory.EstTokens
		if memTokens <= 0 {
			memTokens = (len(c.memory.Content) / 4) + 20
		}
		if usedTokens+memTokens <= budget {
			result.Memories = append(result.Memories, ContextMemory{
				NS:      c.memory.NS,
				Key:     c.memory.Key,
				Kind:    c.memory.Kind,
				Content: c.memory.Content,
				Score:   math.Round(c.score*100) / 100,
			})
			usedTokens += memTokens
		} else if remainingTokens := budget - usedTokens; remainingTokens >= 25 {
			// Partial fit — excerpt
			remainingChars := remainingTokens * 4
			excerpt := c.memory.Content
			if len(excerpt) > remainingChars {
				excerpt = excerpt[:remainingChars] + "..."
			}
			excerptTokens := (len(excerpt) / 4) + 20
			result.Memories = append(result.Memories, ContextMemory{
				NS:      c.memory.NS,
				Key:     c.memory.Key,
				Kind:    c.memory.Kind,
				Content: excerpt,
				Score:   math.Round(c.score*100) / 100,
				Excerpt: true,
			})
			usedTokens += excerptTokens
			break
		} else {
			break
		}
	}

	result.Used = usedTokens

	// Track how many search candidates were skipped due to budget exhaustion.
	// If many candidates couldn't fit, suggest the caller run reflect/compaction.
	includedFromSearch := len(result.Memories) - pinnedCount
	if skipped := len(candidates) - includedFromSearch; skipped > 0 {
		result.Skipped = skipped
		if skipped > 2 {
			result.CompactionSuggested = true
		}
	}

	// Pressure-based compaction signal: if total active memories in namespace
	// exceed threshold, suggest compaction even if budget wasn't exhausted.
	if !result.CompactionSuggested && p.NS != "" {
		count, err := s.countActiveMemories(ctx, p.NS)
		if err == nil && count > 500 {
			result.CompactionSuggested = true
		}
	}

	// Touch access metadata for all returned memories
	var ids []string
	for _, r := range results {
		ids = append(ids, r.ID)
	}
	if err := s.touchMemories(ctx, ids); err != nil {
		_ = err
	}

	// Co-retrieval strengthening: strengthen edges between memories that
	// appear together in this context response (Hebbian: "fire together, wire together").
	// Collect IDs of memories actually returned in the result.
	var returnedIDs []string
	for _, c := range candidates {
		for _, m := range result.Memories {
			if c.memory.NS == m.NS && c.memory.Key == m.Key {
				returnedIDs = append(returnedIDs, c.memory.ID)
				break
			}
		}
	}
	if len(returnedIDs) > 1 {
		s.strengthenCoRetrievedEdges(ctx, returnedIDs)
	}

	return result, nil
}

// expandEdges performs single-hop edge expansion from seed candidates.
// For each seed, it follows top-K edges and adds neighbor memories to the
// candidate pool with propagated scores. If a neighbor is already in the pool
// (direct hit), it gets an additive boost capped by MaxBoostFactor.
func (s *SQLiteStore) expandEdges(ctx context.Context, scoreMap map[string]*contextCandidate, seen map[string]bool, cfg EdgeExpansionConfig, now time.Time) {
	// Snapshot seed IDs + scores (don't iterate map while mutating)
	type seedInfo struct {
		id    string
		score float64
	}
	var seeds []seedInfo
	for id, sc := range scoreMap {
		seeds = append(seeds, seedInfo{id: id, score: sc.score})
	}

	// Sort seeds by score descending so highest-scored seeds expand first
	sort.Slice(seeds, func(i, j int) bool {
		return seeds[i].score > seeds[j].score
	})

	// Track original direct scores for boost capping
	originalScores := map[string]float64{}
	for id, sc := range scoreMap {
		originalScores[id] = sc.score
	}

	totalExpanded := 0
	for _, seed := range seeds {
		if totalExpanded >= cfg.MaxExpansionTotal {
			break
		}

		edges, err := s.getEdgesForExpansion(ctx, seed.id, cfg.MinEdgeWeight, cfg.MaxEdgesPerSeed)
		if err != nil {
			continue
		}

		for _, edge := range edges {
			if totalExpanded >= cfg.MaxExpansionTotal {
				break
			}

			neighborID := edge.ToID
			if neighborID == seed.id {
				continue // self-loop guard
			}

			// Skip if this neighbor is a pinned memory already in context
			if seen[neighborID] {
				continue
			}

			propagated := seed.score * edge.Weight * cfg.Damping

			// contradicts edges get special treatment: the agent must see conflicts.
			// Give contradicting memories a high minimum score so they rank near the top.
			isContradiction := edge.Rel == "contradicts"
			if isContradiction {
				minContradictScore := seed.score * 0.8 // 80% of the seed's score
				if propagated < minContradictScore {
					propagated = minContradictScore
				}
			}

			if existing, ok := scoreMap[neighborID]; ok {
				// Memory already in pool — additive boost, capped
				origScore := originalScores[neighborID]
				maxBoost := origScore * cfg.MaxBoostFactor
				if maxBoost < 0.15 {
					maxBoost = 0.15
				}
				// contradicts edges bypass the cap
				if isContradiction {
					existing.score = math.Max(existing.score, propagated)
				} else {
					alreadyBoosted := existing.score - origScore
					remaining := maxBoost - alreadyBoosted
					if remaining > 0 {
						boost := math.Min(propagated, remaining)
						existing.score += boost
					}
				}
			} else {
				// New neighbor — load from DB
				m, err := s.loadMemoryByID(ctx, neighborID)
				if err != nil {
					continue
				}
				// Cap propagated score for edge-only candidates (except contradicts)
				if !isContradiction && propagated > 0.3 {
					propagated = 0.3
				}
				scoreMap[neighborID] = &contextCandidate{memory: *m, score: propagated}
				originalScores[neighborID] = 0 // no direct score
				totalExpanded++
			}
		}
	}

	// Parent boosting: if a seed is a child of a contains parent,
	// pull the parent into the pool. This ensures summaries appear
	// when their children match the query (even if the summary itself doesn't).
	for _, seed := range seeds {
		parents, err := s.getContainsParents(ctx, seed.id)
		if err != nil || len(parents) == 0 {
			continue
		}
		for _, parentID := range parents {
			if seen[parentID] {
				continue
			}
			if _, ok := scoreMap[parentID]; ok {
				continue // already in pool
			}
			m, err := s.loadMemoryByID(ctx, parentID)
			if err != nil {
				continue
			}
			// Parent gets at least the child's score since it summarizes the child
			parentScore := seed.score
			if parentScore < 0.3 {
				parentScore = 0.3
			}
			scoreMap[parentID] = &contextCandidate{memory: *m, score: parentScore}
			originalScores[parentID] = 0
		}
	}
}

// countActiveMemories returns the number of non-deleted, non-expired memories in a namespace.
func (s *SQLiteStore) countActiveMemories(ctx context.Context, ns string) (int, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories
		 WHERE ns = ? AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at > ?)`,
		ns, now).Scan(&count)
	return count, err
}

// computeContextScore calculates the composite context score for a memory.
func computeContextScore(m model.Memory, similarity float64, now time.Time) float64 {
	// Relevance: use vector similarity when available, otherwise base from search rank
	relevance := 0.5 // base relevance for FTS/LIKE matches
	if similarity > 0 {
		relevance = similarity // use actual cosine similarity
	}

	// Recency: exponential decay, half-life of 7 days
	age := now.Sub(m.CreatedAt).Hours() / 24.0 // days
	recency := math.Exp(-0.1 * age)

	// Importance: use continuous importance field, fall back to priority-based
	importance := m.Importance
	if importance <= 0 {
		importance = priorityScore(m.Priority)
	}

	// Access frequency: log scale
	accessFreq := 0.0
	if m.AccessCount > 0 {
		accessFreq = math.Log(float64(m.AccessCount)+1) / math.Log(100)
		if accessFreq > 1 {
			accessFreq = 1
		}
	}

	// Kind-specific composite weights, then apply tier as multiplicative modifier
	w := kindWeights(m.Kind)
	base := relevance*w.relevance + recency*w.recency + importance*w.importance + accessFreq*w.access
	return base * tierMultiplier(m.Tier)
}

// loadPinnedMemories loads memories with pinned=1, ordered by importance.
func (s *SQLiteStore) loadPinnedMemories(ctx context.Context, ns string) ([]model.Memory, error) {
	now := time.Now().UTC().Format(time.RFC3339)

	where := "m.deleted_at IS NULL AND (m.expires_at IS NULL OR m.expires_at > ?) AND m.pinned = 1"
	args := []interface{}{now}

	if ns != "" {
		nsf := ParseNSFilter(ns)
		clause, nsArgs := nsf.SQL("m.ns")
		if clause != "" {
			where += " AND " + clause
			args = append(args, nsArgs...)
		}
	}

	query := fmt.Sprintf(`SELECT m.id, m.ns, m.key, m.content, m.kind, m.tags, m.version, m.supersedes,
		m.created_at, m.deleted_at, m.priority, m.access_count, m.last_accessed_at, m.meta, m.expires_at,
		m.importance, m.utility_count, m.tier, m.est_tokens, m.pinned
		FROM memories m
		INNER JOIN (
			SELECT ns, key, MAX(version) AS max_ver
			FROM memories WHERE deleted_at IS NULL
			GROUP BY ns, key
		) latest ON m.ns = latest.ns AND m.key = latest.key AND m.version = latest.max_ver
		WHERE %s
		ORDER BY m.importance DESC, m.created_at DESC`, where)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var memories []model.Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		memories = append(memories, m)
	}
	return memories, nil
}

// tierMultiplier returns a multiplicative penalty/boost for tier.
// Applied as a multiplier on the final composite score so that
// tier transitions have meaningful impact on ranking:
//
//	ltm=1.0 (no penalty), stm=0.8, dormant=0.15, sensory=0.1
func tierMultiplier(tier string) float64 {
	switch tier {
	case "ltm":
		return 1.0
	case "stm":
		return 0.8
	case "dormant":
		return 0.15
	case "sensory":
		return 0.1
	default:
		return 0.8
	}
}

func priorityScore(p string) float64 {
	switch p {
	case "critical":
		return 1.0
	case "high":
		return 0.75
	case "normal":
		return 0.5
	case "low":
		return 0.25
	default:
		return 0.5
	}
}

// scoreWeights holds kind-specific scoring weights for context assembly.
// Tier is applied as a multiplicative modifier (see tierMultiplier), not as
// an additive component, so it has meaningful impact on ranking.
type scoreWeights struct {
	relevance  float64
	recency    float64
	importance float64
	access     float64
}

// kindWeights returns scoring weights tuned for different memory kinds.
// Inspired by cognitive science:
//   - Episodic: recency-heavy (temporal, context-dependent retrieval)
//   - Semantic: relevance + importance (decontextualized, timeless facts)
//   - Procedural: access-heavy (skills strengthen through practice/testing effect)
//
// Weights sum to 1.0. Tier boost was removed from additive weights and is
// now applied as a multiplier on the composite score.
func kindWeights(kind string) scoreWeights {
	switch kind {
	case "episodic":
		return scoreWeights{relevance: 0.30, recency: 0.40, importance: 0.15, access: 0.15}
	case "procedural":
		return scoreWeights{relevance: 0.35, recency: 0.05, importance: 0.15, access: 0.45}
	default: // semantic
		return scoreWeights{relevance: 0.45, recency: 0.10, importance: 0.30, access: 0.15}
	}
}

package store

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/rcliao/ghost/internal/model"
)

// ContextParams holds parameters for context assembly.
type ContextParams struct {
	NS           string
	Query        string
	Kind         string
	Tags         []string
	Budget       int      // max tokens in output
	PinTiers     []string // tiers always injected first (e.g. ["identity", "ltm"])
	PinBudget    int      // token budget reserved for pinned tiers (default: Budget/3)
	SearchBudget int      // remaining budget for query-relevant search (default: Budget - PinBudget)
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

// Context assembles relevant memories within a token budget.
func (s *SQLiteStore) Context(ctx context.Context, p ContextParams) (*ContextResult, error) {
	budget := p.Budget
	if budget <= 0 {
		budget = 4000
	}

	result := &ContextResult{Budget: budget, Memories: []ContextMemory{}}
	usedTokens := 0
	seen := map[string]bool{} // track memory IDs to deduplicate

	// Default PinTiers to identity + ltm when not explicitly set.
	pinTiers := p.PinTiers
	if len(pinTiers) == 0 {
		pinTiers = []string{"identity", "ltm"}
	}

	// Phase 1: Load pinned tier memories first
	if len(pinTiers) > 0 {
		pinBudget := p.PinBudget
		if pinBudget <= 0 {
			pinBudget = budget / 3
		}

		pinned, err := s.loadPinnedTierMemories(ctx, p.NS, pinTiers)
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
	type scored struct {
		memory model.Memory
		score  float64
	}
	var candidates []scored

	for _, r := range results {
		if seen[r.ID] {
			continue // already included from pinned tiers
		}
		m := r.Memory
		// Relevance: use vector similarity when available, otherwise base from search rank
		relevance := 0.5 // base relevance for FTS/LIKE matches
		if r.Similarity > 0 {
			relevance = r.Similarity // use actual cosine similarity
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

		// Tier boost: identity/ltm memories score higher than stm/dormant
		tierBoost := tierScore(m.Tier)

		// Kind-specific composite weights
		w := kindWeights(m.Kind)
		score := relevance*w.relevance + recency*w.recency + importance*w.importance + accessFreq*w.access + tierBoost*w.tier

		candidates = append(candidates, scored{memory: m, score: score})
	}

	// Sort by score descending
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	// Greedy packing into remaining budget
	pinnedCount := len(result.Memories)
	for _, c := range candidates {
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

	// Touch access metadata for all returned memories
	var ids []string
	for _, r := range results {
		ids = append(ids, r.ID)
	}
	if err := s.touchMemories(ctx, ids); err != nil {
		_ = err
	}

	return result, nil
}

// loadPinnedTierMemories loads memories from the specified tiers, ordered by importance.
func (s *SQLiteStore) loadPinnedTierMemories(ctx context.Context, ns string, tiers []string) ([]model.Memory, error) {
	if len(tiers) == 0 {
		return nil, nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	placeholders := strings.Repeat("?,", len(tiers))
	placeholders = placeholders[:len(placeholders)-1]

	where := fmt.Sprintf("m.deleted_at IS NULL AND (m.expires_at IS NULL OR m.expires_at > ?) AND m.tier IN (%s)", placeholders)
	args := []interface{}{now}
	for _, t := range tiers {
		args = append(args, t)
	}

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
		m.importance, m.utility_count, m.tier, m.est_tokens
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

func tierScore(tier string) float64 {
	switch tier {
	case "identity":
		return 1.0
	case "ltm":
		return 0.75
	case "stm":
		return 0.25
	case "sensory":
		return 0.05
	case "dormant":
		return 0.1
	default:
		return 0.25
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
type scoreWeights struct {
	relevance float64
	recency   float64
	importance float64
	access    float64
	tier      float64
}

// kindWeights returns scoring weights tuned for different memory kinds.
// Inspired by cognitive science:
//   - Episodic: recency-heavy (temporal, context-dependent retrieval)
//   - Semantic: relevance + importance (decontextualized, timeless facts)
//   - Procedural: access-heavy (skills strengthen through practice/testing effect)
func kindWeights(kind string) scoreWeights {
	switch kind {
	case "episodic":
		return scoreWeights{relevance: 0.25, recency: 0.30, importance: 0.15, access: 0.10, tier: 0.20}
	case "procedural":
		return scoreWeights{relevance: 0.30, recency: 0.05, importance: 0.15, access: 0.35, tier: 0.15}
	default: // semantic
		return scoreWeights{relevance: 0.40, recency: 0.05, importance: 0.25, access: 0.15, tier: 0.15}
	}
}

package store

import (
	"context"
	"math"
	"os"
	"sort"
	"strconv"
	"time"
)

// Personalized PageRank multi-hop retrieval (opt-in via GHOST_PPR=1).
//
// The default Phase-3 (expandEdges) is single-hop: each seed propagates score to
// its direct neighbours once. PPR instead runs a damped random-walk-with-restart
// over the namespace's edge graph, seeded from the Phase-2 retrieval scores. This
// reaches 2-3 hops and rewards multi-path convergence (a memory reachable from
// several seeds accumulates mass) — the signal single-hop discards. The edge
// substrate for this is real: on the live graph ~52% of edges are entity/topic
// bridges (low cosine, shared entity), not similarity echoes (see the
// TestPPRAblationEdgeProvenance diagnostic).
//
// Fully pure-Go, no new deps, no schema change (adjacency already lives in
// memory_edges). Off by default so the strong single-hop path (LongMemEval_S
// R@5 0.908) can't regress. The contradicts force-include and contains
// parent-boosting behaviours are preserved outside the PPR blend.

func pprEnabled() bool { return os.Getenv("GHOST_PPR") == "1" }

func pprAlpha() float64 {
	if v := os.Getenv("GHOST_PPR_ALPHA"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f < 1 {
			return f
		}
	}
	return 0.5 // HippoRAG-style high restart: stay local rather than global-central
}

func pprIters() int {
	if v := os.Getenv("GHOST_PPR_ITERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			return n
		}
	}
	return 20
}

type adjEdge struct {
	to string
	w  float64
}

// loadNamespaceAdjacency builds the symmetrized, edge-weighted adjacency for a
// namespace from memory_edges (excluding merged_into audit edges). Edges are
// mostly directional, so both directions are added to let mass flow.
func (s *SQLiteStore) loadNamespaceAdjacency(ctx context.Context, ns string) map[string][]adjEdge {
	rows, err := s.db.QueryContext(ctx,
		`SELECT e.from_id, e.to_id, e.weight FROM memory_edges e
		 JOIN memories m ON m.id = e.from_id
		 WHERE m.ns = ? AND e.rel != 'merged_into' AND e.weight > 0`, ns)
	if err != nil {
		return nil
	}
	defer rows.Close()

	adj := map[string][]adjEdge{}
	for rows.Next() {
		var from, to string
		var w float64
		if rows.Scan(&from, &to, &w) != nil {
			continue
		}
		adj[from] = append(adj[from], adjEdge{to: to, w: w})
		adj[to] = append(adj[to], adjEdge{to: from, w: w})
	}
	return adj
}

// personalizedPageRank runs damped power iteration r = (1-α)·s + α·Wᵀr, where s
// is the normalized seed/teleport distribution and W is the row-normalized
// adjacency. Returns steady-state mass per node.
func personalizedPageRank(seeds map[string]float64, adj map[string][]adjEdge, alpha float64, iters int) map[string]float64 {
	var ssum float64
	for _, v := range seeds {
		if v > 0 {
			ssum += v
		}
	}
	if ssum == 0 || len(adj) == 0 {
		return nil
	}
	// Normalized teleport vector + initial rank.
	teleport := make(map[string]float64, len(seeds))
	r := make(map[string]float64, len(seeds))
	for id, v := range seeds {
		if v > 0 {
			teleport[id] = v / ssum
			r[id] = v / ssum
		}
	}
	// Row sums for out-normalization.
	out := make(map[string]float64, len(adj))
	for id, es := range adj {
		for _, e := range es {
			out[id] += e.w
		}
	}

	for it := 0; it < iters; it++ {
		nr := make(map[string]float64, len(r))
		for id, v := range teleport {
			nr[id] += (1 - alpha) * v
		}
		for u, ru := range r {
			ow := out[u]
			if ow == 0 || ru == 0 {
				continue
			}
			share := alpha * ru
			for _, e := range adj[u] {
				nr[e.to] += share * (e.w / ow)
			}
		}
		r = nr
	}
	return r
}

// expandEdgesPPR is the PPR-based Phase-3, a drop-in for expandEdges. It seeds
// PPR from the current Phase-2 scores, blends the resulting mass into the
// candidate pool (bounded to the same score regime as single-hop expansion), and
// preserves the contradicts force-include and contains parent-boosting passes.
func (s *SQLiteStore) expandEdgesPPR(ctx context.Context, scoreMap map[string]*contextCandidate, seen map[string]bool, cfg EdgeExpansionConfig, now time.Time) {
	if len(scoreMap) == 0 {
		return
	}
	var ns string
	seeds := make(map[string]float64, len(scoreMap))
	originalScores := make(map[string]float64, len(scoreMap))
	for id, sc := range scoreMap {
		seeds[id] = sc.score
		originalScores[id] = sc.score
		if ns == "" {
			ns = sc.memory.NS
		}
	}

	adj := s.loadNamespaceAdjacency(ctx, ns)
	mass := personalizedPageRank(seeds, adj, pprAlpha(), pprIters())

	// Rank non-seed nodes by PPR mass and normalize against the top so the blend
	// stays in the [0,0.3]/boost regime the sort and packing expect.
	type massNode struct {
		id string
		m  float64
	}
	var ranked []massNode
	var maxMass float64
	for id, m := range mass {
		if _, isSeed := seeds[id]; isSeed || seen[id] {
			continue
		}
		if m > maxMass {
			maxMass = m
		}
		ranked = append(ranked, massNode{id, m})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].m > ranked[j].m })

	if maxMass > 0 {
		added := 0
		for _, n := range ranked {
			if added >= cfg.MaxExpansionTotal {
				break
			}
			rel := n.m / maxMass // 0..1
			if rel < 0.1 {
				break // below this, convergence mass is noise
			}
			if existing, ok := scoreMap[n.id]; ok {
				// Convergent boost for a memory already in the pool, capped.
				orig := originalScores[n.id]
				maxBoost := orig * cfg.MaxBoostFactor
				if maxBoost < 0.15 {
					maxBoost = 0.15
				}
				boost := rel * maxBoost
				alreadyBoosted := existing.score - orig
				if remaining := maxBoost - alreadyBoosted; remaining > 0 {
					existing.score += math.Min(boost, remaining)
				}
				continue
			}
			m, err := s.loadMemoryByID(ctx, n.id)
			if err != nil || m.Tier == "dormant" {
				continue
			}
			scoreMap[n.id] = &contextCandidate{memory: *m, score: math.Min(0.3, rel*0.3), via: ViaEdge}
			originalScores[n.id] = 0
			added++
		}
	}

	// Preserve special-cased single-hop behaviours the PPR blend doesn't cover.
	s.forceIncludeContradictions(ctx, scoreMap, seen, originalScores, seeds, cfg)
	s.boostContainsParents(ctx, scoreMap, seen, seeds)
}

// forceIncludeContradictions surfaces contradicting memories near their seed's
// score regardless of PPR mass — the agent must always see conflicts.
func (s *SQLiteStore) forceIncludeContradictions(ctx context.Context, scoreMap map[string]*contextCandidate, seen map[string]bool, originalScores map[string]float64, seeds map[string]float64, cfg EdgeExpansionConfig) {
	for id, seedScore := range seeds {
		edges, err := s.getEdgesForExpansion(ctx, id, cfg.MinEdgeWeight, cfg.MaxEdgesPerSeed)
		if err != nil {
			continue
		}
		for _, e := range edges {
			if e.Rel != "contradicts" || e.ToID == id || seen[e.ToID] {
				continue
			}
			floor := seedScore * 0.8
			if existing, ok := scoreMap[e.ToID]; ok {
				existing.score = math.Max(existing.score, floor)
				existing.keepFloorExempt = true
				continue
			}
			m, err := s.loadMemoryByID(ctx, e.ToID)
			if err != nil || m.Tier == "dormant" {
				continue
			}
			scoreMap[e.ToID] = &contextCandidate{memory: *m, score: floor, via: ViaEdge, keepFloorExempt: true}
			originalScores[e.ToID] = 0
		}
	}
}

// boostContainsParents pulls a seed's contains-parent (summary) into the pool so
// summaries appear when their children match, mirroring expandEdges.
func (s *SQLiteStore) boostContainsParents(ctx context.Context, scoreMap map[string]*contextCandidate, seen map[string]bool, seeds map[string]float64) {
	for id, seedScore := range seeds {
		parents, err := s.getContainsParents(ctx, id)
		if err != nil {
			continue
		}
		for _, parentID := range parents {
			if seen[parentID] {
				continue
			}
			if _, ok := scoreMap[parentID]; ok {
				continue
			}
			m, err := s.loadMemoryByID(ctx, parentID)
			if err != nil || m.Tier == "dormant" {
				continue
			}
			parentScore := seedScore
			if parentScore < 0.3 {
				parentScore = 0.3
			}
			scoreMap[parentID] = &contextCandidate{memory: *m, score: parentScore, via: ViaEdge}
		}
	}
}

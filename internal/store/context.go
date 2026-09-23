package store

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/rcliao/ghost/internal/model"
)

// SessionScope biases scoring toward memories from the current session window
// without filtering anything out. Memories that match either ScopeTags (any) or
// were created at/after Since get their score multiplied by BoostFactor.
// Designed for chat- and date-scoped recall (e.g. "today's meal memos in
// chat:800000001") so same-day same-chat content reliably survives session
// rotation packs and short-budget context assembly.
type SessionScope struct {
	Tags        []string  // e.g. ["chat:800000001", "date:2026-05-12"] — OR-matched against memory.Tags
	Since       time.Time // optional: zero means ignore. Memories with CreatedAt >= Since also get boosted.
	BoostFactor float64   // multiplier on score (default 2.5 when applied; <=1 disables the boost)
}

// matches reports whether the given memory falls inside this scope.
func (s *SessionScope) matches(m model.Memory) bool {
	if s == nil {
		return false
	}
	if !s.Since.IsZero() && !m.CreatedAt.Before(s.Since) {
		return true
	}
	if len(s.Tags) == 0 {
		return false
	}
	mtags := map[string]bool{}
	for _, t := range m.Tags {
		mtags[t] = true
	}
	for _, t := range s.Tags {
		if mtags[t] {
			return true
		}
	}
	return false
}

// boost returns the score multiplier to apply, or 1.0 if not in scope.
func (s *SessionScope) boost(m model.Memory) float64 {
	if s == nil || s.BoostFactor <= 1.0 {
		return 1.0
	}
	if s.matches(m) {
		return s.BoostFactor
	}
	return 1.0
}

// ContextParams holds parameters for context assembly.
type ContextParams struct {
	NS              string
	Query           string
	Kind            string
	Tags            []string
	Budget          int                  // max tokens in output
	PinTiers        []string             // tiers always injected first (e.g. ["identity", "ltm"])
	PinBudget       int                  // token budget reserved for pinned tiers (default: Budget/3)
	SearchBudget    int                  // remaining budget for query-relevant search (default: Budget - PinBudget)
	EdgeExpansion   *EdgeExpansionConfig // edge expansion config; nil means use defaults
	ExcludePinned   bool                 // skip Phase 1 pinned memories, use full budget for search
	MaxMemoryTokens int                  // max tokens per memory; larger memories get excerpted (default: 400, 0 = no limit)
	MinScore        float64              // absolute score floor; candidates below this are dropped (0 = no filter)
	MinSpread       float64              // top-1 must exceed top-N by this delta (0 = no filter). Catches "flat noise" queries where retrieval couldn't discriminate.
	Scope           *SessionScope        // optional: boost memories matching the current session window (chat tag, date tag, or since cutoff)
	// ForUser is the person the agent is currently talking to. Memories whose
	// source_user matches get a score boost — the interlocutor's own stated
	// facts and preferences surface first when the budget is contested. A
	// boost, never a filter: other people's facts stay retrievable (a standing
	// instruction from one family member can matter mid-conversation with
	// another). Empty = no effect, preserving prior behaviour exactly.
	ForUser string
	// ForScope is the place the conversation is happening — the project a
	// coding agent is working in, the chat a messenger agent is serving.
	// Memories born in the same scope (source_scope match, alias-resolved)
	// get a score boost. Boost, never filter; empty = no effect.
	ForScope string
}

// forUserBoost is the multiplicative score boost for memories originated by
// the current interlocutor (ContextParams.ForUser) — enough to win a
// contested budget slot against same-topic competition, not enough for
// person-affinity to outrank the relevance machinery. Magnitude pinned by
// TestProvenanceInterlocutorBoost; 1.8 is a starting point, not a
// validated optimum.
const forUserBoost = 1.8

// forScopeBoost is the same-magnitude sibling for encoding context: memories
// born where the conversation is happening (ForScope, alias-resolved) win
// contested slots against out-of-scope twins. Pinned by
// TestScopeBoostContested; applied before the MinScore floor like ForUser.
const forScopeBoost = 1.8

// ContextMemory is a scored memory for context output.
type ContextMemory struct {
	NS      string  `json:"ns"`
	Key     string  `json:"key"`
	Kind    string  `json:"kind"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
	Excerpt bool    `json:"excerpt,omitempty"`
	// SummaryOf lists the child keys this memory replaced during packing
	// substitution (LCM compression under budget pressure). The caller can
	// recover full detail via `ghost expand <key>` / ghost_expand.
	SummaryOf []string `json:"summary_of,omitempty"`
	// SourceUser/SourceKind carry write provenance into the assembled
	// context so the renderer and the agent can attribute: "[mami, stated]"
	// vs "[inferred]". The agent is the one component that can already apply
	// the authority ladder — it just needs to see which is which.
	SourceUser string `json:"source_user,omitempty"`
	SourceKind string `json:"source_kind,omitempty"`
	// SourceScope is where the memory was born (encoding context), verbatim.
	SourceScope string `json:"source_scope,omitempty"`
}

// ContextResult is the assembled context response.
type ContextResult struct {
	Budget              int             `json:"budget"`
	Used                int             `json:"used"`
	Memories            []ContextMemory `json:"memories"`
	Skipped             int             `json:"skipped,omitempty"`
	CompactionSuggested bool            `json:"compaction_suggested,omitempty"`
	// Stages counts what each retrieval stage CONTRIBUTED to the packed
	// output (liveness canary, see diagnostics.go). Additive; omitted when
	// empty for wire compatibility.
	Stages map[string]int `json:"stages,omitempty"`
}

// contextCandidate is a memory with its computed score for context ranking.
type contextCandidate struct {
	memory model.Memory
	score  float64
	// reserved marks a candidate that reached the pool through a relation whose
	// handling class is handleReserve — a contradiction, a prerequisite, or a
	// constraint. Something already in context is false, broken or unsafe
	// without it. Tracked so packing can hold room rather than letting it be
	// squeezed out by the very neighbours that made it hard to see.
	reserved bool
	// relevance is the topical-match strength that fed the composite score: a
	// real cosine for vector-sourced candidates, a graded term overlap for
	// FTS/LIKE ones, 0 for edge-expanded arrivals. Kept so the MinScore floor
	// can distinguish "weak match" from "strong match sunk by recency".
	relevance float64
	// viaEdge marks a candidate that entered the pool through graph expansion
	// rather than search. These carry structural evidence, not topical
	// relevance — their relevance is 0 BY CONSTRUCTION (being reachable only
	// through an edge is what the edge is for), so any filter that judges
	// candidates on relevance must not judge them at all.
	viaEdge bool
}

// Context assembles relevant memories within a token budget.
func (s *SQLiteStore) Context(ctx context.Context, p ContextParams) (*ContextResult, error) {
	budget := p.Budget
	if budget <= 0 {
		budget = 4000
	}

	result := &ContextResult{Budget: budget, Memories: []ContextMemory{}}
	result.Stages = map[string]int{}
	usedTokens := 0
	seen := map[string]bool{} // track memory IDs to deduplicate
	// Pinned memories seed expansion without joining the candidate pool — they
	// are already in the result, so re-adding them would duplicate.
	var pinnedSeeds []expansionSeed

	// Phase 1: Load pinned memories first (chronically accessible)
	if !p.ExcludePinned {
		pinBudget := p.PinBudget
		if pinBudget <= 0 {
			pinBudget = budget / 2
		}

		pinDropped := 0
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
				result.Stages["packed_pinned"]++
				result.Memories = append(result.Memories, ContextMemory{
					NS:         m.NS,
					Key:        m.Key,
					Kind:       m.Kind,
					Content:    m.Content,
					Score:      m.Importance, // pinned memories use importance as score
					SourceUser: m.SourceUser,
					SourceKind: m.SourceKind, SourceScope: m.SourceScope,
				})
				usedTokens += memTokens
				seen[m.ID] = true
				// A pinned memory is present every single turn, which makes it
				// the most reliable seed the graph has — and it was the one
				// candidate that never got to act as one. Phase 1 appends
				// pinned memories straight to the result and marks them seen;
				// they never enter scoreMap, which is the seed set for
				// spreading activation. So the memory guaranteed to be in
				// context could pull in nothing linked to it, and a correction
				// pointing at a pinned stale fact stayed unreachable even after
				// per-relation traversal landed. Measured by
				// TestEvalGraphPullThrough, where the pinned case failed while
				// the identical unpinned case passed.
				pinnedSeeds = append(pinnedSeeds, expansionSeed{id: m.ID, score: m.Importance, createdAt: m.CreatedAt})
			} else {
				pinDropped++
			}
		}
		// "Pinned" reads as a guarantee — the caller marked these
		// always-load. It is not: pinned competes for a sub-budget
		// (default budget/2), ordered by importance, and the overflow is
		// silently discarded every turn. Measured on two production stores
		// 2026-08-01: 29 pinned / 4915 tokens with only 7 admitted (24%),
		// and 17 / 3372 with 5 admitted (29%) — including a food-allergy
		// memory the household had pinned precisely so it would always be
		// present. Warn once per namespace so an oversubscribed pin set is
		// visible instead of being discovered by a wrong answer.
		if pinDropped > 0 {
			if _, warned := s.pinOverflowWarned.LoadOrStore(p.NS, true); !warned {
				fmt.Fprintf(os.Stderr,
					"ghost: pinned set exceeds pin budget for %s — %d of %d pinned memories dropped (pin budget %d tokens). Raise PinBudget/Budget or reduce the pinned set; lowest-importance pins are never loaded.\n",
					p.NS, pinDropped, pinDropped+len(result.Memories), pinBudget)
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

	result.Stages["search_results"] = len(results)

	// Score each memory using kind-specific weights.
	// Cognitive rationale:
	//   Episodic (events): recency dominates — time-bound observations
	//   Semantic (facts): relevance + importance — timeless knowledge
	//   Procedural (skills): access frequency — strengthened by practice (testing effect)
	now := s.now()

	// ACT-R mode: prefetch real access histories so activation uses the exact
	// log where present, falling back to the approximation otherwise.
	var accessTimes map[string][]time.Time
	if actrEnabled() {
		ids := make([]string, 0, len(results))
		for _, r := range results {
			ids = append(ids, r.ID)
		}
		accessTimes, _ = s.loadAccessTimes(ctx, ids)
	}

	// Liveness-scaled retrieval rights for direct-matched summaries: a
	// contains-parent inherits searchability from its children's death.
	// While children are alive the specific memories answer queries and
	// the summary defers (weight → ~0); as children age into dormancy the
	// summary becomes the epoch's surviving account and regains full
	// strength (weight → 1). Replaces the earlier flat 0.25 demotion.
	// GHOST_SUMMARY_DIRECT_WEIGHT, if set, overrides with a flat weight.
	flatWeight := envFloatDefault("GHOST_SUMMARY_DIRECT_WEIGHT", -1)
	var parentLiveness map[string]float64 // parentID → active-children fraction
	{
		asResults := make([]SearchResult, len(results))
		copy(asResults, results)
		parentLiveness = s.containsParentLiveness(ctx, asResults)
	}
	summaryWeightFor := func(id string) (float64, bool) {
		activeFrac, isParent := parentLiveness[id]
		if !isParent {
			return 1, false
		}
		if flatWeight >= 0 {
			return flatWeight, true
		}
		w := 1 - activeFrac
		if w < 0.05 {
			w = 0.05
		}
		return w, true
	}

	// Interlocutor boost: the person in the conversation gets their own
	// stated facts preferred when the budget is contested. Resolution goes
	// through the declared alias table so a fact stored under any spelling
	// of the person still matches (source-identity-design.md). Applied
	// BEFORE the MinScore floor — the interlocutor's own sub-floor fact may
	// be lifted over it, in the same spirit as the viaEdge/reserved
	// exemptions (#103: undocumented MinScore interactions kill subsystems).
	var resolver sourceResolver
	if p.ForUser != "" || p.ForScope != "" {
		resolver = s.loadSourceResolver(ctx, p.NS)
	}

	// scoreMap tracks scores by memory ID for edge boost merging
	scoreMap := map[string]*contextCandidate{}

	for _, r := range results {
		if seen[r.ID] {
			continue // already included from pinned tiers
		}
		m := r.Memory
		sim := r.Similarity
		if sim < 0.3 {
			// No cosine available (FTS/LIKE-sourced candidate): grade
			// relevance by actual term-match strength instead of the old
			// flat 0.5 — topically-unmatched filler (graze-one-template-
			// term, ride recency over the floor) scores near zero here.
			// Mapped to [0.15, 0.70] so strong keyword matches score
			// ABOVE the old flat value and zero-match filler falls well
			// below the MinScore floor.
			overlap := gradedTermRelevance(p.Query, m.Content, englishStopWords)
			sim = 0.15 + 0.55*overlap
		}
		score := computeContextScoreWithAccess(m, sim, now, p.Scope, accessTimes[m.ID])
		if w, isParent := summaryWeightFor(m.ID); isParent {
			score *= w
		}
		if p.ForUser != "" && resolver.sameSource(m.SourceUser, p.ForUser) {
			score *= forUserBoost
		}
		if p.ForScope != "" && resolver.sameSource(m.SourceScope, p.ForScope) {
			score *= forScopeBoost
		}
		scoreMap[m.ID] = &contextCandidate{memory: m, score: score, relevance: sim}
	}

	// Phase 3: Edge expansion — spreading activation
	edgeCfg := DefaultEdgeExpansion()
	if p.EdgeExpansion != nil {
		edgeCfg = *p.EdgeExpansion
	}

	if edgeCfg.Enabled && len(scoreMap) > 0 {
		if pprEnabled() {
			// NOTE: the PPR path does not take pinned seeds. PPR measured
			// NO-GO as a default and is env-gated, so it is left alone rather
			// than carrying an unexercised code path.
			s.expandEdgesPPR(ctx, scoreMap, seen, edgeCfg, now)
		} else {
			poolBefore, reservedBefore := len(scoreMap), 0
			for _, c := range scoreMap {
				if c.reserved {
					reservedBefore++
				}
			}
			s.expandEdges(ctx, scoreMap, seen, edgeCfg, now, pinnedSeeds)
			reservedAfter := 0
			for _, c := range scoreMap {
				if c.reserved {
					reservedAfter++
				}
			}
			result.Stages["edge_added"] = len(scoreMap) - poolBefore
			result.Stages["edge_marked_reserved"] = reservedAfter - reservedBefore
		}
	}

	// Authority: inference-never-without-statement (the dairy rule,
	// provenance-encoding-context-design.md mechanism (c), gated by
	// TestProvenanceAuthorityBaseline). When a person-attributed inference
	// (kind=observed) is in the pool, the same person's own statement on the
	// same query — if retrieval surfaced one — must not be squeezed out of
	// packing by the very inference it corrects. The statement is marked
	// reserved, riding the same hoist + floor exemption as refutations: the
	// store never suppresses the inference or ranks by authority; it
	// guarantees the reader sees both and applies the ladder itself.
	result.Stages["authority_reserved"] = s.reserveStatementsForInferences(ctx, p.NS, scoreMap)

	// Collect and sort candidates
	var candidates []contextCandidate
	for _, sc := range scoreMap {
		candidates = append(candidates, *sc)
	}

	// Sort by score descending, with deterministic tie-breaks: on equal scores,
	// prefer higher priority, then order by key. Without this, equal-scored
	// candidates come back in map-iteration order (nondeterministic).
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.score != b.score {
			return a.score > b.score
		}
		pa, pb := priorityScore(a.memory.Priority), priorityScore(b.memory.Priority)
		if pa != pb {
			return pa > pb
		}
		return a.memory.Key < b.memory.Key
	})

	// Confidence filter: drop low-score candidates and detect "flat noise"
	// (top-1 too close to the tail — retrieval couldn't discriminate).
	// Both filters are opt-in via zero defaults.
	// Env-default confidence floor: callers that don't set MinScore inherit
	// GHOST_MIN_SCORE (default 0 = off), so the false-confidence guard can be
	// enabled fleet-wide without changing every call site.
	if p.MinScore == 0 {
		p.MinScore = envFloatDefault("GHOST_MIN_SCORE", 0)
	}
	// Env fallback for the flat-noise filter. Measured live (2026-07-13)
	// and left OFF by default: flat score distributions cannot distinguish
	// "wall of topically-unmatched filler" (gadget question in a meal-log
	// chat) from "wall of equally-relevant items" (a meal-memo query where
	// every recent memo is a useful template) — at 0.15 the filter killed
	// the best query in the corpus while missing the worst. Keep as an
	// explicit per-call knob for callers who know their distribution.
	if p.MinSpread == 0 {
		p.MinSpread = envFloatDefault("GHOST_MIN_SPREAD", 0)
	}
	if len(candidates) > 0 && (p.MinScore > 0 || p.MinSpread > 0) {
		if p.MinScore > 0 {
			// Relevance-confident rescue (query-relative): the floor exists to
			// kill topically-unmatched filler that rides recency over it; it
			// must not blank a strong topical match whose composite sank only
			// because the memory is old (measured: a once-stated fact at
			// cosine 0.38 vs a 0.04 noise floor scored 0.21 composite at two
			// weeks old — the assembled context came back EMPTY). A candidate
			// with real topical confidence (relevance ≥ 0.35, i.e. genuine
			// cosine territory) that is also dominant among this query's
			// candidates (≥ 0.8× the best relevance seen) survives the floor.
			// Flood noise cannot ride the exemption: dominance is relative,
			// and near-duplicate template noise sits far below the top hit.
			maxRel := 0.0
			for _, c := range candidates {
				if c.relevance > maxRel {
					maxRel = c.relevance
				}
			}
			keep := candidates[:0]
			for _, c := range candidates {
				// Structural candidates are exempt. The floor judges topical
				// relevance, and a TYPED-edge candidate has none BY
				// CONSTRUCTION — it was admitted on graph evidence, which has
				// its own noise controls (per-relation direction and priority,
				// weight threshold, damping, per-seed and total caps). Judging
				// it here re-applies exactly the filter the graph exists to
				// bypass. Measured before this exemption: with the production
				// GHOST_MIN_SCORE=0.3, every non-contradicts edge neighbour
				// was dropped ahead of the reserve hoist — the entire reserve
				// class and all multi-hop traversal were inert in production
				// while every eval passed, because the eval never set the
				// floor. relates_to arrivals stay subject to the floor
				// (viaEdge is not set for them): similarity edges make a
				// similarity claim, and a relevance floor is a fair judge of
				// that. `reserved` is listed separately: a refutation that is
				// ALSO a weak direct search hit carries the same structural
				// claim without viaEdge.
				if c.viaEdge || c.reserved ||
					c.score >= p.MinScore ||
					(c.relevance >= 0.35 && c.relevance >= 0.8*maxRel) {
					keep = append(keep, c)
				}
			}
			candidates = keep
		}
		if p.MinSpread > 0 && len(candidates) >= 2 {
			// Compare top-1 vs the smaller of (5th result, last result)
			tailIdx := 4
			if tailIdx >= len(candidates) {
				tailIdx = len(candidates) - 1
			}
			spread := candidates[0].score - candidates[tailIdx].score
			if spread < p.MinSpread {
				// Flat distribution — likely all noise. Keep only the top candidate.
				candidates = candidates[:1]
			}
		}
	}

	// MMR diversity re-rank (opt-in via GHOST_MMR_LAMBDA): demote candidates
	// textually redundant with higher-ranked ones so near-duplicates don't
	// crowd the packing budget.
	if lambda := mmrLambda(); lambda > 0 {
		candidates = mmrRerank(candidates, lambda)
	}

	// Reserve room for the handleReserve class before greedy packing takes over.
	//
	// Scoring and packing are separate steps, and score turned out not to be a
	// working lever at all: a neighbour pulled in by an edge cannot out-score
	// the near-duplicate siblings that buried it, even given a floor above the
	// seed's own score (finding 4 in eval_graph_test.go). `contradicts` reaches
	// context because it is hoisted here, not because it ranks. Measured:
	// pull-through worked at a 700-token budget and vanished entirely at 400
	// and below — exactly the regime where these relations matter most.
	//
	// The class is scarce by design. Reserved budget is taken from direct search
	// hits, so it holds only the three relations whose absence makes the agent
	// assert something false rather than merely incomplete: a contradiction, a
	// missing prerequisite, or a violated constraint. See edgeExpansionPolicies.
	//
	// Hoisting is bounded on purpose. A reservation storm must not become its
	// own denial of service, so the class may claim at most a third of the
	// budget; past that its members fall back into ordinary score order. These
	// relations are curated or supersede-minted rather than auto-linked, so in
	// practice this moves one or two memories.
	if len(candidates) > 1 {
		reserve := (budget - usedTokens) / 3
		var held, rest []contextCandidate
		claimed := 0
		for _, c := range candidates {
			tok := c.memory.EstTokens
			if tok <= 0 {
				tok = (len(c.memory.Content) / 4) + 20
			}
			if c.reserved && claimed+tok <= reserve {
				held = append(held, c)
				claimed += tok
				continue
			}
			rest = append(rest, c)
		}
		if len(held) > 0 {
			candidates = append(held, rest...)
		}
	}

	// Packing substitution (LCM compression under pressure, the redesign's
	// lossless move): when 3+ candidates share a contains-parent and the
	// full candidate set overflows the budget, the parent substitutes for
	// the group — one summary in context, children recoverable via
	// `ghost expand` (SummaryOf carries the keys). Compression is
	// proportional to pressure: with room, full detail is packed and the
	// substitution never fires. GHOST_PACK_SUBSTITUTE=0 disables.
	substituted := map[string][]string{} // parentID → child keys replaced
	if os.Getenv("GHOST_PACK_SUBSTITUTE") != "0" && len(candidates) > 0 {
		candidates = s.substituteParents(ctx, candidates, budget-usedTokens, substituted)
	}

	// Redesign note: unconditional parent→child suppression is gone.
	// Children are first-class; a parent only replaces them via packing
	// substitution above (budget pressure), and a direct-matched parent
	// with living children is already liveness-demoted to ~0. Suppressing
	// specifics in favor of their summary while there was room was how
	// digests degraded injection quality (measured 2026-07-13).
	suppressed := map[string]bool{}

	// Greedy packing into remaining budget with contains-suppression.
	// MaxMemoryTokens caps individual memories to prevent budget domination.
	maxMemTok := p.MaxMemoryTokens
	if maxMemTok == 0 {
		maxMemTok = 400 // default: 400 tokens per memory
	}
	if maxMemTok < 0 {
		maxMemTok = 0 // negative means no limit
	}
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

		content := c.memory.Content
		isExcerpt := false

		// Cap individual memory size to prevent large memories from hogging budget.
		if maxMemTok > 0 && memTokens > maxMemTok {
			maxChars := maxMemTok * 4
			if len(content) > maxChars {
				content = content[:maxChars] + "..."
				isExcerpt = true
			}
			memTokens = (len(content) / 4) + 20
		}

		if usedTokens+memTokens <= budget {
			switch {
			case c.viaEdge:
				result.Stages["packed_via_edge"]++
			case c.reserved:
				result.Stages["packed_reserved"]++
			default:
				result.Stages["packed_search"]++
			}
			result.Memories = append(result.Memories, ContextMemory{
				NS:         c.memory.NS,
				Key:        c.memory.Key,
				Kind:       c.memory.Kind,
				Content:    content,
				Score:      math.Round(c.score*100) / 100,
				Excerpt:    isExcerpt,
				SummaryOf:  substituted[c.memory.ID],
				SourceUser: c.memory.SourceUser,
				SourceKind: c.memory.SourceKind, SourceScope: c.memory.SourceScope,
			})
			usedTokens += memTokens
		} else if remainingTokens := budget - usedTokens; remainingTokens >= 25 {
			// Partial fit — excerpt to remaining budget
			remainingChars := remainingTokens * 4
			excerpt := content
			if len(excerpt) > remainingChars {
				excerpt = excerpt[:remainingChars] + "..."
			}
			excerptTokens := (len(excerpt) / 4) + 20
			result.Memories = append(result.Memories, ContextMemory{
				NS:         c.memory.NS,
				Key:        c.memory.Key,
				Kind:       c.memory.Kind,
				Content:    excerpt,
				Score:      math.Round(c.score*100) / 100,
				Excerpt:    true,
				SourceUser: c.memory.SourceUser,
				SourceKind: c.memory.SourceKind, SourceScope: c.memory.SourceScope,
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
	logStages(result.Stages)
	if !result.CompactionSuggested && p.NS != "" {
		count, err := s.countActiveMemories(ctx, p.NS)
		if err == nil && count > 500 {
			result.CompactionSuggested = true
		}
	}

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

	// Only Phase-2 direct search hits earn utility credit (not edge passengers).
	directHit := make(map[string]bool, len(results))
	for _, r := range results {
		directHit[r.ID] = true
	}
	var utilityIDs []string
	for _, id := range returnedIDs {
		if directHit[id] {
			utilityIDs = append(utilityIDs, id)
		}
	}

	// Touch access metadata only for returned direct hits. Raw search hits the
	// MinScore/MinSpread floor or the budget dropped were never surfaced, and
	// edge passengers (typed-edge neighbours, force-included contradicts, pulled
	// out of dormancy) never matched the query. Counting either as an access let
	// a memory accrue access days without earning utility, which
	// sys-promote-to-ltm reads as rehearsal and the spaced-access guard then
	// shields from sys-prune-low-utility.
	if err := s.touchMemories(ctx, utilityIDs); err != nil {
		_ = err
	}

	// Co-retrieval strengthening: strengthen edges between memories that
	// appear together in this context response (Hebbian: "fire together, wire together").
	// Edges need a pair, but utility credit does not: a lone direct hit is
	// credited too, or it would read as never-useful to sys-prune-low-utility.
	if len(returnedIDs) > 1 {
		s.strengthenCoRetrievedEdges(ctx, returnedIDs, utilityIDs)
	} else if len(utilityIDs) == 1 {
		_ = s.UtilityInc(ctx, utilityIDs[0])
	}

	return result, nil
}

// expandEdges performs single-hop edge expansion from seed candidates.
// For each seed, it follows top-K edges and adds neighbor memories to the
// candidate pool with propagated scores. If a neighbor is already in the pool
// (direct hit), it gets an additive boost capped by MaxBoostFactor.
func (s *SQLiteStore) expandEdges(ctx context.Context, scoreMap map[string]*contextCandidate, seen map[string]bool, cfg EdgeExpansionConfig, now time.Time, extraSeeds []expansionSeed) {
	// Snapshot seed IDs + scores (don't iterate map while mutating)
	var seeds []expansionSeed
	for id, sc := range scoreMap {
		seeds = append(seeds, expansionSeed{id: id, score: sc.score, createdAt: sc.memory.CreatedAt})
	}
	// Pinned memories seed expansion but are NOT candidates — they are already
	// in the result, and seen[] keeps expansion from re-adding them.
	seeds = append(seeds, extraSeeds...)

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
	// Breadth-first over hops. `frontier` is the set of seeds for the current
	// hop; neighbours discovered during it become the next hop's frontier, but
	// only along relations whose policy grants that depth (see MaxHops). Depth
	// is capped per relation rather than globally so a prerequisite chain can be
	// followed without `relates_to` dragging in a second-order neighbourhood.
	expandedFrom := map[string]bool{}
	frontier := seeds
	for hop := 1; hop <= maxHopsConfigured(); hop++ {
		var nextFrontier []expansionSeed
		for _, seed := range frontier {
			if totalExpanded >= cfg.MaxExpansionTotal {
				break
			}
			// A memory expands once, at the shallowest depth it is reached.
			if expandedFrom[seed.id] {
				continue
			}
			expandedFrom[seed.id] = true

			edges, err := s.getEdgesForExpansion(ctx, seed.id, cfg.MinEdgeWeight, cfg.MaxEdgesPerSeed)
			if err != nil {
				continue
			}

			for _, edge := range edges {
				if totalExpanded >= cfg.MaxExpansionTotal {
					break
				}

				// Depth gate: past the first hop a relation is followed only if
				// its own policy grants that much reach.
				if hop > hopsFor(edge.Rel) {
					continue
				}

				neighborID, followable := neighborForSeed(edge, seed.id)
				if !followable {
					continue // self-loop, or the relation does not travel this way
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

				// Handling class decides whether this neighbour may claim reserved
				// budget during packing. Score handling below stays specific to
				// `contradicts`: measurement showed score is not what carries a
				// neighbour past the near-duplicate wall, so widening it would add
				// risk without adding effect. Reservation is the lever being widened.
				isReserved := reservesBudget(edge.Rel)
				if isReserved {
					if existing, ok := scoreMap[neighborID]; ok {
						existing.reserved = true
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
					// Dormant memories are archived and do not surface — unless a
					// RESERVE-CLASS edge is what reached them. Dormant means
					// "not worth surfacing on its own", and a reserve-class
					// neighbour is never surfacing on its own: something already
					// in context is false without it. Measured need on the live
					// umbreon store: 78 of 144 contradicts refuters had decayed
					// to dormant while their stale targets stayed live in ltm —
					// stale-without-fresh manufactured by the lifecycle, the
					// same shape that once hid a correct allergy fact.
					// Background and accompany relations still respect
					// dormancy, so archives stay archives.
					if m.Tier == "dormant" && !resurrectsDormant(edge, neighborID, m.CreatedAt, seed.createdAt) {
						continue
					}
					// Cap propagated score for edge-only candidates (except contradicts)
					if !isContradiction && propagated > 0.3 {
						propagated = 0.3
					}
					// viaEdge is deliberately NOT set for relates_to arrivals.
					// relates_to is itself a similarity claim, so a relevance
					// floor legitimately applies to it — and on a store with tens
					// of thousands of auto-linked near-duplicate edges, exempting
					// it would re-admit exactly the sibling glut the floor
					// happens to suppress. Typed relations assert something that
					// is not similarity; those are the ones the floor must not
					// judge.
					structural := expansionDirectionsFor(edge.Rel).Handling != handleBackground
					scoreMap[neighborID] = &contextCandidate{memory: *m, score: propagated, reserved: isReserved, viaEdge: structural}
					originalScores[neighborID] = 0 // no direct score
					totalExpanded++
					// This neighbour may itself seed the next hop. Its propagated
					// score carries forward, so damping compounds with depth and a
					// distant memory cannot outrank a nearer one.
					nextFrontier = append(nextFrontier, expansionSeed{id: neighborID, score: propagated, createdAt: m.CreatedAt})
				}
			}
		}
		if len(nextFrontier) == 0 {
			break
		}
		sort.Slice(nextFrontier, func(i, j int) bool { return nextFrontier[i].score > nextFrontier[j].score })
		frontier = nextFrontier
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
			// LCM parent-boost is for tight consolidations. A parent with
			// huge fan-out (auto-digest of ~95 heartbeat conversations)
			// summarizes everything and informs nothing — boosting it (at
			// the child's score, floor 0.3) plus child-suppression REPLACED
			// the specific relevant memory with the blob on every hit.
			if kids, err := s.getContainsChildren(ctx, parentID); err != nil ||
				len(kids) > parentBoostMaxChildren() {
				continue
			}
			m, err := s.loadMemoryByID(ctx, parentID)
			if err != nil {
				continue
			}
			// Skip dormant memories — they are archived and should not surface.
			if m.Tier == "dormant" {
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
	now := s.now().UTC().Format(time.RFC3339)
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories
		 WHERE ns = ? AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at > ?)`,
		ns, now).Scan(&count)
	return count, err
}

// computeContextScore calculates the composite context score for a memory.
func computeContextScore(m model.Memory, similarity float64, now time.Time, scope *SessionScope) float64 {
	return computeContextScoreWithAccess(m, similarity, now, scope, nil)
}

// computeContextScoreWithAccess is computeContextScore plus an optional logged
// access history; only the ACT-R path (GHOST_ACTR=1) consumes it, using exact
// activation when timestamps exist and the approximation otherwise.
func computeContextScoreWithAccess(m model.Memory, similarity float64, now time.Time, scope *SessionScope, accesses []time.Time) float64 {
	// Relevance: use vector cosine similarity when available (>= 0.3 threshold from
	// vector search), otherwise use 0.5 base for FTS/LIKE matches. Values below 0.3
	// are RRF fusion scores (not cosine), and would cripple relevance if used directly.
	// similarity carries either a real cosine (vector-sourced) or a graded
	// term-match relevance in [0.15, 0.70] computed at the call site; both
	// are usable directly. Zero means "ungraded" (legacy callers) → neutral.
	relevance := similarity
	if relevance <= 0 {
		relevance = 0.5
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

	// Relevance gate: with weak topical evidence, the other signals must
	// not carry the memory into context — recency/importance are tie-
	// breakers among relevant candidates, not substitutes for relevance.
	// Smooth ramp: full strength at relevance ≥0.45 (any real cosine hit
	// or a strong term match), scaling down toward zero below. This is
	// what actually stops novel-topic flooding: a graze-one-template-term
	// filler memory grades ~0.3 relevance and gets ~40% of its composite,
	// landing under the MinScore floor despite perfect recency.
	if relevance < 0.45 {
		gate := (relevance - 0.15) / 0.30
		if gate < 0.05 {
			gate = 0.05
		}
		base *= gate
	}

	// Opt-in ACT-R mode (GHOST_ACTR=1): one activation scalar subsumes the
	// separate recency and access-frequency heuristics (see actr.go).
	if actrEnabled() {
		activation := actrActivation(m, now)
		if len(accesses) > 0 {
			activation = actrActivationExact(m.CreatedAt, accesses, now)
		}
		base = relevance*w.relevance + importance*w.importance +
			activation*(w.recency+w.access)
	}

	// Opt-in utility term: reward memories that proved useful (co-retrieved) to
	// THIS user. utility_count is captured but unused by default; GHOST_UTILITY_WEIGHT
	// turns it into ranking lift. Additive so accessFreq stays intact. Default 0.
	if uw := utilityWeight(); uw > 0 {
		base += utilityRatio(m) * uw
	}

	return base * tierMultiplier(m.Tier) * scope.boost(m)
}

// utilityWeight is the opt-in weight for the utility signal (GHOST_UTILITY_WEIGHT,
// default 0 = off). Gated pending a public-suite A/B; the personal-agent eval
// exercises it with the knob on.
func utilityWeight() float64 {
	if env := os.Getenv("GHOST_UTILITY_WEIGHT"); env != "" {
		if v, err := strconv.ParseFloat(env, 64); err == nil && v >= 0 && v <= 2 {
			return v
		}
	}
	return 0
}

// utilityRatio is utility_count / access_count, clamped to [0,1] — the fraction
// of retrievals in which this memory proved useful.
func utilityRatio(m model.Memory) float64 {
	if m.AccessCount <= 0 {
		return 0
	}
	r := float64(m.UtilityCount) / float64(m.AccessCount)
	if r > 1 {
		r = 1
	}
	return r
}

// loadPinnedMemories loads memories with pinned=1, ordered by importance.
func (s *SQLiteStore) loadPinnedMemories(ctx context.Context, ns string) ([]model.Memory, error) {
	now := s.now().UTC().Format(time.RFC3339)

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
		m.importance, m.utility_count, m.tier, m.est_tokens, m.pinned, m.source_user, m.source_kind, m.source_scope
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

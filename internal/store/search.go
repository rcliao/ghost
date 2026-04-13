package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/rcliao/ghost/internal/embedding"
	"github.com/rcliao/ghost/internal/model"
)

// SearchParams holds parameters for searching memories.
type SearchParams struct {
	NS            string
	Query         string
	Kind          string
	Tags          []string
	Limit         int
	ExcludeTiers  []string  // tiers to exclude (e.g. ["dormant", "sensory"])
	IncludeAll    bool      // if true, skip default tier exclusions
	ReferenceTime time.Time // if set, temporal scoring uses this instead of now()
	After         time.Time // if set, only return memories created after this time
	Before        time.Time // if set, only return memories created before this time
	MultiQuery    bool      // if true, decompose query into sub-queries for multi-hop retrieval
	ExpandEdges   bool      // if true, expand results via 1-hop graph edges for multi-hop retrieval
}

// SearchResult wraps a memory with optional match info.
type SearchResult struct {
	model.Memory
	MatchChunk *model.Chunk `json:"match_chunk,omitempty"`
	Similarity float64      `json:"similarity,omitempty"`
}

// englishStopWords contains common English stop words that FTS5 treats as noise.
// Queries consisting entirely of stop words would match nothing with AND-join,
// so we strip them and use OR-join for the remaining terms.
var englishStopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "but": true, "by": true, "do": true, "for": true, "from": true,
	"had": true, "has": true, "have": true, "he": true, "her": true, "his": true,
	"how": true, "i": true, "if": true, "in": true, "into": true, "is": true,
	"it": true, "its": true, "me": true, "my": true, "no": true, "not": true,
	"of": true, "on": true, "or": true, "our": true, "she": true, "so": true,
	"that": true, "the": true, "their": true, "them": true, "then": true,
	"there": true, "these": true, "they": true, "this": true, "to": true,
	"us": true, "was": true, "we": true, "were": true, "what": true, "when": true,
	"where": true, "which": true, "who": true, "will": true, "with": true,
	"would": true, "you": true, "your": true,
}

// buildFTSQuery strips stop words from the query and joins remaining terms
// with OR. Returns empty string if no meaningful terms remain.
func buildFTSQuery(query string) string {
	words := strings.Fields(query)
	var terms []string
	for _, w := range words {
		lower := strings.ToLower(w)
		if !englishStopWords[lower] {
			terms = append(terms, lower)
		}
	}
	if len(terms) == 0 {
		return ""
	}
	return strings.Join(terms, " OR ")
}

// rrfScore computes the Reciprocal Rank Fusion score for a result appearing
// at the given ranks across multiple retrieval methods. k=60 per the original
// RRF paper (Cormack et al., 2009).
func rrfScore(ranks []int, k int) float64 {
	if k <= 0 {
		k = 60
	}
	var score float64
	for _, r := range ranks {
		score += 1.0 / float64(k+r)
	}
	return score
}

// queryTermOverlap computes the fraction of query terms (non-stopwords) that
// appear in the content. This is a lightweight keyword-matching signal that
// complements embedding similarity for entity-specific queries.
func queryTermOverlap(query, content string) float64 {
	queryWords := strings.Fields(strings.ToLower(query))
	var terms []string
	for _, w := range queryWords {
		w = strings.Trim(w, "?.,!\"'()[]{}:;")
		if len(w) > 2 && !englishStopWords[w] {
			terms = append(terms, w)
		}
	}
	if len(terms) == 0 {
		return 0
	}
	contentLower := strings.ToLower(content)
	hits := 0
	for _, t := range terms {
		if strings.Contains(contentLower, t) {
			hits++
		}
	}
	return float64(hits) / float64(len(terms))
}

// temporalKeywords are words that signal the user wants recency-ranked results.
var temporalKeywords = map[string]bool{
	"yesterday": true, "today": true, "recent": true, "latest": true,
	"last": true, "newest": true, "just": true, "ago": true,
	"currently": true, "now": true, "earlier": true, "previously": true,
}

// updateKeywords signal the user wants the most current version of a fact.
// Knowledge-update queries like "what is my current address" or "where do I live now"
// need the most recent session mentioning the topic, not the original.
var updateKeywords = map[string]bool{
	"current": true, "currently": true, "now": true, "latest": true,
	"updated": true, "changed": true, "new": true, "moved": true,
	"switched": true, "replaced": true, "nowadays": true,
}

// hasUpdateIntent returns true if the query likely asks about an updated/current fact.
func hasUpdateIntent(query string) bool {
	words := strings.Fields(strings.ToLower(query))
	for _, w := range words {
		if updateKeywords[w] {
			return true
		}
	}
	return false
}

// userTermOverlap computes query term overlap against only user-spoken lines.
// This helps surface sessions where the USER (not the assistant) mentioned relevant facts.
func userTermOverlap(query, content string) float64 {
	queryWords := strings.Fields(strings.ToLower(query))
	var terms []string
	for _, w := range queryWords {
		w = strings.Trim(w, "?.,!\"'()[]{}:;")
		if len(w) > 2 && !englishStopWords[w] {
			terms = append(terms, w)
		}
	}
	if len(terms) == 0 {
		return 0
	}

	// Extract user-spoken lines only
	lines := strings.Split(content, "\n")
	var userText strings.Builder
	for _, line := range lines {
		lower := strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(lower, "user:") || strings.HasPrefix(lower, "human:") {
			userText.WriteString(lower)
			userText.WriteByte(' ')
		}
	}
	userContent := userText.String()
	if userContent == "" {
		return 0
	}

	hits := 0
	for _, t := range terms {
		if strings.Contains(userContent, t) {
			hits++
		}
	}
	return float64(hits) / float64(len(terms))
}

// hasTemporalIntent returns true if the query contains temporal keywords.
func hasTemporalIntent(query string) bool {
	for _, w := range strings.Fields(query) {
		if temporalKeywords[strings.ToLower(w)] {
			return true
		}
	}
	return false
}

// decomposeQuery splits a complex query into sub-queries for multi-hop retrieval.
// Uses heuristic clause splitting on conjunctions. Returns nil if the query is simple.
func decomposeQuery(query string) []string {
	lower := strings.ToLower(query)

	// Pattern 1: Conjunction separators ("and", "also", "as well as")
	for _, sep := range []string{" and ", " also ", " as well as "} {
		if idx := strings.Index(lower, sep); idx > 10 && idx < len(lower)-10 {
			parts := []string{
				strings.TrimSpace(query[:idx]),
				strings.TrimSpace(query[idx+len(sep):]),
			}
			if len(parts[0]) > 15 && len(parts[1]) > 15 {
				return parts
			}
		}
	}

	// Pattern 2: Comma-separated clauses ("who is X, and where does Y")
	if commaIdx := strings.Index(query, ", "); commaIdx > 15 && commaIdx < len(query)-15 {
		after := strings.TrimSpace(query[commaIdx+2:])
		// Strip leading "and" if present
		afterLower := strings.ToLower(after)
		if strings.HasPrefix(afterLower, "and ") {
			after = strings.TrimSpace(after[4:])
		}
		if len(after) > 15 {
			return []string{strings.TrimSpace(query[:commaIdx]), after}
		}
	}

	// Pattern 3: "both X and Y" where X and Y are substantial
	if strings.HasPrefix(lower, "both ") {
		rest := query[5:]
		restLower := strings.ToLower(rest)
		if idx := strings.Index(restLower, " and "); idx > 10 && idx < len(rest)-10 {
			return []string{
				strings.TrimSpace(rest[:idx]),
				strings.TrimSpace(rest[idx+5:]),
			}
		}
	}

	// Pattern 4: Multiple question words ("who... where...", "what... how...")
	qWords := []string{"who ", "what ", "where ", "when ", "how ", "which ", "why "}
	var qPositions []int
	for _, qw := range qWords {
		pos := 0
		for {
			idx := strings.Index(lower[pos:], qw)
			if idx == -1 {
				break
			}
			absIdx := pos + idx
			// Only count if at word boundary
			if absIdx == 0 || lower[absIdx-1] == ' ' || lower[absIdx-1] == ',' {
				qPositions = append(qPositions, absIdx)
			}
			pos = absIdx + len(qw)
		}
	}
	if len(qPositions) >= 2 {
		sort.Ints(qPositions)
		p1 := strings.TrimSpace(query[qPositions[0]:qPositions[1]])
		p2 := strings.TrimSpace(query[qPositions[1]:])
		p1 = strings.TrimRight(p1, ",;")
		if len(p1) > 15 && len(p2) > 15 {
			return []string{p1, p2}
		}
	}

	return nil
}

// Search finds memories whose content or chunks match the query.
// Uses RRF (Reciprocal Rank Fusion) to merge results from FTS5, LIKE, and
// vector search into a single ranked list.
// By default, dormant-tier memories are excluded. Set IncludeAll=true to override.
func (s *SQLiteStore) Search(ctx context.Context, p SearchParams) ([]SearchResult, error) {
	// Multi-query: decompose complex queries and merge results
	if p.MultiQuery {
		if subQueries := decomposeQuery(p.Query); len(subQueries) > 0 {
			return s.searchMultiQuery(ctx, p, subQueries)
		}
	}

	// Default: exclude dormant and sensory tiers unless caller opts in.
	if !p.IncludeAll && len(p.ExcludeTiers) == 0 {
		p.ExcludeTiers = []string{"dormant", "sensory"}
	}

	limit := p.Limit
	if limit <= 0 {
		limit = 20
	}

	// Collect ranked result lists from each retrieval method
	type rankedResult struct {
		result SearchResult
		rank   int // 1-based position in this method's output
	}

	// Map: memory ID → ranks from each method
	methodRanks := map[string][]int{}
	memoryByID := map[string]SearchResult{}

	// 1. FTS5 search
	ftsResults := s.searchFTS(ctx, p, limit)
	for i, r := range ftsResults {
		id := r.ID
		methodRanks[id] = append(methodRanks[id], i+1)
		if _, ok := memoryByID[id]; !ok {
			memoryByID[id] = r
		}
	}

	// 2. LIKE search
	likeResults, _ := s.searchLike(ctx, p, nil, limit)
	for i, r := range likeResults {
		id := r.ID
		methodRanks[id] = append(methodRanks[id], i+1)
		if _, ok := memoryByID[id]; !ok {
			memoryByID[id] = r
		}
	}

	// 3. Vector search (if embedder available)
	if s.embedder != nil {
		vecResults, err := s.searchVector(ctx, p, nil, limit)
		if err == nil {
			for i, r := range vecResults {
				id := r.ID
				methodRanks[id] = append(methodRanks[id], i+1)
				// Prefer the vector result version (has Similarity set)
				if existing, ok := memoryByID[id]; !ok || existing.Similarity == 0 {
					memoryByID[id] = r
				}
			}
		}
	}

	// Merge via RRF
	type fusedResult struct {
		result   SearchResult
		rrfScore float64
	}
	var fused []fusedResult
	for id, ranks := range methodRanks {
		score := rrfScore(ranks, 20)
		r := memoryByID[id]
		// Only set Similarity to RRF score if no actual cosine similarity exists.
		// Vector search results have real cosine similarity (0.0-1.0) that downstream
		// context scoring depends on. Overwriting it with the RRF score (~0.016)
		// would cripple the relevance signal in context assembly.
		if r.Similarity == 0 {
			r.Similarity = math.Round(score*10000) / 10000
		}
		fused = append(fused, fusedResult{result: r, rrfScore: score})
	}

	// Sort by RRF score descending. For temporal queries, blend in recency
	// and prefer episodic memories (events) over procedural/semantic (facts).
	temporal := hasTemporalIntent(p.Query)
	refTime := p.ReferenceTime
	if refTime.IsZero() {
		refTime = time.Now()
	}

	// For temporal or update-intent queries, compute relative recency within
	// the result set. This ensures meaningful differentiation even for old
	// conversations where absolute decay would flatten to ~0 for all results.
	var relRecency map[string]float64
	if (temporal || hasUpdateIntent(p.Query)) && len(fused) > 0 {
		relRecency = make(map[string]float64, len(fused))
		// Find time range: oldest and newest in result set
		var minTime, maxTime time.Time
		for _, f := range fused {
			t := f.result.CreatedAt
			if minTime.IsZero() || t.Before(minTime) {
				minTime = t
			}
			if maxTime.IsZero() || t.After(maxTime) {
				maxTime = t
			}
		}
		span := maxTime.Sub(minTime).Hours()
		if span < 1 {
			span = 1 // avoid division by zero
		}
		for _, f := range fused {
			// Normalize: newest=1.0, oldest=0.0
			age := maxTime.Sub(f.result.CreatedAt).Hours()
			relRecency[f.result.ID] = 1.0 - (age / span)
		}
	}

	// Pre-compute term overlap scores for reranking (B/G improvement)
	termOverlaps := make(map[string]float64, len(fused))
	userOverlaps := make(map[string]float64, len(fused))
	for _, f := range fused {
		termOverlaps[f.result.ID] = queryTermOverlap(p.Query, f.result.Content)
		userOverlaps[f.result.ID] = userTermOverlap(p.Query, f.result.Content)
	}

	// Detect knowledge-update intent: queries about current/updated facts
	// should prefer the most recent session that matches.
	updateIntent := hasUpdateIntent(p.Query)

	sort.Slice(fused, func(i, j int) bool {
		si, sj := fused[i].rrfScore, fused[j].rrfScore

		// Rerank: blend RRF with term overlap for entity-specific queries
		overlapI := termOverlaps[fused[i].result.ID]
		overlapJ := termOverlaps[fused[j].result.ID]
		// User-turn overlap: additive bonus for sessions where user mentioned relevant facts
		uOverlapI := userOverlaps[fused[i].result.ID]
		uOverlapJ := userOverlaps[fused[j].result.ID]
		// Base: 70% RRF + 30% term overlap (unchanged from before)
		// Then add user-turn bonus on top (up to 10% of base score)
		si = si*0.7 + overlapI*0.3*si + uOverlapI*0.1*si
		sj = sj*0.7 + overlapJ*0.3*sj + uOverlapJ*0.1*sj

		if temporal || updateIntent {
			kindBoostI, kindBoostJ := 0.0, 0.0
			if fused[i].result.Kind == "episodic" {
				kindBoostI = 0.3
			}
			if fused[j].result.Kind == "episodic" {
				kindBoostJ = 0.3
			}

			recencyI := 0.0
			recencyJ := 0.0
			if relRecency != nil {
				recencyI = relRecency[fused[i].result.ID]
				recencyJ = relRecency[fused[j].result.ID]
			}

			if updateIntent && !temporal {
				// Knowledge-update: strong recency bias for topically relevant results
				// 50% base + 40% recency + 10% kind
				si = si*0.5 + recencyI*0.4 + kindBoostI*0.1
				sj = sj*0.5 + recencyJ*0.4 + kindBoostJ*0.1
			} else {
				// Temporal: 40% base + 30% relative recency + 30% kind
				si = si*0.4 + recencyI*0.3 + kindBoostI
				sj = sj*0.4 + recencyJ*0.3 + kindBoostJ
			}
		}
		if si != sj {
			return si > sj
		}
		return fused[i].result.CreatedAt.After(fused[j].result.CreatedAt)
	})

	if len(fused) > limit {
		fused = fused[:limit]
	}

	results := make([]SearchResult, len(fused))
	for i, f := range fused {
		results[i] = f.result
	}

	// Graph edge expansion: follow 1-hop edges from top results to find
	// connected memories for multi-hop queries.
	if p.ExpandEdges && len(results) > 0 {
		results = s.expandSearchEdges(ctx, p, results, limit)
	}

	// Cross-encoder reranking with MaxP (max passage) scoring:
	// Rerank the top-N candidates using cross-encoder with chunked scoring.
	// Only rerank top candidates to keep latency reasonable.
	if s.reranker != nil && len(results) > 1 {
		rerankN := 10
		if rerankN > len(results) {
			rerankN = len(results)
		}
		top := s.rerankMaxP(ctx, p.Query, results[:rerankN])
		results = append(top, results[rerankN:]...)
	}

	return results, nil
}

// expandSearchEdges follows 1-hop graph edges from the top search results
// to find connected memories that might be missed by keyword/vector search.
// This helps multi-hop queries where the answer spans multiple sessions
// connected by relates_to, depends_on, or refines edges.
func (s *SQLiteStore) expandSearchEdges(ctx context.Context, p SearchParams, results []SearchResult, limit int) []SearchResult {
	// Only expand from top-5 seeds to keep it fast
	seedN := 5
	if seedN > len(results) {
		seedN = len(results)
	}

	// Collect IDs already in results
	seen := make(map[string]bool, len(results))
	for _, r := range results {
		seen[r.ID] = true
	}

	// Follow edges from top seeds
	var expanded []SearchResult
	for _, seed := range results[:seedN] {
		edges, err := s.GetEdges(ctx, seed.ID)
		if err != nil {
			continue
		}
		for _, edge := range edges {
			// Get the neighbor ID (the other end of the edge)
			neighborID := edge.ToID
			if neighborID == seed.ID {
				neighborID = edge.FromID
			}
			if seen[neighborID] {
				continue
			}
			seen[neighborID] = true

			// Fetch the neighbor memory
			neighbor, err := s.getMemoryByID(ctx, neighborID)
			if err != nil || neighbor == nil {
				continue
			}

			// Only include if it has some query term overlap (not just any connected memory)
			overlap := queryTermOverlap(p.Query, neighbor.Content)
			if overlap < 0.1 {
				continue
			}

			expanded = append(expanded, SearchResult{
				Memory:     *neighbor,
				Similarity: float64(edge.Weight) * overlap, // score = edge weight × query relevance
			})
		}
	}

	if len(expanded) == 0 {
		return results
	}

	// Sort expanded by score
	sort.Slice(expanded, func(i, j int) bool {
		return expanded[i].Similarity > expanded[j].Similarity
	})

	// Interleave: keep original order but insert expanded results
	// Strategy: append expanded after original results, let the limit handle it
	combined := append(results, expanded...)
	if len(combined) > limit {
		combined = combined[:limit]
	}
	return combined
}

// getMemoryByID fetches a single memory by ID.
func (s *SQLiteStore) getMemoryByID(ctx context.Context, id string) (*model.Memory, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, ns, key, content, kind, tags, version, supersedes,
		        created_at, deleted_at, priority, access_count, last_accessed_at, meta, expires_at,
		        importance, utility_count, tier, est_tokens, pinned
		 FROM memories WHERE id = ? AND deleted_at IS NULL`, id)

	var m model.Memory
	var tagsJSON, supersedes, deletedAt, lastAccessed, meta, expiresAt, tier sql.NullString
	var createdAt string
	var importance sql.NullFloat64
	var utilityCount, estTokens, pinned sql.NullInt64

	err := row.Scan(
		&m.ID, &m.NS, &m.Key, &m.Content, &m.Kind, &tagsJSON,
		&m.Version, &supersedes, &createdAt, &deletedAt,
		&m.Priority, &m.AccessCount, &lastAccessed, &meta, &expiresAt,
		&importance, &utilityCount, &tier, &estTokens, &pinned,
	)
	if err != nil {
		return nil, err
	}

	m.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	if importance.Valid {
		m.Importance = importance.Float64
	}
	if estTokens.Valid {
		m.EstTokens = int(estTokens.Int64)
	}
	if tier.Valid {
		m.Tier = tier.String
	}
	if pinned.Valid && pinned.Int64 == 1 {
		m.Pinned = true
	}
	return &m, nil
}

// rerankMaxP re-scores results using cross-encoder with MaxP strategy.
// For each document, it chunks the content, scores each chunk, and uses
// the max score. This handles long sessions where the relevant passage
// may not be at the beginning.
//
// Scaling note: cross-encoder is CPU-bound and expensive. We cap:
//   - chunk size at 1024 chars (fewer chunks per doc)
//   - max 8 chunks per doc (prevents blowup on very long conversations)
//   - total work capped at ~80 cross-encoder calls per query
func (s *SQLiteStore) rerankMaxP(ctx context.Context, query string, results []SearchResult) []SearchResult {
	const maxChunkLen = 1024
	const maxChunksPerDoc = 8

	// Build a flat list of all chunks with their document index
	type chunkRef struct {
		docIdx   int
		chunkIdx int
	}
	var allChunks []string
	var refs []chunkRef

	for i, r := range results {
		chunks := chunkForReranking(r.Content, maxChunkLen)
		if len(chunks) == 0 {
			chunks = []string{r.Content}
		}
		if len(chunks) > maxChunksPerDoc {
			chunks = chunks[:maxChunksPerDoc]
		}
		for ci, c := range chunks {
			allChunks = append(allChunks, c)
			refs = append(refs, chunkRef{docIdx: i, chunkIdx: ci})
		}
	}

	if len(allChunks) == 0 {
		return results
	}

	// Single batch call to cross-encoder for all chunks
	reranked, err := s.reranker.Rerank(ctx, query, allChunks)
	if err != nil {
		return results // fallback to original order
	}

	// Find max score per document
	docMaxScore := make([]float32, len(results))
	for i, rr := range reranked {
		if i < len(refs) {
			docIdx := refs[rr.Index].docIdx
			if rr.Score > docMaxScore[docIdx] {
				docMaxScore[docIdx] = rr.Score
			}
		}
	}

	// Sort by max chunk score descending
	type docScore struct {
		idx   int
		score float32
	}
	scores := make([]docScore, len(results))
	for i := range results {
		scores[i] = docScore{idx: i, score: docMaxScore[i]}
	}
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})

	out := make([]SearchResult, len(results))
	for i, ds := range scores {
		out[i] = results[ds.idx]
		out[i].Similarity = float64(ds.score)
	}
	return out
}

// chunkForReranking splits content into overlapping passages for cross-encoder scoring.
func chunkForReranking(content string, maxLen int) []string {
	lines := strings.Split(content, "\n")
	var chunks []string
	var current []string
	currentLen := 0

	for _, line := range lines {
		lineLen := len(line) + 1 // +1 for newline
		if currentLen+lineLen > maxLen && len(current) > 0 {
			chunks = append(chunks, strings.Join(current, "\n"))
			// Keep last 2 lines for overlap
			if len(current) > 2 {
				overlap := current[len(current)-2:]
				current = make([]string, len(overlap))
				copy(current, overlap)
				currentLen = 0
				for _, l := range current {
					currentLen += len(l) + 1
				}
			} else {
				current = nil
				currentLen = 0
			}
		}
		current = append(current, line)
		currentLen += lineLen
	}
	if len(current) > 0 {
		chunks = append(chunks, strings.Join(current, "\n"))
	}
	return chunks
}

// searchMultiQuery runs multiple sub-queries and merges results via RRF.
// This helps multi-hop questions that need information from different sessions.
func (s *SQLiteStore) searchMultiQuery(ctx context.Context, p SearchParams, subQueries []string) ([]SearchResult, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = 20
	}

	// Run each sub-query
	type scored struct {
		result SearchResult
		ranks  []int
	}
	merged := make(map[string]*scored)

	for qi, sq := range subQueries {
		subP := p
		subP.Query = sq
		subP.MultiQuery = false // prevent recursion
		subP.Limit = limit

		results, err := s.Search(ctx, subP)
		if err != nil {
			continue
		}
		for rank, r := range results {
			if existing, ok := merged[r.ID]; ok {
				existing.ranks = append(existing.ranks, rank+1)
			} else {
				merged[r.ID] = &scored{
					result: r,
					ranks:  make([]int, qi), // pad with zeros for previous queries
				}
				// Pad missing ranks with a high value (not found = rank 100)
				for i := range merged[r.ID].ranks {
					merged[r.ID].ranks[i] = 100
				}
				merged[r.ID].ranks = append(merged[r.ID].ranks, rank+1)
			}
		}
		// Pad existing entries that weren't found in this sub-query
		for _, s := range merged {
			if len(s.ranks) <= qi {
				s.ranks = append(s.ranks, 100)
			}
		}
	}

	// Score via RRF and sort
	type fusedResult struct {
		result   SearchResult
		rrfScore float64
	}
	var fused []fusedResult
	for _, s := range merged {
		score := rrfScore(s.ranks, 20)
		fused = append(fused, fusedResult{result: s.result, rrfScore: score})
	}

	sort.Slice(fused, func(i, j int) bool {
		return fused[i].rrfScore > fused[j].rrfScore
	})

	if len(fused) > limit {
		fused = fused[:limit]
	}

	results := make([]SearchResult, len(fused))
	for i, f := range fused {
		results[i] = f.result
	}
	return results, nil
}

// appendDateFilter adds After/Before WHERE clauses to a query builder.
func appendDateFilter(where []string, args []interface{}, p SearchParams) ([]string, []interface{}) {
	if !p.After.IsZero() {
		where = append(where, "m.created_at >= ?")
		args = append(args, p.After.UTC().Format(time.RFC3339))
	}
	if !p.Before.IsZero() {
		where = append(where, "m.created_at <= ?")
		args = append(args, p.Before.UTC().Format(time.RFC3339))
	}
	return where, args
}

// searchFTS runs the FTS5 full-text search and returns ranked results.
func (s *SQLiteStore) searchFTS(ctx context.Context, p SearchParams, limit int) []SearchResult {
	ftsQuery := buildFTSQuery(p.Query)
	if ftsQuery == "" {
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	where := []string{"m.deleted_at IS NULL", "(m.expires_at IS NULL OR m.expires_at > ?)"}
	args := []interface{}{now}

	if p.NS != "" {
		nsf := ParseNSFilter(p.NS)
		clause, nsArgs := nsf.SQL("m.ns")
		if clause != "" {
			where = append(where, clause)
			args = append(args, nsArgs...)
		}
	}
	if p.Kind != "" {
		where = append(where, "m.kind = ?")
		args = append(args, p.Kind)
	}
	for _, tag := range p.Tags {
		where = append(where, "m.tags LIKE ?")
		args = append(args, "%\""+tag+"\"%")
	}
	for _, tier := range p.ExcludeTiers {
		where = append(where, "COALESCE(m.tier, 'stm') != ?")
		args = append(args, tier)
	}
	where, args = appendDateFilter(where, args, p)

	// Boost recency weight when query has temporal intent
	recencyWeight := 0.3
	ftsWeight := 0.5
	if hasTemporalIntent(p.Query) {
		recencyWeight = 0.7
		ftsWeight = 0.2
	}

	q := fmt.Sprintf(`
		SELECT m.id, m.ns, m.key, m.content, m.kind, m.tags, m.version, m.supersedes,
		       m.created_at, m.deleted_at, m.priority, m.access_count, m.last_accessed_at, m.meta, m.expires_at,
		       m.importance, m.utility_count, m.tier, m.est_tokens, m.pinned
		FROM memories m
		INNER JOIN (
			SELECT ns, key, MAX(version) AS max_ver
			FROM memories WHERE deleted_at IS NULL
			GROUP BY ns, key
		) latest ON m.ns = latest.ns AND m.key = latest.key AND m.version = latest.max_ver
		INNER JOIN chunks c ON c.memory_id = m.id
		INNER JOIN chunks_fts fts ON c.rowid = fts.rowid
		WHERE %s AND chunks_fts MATCH ?
		GROUP BY m.id
		ORDER BY
			(CASE m.priority WHEN 'critical' THEN 4 WHEN 'high' THEN 3 WHEN 'normal' THEN 2 ELSE 1 END) * 0.1
			+ (1.0 / (1.0 + (julianday('now') - julianday(m.created_at)) / 7.0)) * %f
			+ (MIN(fts.rank) * -%f)
			DESC
		LIMIT ?`, strings.Join(where, " AND "), recencyWeight, ftsWeight)

	args = append(args, ftsQuery, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var results []SearchResult
	seen := map[string]bool{}
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			continue
		}
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		results = append(results, SearchResult{Memory: m})
	}
	return results
}

// searchVector performs semantic search using embeddings.
func (s *SQLiteStore) searchVector(ctx context.Context, p SearchParams, exclude map[string]bool, limit int) ([]SearchResult, error) {
	// Embed the query
	queryVec, err := s.embedder.Embed(ctx, p.Query)
	if err != nil {
		return nil, err
	}

	// Fetch all chunks with embeddings (filtered by ns if provided)
	now := time.Now().UTC().Format(time.RFC3339)
	where := "m.deleted_at IS NULL AND (m.expires_at IS NULL OR m.expires_at > ?) AND c.embedding IS NOT NULL"
	args := []interface{}{now}

	if p.NS != "" {
		nsf := ParseNSFilter(p.NS)
		clause, nsArgs := nsf.SQL("m.ns")
		if clause != "" {
			where += " AND " + clause
			args = append(args, nsArgs...)
		}
	}
	if p.Kind != "" {
		where += " AND m.kind = ?"
		args = append(args, p.Kind)
	}
	for _, tag := range p.Tags {
		where += " AND m.tags LIKE ?"
		args = append(args, "%\""+tag+"\"%")
	}
	for _, tier := range p.ExcludeTiers {
		where += " AND COALESCE(m.tier, 'stm') != ?"
		args = append(args, tier)
	}
	if !p.After.IsZero() {
		where += " AND m.created_at >= ?"
		args = append(args, p.After.UTC().Format(time.RFC3339))
	}
	if !p.Before.IsZero() {
		where += " AND m.created_at <= ?"
		args = append(args, p.Before.UTC().Format(time.RFC3339))
	}

	query := fmt.Sprintf(`
		SELECT m.id, m.ns, m.key, m.content, m.kind, m.tags, m.version, m.supersedes,
		       m.created_at, m.deleted_at, m.priority, m.access_count, m.last_accessed_at, m.meta, m.expires_at,
		       m.importance, m.utility_count, m.tier, m.est_tokens, m.pinned,
		       c.embedding
		FROM memories m
		INNER JOIN (
			SELECT ns, key, MAX(version) AS max_ver
			FROM memories WHERE deleted_at IS NULL
			GROUP BY ns, key
		) latest ON m.ns = latest.ns AND m.key = latest.key AND m.version = latest.max_ver
		INNER JOIN chunks c ON c.memory_id = m.id
		WHERE %s
		ORDER BY m.created_at DESC`, where)
	// No LIMIT: scan all memories for vector similarity. With 384-dim embeddings
	// (~1.5KB each), even 10K memories is only ~15MB and <50ms of cosine math.
	// The previous LIMIT 500 made older memories invisible to vector search.

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Score each chunk by cosine similarity, keep best per memory
	type scored struct {
		memory     model.Memory
		similarity float64
	}
	best := map[string]*scored{}

	for rows.Next() {
		var embJSON string
		m, err := scanMemoryWithExtra(rows, &embJSON)
		if err != nil {
			continue
		}
		if exclude[m.ID] {
			// Already in results, but we might want to add similarity score
		}

		var chunkVec embedding.Vector
		if err := json.Unmarshal([]byte(embJSON), &chunkVec); err != nil {
			continue
		}

		sim := embedding.CosineSimilarity(queryVec, chunkVec)
		if existing, ok := best[m.ID]; !ok || sim > existing.similarity {
			best[m.ID] = &scored{memory: m, similarity: sim}
		}
	}

	// Convert to results, filter by minimum similarity
	var results []SearchResult
	for _, s := range best {
		if s.similarity < 0.2 { // minimum threshold
			continue
		}
		results = append(results, SearchResult{
			Memory:     s.memory,
			Similarity: math.Round(s.similarity*1000) / 1000,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Similarity > results[j].Similarity
	})

	if len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}

// scanMemoryWithExtra scans a memory row plus additional columns.
func scanMemoryWithExtra(row scanner, extras ...interface{}) (model.Memory, error) {
	var m model.Memory
	var tagsJSON, supersedes, deletedAt, lastAccessed, meta, expiresAt, tier sql.NullString
	var createdAt string
	var importance sql.NullFloat64
	var utilityCount, estTokens, pinned sql.NullInt64

	dest := []interface{}{
		&m.ID, &m.NS, &m.Key, &m.Content, &m.Kind, &tagsJSON,
		&m.Version, &supersedes, &createdAt, &deletedAt,
		&m.Priority, &m.AccessCount, &lastAccessed, &meta, &expiresAt,
		&importance, &utilityCount, &tier, &estTokens, &pinned,
	}
	dest = append(dest, extras...)

	err := row.Scan(dest...)
	if err != nil {
		return m, err
	}

	m.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	if supersedes.Valid {
		m.Supersedes = supersedes.String
	}
	if deletedAt.Valid {
		t, _ := time.Parse(time.RFC3339, deletedAt.String)
		m.DeletedAt = &t
	}
	if lastAccessed.Valid {
		t, _ := time.Parse(time.RFC3339, lastAccessed.String)
		m.LastAccessedAt = &t
	}
	if meta.Valid {
		m.Meta = meta.String
	}
	if tagsJSON.Valid {
		json.Unmarshal([]byte(tagsJSON.String), &m.Tags)
	}
	if expiresAt.Valid {
		t, _ := time.Parse(time.RFC3339, expiresAt.String)
		m.ExpiresAt = &t
	}
	if importance.Valid {
		m.Importance = importance.Float64
	} else {
		m.Importance = 0.5
	}
	if utilityCount.Valid {
		m.UtilityCount = int(utilityCount.Int64)
	}
	if tier.Valid {
		m.Tier = tier.String
	} else {
		m.Tier = "stm"
	}
	if estTokens.Valid {
		m.EstTokens = int(estTokens.Int64)
	}
	if pinned.Valid && pinned.Int64 != 0 {
		m.Pinned = true
	}

	return m, nil
}

// searchLike is the fallback when FTS5 fails.
// It matches individual non-stop-word terms with OR for better recall on
// natural language queries (e.g. "do you know who EV is" → LIKE '%EV%').
func (s *SQLiteStore) searchLike(ctx context.Context, p SearchParams, baseWhere []string, limit int) ([]SearchResult, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	where := []string{"m.deleted_at IS NULL", "(m.expires_at IS NULL OR m.expires_at > ?)"}
	args := []interface{}{now}

	if p.NS != "" {
		nsf := ParseNSFilter(p.NS)
		clause, nsArgs := nsf.SQL("m.ns")
		if clause != "" {
			where = append(where, clause)
			args = append(args, nsArgs...)
		}
	}
	if p.Kind != "" {
		where = append(where, "m.kind = ?")
		args = append(args, p.Kind)
	}
	for _, tag := range p.Tags {
		where = append(where, "m.tags LIKE ?")
		args = append(args, "%\""+tag+"\"%")
	}
	for _, tier := range p.ExcludeTiers {
		where = append(where, "COALESCE(m.tier, 'stm') != ?")
		args = append(args, tier)
	}
	where, args = appendDateFilter(where, args, p)
	_ = baseWhere // we rebuild where clauses here

	// Build per-term LIKE clauses joined with OR for better recall.
	var likeClauses []string
	words := strings.Fields(p.Query)
	for _, w := range words {
		if englishStopWords[strings.ToLower(w)] {
			continue
		}
		pattern := "%" + w + "%"
		likeClauses = append(likeClauses, "(m.content LIKE ? OR m.key LIKE ? OR c.text LIKE ?)")
		args = append(args, pattern, pattern, pattern)
	}
	// If all words were stop words, fall back to full-phrase LIKE
	if len(likeClauses) == 0 {
		pattern := "%" + p.Query + "%"
		likeClauses = append(likeClauses, "(m.content LIKE ? OR m.key LIKE ? OR c.text LIKE ?)")
		args = append(args, pattern, pattern, pattern)
	}

	likeExpr := "(" + strings.Join(likeClauses, " OR ") + ")"

	sql := fmt.Sprintf(`
		SELECT DISTINCT m.id, m.ns, m.key, m.content, m.kind, m.tags, m.version, m.supersedes,
		       m.created_at, m.deleted_at, m.priority, m.access_count, m.last_accessed_at, m.meta, m.expires_at,
		       m.importance, m.utility_count, m.tier, m.est_tokens, m.pinned
		FROM memories m
		INNER JOIN (
			SELECT ns, key, MAX(version) AS max_ver
			FROM memories WHERE deleted_at IS NULL
			GROUP BY ns, key
		) latest ON m.ns = latest.ns AND m.key = latest.key AND m.version = latest.max_ver
		LEFT JOIN chunks c ON c.memory_id = m.id
		WHERE %s AND %s
		ORDER BY m.created_at DESC
		LIMIT ?`, strings.Join(where, " AND "), likeExpr)

	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	seen := map[string]bool{}
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		results = append(results, SearchResult{Memory: m})
	}
	return results, nil
}

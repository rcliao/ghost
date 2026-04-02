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
	NS           string
	Query        string
	Kind         string
	Tags         []string
	Limit        int
	ExcludeTiers []string // tiers to exclude (e.g. ["dormant", "sensory"])
	IncludeAll   bool     // if true, skip default tier exclusions
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

// temporalKeywords are words that signal the user wants recency-ranked results.
var temporalKeywords = map[string]bool{
	"yesterday": true, "today": true, "recent": true, "latest": true,
	"last": true, "newest": true, "just": true, "ago": true,
	"currently": true, "now": true, "earlier": true, "previously": true,
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

// Search finds memories whose content or chunks match the query.
// Uses RRF (Reciprocal Rank Fusion) to merge results from FTS5, LIKE, and
// vector search into a single ranked list.
// By default, dormant-tier memories are excluded. Set IncludeAll=true to override.
func (s *SQLiteStore) Search(ctx context.Context, p SearchParams) ([]SearchResult, error) {
	// Default: exclude dormant and sensory tiers unless caller opts in.
	// Dormant = archived, sensory = ephemeral observations not yet promoted.
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
		score := rrfScore(ranks, 60)
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
	sort.Slice(fused, func(i, j int) bool {
		si, sj := fused[i].rrfScore, fused[j].rrfScore
		if temporal {
			// Episodic memories are events — boost them for temporal queries
			kindBoostI, kindBoostJ := 0.0, 0.0
			if fused[i].result.Kind == "episodic" {
				kindBoostI = 0.3
			}
			if fused[j].result.Kind == "episodic" {
				kindBoostJ = 0.3
			}
			// Blend: 40% RRF + 30% recency + 30% kind
			now := time.Now()
			ageDaysI := now.Sub(fused[i].result.CreatedAt).Hours() / 24.0
			ageDaysJ := now.Sub(fused[j].result.CreatedAt).Hours() / 24.0
			recencyI := math.Exp(-0.1 * ageDaysI)
			recencyJ := math.Exp(-0.1 * ageDaysJ)
			si = si*0.4 + recencyI*0.3 + kindBoostI
			sj = sj*0.4 + recencyJ*0.3 + kindBoostJ
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
	return results, nil
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
		ORDER BY m.created_at DESC
		LIMIT 500`, where)

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
		if s.similarity < 0.3 { // minimum threshold
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

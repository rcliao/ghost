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

	"github.com/rcliao/agent-memory/internal/embedding"
	"github.com/rcliao/agent-memory/internal/model"
)

// SearchParams holds parameters for searching memories.
type SearchParams struct {
	NS    string
	Query string
	Kind  string
	Limit int
}

// SearchResult wraps a memory with optional match info.
type SearchResult struct {
	model.Memory
	MatchChunk *model.Chunk `json:"match_chunk,omitempty"`
	Similarity float64      `json:"similarity,omitempty"`
}

// Search finds memories whose content or chunks match the query substring.
func (s *SQLiteStore) Search(ctx context.Context, p SearchParams) ([]SearchResult, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = 20
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

	// Try FTS5 first for ranked results, fall back to LIKE for simple substrings
	// FTS5 query: split terms and join with AND for better matching
	ftsQuery := strings.Join(strings.Fields(p.Query), " AND ")

	sql := fmt.Sprintf(`
		SELECT m.id, m.ns, m.key, m.content, m.kind, m.tags, m.version, m.supersedes,
		       m.created_at, m.deleted_at, m.priority, m.access_count, m.last_accessed_at, m.meta, m.expires_at
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
			(CASE m.priority WHEN 'critical' THEN 4 WHEN 'high' THEN 3 WHEN 'normal' THEN 2 ELSE 1 END) * 0.2
			+ (julianday(m.created_at) / julianday('now')) * 0.3
			+ (MIN(fts.rank) * -0.5)
			DESC
		LIMIT ?`, strings.Join(where, " AND "))

	args = append(args, ftsQuery, limit)

	// Try FTS5 first; on error fall back to LIKE entirely
	rows, err := s.db.QueryContext(ctx, sql, args...)
	if err != nil {
		return s.searchLike(ctx, p, where, limit)
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

	// Supplement with LIKE matches (catches key matches and content that FTS5 tokenizer misses)
	if len(results) < limit {
		likeResults, err := s.searchLike(ctx, p, where, limit-len(results))
		if err == nil {
			for _, r := range likeResults {
				if !seen[r.ID] {
					seen[r.ID] = true
					results = append(results, r)
				}
			}
		}
	}

	// If embedder is available, do vector search and merge/re-rank
	if s.embedder != nil {
		vecResults, err := s.searchVector(ctx, p, seen, limit)
		if err == nil && len(vecResults) > 0 {
			for _, r := range vecResults {
				if !seen[r.ID] {
					seen[r.ID] = true
					results = append(results, r)
				}
			}
			// Re-rank by similarity when we have vector scores
			sort.Slice(results, func(i, j int) bool {
				// Prefer higher similarity; fall back to recency
				si, sj := results[i].Similarity, results[j].Similarity
				if si != sj {
					return si > sj
				}
				return results[i].CreatedAt.After(results[j].CreatedAt)
			})
			if len(results) > limit {
				results = results[:limit]
			}
		}
	}

	return results, nil
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

	query := fmt.Sprintf(`
		SELECT m.id, m.ns, m.key, m.content, m.kind, m.tags, m.version, m.supersedes,
		       m.created_at, m.deleted_at, m.priority, m.access_count, m.last_accessed_at, m.meta, m.expires_at,
		       c.embedding
		FROM memories m
		INNER JOIN (
			SELECT ns, key, MAX(version) AS max_ver
			FROM memories WHERE deleted_at IS NULL
			GROUP BY ns, key
		) latest ON m.ns = latest.ns AND m.key = latest.key AND m.version = latest.max_ver
		INNER JOIN chunks c ON c.memory_id = m.id
		WHERE %s`, where)

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
	var tagsJSON, supersedes, deletedAt, lastAccessed, meta, expiresAt sql.NullString
	var createdAt string

	dest := []interface{}{
		&m.ID, &m.NS, &m.Key, &m.Content, &m.Kind, &tagsJSON,
		&m.Version, &supersedes, &createdAt, &deletedAt,
		&m.Priority, &m.AccessCount, &lastAccessed, &meta, &expiresAt,
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

	return m, nil
}

// searchLike is the fallback when FTS5 fails.
func (s *SQLiteStore) searchLike(ctx context.Context, p SearchParams, baseWhere []string, limit int) ([]SearchResult, error) {
	likeQuery := "%" + p.Query + "%"
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
	_ = baseWhere // we rebuild where clauses here

	sql := fmt.Sprintf(`
		SELECT DISTINCT m.id, m.ns, m.key, m.content, m.kind, m.tags, m.version, m.supersedes,
		       m.created_at, m.deleted_at, m.priority, m.access_count, m.last_accessed_at, m.meta, m.expires_at
		FROM memories m
		INNER JOIN (
			SELECT ns, key, MAX(version) AS max_ver
			FROM memories WHERE deleted_at IS NULL
			GROUP BY ns, key
		) latest ON m.ns = latest.ns AND m.key = latest.key AND m.version = latest.max_ver
		LEFT JOIN chunks c ON c.memory_id = m.id
		WHERE %s AND (m.content LIKE ? OR m.key LIKE ? OR c.text LIKE ?)
		ORDER BY m.created_at DESC
		LIMIT ?`, strings.Join(where, " AND "))

	args = append(args, likeQuery, likeQuery, likeQuery, limit)

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

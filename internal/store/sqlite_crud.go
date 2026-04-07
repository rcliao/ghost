package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rcliao/ghost/internal/chunker"
	"github.com/rcliao/ghost/internal/embedding"
	"github.com/rcliao/ghost/internal/model"
)

func (s *SQLiteStore) Put(ctx context.Context, p PutParams) (*model.Memory, error) {
	if err := ValidateNS(p.NS); err != nil {
		return nil, fmt.Errorf("invalid namespace: %w", err)
	}

	now := time.Now().UTC()
	id := s.newID()

	// Backward compat: tier=identity → ltm + pinned
	pinned := p.Pinned
	if p.Tier == "identity" {
		pinned = true
	}
	tier := tierOrDefault(p.Tier)

	kind := p.Kind
	if kind == "" {
		// Default kind based on tier: sensory/stm memories are temporal observations
		// (episodic) until consolidated into decontextualized facts (semantic).
		switch tier {
		case "sensory", "stm":
			kind = "episodic"
		default: // ltm, dormant
			kind = "semantic"
		}
	}
	priority := p.Priority
	if priority == "" {
		priority = "normal"
	}

	var tagsJSON *string
	if len(p.Tags) > 0 {
		b, _ := json.Marshal(p.Tags)
		s := string(b)
		tagsJSON = &s
	}

	var metaPtr *string
	if p.Meta != "" {
		metaPtr = &p.Meta
	}

	var expiresAt *string
	if p.TTL != "" {
		d, err := ParseTTL(p.TTL)
		if err != nil {
			return nil, fmt.Errorf("invalid ttl: %w", err)
		}
		exp := now.Add(d).Format(time.RFC3339)
		expiresAt = &exp
	}

	// Exact content dedup: skip if an active memory with identical content exists in namespace.
	// Fast check (no embeddings) that catches verbatim duplicate captures across sessions.
	if p.Dedup {
		var existingKey string
		s.db.QueryRowContext(ctx,
			`SELECT key FROM memories WHERE ns = ? AND content = ? AND deleted_at IS NULL
			 ORDER BY version DESC LIMIT 1`, p.NS, p.Content).Scan(&existingKey)
		if existingKey != "" {
			existing, err := s.Get(ctx, GetParams{NS: p.NS, Key: existingKey})
			if err == nil && len(existing) > 0 {
				return &existing[0], nil
			}
		}
	}

	// Semantic dedup: if enabled, search for semantically similar memories and skip if found
	if p.Dedup && s.embedder != nil {
		vec, err := s.embedder.Embed(ctx, p.Content)
		if err == nil && len(vec) > 0 {
			similar := s.findSimilarForDedup(ctx, p.NS, vec, 0.82)
			if similar != "" {
				// Return the existing memory instead of creating a duplicate
				existing, err := s.Get(ctx, GetParams{NS: p.NS, Key: similar})
				if err == nil && len(existing) > 0 {
					return &existing[0], nil
				}
			}
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Check for existing latest version
	var prevID string
	var prevVersion int
	err = tx.QueryRowContext(ctx,
		`SELECT id, version FROM memories
		 WHERE ns = ? AND key = ? AND deleted_at IS NULL
		 ORDER BY version DESC LIMIT 1`, p.NS, p.Key).Scan(&prevID, &prevVersion)

	version := 1
	var supersedes *string
	if err == nil {
		version = prevVersion + 1
		supersedes = &prevID
	}

	importance := p.Importance
	if importance <= 0 {
		importance = 0.5
	}
	estTokens := estimateTokens(p.Content)

	pinnedInt := 0
	if pinned {
		pinnedInt = 1
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO memories (id, ns, key, content, kind, tags, version, supersedes, created_at, priority, access_count, meta, expires_at, importance, est_tokens, tier, pinned)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?)`,
		id, p.NS, p.Key, p.Content, kind, tagsJSON, version, supersedes,
		now.Format(time.RFC3339), priority, metaPtr, expiresAt, importance, estTokens, tier, pinnedInt)
	if err != nil {
		return nil, fmt.Errorf("insert memory: %w", err)
	}

	// Chunk the content and batch-embed all chunks at once
	chunks := chunker.Chunk(p.Content, chunker.DefaultOptions())

	// Batch embed all chunk texts in a single call (much faster than one-at-a-time)
	var chunkVecs []embedding.Vector
	if s.embedder != nil && len(chunks) > 0 {
		texts := make([]string, len(chunks))
		for i, c := range chunks {
			texts[i] = c.Text
		}
		vecs, err := embedding.EmbedBatch(ctx, s.embedder, texts)
		if err == nil {
			chunkVecs = vecs
		}
		// Silently skip embedding errors — FTS5 still works
	}

	var firstChunkVec embedding.Vector
	for i, c := range chunks {
		chunkID := s.newID()

		var embeddingJSON *string
		if i < len(chunkVecs) && len(chunkVecs[i]) > 0 {
			b, _ := json.Marshal(chunkVecs[i])
			str := string(b)
			embeddingJSON = &str
			if i == 0 {
				firstChunkVec = chunkVecs[i]
			}
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO chunks (id, memory_id, seq, text, start_line, end_line, embedding)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			chunkID, id, i, c.Text, c.StartLine, c.EndLine, embeddingJSON)
		if err != nil {
			return nil, fmt.Errorf("insert chunk: %w", err)
		}
	}

	// Insert file references
	var fileRefs []model.FileRef
	for _, f := range p.Files {
		rel := f.Rel
		if rel == "" {
			rel = "modified"
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO memory_files (memory_id, path, rel, created_at) VALUES (?, ?, ?, ?)`,
			id, f.Path, rel, now.Format(time.RFC3339))
		if err != nil {
			return nil, fmt.Errorf("insert file ref: %w", err)
		}
		fileRefs = append(fileRefs, model.FileRef{Path: f.Path, Rel: rel})
	}

	// Re-link edges from old version to new version
	if supersedes != nil {
		if err := relinkEdges(ctx, tx, *supersedes, id); err != nil {
			return nil, fmt.Errorf("relink edges: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	// Auto-link edges to similar memories (after commit, non-transactional)
	// Errors are silently ignored — auto-linking is best-effort.
	if !p.SkipAutoLink {
		s.autoLinkEdges(ctx, id, p.NS, firstChunkVec)
	}

	mem := &model.Memory{
		ID:         id,
		NS:         p.NS,
		Key:        p.Key,
		Content:    p.Content,
		Kind:       kind,
		Tags:       p.Tags,
		Version:    version,
		CreatedAt:  now,
		Priority:   priority,
		Importance: importance,
		Tier:       tier,
		Pinned:     pinned,
		EstTokens:  estTokens,
		Meta:       p.Meta,
		ChunkCount: len(chunks),
		Files:      fileRefs,
	}
	if expiresAt != nil {
		t, _ := time.Parse(time.RFC3339, *expiresAt)
		mem.ExpiresAt = &t
	}
	if supersedes != nil {
		mem.Supersedes = *supersedes
	}

	return mem, nil
}

// BenchInsert is a fast-path insert for benchmarking. It creates a single memory
// with a single chunk (no splitting), pre-computed embedding from the embedder,
// and skips auto-linking, dedup, versioning, and file refs.
func (s *SQLiteStore) BenchInsert(ctx context.Context, ns, key, content string) error {
	now := time.Now().UTC()
	id := s.newID()
	chunkID := s.newID()

	// Get embedding for the full content (will hit cache if CachedEmbedder is used)
	var embJSON *string
	if s.embedder != nil {
		vec, err := s.embedder.Embed(ctx, content)
		if err == nil && len(vec) > 0 {
			b, _ := json.Marshal(vec)
			str := string(b)
			embJSON = &str
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO memories (id, ns, key, content, kind, version, created_at, priority, access_count, importance, est_tokens, tier, pinned)
		 VALUES (?, ?, ?, ?, 'episodic', 1, ?, 'normal', 0, 0.5, ?, 'stm', 0)`,
		id, ns, key, content, now.Format(time.RFC3339), estimateTokens(content))
	if err != nil {
		return fmt.Errorf("insert memory: %w", err)
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO chunks (id, memory_id, seq, text, start_line, end_line, embedding)
		 VALUES (?, ?, 0, ?, 1, ?, ?)`,
		chunkID, id, content, strings.Count(content, "\n")+1, embJSON)
	if err != nil {
		return fmt.Errorf("insert chunk: %w", err)
	}

	return tx.Commit()
}

func (s *SQLiteStore) Get(ctx context.Context, p GetParams) ([]model.Memory, error) {
	var query string
	var args []interface{}

	now := time.Now().UTC().Format(time.RFC3339)

	if p.History {
		// History shows all versions including expired (for audit)
		query = `SELECT id, ns, key, content, kind, tags, version, supersedes,
				        created_at, deleted_at, priority, access_count, last_accessed_at, meta, expires_at,
				        importance, utility_count, tier, est_tokens, pinned
				 FROM memories WHERE ns = ? AND key = ? AND deleted_at IS NULL
				 ORDER BY version DESC`
		args = []interface{}{p.NS, p.Key}
	} else if p.Version > 0 {
		query = `SELECT id, ns, key, content, kind, tags, version, supersedes,
				        created_at, deleted_at, priority, access_count, last_accessed_at, meta, expires_at,
				        importance, utility_count, tier, est_tokens, pinned
				 FROM memories WHERE ns = ? AND key = ? AND version = ? AND deleted_at IS NULL
				   AND (expires_at IS NULL OR expires_at > ?)
				 LIMIT 1`
		args = []interface{}{p.NS, p.Key, p.Version, now}
	} else {
		query = `SELECT id, ns, key, content, kind, tags, version, supersedes,
				        created_at, deleted_at, priority, access_count, last_accessed_at, meta, expires_at,
				        importance, utility_count, tier, est_tokens, pinned
				 FROM memories WHERE ns = ? AND key = ? AND deleted_at IS NULL
				   AND (expires_at IS NULL OR expires_at > ?)
				 ORDER BY version DESC LIMIT 1`
		args = []interface{}{p.NS, p.Key, now}
	}

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

	if len(memories) == 0 {
		return nil, fmt.Errorf("memory not found: %s/%s", p.NS, p.Key)
	}

	// Update access tracking for the latest
	if !p.History {
		now := time.Now().UTC().Format(time.RFC3339)
		s.db.ExecContext(ctx,
			`UPDATE memories SET access_count = access_count + 1, last_accessed_at = ? WHERE id = ?`,
			now, memories[0].ID)
	}

	// Load file references
	if err := s.loadFilesForMemories(ctx, memories); err != nil {
		return nil, err
	}

	return memories, nil
}

func (s *SQLiteStore) History(ctx context.Context, p HistoryParams) ([]model.Memory, error) {
	query := `SELECT id, ns, key, content, kind, tags, version, supersedes,
			         created_at, deleted_at, priority, access_count, last_accessed_at, meta, expires_at,
				        importance, utility_count, tier, est_tokens, pinned
			  FROM memories WHERE ns = ? AND key = ?
			  ORDER BY version ASC`

	rows, err := s.db.QueryContext(ctx, query, p.NS, p.Key)
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

	if len(memories) == 0 {
		return nil, fmt.Errorf("memory not found: %s/%s", p.NS, p.Key)
	}

	// Load file references
	if err := s.loadFilesForMemories(ctx, memories); err != nil {
		return nil, err
	}

	return memories, nil
}

func (s *SQLiteStore) List(ctx context.Context, p ListParams) ([]model.Memory, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = 20
	}

	// Build a query that returns only the latest version of each ns+key
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

	// Tag filtering
	for _, tag := range p.Tags {
		where = append(where, "m.tags LIKE ?")
		args = append(args, "%\""+tag+"\"%")
	}

	query := fmt.Sprintf(`
		SELECT m.id, m.ns, m.key, m.content, m.kind, m.tags, m.version, m.supersedes,
		       m.created_at, m.deleted_at, m.priority, m.access_count, m.last_accessed_at, m.meta, m.expires_at,
		       m.importance, m.utility_count, m.tier, m.est_tokens, m.pinned
		FROM memories m
		INNER JOIN (
			SELECT ns, key, MAX(version) AS max_ver
			FROM memories WHERE deleted_at IS NULL
			GROUP BY ns, key
		) latest ON m.ns = latest.ns AND m.key = latest.key AND m.version = latest.max_ver
		WHERE %s
		ORDER BY m.created_at DESC
		LIMIT ?`, strings.Join(where, " AND "))
	args = append(args, limit)

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

func (s *SQLiteStore) Rm(ctx context.Context, p RmParams) error {
	if p.Hard {
		if p.AllVersions {
			// Delete edges first
			s.db.ExecContext(ctx,
				`DELETE FROM memory_edges WHERE from_id IN (SELECT id FROM memories WHERE ns = ? AND key = ?) OR to_id IN (SELECT id FROM memories WHERE ns = ? AND key = ?)`,
				p.NS, p.Key, p.NS, p.Key)
			// Delete chunks
			_, err := s.db.ExecContext(ctx,
				`DELETE FROM chunks WHERE memory_id IN (SELECT id FROM memories WHERE ns = ? AND key = ?)`,
				p.NS, p.Key)
			if err != nil {
				return err
			}
			_, err = s.db.ExecContext(ctx, `DELETE FROM memories WHERE ns = ? AND key = ?`, p.NS, p.Key)
			return err
		}
		// Hard delete latest only
		var id string
		err := s.db.QueryRowContext(ctx,
			`SELECT id FROM memories WHERE ns = ? AND key = ? AND deleted_at IS NULL ORDER BY version DESC LIMIT 1`,
			p.NS, p.Key).Scan(&id)
		if err != nil {
			return fmt.Errorf("memory not found: %s/%s", p.NS, p.Key)
		}
		s.db.ExecContext(ctx, `DELETE FROM memory_edges WHERE from_id = ? OR to_id = ?`, id, id)
		s.db.ExecContext(ctx, `DELETE FROM chunks WHERE memory_id = ?`, id)
		_, err = s.db.ExecContext(ctx, `DELETE FROM memories WHERE id = ?`, id)
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if p.AllVersions {
		_, err := s.db.ExecContext(ctx,
			`UPDATE memories SET deleted_at = ? WHERE ns = ? AND key = ? AND deleted_at IS NULL`,
			now, p.NS, p.Key)
		return err
	}

	// Soft-delete latest version only
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM memories WHERE ns = ? AND key = ? AND deleted_at IS NULL ORDER BY version DESC LIMIT 1`,
		p.NS, p.Key).Scan(&id)
	if err != nil {
		return fmt.Errorf("memory not found: %s/%s", p.NS, p.Key)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE memories SET deleted_at = ? WHERE id = ?`, now, id)
	return err
}

// RmNamespace soft-deletes (or hard-deletes) all memories in the given namespace.
// Supports prefix matching (e.g., "reflect:*" deletes all under "reflect:").
func (s *SQLiteStore) RmNamespace(ctx context.Context, ns string, hard bool) (int64, error) {
	nsf := ParseNSFilter(ns)
	clause, args := nsf.SQL("ns")
	if clause == "" {
		return 0, fmt.Errorf("namespace filter cannot be empty")
	}

	if hard {
		// Delete chunks first
		_, err := s.db.ExecContext(ctx,
			`DELETE FROM chunks WHERE memory_id IN (SELECT id FROM memories WHERE `+clause+`)`, args...)
		if err != nil {
			return 0, fmt.Errorf("delete chunks: %w", err)
		}
		// Delete file refs
		_, err = s.db.ExecContext(ctx,
			`DELETE FROM memory_files WHERE memory_id IN (SELECT id FROM memories WHERE `+clause+`)`, args...)
		if err != nil {
			return 0, fmt.Errorf("delete file refs: %w", err)
		}
		// Delete links
		_, err = s.db.ExecContext(ctx,
			`DELETE FROM memory_links WHERE from_id IN (SELECT id FROM memories WHERE `+clause+`) OR to_id IN (SELECT id FROM memories WHERE `+clause+`)`,
			append(args, args...)...)
		if err != nil {
			return 0, fmt.Errorf("delete links: %w", err)
		}
		// Delete edges
		_, err = s.db.ExecContext(ctx,
			`DELETE FROM memory_edges WHERE from_id IN (SELECT id FROM memories WHERE `+clause+`) OR to_id IN (SELECT id FROM memories WHERE `+clause+`)`,
			append(args, args...)...)
		if err != nil {
			return 0, fmt.Errorf("delete edges: %w", err)
		}
		// Delete memories
		res, err := s.db.ExecContext(ctx,
			`DELETE FROM memories WHERE `+clause, args...)
		if err != nil {
			return 0, fmt.Errorf("delete memories: %w", err)
		}
		return res.RowsAffected()
	}

	// Soft delete
	now := time.Now().UTC().Format(time.RFC3339)
	allArgs := append([]interface{}{now}, args...)
	res, err := s.db.ExecContext(ctx,
		`UPDATE memories SET deleted_at = ? WHERE deleted_at IS NULL AND `+clause, allArgs...)
	if err != nil {
		return 0, fmt.Errorf("soft-delete namespace: %w", err)
	}
	return res.RowsAffected()
}

// findSimilarForDedup checks if a semantically similar memory already exists.
// Returns the key of the most similar memory above the threshold, or empty string.
func (s *SQLiteStore) findSimilarForDedup(ctx context.Context, ns string, vec embedding.Vector, threshold float64) string {
	now := time.Now().UTC().Format(time.RFC3339)

	// Load embeddings from recent memories in the same namespace
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.key, c.embedding
		 FROM memories m
		 JOIN chunks c ON c.memory_id = m.id AND c.seq = 0
		 WHERE m.ns = ? AND m.deleted_at IS NULL AND (m.expires_at IS NULL OR m.expires_at > ?)
		   AND c.embedding IS NOT NULL
		 ORDER BY m.created_at DESC
		 LIMIT 50`, ns, now)
	if err != nil {
		return ""
	}
	defer rows.Close()

	var bestKey string
	var bestSim float64

	for rows.Next() {
		var key, embJSON string
		if rows.Scan(&key, &embJSON) != nil {
			continue
		}
		var existingVec embedding.Vector
		if json.Unmarshal([]byte(embJSON), &existingVec) != nil {
			continue
		}
		sim := embedding.CosineSimilarity(vec, existingVec)
		if sim > threshold && sim > bestSim {
			bestSim = sim
			bestKey = key
		}
	}
	return bestKey
}

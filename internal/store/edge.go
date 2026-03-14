package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/rcliao/ghost/internal/embedding"
	"github.com/rcliao/ghost/internal/model"
)

// Edge represents a weighted, typed relation between two memories.
type Edge struct {
	FromID         string     `json:"from_id"`
	ToID           string     `json:"to_id"`
	Rel            string     `json:"rel"`
	Weight         float64    `json:"weight"`
	AccessCount    int        `json:"access_count"`
	LastAccessedAt *time.Time `json:"last_accessed_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// EdgeParams holds parameters for creating or removing an edge.
type EdgeParams struct {
	FromNS  string
	FromKey string
	ToNS    string
	ToKey   string
	Rel     string
	Weight  float64 // 0 means use default for rel type
}

// EdgeExpansionConfig controls edge expansion in context assembly.
type EdgeExpansionConfig struct {
	Enabled           bool    // default true
	Damping           float64 // default 0.3
	MaxEdgesPerSeed   int     // default 5
	MinEdgeWeight     float64 // default 0.1
	MaxExpansionTotal int     // default 50
	MaxBoostFactor    float64 // default 0.5 (cap relative to direct score)
}

// DefaultEdgeExpansion returns the default edge expansion configuration.
func DefaultEdgeExpansion() EdgeExpansionConfig {
	return EdgeExpansionConfig{
		Enabled:           true,
		Damping:           0.3,
		MaxEdgesPerSeed:   5,
		MinEdgeWeight:     0.1,
		MaxExpansionTotal: 50,
		MaxBoostFactor:    0.5,
	}
}

// PutResult wraps the result of a Put operation, including auto-linked edges.
type PutResult struct {
	Memory     *model.Memory `json:"memory"`
	AutoLinked []Edge        `json:"auto_linked,omitempty"`
}

// validEdgeRels lists the accepted edge relation types.
var validEdgeRels = map[string]bool{
	"relates_to":  true,
	"contradicts": true,
	"depends_on":  true,
	"refines":     true,
	"contains":    true,
	"merged_into": true,
}

// defaultEdgeWeight returns the default weight for a given relation type.
func defaultEdgeWeight(rel string) float64 {
	switch rel {
	case "contradicts":
		return 0.9
	case "refines":
		return 0.8
	case "depends_on":
		return 0.7
	case "contains":
		return 0.6
	case "relates_to":
		return 0.5
	case "merged_into":
		return 0.0
	default:
		return 0.5
	}
}

// edgeAutoLinkThreshold returns the cosine similarity threshold for auto-linking.
// Configurable via GHOST_EDGE_THRESHOLD env var.
func edgeAutoLinkThreshold() float64 {
	if env := os.Getenv("GHOST_EDGE_THRESHOLD"); env != "" {
		if v, err := strconv.ParseFloat(env, 64); err == nil && v > 0 && v <= 1 {
			return v
		}
	}
	return 0.80
}

// CreateEdge creates a weighted edge between two memories.
func (s *SQLiteStore) CreateEdge(ctx context.Context, p EdgeParams) (*Edge, error) {
	if !validEdgeRels[p.Rel] {
		return nil, fmt.Errorf("invalid relation %q (valid: relates_to, contradicts, depends_on, refines, contains, merged_into)", p.Rel)
	}

	fromID, err := s.resolveMemoryID(ctx, p.FromNS, p.FromKey)
	if err != nil {
		return nil, fmt.Errorf("resolve from: %w", err)
	}
	toID, err := s.resolveMemoryID(ctx, p.ToNS, p.ToKey)
	if err != nil {
		return nil, fmt.Errorf("resolve to: %w", err)
	}

	weight := p.Weight
	if weight <= 0 {
		weight = defaultEdgeWeight(p.Rel)
	}

	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO memory_edges (from_id, to_id, rel, weight, access_count, last_accessed_at, created_at)
		 VALUES (?, ?, ?, ?, 0, NULL, ?)`,
		fromID, toID, p.Rel, weight, now.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}

	return &Edge{
		FromID:    fromID,
		ToID:      toID,
		Rel:       p.Rel,
		Weight:    weight,
		CreatedAt: now,
	}, nil
}

// DeleteEdge removes an edge between two memories.
func (s *SQLiteStore) DeleteEdge(ctx context.Context, p EdgeParams) error {
	fromID, err := s.resolveMemoryID(ctx, p.FromNS, p.FromKey)
	if err != nil {
		return fmt.Errorf("resolve from: %w", err)
	}
	toID, err := s.resolveMemoryID(ctx, p.ToNS, p.ToKey)
	if err != nil {
		return fmt.Errorf("resolve to: %w", err)
	}

	_, err = s.db.ExecContext(ctx,
		`DELETE FROM memory_edges WHERE from_id = ? AND to_id = ? AND rel = ?`,
		fromID, toID, p.Rel)
	return err
}

// GetEdges returns all edges where the given memory is source or target.
func (s *SQLiteStore) GetEdges(ctx context.Context, memoryID string) ([]Edge, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT from_id, to_id, rel, weight, access_count, last_accessed_at, created_at
		 FROM memory_edges
		 WHERE from_id = ? OR to_id = ?`, memoryID, memoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanEdges(rows)
}

// GetEdgesByNSKey returns all edges for a memory identified by namespace and key.
func (s *SQLiteStore) GetEdgesByNSKey(ctx context.Context, ns, key string) ([]Edge, error) {
	id, err := s.resolveMemoryID(ctx, ns, key)
	if err != nil {
		return nil, err
	}
	return s.GetEdges(ctx, id)
}

// getEdgesForExpansion returns outgoing edges from a memory, filtered by min weight, limited.
func (s *SQLiteStore) getEdgesForExpansion(ctx context.Context, memoryID string, minWeight float64, limit int) ([]Edge, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT from_id, to_id, rel, weight, access_count, last_accessed_at, created_at
		 FROM memory_edges
		 WHERE from_id = ? AND weight >= ? AND rel != 'merged_into'
		 ORDER BY weight DESC
		 LIMIT ?`, memoryID, minWeight, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanEdges(rows)
}

func scanEdges(rows *sql.Rows) ([]Edge, error) {
	var edges []Edge
	for rows.Next() {
		var e Edge
		var lastAccessed sql.NullString
		var createdAt string
		if err := rows.Scan(&e.FromID, &e.ToID, &e.Rel, &e.Weight, &e.AccessCount, &lastAccessed, &createdAt); err != nil {
			return nil, err
		}
		e.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		if lastAccessed.Valid {
			t, _ := time.Parse(time.RFC3339, lastAccessed.String)
			e.LastAccessedAt = &t
		}
		edges = append(edges, e)
	}
	return edges, nil
}

// relinkEdges updates all edges referencing oldID to point to newID.
// Called within a transaction when a memory is versioned (new ID supersedes old).
func relinkEdges(ctx context.Context, tx *sql.Tx, oldID, newID string) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE memory_edges SET from_id = ? WHERE from_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("relink edges from: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE memory_edges SET to_id = ? WHERE to_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("relink edges to: %w", err)
	}
	return nil
}

// autoLinkEdges finds similar memories and creates relates_to edges.
// Called after a successful Put when embeddings are available.
func (s *SQLiteStore) autoLinkEdges(ctx context.Context, memoryID, ns string, memoryVec embedding.Vector) ([]Edge, error) {
	if s.embedder == nil || len(memoryVec) == 0 {
		return nil, nil
	}

	threshold := edgeAutoLinkThreshold()

	// Fetch candidate embeddings from same namespace (latest versions only, non-deleted)
	now := time.Now().UTC().Format(time.RFC3339)
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.id, c.embedding
		 FROM memories m
		 INNER JOIN (
			SELECT ns, key, MAX(version) AS max_ver
			FROM memories WHERE deleted_at IS NULL
			GROUP BY ns, key
		 ) latest ON m.ns = latest.ns AND m.key = latest.key AND m.version = latest.max_ver
		 INNER JOIN chunks c ON c.memory_id = m.id AND c.seq = 0
		 WHERE m.ns = ? AND m.deleted_at IS NULL AND (m.expires_at IS NULL OR m.expires_at > ?)
		   AND m.id != ? AND c.embedding IS NOT NULL
		 ORDER BY m.created_at DESC
		 LIMIT 50`, ns, now, memoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type candidate struct {
		id         string
		similarity float64
	}
	var candidates []candidate

	for rows.Next() {
		var id, embJSON string
		if err := rows.Scan(&id, &embJSON); err != nil {
			continue
		}
		var vec embedding.Vector
		if err := json.Unmarshal([]byte(embJSON), &vec); err != nil {
			continue
		}
		sim := embedding.CosineSimilarity(memoryVec, vec)
		if sim >= threshold {
			candidates = append(candidates, candidate{id: id, similarity: sim})
		}
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	// Sort by similarity descending, take top 5
	for i := 0; i < len(candidates)-1; i++ {
		for j := i + 1; j < len(candidates); j++ {
			if candidates[j].similarity > candidates[i].similarity {
				candidates[i], candidates[j] = candidates[j], candidates[i]
			}
		}
	}
	if len(candidates) > 5 {
		candidates = candidates[:5]
	}

	var edges []Edge
	nowTime := time.Now().UTC()
	for _, c := range candidates {
		// Use similarity as weight, clamped to [0.5, 1.0]
		weight := math.Max(0.5, c.similarity)
		_, err := s.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO memory_edges (from_id, to_id, rel, weight, access_count, last_accessed_at, created_at)
			 VALUES (?, ?, 'relates_to', ?, 0, NULL, ?)`,
			memoryID, c.id, weight, nowTime.Format(time.RFC3339))
		if err != nil {
			continue
		}
		edges = append(edges, Edge{
			FromID:    memoryID,
			ToID:      c.id,
			Rel:       "relates_to",
			Weight:    weight,
			CreatedAt: nowTime,
		})
	}

	return edges, nil
}

// getContainsChildren returns the IDs of memories that are children of the given
// parent via 'contains' edges. Used for suppression in context assembly.
func (s *SQLiteStore) getContainsChildren(ctx context.Context, parentID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT to_id FROM memory_edges WHERE from_id = ? AND rel = 'contains'`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// decayEdges weakens edges that haven't been co-retrieved recently and prunes
// very weak edges. Called during the reflect cycle.
func (s *SQLiteStore) decayEdges(ctx context.Context, result *ReflectResult) {
	now := time.Now().UTC()
	cutoff := now.Add(-30 * 24 * time.Hour).Format(time.RFC3339) // 30 days ago

	// Decay: edges not accessed in 30+ days with <3 accesses → weight *= 0.9
	res, err := s.db.ExecContext(ctx,
		`UPDATE memory_edges
		 SET weight = weight * 0.9
		 WHERE access_count < 3
		   AND (last_accessed_at IS NULL OR last_accessed_at < ?)
		   AND created_at < ?
		   AND weight >= 0.05`,
		cutoff, cutoff)
	if err == nil {
		n, _ := res.RowsAffected()
		result.EdgesDecayed = int(n)
	}

	// Prune: edges with weight < 0.05 are too weak to be useful → delete
	res2, err := s.db.ExecContext(ctx,
		`DELETE FROM memory_edges WHERE weight < 0.05 AND weight > 0`)
	if err == nil {
		n, _ := res2.RowsAffected()
		result.EdgesPruned = int(n)
	}
}

// strengthenCoRetrievedEdges increments access_count and weight for edges
// between memories that were returned together in the same context response.
// Implements Hebbian learning: "neurons that fire together wire together."
func (s *SQLiteStore) strengthenCoRetrievedEdges(ctx context.Context, memoryIDs []string) {
	if len(memoryIDs) < 2 {
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// For each pair of returned memories, strengthen any existing edge between them.
	// We use diminishing returns: weight += 0.05 × (1 - weight) so it asymptotically approaches 1.0.
	for i := 0; i < len(memoryIDs); i++ {
		for j := i + 1; j < len(memoryIDs); j++ {
			// Strengthen in both directions (edges are directional)
			s.db.ExecContext(ctx,
				`UPDATE memory_edges
				 SET access_count = access_count + 1,
				     last_accessed_at = ?,
				     weight = MIN(1.0, weight + 0.05 * (1.0 - weight))
				 WHERE (from_id = ? AND to_id = ?) OR (from_id = ? AND to_id = ?)`,
				now, memoryIDs[i], memoryIDs[j], memoryIDs[j], memoryIDs[i])
		}
	}
}

// loadMemoryByID loads a single non-deleted memory by its ID.
func (s *SQLiteStore) loadMemoryByID(ctx context.Context, id string) (*model.Memory, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	row := s.db.QueryRowContext(ctx,
		`SELECT id, ns, key, content, kind, tags, version, supersedes,
		        created_at, deleted_at, priority, access_count, last_accessed_at, meta, expires_at,
		        importance, utility_count, tier, est_tokens, pinned
		 FROM memories
		 WHERE id = ? AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at > ?)`,
		id, now)

	m, err := scanMemory(row)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

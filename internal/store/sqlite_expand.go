package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

)

// Peek returns a lightweight index of memory state for lazy discovery.
func (s *SQLiteStore) Peek(ctx context.Context, ns string) (*PeekResult, error) {
	result := &PeekResult{
		NS:             ns,
		MemoryCounts:   map[string]int{},
		TotalEstTokens: map[string]int{},
	}

	// Build WHERE clause for namespace filter
	where := "deleted_at IS NULL AND (expires_at IS NULL OR expires_at > ?)"
	args := []interface{}{time.Now().UTC().Format(time.RFC3339)}
	if ns != "" {
		nsf := ParseNSFilter(ns)
		clause, nsArgs := nsf.SQL("ns")
		if clause != "" {
			where += " AND " + clause
			args = append(args, nsArgs...)
		}
	}

	// 1. Memory counts and token totals by tier
	rows, err := s.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT tier, COUNT(*), COALESCE(SUM(est_tokens), 0)
			FROM memories WHERE %s GROUP BY tier`, where), args...)
	if err != nil {
		return nil, fmt.Errorf("peek tier counts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tier string
		var count, tokens int
		if err := rows.Scan(&tier, &count, &tokens); err != nil {
			return nil, err
		}
		result.MemoryCounts[tier] = count
		result.TotalEstTokens[tier] = tokens
	}

	// 2. Pinned summary (first pinned memory)
	var pinnedContent sql.NullString
	s.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT content FROM memories WHERE %s AND pinned = 1
			ORDER BY importance DESC, created_at DESC LIMIT 1`, where), args...).Scan(&pinnedContent)
	if pinnedContent.Valid {
		result.PinnedSummary = truncate(pinnedContent.String, 200)
	}

	// 3. Recent topics: top-5 tags by recency
	tagRows, err := s.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT tags FROM memories WHERE %s AND tags IS NOT NULL
			ORDER BY created_at DESC LIMIT 20`, where), args...)
	if err == nil {
		defer tagRows.Close()
		tagSeen := map[string]bool{}
		var topics []string
		for tagRows.Next() && len(topics) < 5 {
			var tagsJSON string
			if tagRows.Scan(&tagsJSON) != nil {
				continue
			}
			var tags []string
			if json.Unmarshal([]byte(tagsJSON), &tags) != nil {
				continue
			}
			for _, t := range tags {
				if !tagSeen[t] && len(topics) < 5 {
					tagSeen[t] = true
					topics = append(topics, t)
				}
			}
		}
		result.RecentTopics = topics
	}
	if result.RecentTopics == nil {
		result.RecentTopics = []string{}
	}

	// 4. Top-5 by importance
	stubRows, err := s.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT id, key, kind, tier, importance, est_tokens, content
			FROM memories WHERE %s
			ORDER BY importance DESC, created_at DESC LIMIT 5`, where), args...)
	if err != nil {
		return nil, fmt.Errorf("peek high importance: %w", err)
	}
	defer stubRows.Close()
	for stubRows.Next() {
		var stub MemoryStub
		var content string
		if err := stubRows.Scan(&stub.ID, &stub.Key, &stub.Kind, &stub.Tier,
			&stub.Importance, &stub.EstTokens, &content); err != nil {
			return nil, err
		}
		stub.Summary = truncate(content, 80)
		result.HighImportance = append(result.HighImportance, stub)
	}
	if result.HighImportance == nil {
		result.HighImportance = []MemoryStub{}
	}

	return result, nil
}

// Expand returns the children of a consolidation node (by key), or lists
// all consolidation nodes in a namespace (when key is empty).
func (s *SQLiteStore) Expand(ctx context.Context, p ExpandParams) (*ExpandResult, error) {
	result := &ExpandResult{}

	if p.Key == "" {
		// List all consolidation nodes in namespace
		now := time.Now().UTC().Format(time.RFC3339)
		rows, err := s.db.QueryContext(ctx,
			`SELECT m.key, m.kind, m.importance, m.est_tokens, m.content, COUNT(e.to_id) AS child_count
			 FROM memories m
			 INNER JOIN memory_edges e ON e.from_id = m.id AND e.rel = 'contains'
			 WHERE m.ns = ? AND m.deleted_at IS NULL AND (m.expires_at IS NULL OR m.expires_at > ?)
			 GROUP BY m.id
			 ORDER BY m.created_at DESC`, p.NS, now)
		if err != nil {
			return nil, fmt.Errorf("expand list: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var node ConsolidationNode
			var content string
			if err := rows.Scan(&node.Key, &node.Kind, &node.Importance, &node.EstTokens, &content, &node.Children); err != nil {
				return nil, err
			}
			node.Summary = truncate(content, 200)
			result.Nodes = append(result.Nodes, node)
		}

		// Add emergent clusters (relates_to groups without a consolidation parent)
		clusters, err := s.GetSimilarClusters(ctx, p.NS)
		if err == nil && len(clusters) > 0 {
			// Filter out clusters where a majority of members already have a contains parent
			for _, c := range clusters {
				resolved := 0
				withParent := 0
				for _, key := range c.Keys {
					id, err := s.resolveMemoryID(ctx, p.NS, key)
					if err != nil {
						continue
					}
					resolved++
					parents, err := s.getContainsParents(ctx, id)
					if err == nil && len(parents) > 0 {
						withParent++
					}
				}
				// Include cluster if fewer than half its members are consolidated
				if resolved == 0 || withParent*2 <= resolved {
					result.Clusters = append(result.Clusters, c)
				}
			}
		}

		return result, nil
	}

	// Expand a specific consolidation node
	parentID, err := s.resolveMemoryID(ctx, p.NS, p.Key)
	if err != nil {
		return nil, fmt.Errorf("expand resolve parent: %w", err)
	}

	parentMem, err := s.loadMemoryByID(ctx, parentID)
	if err != nil {
		return nil, fmt.Errorf("expand load parent: %w", err)
	}
	result.Parent = &MemoryStub{
		ID:         parentMem.ID,
		Key:        parentMem.Key,
		Kind:       parentMem.Kind,
		Tier:       parentMem.Tier,
		Importance: parentMem.Importance,
		EstTokens:  parentMem.EstTokens,
		Summary:    truncate(parentMem.Content, 200),
	}

	childIDs, err := s.getContainsChildren(ctx, parentID)
	if err != nil {
		return nil, fmt.Errorf("expand get children: %w", err)
	}

	for _, childID := range childIDs {
		child, err := s.loadMemoryByID(ctx, childID)
		if err != nil {
			continue
		}
		// Count grandchildren to indicate if this child is expandable
		grandchildren, _ := s.getContainsChildren(ctx, childID)
		result.Children = append(result.Children, ExpandChild{
			Key:        child.Key,
			Kind:       child.Kind,
			Importance: child.Importance,
			EstTokens:  child.EstTokens,
			Content:    child.Content,
			Children:   len(grandchildren),
		})
	}

	return result, nil
}

// Consolidate creates a summary memory and contains edges to source memories.
func (s *SQLiteStore) Consolidate(ctx context.Context, p ConsolidateParams) (*ConsolidateResult, error) {
	if len(p.SourceKeys) < 2 {
		return nil, fmt.Errorf("consolidate requires at least 2 source keys, got %d", len(p.SourceKeys))
	}

	// Verify all source memories exist (use History mode to avoid incrementing access_count)
	for _, key := range p.SourceKeys {
		_, err := s.Get(ctx, GetParams{NS: p.NS, Key: key, History: true})
		if err != nil {
			return nil, fmt.Errorf("source memory not found: %s/%s: %w", p.NS, key, err)
		}
	}

	kind := p.Kind
	if kind == "" {
		kind = "semantic"
	}
	importance := p.Importance
	if importance == 0 {
		importance = 0.7
	}

	// Create summary memory
	summary, err := s.Put(ctx, PutParams{
		NS:         p.NS,
		Key:        p.SummaryKey,
		Content:    p.Content,
		Kind:       kind,
		Importance: importance,
		Tags:       p.Tags,
	})
	if err != nil {
		return nil, fmt.Errorf("consolidate put summary: %w", err)
	}

	// Create contains edges from summary to each source
	var edges []Edge
	for _, key := range p.SourceKeys {
		edge, err := s.CreateEdge(ctx, EdgeParams{
			FromNS:  p.NS,
			FromKey: p.SummaryKey,
			ToNS:    p.NS,
			ToKey:   key,
			Rel:     "contains",
		})
		if err != nil {
			return nil, fmt.Errorf("consolidate create edge to %s: %w", key, err)
		}
		edges = append(edges, *edge)
	}

	return &ConsolidateResult{Summary: summary, Edges: edges}, nil
}


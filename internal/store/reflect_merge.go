package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rcliao/ghost/internal/embedding"
	"github.com/rcliao/ghost/internal/model"
)

// similarityMergeResult holds the outcome of a similarity merge/link pass.
type similarityMergeResult struct {
	absorbed    int
	linked      int
	absorbedIDs []string
	clusters    []MemoryCluster
}

// applySimilarityMerge runs the similarity-based merge for a single rule.
// Returns the count of absorbed memories, linked edges, and absorbed IDs.
func (s *SQLiteStore) applySimilarityMerge(ctx context.Context, rule ReflectRule, allMemories []model.Memory, deletedIDs map[string]bool, dryRun bool) (*similarityMergeResult, error) {
	now := time.Now().UTC()

	// 1. Filter candidates by non-similarity conditions
	var candidates []model.Memory
	for _, m := range allMemories {
		if deletedIDs[m.ID] {
			continue
		}
		ageHours := now.Sub(m.CreatedAt).Hours()
		unaccessedHours := ageHours
		if m.LastAccessedAt != nil {
			unaccessedHours = now.Sub(*m.LastAccessedAt).Hours()
		}
		utilityRatio := 0.0
		if m.AccessCount > 0 {
			utilityRatio = float64(m.UtilityCount) / float64(m.AccessCount)
		}
		if ruleMatchesNonSimilarity(rule, m, ageHours, unaccessedHours, utilityRatio) {
			candidates = append(candidates, m)
		}
	}

	if len(candidates) < 2 {
		return &similarityMergeResult{}, nil
	}

	// Cap at 500 candidates
	if len(candidates) > 500 {
		candidates = candidates[:500]
	}

	// 2. Load embeddings for candidates (batch query for seq=0 chunks)
	ids := make([]string, len(candidates))
	for i, m := range candidates {
		ids[i] = m.ID
	}

	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	embArgs := make([]interface{}, len(ids))
	for i, id := range ids {
		embArgs[i] = id
	}

	embRows, err := s.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT memory_id, embedding FROM chunks WHERE memory_id IN (%s) AND seq = 0 AND embedding IS NOT NULL`, placeholders),
		embArgs...)
	if err != nil {
		return &similarityMergeResult{}, fmt.Errorf("load embeddings: %w", err)
	}
	defer embRows.Close()

	embMap := map[string]embedding.Vector{}
	for embRows.Next() {
		var memID, embJSON string
		if err := embRows.Scan(&memID, &embJSON); err != nil {
			continue
		}
		var vec embedding.Vector
		if err := json.Unmarshal([]byte(embJSON), &vec); err != nil {
			continue
		}
		embMap[memID] = vec
	}

	// Filter to only candidates with embeddings
	var withEmb []model.Memory
	for _, m := range candidates {
		if _, ok := embMap[m.ID]; ok {
			withEmb = append(withEmb, m)
		}
	}

	if len(withEmb) < 2 {
		return &similarityMergeResult{}, nil
	}

	// 3. Sort by importance DESC (greedy clustering pivot order)
	sort.Slice(withEmb, func(i, j int) bool {
		if withEmb[i].Importance != withEmb[j].Importance {
			return withEmb[i].Importance > withEmb[j].Importance
		}
		return withEmb[i].CreatedAt.After(withEmb[j].CreatedAt)
	})

	// 4. Greedy clustering
	assigned := map[string]bool{}
	threshold := rule.Cond.SimilarityGT
	var clusters [][]model.Memory

	for i, pivot := range withEmb {
		if assigned[pivot.ID] {
			continue
		}
		assigned[pivot.ID] = true
		cluster := []model.Memory{pivot}
		pivotVec := embMap[pivot.ID]

		for j := i + 1; j < len(withEmb); j++ {
			other := withEmb[j]
			if assigned[other.ID] {
				continue
			}
			sim := embedding.CosineSimilarity(pivotVec, embMap[other.ID])
			if sim >= threshold {
				cluster = append(cluster, other)
				assigned[other.ID] = true
			}
		}

		if len(cluster) >= 2 {
			clusters = append(clusters, cluster)
		}
	}

	// 5. Determine strategy: link_only (non-destructive) or keep_highest_importance (destructive merge)
	strategy := "link_only"
	if rule.Action.Params != nil {
		if s, ok := rule.Action.Params["strategy"].(string); ok {
			strategy = s
		}
	}

	totalAbsorbed := 0
	totalLinked := 0
	var allAbsorbedIDs []string
	var linkedClusters []MemoryCluster
	for _, cluster := range clusters {
		if strategy == "link_only" {
			// Collect cluster keys for the response
			var keys []string
			for _, m := range cluster {
				keys = append(keys, m.Key)
			}
			linkedClusters = append(linkedClusters, MemoryCluster{Keys: keys, Count: len(keys)})

			if dryRun {
				// Count edges that would be created (n*(n-1)/2 pairs)
				n := len(cluster)
				totalLinked += n * (n - 1) / 2
				continue
			}
			linked, err := s.applyLinkSimilar(ctx, cluster)
			if err != nil {
				return &similarityMergeResult{absorbed: totalAbsorbed, absorbedIDs: allAbsorbedIDs}, fmt.Errorf("link cluster: %w", err)
			}
			totalLinked += linked
		} else {
			// Destructive merge (legacy behavior)
			if dryRun {
				for _, m := range cluster[1:] {
					totalAbsorbed++
					allAbsorbedIDs = append(allAbsorbedIDs, m.ID)
				}
				continue
			}
			absorbed, err := s.applyMerge(ctx, cluster)
			if err != nil {
				return &similarityMergeResult{absorbed: totalAbsorbed, absorbedIDs: allAbsorbedIDs}, fmt.Errorf("merge cluster: %w", err)
			}
			totalAbsorbed += absorbed
			// Track absorbed IDs (all non-survivor, non-pinned members)
			for _, m := range cluster[1:] {
				if !m.Pinned {
					allAbsorbedIDs = append(allAbsorbedIDs, m.ID)
				}
			}
		}
	}

	return &similarityMergeResult{absorbed: totalAbsorbed, linked: totalLinked, absorbedIDs: allAbsorbedIDs, clusters: linkedClusters}, nil
}

// applyLinkSimilar creates relates_to edges between all memories in a cluster.
// Non-destructive: no content is deleted. Returns the number of edges created.
func (s *SQLiteStore) applyLinkSimilar(ctx context.Context, group []model.Memory) (int, error) {
	if len(group) < 2 {
		return 0, nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	linked := 0

	for i := 0; i < len(group); i++ {
		for j := i + 1; j < len(group); j++ {
			// Use INSERT OR IGNORE — don't overwrite existing edges
			_, err := s.db.ExecContext(ctx,
				`INSERT OR IGNORE INTO memory_edges (from_id, to_id, rel, weight, access_count, last_accessed_at, created_at)
				 VALUES (?, ?, 'relates_to', 0.5, 0, NULL, ?)`,
				group[i].ID, group[j].ID, now)
			if err != nil {
				return linked, fmt.Errorf("create edge %s→%s: %w", group[i].ID, group[j].ID, err)
			}
			linked++
		}
	}

	return linked, nil
}

// applyMerge merges a group of memories into a single survivor.
// Returns the number of absorbed (soft-deleted) memories.
func (s *SQLiteStore) applyMerge(ctx context.Context, group []model.Memory) (int, error) {
	if len(group) < 2 {
		return 0, nil
	}

	// Determine survivor: highest importance, tiebreak by most recent created_at, then highest access_count
	sort.Slice(group, func(i, j int) bool {
		if group[i].Importance != group[j].Importance {
			return group[i].Importance > group[j].Importance
		}
		if !group[i].CreatedAt.Equal(group[j].CreatedAt) {
			return group[i].CreatedAt.After(group[j].CreatedAt)
		}
		return group[i].AccessCount > group[j].AccessCount
	})

	survivor := group[0]

	// Compute merged values
	tagSet := map[string]bool{}
	for _, t := range survivor.Tags {
		tagSet[t] = true
	}
	maxImportance := survivor.Importance
	totalAccessCount := survivor.AccessCount
	totalUtilityCount := survivor.UtilityCount
	highestTier := survivor.Tier

	for _, m := range group[1:] {
		for _, t := range m.Tags {
			tagSet[t] = true
		}
		if m.Importance > maxImportance {
			maxImportance = m.Importance
		}
		totalAccessCount += m.AccessCount
		totalUtilityCount += m.UtilityCount
		if tierRank(m.Tier) > tierRank(highestTier) {
			highestTier = m.Tier
		}
	}

	// Build merged tags
	var mergedTags []string
	for t := range tagSet {
		mergedTags = append(mergedTags, t)
	}
	sort.Strings(mergedTags)

	var tagsJSON *string
	if len(mergedTags) > 0 {
		b, _ := json.Marshal(mergedTags)
		s := string(b)
		tagsJSON = &s
	}

	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// Update survivor
	_, err = tx.ExecContext(ctx,
		`UPDATE memories SET tags = ?, importance = ?, access_count = ?, utility_count = ?, tier = ?
		 WHERE id = ? AND deleted_at IS NULL`,
		tagsJSON, maxImportance, totalAccessCount, totalUtilityCount, highestTier, survivor.ID)
	if err != nil {
		return 0, fmt.Errorf("update survivor: %w", err)
	}

	// Soft-delete absorbed memories and create merged_into links
	absorbed := 0
	for _, m := range group[1:] {
		// Pinned memories must never be absorbed
		if m.Pinned {
			continue
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE memories SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`, now, m.ID)
		if err != nil {
			return absorbed, fmt.Errorf("soft-delete absorbed %s: %w", m.ID, err)
		}
		// Create merged_into link
		_, err = tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO memory_links (from_id, to_id, rel, created_at) VALUES (?, ?, 'merged_into', ?)`,
			m.ID, survivor.ID, now)
		if err != nil {
			return absorbed, fmt.Errorf("create merged_into link: %w", err)
		}
		absorbed++
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return absorbed, nil
}

// autoConsolidateMinCluster is the minimum cluster size for auto-consolidation.
const autoConsolidateMinCluster = 5

// autoConsolidateClusters creates skeleton parent nodes for large orphan clusters.
// Returns the number of clusters consolidated.
func (s *SQLiteStore) autoConsolidateClusters(ctx context.Context, clusters []MemoryCluster, allMemories []model.Memory, p ReflectParams) int {
	if p.DryRun {
		// Count clusters that would be consolidated
		count := 0
		for _, cluster := range clusters {
			if cluster.Count < autoConsolidateMinCluster {
				continue
			}
			// In dry-run mode, we can't check parents without DB queries,
			// but we do check to provide accurate counts.
			if s.clusterHasConsolidationParent(ctx, cluster, allMemories) {
				continue
			}
			count++
		}
		return count
	}

	// Build key→Memory map for quick lookup
	keyToMem := make(map[string]model.Memory, len(allMemories))
	for _, m := range allMemories {
		keyToMem[m.Key] = m
	}

	consolidated := 0
	for _, cluster := range clusters {
		if cluster.Count < autoConsolidateMinCluster {
			continue
		}

		// Check if any member already has a contains parent — skip if so
		if s.clusterHasConsolidationParent(ctx, cluster, allMemories) {
			continue
		}

		// Build skeleton content and collect common tags
		var contentParts []string
		var ns string
		tagCounts := map[string]int{}
		memberCount := 0

		for _, key := range cluster.Keys {
			m, ok := keyToMem[key]
			if !ok {
				continue
			}
			if ns == "" {
				ns = m.NS
			}
			contentParts = append(contentParts, fmt.Sprintf("[%s]: %s", key, m.Content))
			for _, t := range m.Tags {
				tagCounts[t]++
			}
			memberCount++
		}

		if memberCount < autoConsolidateMinCluster {
			continue
		}

		// Common tags = tags present in all members
		var commonTags []string
		for tag, count := range tagCounts {
			if count == memberCount {
				commonTags = append(commonTags, tag)
			}
		}
		sort.Strings(commonTags)

		// Use namespace from params if available, else from first member
		if p.NS != "" {
			ns = p.NS
		}

		summaryKey := fmt.Sprintf("auto-summary-%d", time.Now().UnixNano())
		content := strings.Join(contentParts, "\n\n")

		_, err := s.Consolidate(ctx, ConsolidateParams{
			NS:         ns,
			SummaryKey: summaryKey,
			Content:    content,
			SourceKeys: cluster.Keys,
			Kind:       "semantic",
			Importance: 0.6,
			Tags:       commonTags,
		})
		if err != nil {
			continue
		}
		consolidated++
	}

	return consolidated
}

// clusterHasConsolidationParent checks if any member of a cluster already has
// a contains parent edge. Returns true if any member is already consolidated.
func (s *SQLiteStore) clusterHasConsolidationParent(ctx context.Context, cluster MemoryCluster, allMemories []model.Memory) bool {
	// Build key→ID map for the cluster members
	keyToID := make(map[string]string, len(allMemories))
	for _, m := range allMemories {
		keyToID[m.Key] = m.ID
	}

	for _, key := range cluster.Keys {
		id, ok := keyToID[key]
		if !ok {
			continue
		}
		parents, err := s.getContainsParents(ctx, id)
		if err != nil {
			continue
		}
		if len(parents) > 0 {
			return true
		}
	}
	return false
}

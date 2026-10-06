package store

import (
	"context"
	"fmt"
	"strings"
)

// substituteParents implements packing substitution: if the scored candidate
// set overflows the remaining budget and 3+ candidates share a contains-
// parent (with fan-out within the LCM cap), the group is replaced by its
// parent at the best member's position and score. The substituted child keys
// are recorded so the injected summary carries drill-down pointers.
func (s *SQLiteStore) substituteParents(ctx context.Context, candidates []contextCandidate,
	remainingBudget int, substituted map[string][]string) []contextCandidate {

	// No pressure → no compression.
	total := 0
	for _, c := range candidates {
		total += estTokensOf(c.memory.EstTokens, c.memory.Content)
	}
	if total <= remainingBudget {
		return candidates
	}

	// Map candidate children → parents (one batch query).
	ids := make([]string, len(candidates))
	for i, c := range candidates {
		ids[i] = c.memory.ID
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT e.from_id, e.to_id FROM memory_edges e
		 WHERE e.rel = 'contains' AND e.to_id IN (%s)`, placeholders), args...)
	if err != nil {
		return candidates
	}
	groups := map[string][]string{} // parentID → candidate child IDs
	for rows.Next() {
		var parent, child string
		if rows.Scan(&parent, &child) == nil {
			groups[parent] = append(groups[parent], child)
		}
	}
	rows.Close()

	byID := map[string]int{}
	for i, c := range candidates {
		byID[c.memory.ID] = i
	}

	for parentID, childIDs := range groups {
		if len(childIDs) < 3 {
			continue
		}
		// Fan-out cap: archive digests never substitute.
		if kids, err := s.getContainsChildren(ctx, parentID); err != nil ||
			len(kids) > parentBoostMaxChildren() {
			continue
		}
		parent, err := s.loadMemoryByID(ctx, parentID)
		if err != nil || parent.Tier == "dormant" {
			continue
		}

		// Best member position/score anchor the parent.
		bestIdx, bestScore := len(candidates), 0.0
		var childKeys []string
		for _, cid := range childIDs {
			i := byID[cid]
			if i < bestIdx {
				bestIdx = i
			}
			if candidates[i].score > bestScore {
				bestScore = candidates[i].score
			}
			childKeys = append(childKeys, candidates[i].memory.Key)
		}

		// Drop the children; place the parent at the best member's position
		// with the best member's score (if the liveness-demoted parent was
		// already a weak candidate, this promotes and dedupes it).
		keep := candidates[:0:0]
		childSet := map[string]bool{}
		for _, cid := range childIDs {
			childSet[cid] = true
		}
		inserted := false
		for i, c := range candidates {
			if i == bestIdx && !inserted {
				keep = append(keep, contextCandidate{memory: *parent, score: bestScore, via: ViaParent})
				inserted = true
			}
			if childSet[c.memory.ID] || c.memory.ID == parentID {
				continue
			}
			keep = append(keep, c)
		}
		if !inserted {
			keep = append(keep, contextCandidate{memory: *parent, score: bestScore, via: ViaParent})
		}
		candidates = keep
		substituted[parentID] = childKeys
		// Rebuild index for subsequent groups.
		byID = map[string]int{}
		for i, c := range candidates {
			byID[c.memory.ID] = i
		}
	}
	return candidates
}

func estTokensOf(est int, content string) int {
	if est > 0 {
		return est
	}
	return (len(content) / 4) + 20
}

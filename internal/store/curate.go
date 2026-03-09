package store

import (
	"context"
	"fmt"
	"time"
)

// CurateParams holds parameters for a single-memory lifecycle action.
type CurateParams struct {
	NS  string // namespace
	Key string // key within namespace
	Op  string // promote | demote | boost | diminish | archive | delete | pin | unpin
}

// CurateResult describes what happened after curation.
type CurateResult struct {
	NS            string  `json:"ns"`
	Key           string  `json:"key"`
	Op            string  `json:"op"`
	OldTier       string  `json:"old_tier,omitempty"`
	NewTier       string  `json:"new_tier,omitempty"`
	OldImportance float64 `json:"old_importance,omitempty"`
	NewImportance float64 `json:"new_importance,omitempty"`
	OldPinned     bool    `json:"old_pinned,omitempty"`
	NewPinned     bool    `json:"new_pinned,omitempty"`
}

// tier promotion order: dormant → stm → ltm (ltm is ceiling)
var tierUp = map[string]string{
	"dormant": "stm",
	"stm":     "ltm",
}

// tier demotion order: ltm → stm → dormant (dormant is floor)
var tierDown = map[string]string{
	"ltm": "stm",
	"stm": "dormant",
}

// validCurateOps lists the allowed operations.
var validCurateOps = map[string]bool{
	"promote": true, "demote": true, "boost": true,
	"diminish": true, "archive": true, "delete": true,
	"pin": true, "unpin": true,
}

// Curate applies a lifecycle action to a single memory identified by ns+key.
func (s *SQLiteStore) Curate(ctx context.Context, p CurateParams) (*CurateResult, error) {
	if p.NS == "" || p.Key == "" {
		return nil, fmt.Errorf("ns and key are required")
	}
	if !validCurateOps[p.Op] {
		return nil, fmt.Errorf("invalid op %q; must be one of: promote, demote, boost, diminish, archive, delete, pin, unpin", p.Op)
	}

	// Resolve to latest version
	mems, err := s.Get(ctx, GetParams{NS: p.NS, Key: p.Key})
	if err != nil {
		return nil, fmt.Errorf("get memory: %w", err)
	}
	if len(mems) == 0 {
		return nil, fmt.Errorf("memory not found: %s/%s", p.NS, p.Key)
	}
	m := mems[0]

	result := &CurateResult{NS: p.NS, Key: p.Key, Op: p.Op}

	switch p.Op {
	case "promote":
		result.OldTier = m.Tier
		newTier, ok := tierUp[m.Tier]
		if !ok {
			return nil, fmt.Errorf("cannot promote from tier %q (already at highest)", m.Tier)
		}
		result.NewTier = newTier
		return result, s.applyTierChange(ctx, m.ID, map[string]any{"to_tier": newTier}, "promote")

	case "demote":
		result.OldTier = m.Tier
		newTier, ok := tierDown[m.Tier]
		if !ok {
			return nil, fmt.Errorf("cannot demote from tier %q (already at lowest)", m.Tier)
		}
		result.NewTier = newTier
		return result, s.applyTierChange(ctx, m.ID, map[string]any{"to_tier": newTier}, "demote")

	case "boost":
		result.OldImportance = m.Importance
		newImp := m.Importance + 0.2
		if newImp > 1.0 {
			newImp = 1.0
		}
		result.NewImportance = newImp
		_, err := s.db.ExecContext(ctx,
			`UPDATE memories SET importance = ? WHERE id = ? AND deleted_at IS NULL`, newImp, m.ID)
		return result, err

	case "diminish":
		result.OldImportance = m.Importance
		newImp := m.Importance - 0.2
		if newImp < 0.1 {
			newImp = 0.1
		}
		result.NewImportance = newImp
		_, err := s.db.ExecContext(ctx,
			`UPDATE memories SET importance = ? WHERE id = ? AND deleted_at IS NULL`, newImp, m.ID)
		return result, err

	case "archive":
		result.OldTier = m.Tier
		result.NewTier = "dormant"
		return result, s.applyTierChange(ctx, m.ID, map[string]any{"to_tier": "dormant"}, "archive")

	case "delete":
		now := time.Now().UTC().Format(time.RFC3339)
		return result, s.applyDelete(ctx, m.ID, now)

	case "pin":
		result.OldPinned = m.Pinned
		result.NewPinned = true
		_, err := s.db.ExecContext(ctx,
			`UPDATE memories SET pinned = 1 WHERE id = ? AND deleted_at IS NULL`, m.ID)
		return result, err

	case "unpin":
		result.OldPinned = m.Pinned
		result.NewPinned = false
		_, err := s.db.ExecContext(ctx,
			`UPDATE memories SET pinned = 0 WHERE id = ? AND deleted_at IS NULL`, m.ID)
		return result, err

	default:
		return nil, fmt.Errorf("unhandled op: %s", p.Op)
	}
}

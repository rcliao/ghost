package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rcliao/ghost/internal/model"
)

// ReflectRule defines a condition→action pair for the reflect engine.
type ReflectRule struct {
	ID        string     `json:"id"`
	NS        string     `json:"ns"`
	Name      string     `json:"name"`
	Priority  int        `json:"priority"`
	Scope     string     `json:"scope"`
	CreatedBy string     `json:"created_by"`
	Cond      RuleCond   `json:"cond"`
	Action    RuleAction `json:"action"`
	ExpiresAt string     `json:"expires_at,omitempty"`
	CreatedAt string     `json:"created_at"`
}

// RuleCond holds the condition fields for a reflect rule. All non-zero fields are AND-joined.
type RuleCond struct {
	Tier              string  `json:"tier,omitempty"`
	AgeGTHours        float64 `json:"age_gt_hours,omitempty"`
	UnaccessedGTHours float64 `json:"unaccessed_gt_hours,omitempty"` // hours since last_accessed_at (or created_at if never accessed)
	ImportanceLT      float64 `json:"importance_lt,omitempty"`
	AccessLT          int     `json:"access_lt,omitempty"`
	AccessGT          int     `json:"access_gt,omitempty"`
	UtilityLT         float64 `json:"utility_lt,omitempty"`
	Kind              string  `json:"kind,omitempty"`
	TagIncludes       string  `json:"tag_includes,omitempty"`
	SimilarityGT      float64 `json:"similarity_gt,omitempty"`
}

// RuleAction holds the action to perform when a rule matches.
type RuleAction struct {
	Op     string         `json:"op"`     // DECAY, DELETE, PROMOTE, DEMOTE, ARCHIVE, TTL_SET, PIN
	Params map[string]any `json:"params,omitempty"`
}

// ReflectParams controls a reflect cycle.
type ReflectParams struct {
	NS     string
	DryRun bool
}

// ReflectResult summarizes what the reflect cycle did.
type ReflectResult struct {
	MemoriesEvaluated int      `json:"memories_evaluated"`
	RulesApplied      int      `json:"rules_applied"`
	Decayed           int      `json:"decayed"`
	Promoted          int      `json:"promoted"`
	Demoted           int      `json:"demoted"`
	Archived          int      `json:"archived"`
	Deleted           int      `json:"deleted"`
	Merged            int      `json:"merged"`
	Linked            int              `json:"linked,omitempty"`
	LinkedClusters    []MemoryCluster  `json:"linked_clusters,omitempty"`
	AutoConsolidated  int              `json:"auto_consolidated,omitempty"`
	EdgesDecayed      int              `json:"edges_decayed,omitempty"`
	EdgesPruned       int              `json:"edges_pruned,omitempty"`
	Errors            []string `json:"errors,omitempty"`
}

// Valid action operations.
var validActionOps = map[string]bool{
	"DECAY": true, "DELETE": true, "PROMOTE": true, "DEMOTE": true,
	"ARCHIVE": true, "TTL_SET": true, "PIN": true, "MERGE": true,
}

// builtinRules are seeded on startup with ON CONFLICT IGNORE semantics.
var builtinRules = []ReflectRule{
	// Note: pinned memories are skipped before rule evaluation,
	// so no PIN rule is needed. The old sys-pin-identity rule is
	// kept in existing DBs but never matches (no more identity tier).
	{
		ID:        "sys-promote-sensory",
		Name:      "Promote attended sensory memories to STM",
		Scope:     "reflect",
		Priority:  5,
		CreatedBy: "system",
		Cond:      RuleCond{Tier: "sensory", AgeGTHours: 1, AccessGT: 1},
		Action:    RuleAction{Op: "PROMOTE", Params: map[string]any{"to_tier": "stm"}},
	},
	{
		ID:        "sys-decay-sensory",
		Name:      "Delete unattended sensory memories after 4 hours",
		Scope:     "reflect",
		Priority:  4,
		CreatedBy: "system",
		Cond:      RuleCond{Tier: "sensory", AgeGTHours: 4},
		Action:    RuleAction{Op: "DELETE"},
	},
	{
		ID:        "sys-decay-unaccessed",
		Name:      "Decay importance for infrequently accessed STM memories",
		Scope:     "reflect",
		Priority:  10,
		CreatedBy: "system",
		Cond:      RuleCond{Tier: "stm", AgeGTHours: 48, AccessLT: 10},
		Action:    RuleAction{Op: "DECAY", Params: map[string]any{"factor": 0.95, "min": 0.1}},
	},
	{
		ID:        "sys-promote-to-ltm",
		Name:      "Promote frequently accessed STM to LTM",
		Scope:     "reflect",
		Priority:  50,
		CreatedBy: "system",
		Cond:      RuleCond{Tier: "stm", AccessGT: 10, AgeGTHours: 24},
		Action:    RuleAction{Op: "PROMOTE", Params: map[string]any{"to_tier": "ltm"}},
	},
	{
		ID:        "sys-demote-stale-ltm",
		Name:      "Demote LTM not accessed in 7 days",
		Scope:     "reflect",
		Priority:  50,
		CreatedBy: "system",
		Cond:      RuleCond{Tier: "ltm", UnaccessedGTHours: 168},
		Action:    RuleAction{Op: "DEMOTE", Params: map[string]any{"to_tier": "dormant"}},
	},
	{
		ID:        "sys-prune-low-utility",
		Name:      "Delete low-utility memories",
		Scope:     "reflect",
		Priority:  90,
		CreatedBy: "system",
		Cond:      RuleCond{AccessGT: 5, UtilityLT: 0.2},
		Action:    RuleAction{Op: "DELETE"},
	},
	{
		ID:        "sys-merge-similar",
		Name:      "link similar STM memories",
		Scope:     "reflect",
		Priority:  40,
		CreatedBy: "system",
		Cond:      RuleCond{Tier: "stm", SimilarityGT: 0.9},
		Action:    RuleAction{Op: "MERGE", Params: map[string]any{"strategy": "link_only"}},
	},
}

// seedBuiltinRules inserts built-in rules if they don't already exist.
func (s *SQLiteStore) seedBuiltinRules() {
	now := time.Now().UTC().Format(time.RFC3339)
	for _, r := range builtinRules {
		paramsJSON, _ := json.Marshal(r.Action.Params)
		s.db.Exec(`INSERT OR IGNORE INTO reflect_rules
			(id, ns, name, priority, scope, created_by,
			 cond_tier, cond_age_gt_hours, cond_unaccessed_gt_hours, cond_importance_lt, cond_access_lt, cond_access_gt, cond_utility_lt, cond_kind, cond_tag_includes,
			 cond_similarity_gt, action_op, action_params, rule_expires_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.NS, r.Name, r.Priority, r.Scope, r.CreatedBy,
			nilIfEmpty(r.Cond.Tier), nilIfZeroF(r.Cond.AgeGTHours), nilIfZeroF(r.Cond.UnaccessedGTHours),
			nilIfZeroF(r.Cond.ImportanceLT),
			nilIfZero(r.Cond.AccessLT), nilIfZero(r.Cond.AccessGT), nilIfZeroF(r.Cond.UtilityLT),
			nilIfEmpty(r.Cond.Kind), nilIfEmpty(r.Cond.TagIncludes),
			nilIfZeroF(r.Cond.SimilarityGT),
			r.Action.Op, string(paramsJSON), nilIfEmpty(r.ExpiresAt), now,
		)
	}
}

// Reflect evaluates all matching rules against memories and applies actions.
func (s *SQLiteStore) Reflect(ctx context.Context, p ReflectParams) (*ReflectResult, error) {
	result := &ReflectResult{}

	// Load applicable rules
	rules, err := s.RuleList(ctx, p.NS)
	if err != nil {
		return nil, fmt.Errorf("load rules: %w", err)
	}

	// Load candidate memories
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)
	where := []string{"m.deleted_at IS NULL", "(m.expires_at IS NULL OR m.expires_at > ?)"}
	args := []interface{}{nowStr}

	if p.NS != "" {
		nsf := ParseNSFilter(p.NS)
		clause, nsArgs := nsf.SQL("m.ns")
		if clause != "" {
			where = append(where, clause)
			args = append(args, nsArgs...)
		}
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
		WHERE %s`, strings.Join(where, " AND "))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query memories: %w", err)
	}
	defer rows.Close()

	type memAction struct {
		id     string
		action RuleAction
		rule   string
	}
	var actions []memAction
	var allMemories []model.Memory

	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("scan: %v", err))
			continue
		}
		result.MemoriesEvaluated++
		allMemories = append(allMemories, m)

		// Pinned memories are exempt from all lifecycle rules
		if m.Pinned {
			continue
		}

		ageHours := now.Sub(m.CreatedAt).Hours()
		// unaccessedHours: time since last access (or since creation if never accessed)
		unaccessedHours := ageHours
		if m.LastAccessedAt != nil {
			unaccessedHours = now.Sub(*m.LastAccessedAt).Hours()
		}
		utilityRatio := 0.0
		if m.AccessCount > 0 {
			utilityRatio = float64(m.UtilityCount) / float64(m.AccessCount)
		}

		// Evaluate rules in priority order (already sorted by RuleList)
		for _, rule := range rules {
			// Skip similarity rules — they're handled in a separate pass
			if rule.Cond.SimilarityGT > 0 {
				continue
			}
			if !ruleMatches(rule, m, ageHours, unaccessedHours, utilityRatio) {
				continue
			}
			// Check for conflicts: PIN/PRESERVE beats destructive ops
			actions = append(actions, memAction{id: m.ID, action: rule.Action, rule: rule.Name})
			break // first matching rule wins per memory
		}
	}

	// Track which memories were deleted by the per-memory pass
	deletedIDs := map[string]bool{}

	if p.DryRun {
		for _, a := range actions {
			result.RulesApplied++
			switch a.action.Op {
			case "DECAY":
				result.Decayed++
			case "DELETE":
				result.Deleted++
				deletedIDs[a.id] = true
			case "PROMOTE":
				result.Promoted++
			case "DEMOTE":
				result.Demoted++
			case "ARCHIVE":
				result.Archived++
			}
		}
	} else {
		// Apply actions
		for _, a := range actions {
			var applyErr error
			switch a.action.Op {
			case "DECAY":
				applyErr = s.applyDecay(ctx, a.id, a.action.Params)
				if applyErr == nil {
					result.Decayed++
				}
			case "DELETE":
				applyErr = s.applyDelete(ctx, a.id, nowStr)
				if applyErr == nil {
					result.Deleted++
					deletedIDs[a.id] = true
				}
			case "PROMOTE":
				applyErr = s.applyTierChange(ctx, a.id, a.action.Params, "promote")
				if applyErr == nil {
					result.Promoted++
				}
			case "DEMOTE":
				applyErr = s.applyTierChange(ctx, a.id, a.action.Params, "demote")
				if applyErr == nil {
					result.Demoted++
				}
			case "ARCHIVE":
				applyErr = s.applyTierChange(ctx, a.id, map[string]any{"to_tier": "archived"}, "archive")
				if applyErr == nil {
					result.Archived++
				}
			case "PIN":
				// PIN is a no-op marker — it prevents other rules from matching
			case "TTL_SET":
				applyErr = s.applyTTLSet(ctx, a.id, a.action.Params)
			}
			if applyErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("rule %q on %s: %v", a.rule, a.id, applyErr))
			} else {
				result.RulesApplied++
			}
		}
	}

	// Similarity merge pass: collect rules with SimilarityGT > 0 and Action.Op == "MERGE"
	for _, rule := range rules {
		if rule.Cond.SimilarityGT <= 0 || rule.Action.Op != "MERGE" {
			continue
		}
		smResult, mergeErr := s.applySimilarityMerge(ctx, rule, allMemories, deletedIDs, p.DryRun)
		if mergeErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("similarity merge %q: %v", rule.Name, mergeErr))
		} else {
			result.Merged += smResult.absorbed
			result.Linked += smResult.linked
			result.LinkedClusters = append(result.LinkedClusters, smResult.clusters...)
			if smResult.absorbed > 0 || smResult.linked > 0 {
				result.RulesApplied++
			}
			// Track absorbed IDs so subsequent similarity rules don't re-process them
			for _, id := range smResult.absorbedIDs {
				deletedIDs[id] = true
			}
		}
	}

	// Auto-consolidation pass: create skeleton parent nodes for large orphan clusters.
	if len(result.LinkedClusters) > 0 {
		autoConsResult := s.autoConsolidateClusters(ctx, result.LinkedClusters, allMemories, p)
		result.AutoConsolidated += autoConsResult
	}

	// Edge decay pass: weaken unused edges, prune very weak ones.
	// Edges not accessed in 30+ days with <3 accesses decay; weight <0.05 → deleted.
	if !p.DryRun {
		s.decayEdges(ctx, result)
	}

	return result, nil
}

func ruleMatches(rule ReflectRule, m model.Memory, ageHours, unaccessedHours, utilityRatio float64) bool {
	c := rule.Cond
	// Similarity conditions are handled in the separate merge pass — skip here
	if c.SimilarityGT > 0 {
		return false
	}
	if c.Tier != "" && m.Tier != c.Tier {
		return false
	}
	if c.AgeGTHours > 0 && ageHours <= c.AgeGTHours {
		return false
	}
	if c.UnaccessedGTHours > 0 && unaccessedHours <= c.UnaccessedGTHours {
		return false
	}
	if c.ImportanceLT > 0 && m.Importance >= c.ImportanceLT {
		return false
	}
	if c.AccessLT > 0 && m.AccessCount >= c.AccessLT {
		return false
	}
	if c.AccessGT > 0 && m.AccessCount <= c.AccessGT {
		return false
	}
	if c.UtilityLT > 0 && (m.AccessCount == 0 || m.UtilityCount == 0 || utilityRatio >= c.UtilityLT) {
		return false
	}
	if c.Kind != "" && m.Kind != c.Kind {
		return false
	}
	if c.TagIncludes != "" {
		found := false
		for _, t := range m.Tags {
			if t == c.TagIncludes {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// ruleMatchesNonSimilarity checks all rule conditions except SimilarityGT.
// Used to filter candidates for the similarity merge pass.
func ruleMatchesNonSimilarity(rule ReflectRule, m model.Memory, ageHours, unaccessedHours, utilityRatio float64) bool {
	c := rule.Cond
	if c.Tier != "" && m.Tier != c.Tier {
		return false
	}
	if c.AgeGTHours > 0 && ageHours <= c.AgeGTHours {
		return false
	}
	if c.UnaccessedGTHours > 0 && unaccessedHours <= c.UnaccessedGTHours {
		return false
	}
	if c.ImportanceLT > 0 && m.Importance >= c.ImportanceLT {
		return false
	}
	if c.AccessLT > 0 && m.AccessCount >= c.AccessLT {
		return false
	}
	if c.AccessGT > 0 && m.AccessCount <= c.AccessGT {
		return false
	}
	if c.UtilityLT > 0 && (m.AccessCount == 0 || m.UtilityCount == 0 || utilityRatio >= c.UtilityLT) {
		return false
	}
	if c.Kind != "" && m.Kind != c.Kind {
		return false
	}
	if c.TagIncludes != "" {
		found := false
		for _, t := range m.Tags {
			if t == c.TagIncludes {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (s *SQLiteStore) applyDecay(ctx context.Context, id string, params map[string]any) error {
	factor := 0.95
	minVal := 0.1
	if f, ok := params["factor"].(float64); ok {
		factor = f
	}
	if m, ok := params["min"].(float64); ok {
		minVal = m
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE memories SET importance = MAX(?, importance * ?) WHERE id = ? AND deleted_at IS NULL`,
		minVal, factor, id)
	return err
}

func (s *SQLiteStore) applyDelete(ctx context.Context, id, now string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE memories SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`, now, id)
	return err
}

func (s *SQLiteStore) applyTierChange(ctx context.Context, id string, params map[string]any, op string) error {
	toTier, ok := params["to_tier"].(string)
	if !ok || toTier == "" {
		return fmt.Errorf("%s: missing to_tier param", op)
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE memories SET tier = ? WHERE id = ? AND deleted_at IS NULL`, toTier, id)
	return err
}

// tierRank returns a numeric rank for tier ordering (higher = more permanent).
func tierRank(tier string) int {
	switch tier {
	case "dormant":
		return 0
	case "sensory":
		return 1
	case "stm":
		return 2
	case "ltm":
		return 3
	default:
		return 0
	}
}

func (s *SQLiteStore) applyTTLSet(ctx context.Context, id string, params map[string]any) error {
	ttlStr, ok := params["ttl"].(string)
	if !ok || ttlStr == "" {
		return fmt.Errorf("TTL_SET: missing ttl param")
	}
	d, err := ParseTTL(ttlStr)
	if err != nil {
		return fmt.Errorf("TTL_SET: %w", err)
	}
	exp := time.Now().UTC().Add(d).Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx,
		`UPDATE memories SET expires_at = ? WHERE id = ? AND deleted_at IS NULL`, exp, id)
	return err
}

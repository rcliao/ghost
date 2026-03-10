package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rcliao/ghost/internal/embedding"
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
	Tier          string  `json:"tier,omitempty"`
	AgeGTHours    float64 `json:"age_gt_hours,omitempty"`
	ImportanceLT  float64 `json:"importance_lt,omitempty"`
	AccessLT      int     `json:"access_lt,omitempty"`
	AccessGT      int     `json:"access_gt,omitempty"`
	UtilityLT     float64 `json:"utility_lt,omitempty"`
	Kind          string  `json:"kind,omitempty"`
	TagIncludes   string  `json:"tag_includes,omitempty"`
	SimilarityGT  float64 `json:"similarity_gt,omitempty"`
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
		Name:      "Decay importance for unaccessed STM memories",
		Scope:     "reflect",
		Priority:  10,
		CreatedBy: "system",
		Cond:      RuleCond{Tier: "stm", AgeGTHours: 72, AccessLT: 3},
		Action:    RuleAction{Op: "DECAY", Params: map[string]any{"factor": 0.95, "min": 0.1}},
	},
	{
		ID:        "sys-promote-to-ltm",
		Name:      "Promote accessed STM to LTM",
		Scope:     "reflect",
		Priority:  50,
		CreatedBy: "system",
		Cond:      RuleCond{Tier: "stm", AccessGT: 3, AgeGTHours: 24},
		Action:    RuleAction{Op: "PROMOTE", Params: map[string]any{"to_tier": "ltm"}},
	},
	{
		ID:        "sys-demote-stale-ltm",
		Name:      "Demote LTM not accessed in 7 days",
		Scope:     "reflect",
		Priority:  50,
		CreatedBy: "system",
		Cond:      RuleCond{Tier: "ltm", AgeGTHours: 168, AccessLT: 2},
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
		Name:      "merge similar STM memories",
		Scope:     "reflect",
		Priority:  40,
		CreatedBy: "system",
		Cond:      RuleCond{Tier: "stm", SimilarityGT: 0.9},
		Action:    RuleAction{Op: "MERGE", Params: map[string]any{"strategy": "keep_highest_importance"}},
	},
}

// seedBuiltinRules inserts built-in rules if they don't already exist.
func (s *SQLiteStore) seedBuiltinRules() {
	now := time.Now().UTC().Format(time.RFC3339)
	for _, r := range builtinRules {
		paramsJSON, _ := json.Marshal(r.Action.Params)
		s.db.Exec(`INSERT OR IGNORE INTO reflect_rules
			(id, ns, name, priority, scope, created_by,
			 cond_tier, cond_age_gt_hours, cond_importance_lt, cond_access_lt, cond_access_gt, cond_utility_lt, cond_kind, cond_tag_includes,
			 cond_similarity_gt, action_op, action_params, rule_expires_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.NS, r.Name, r.Priority, r.Scope, r.CreatedBy,
			nilIfEmpty(r.Cond.Tier), nilIfZeroF(r.Cond.AgeGTHours), nilIfZeroF(r.Cond.ImportanceLT),
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
			if !ruleMatches(rule, m, ageHours, utilityRatio) {
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
		merged, absorbedIDs, mergeErr := s.applySimilarityMerge(ctx, rule, allMemories, deletedIDs, p.DryRun)
		if mergeErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("similarity merge %q: %v", rule.Name, mergeErr))
		} else {
			result.Merged += merged
			if merged > 0 {
				result.RulesApplied++
			}
			// Track absorbed IDs so subsequent similarity rules don't re-process them
			for _, id := range absorbedIDs {
				deletedIDs[id] = true
			}
		}
	}

	return result, nil
}

func ruleMatches(rule ReflectRule, m model.Memory, ageHours, utilityRatio float64) bool {
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
func ruleMatchesNonSimilarity(rule ReflectRule, m model.Memory, ageHours, utilityRatio float64) bool {
	c := rule.Cond
	if c.Tier != "" && m.Tier != c.Tier {
		return false
	}
	if c.AgeGTHours > 0 && ageHours <= c.AgeGTHours {
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

// applySimilarityMerge runs the similarity-based merge for a single rule.
// Returns the count of absorbed memories and their IDs (for cross-rule dedup).
func (s *SQLiteStore) applySimilarityMerge(ctx context.Context, rule ReflectRule, allMemories []model.Memory, deletedIDs map[string]bool, dryRun bool) (int, []string, error) {
	now := time.Now().UTC()

	// 1. Filter candidates by non-similarity conditions
	var candidates []model.Memory
	for _, m := range allMemories {
		if deletedIDs[m.ID] {
			continue
		}
		ageHours := now.Sub(m.CreatedAt).Hours()
		utilityRatio := 0.0
		if m.AccessCount > 0 {
			utilityRatio = float64(m.UtilityCount) / float64(m.AccessCount)
		}
		if ruleMatchesNonSimilarity(rule, m, ageHours, utilityRatio) {
			candidates = append(candidates, m)
		}
	}

	if len(candidates) < 2 {
		return 0, nil, nil
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
		return 0, nil, fmt.Errorf("load embeddings: %w", err)
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
		return 0, nil, nil
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

	// 5. Apply merge for each cluster
	totalAbsorbed := 0
	var allAbsorbedIDs []string
	for _, cluster := range clusters {
		if dryRun {
			for _, m := range cluster[1:] {
				totalAbsorbed++
				allAbsorbedIDs = append(allAbsorbedIDs, m.ID)
			}
			continue
		}
		absorbed, err := s.applyMerge(ctx, cluster)
		if err != nil {
			return totalAbsorbed, allAbsorbedIDs, fmt.Errorf("merge cluster: %w", err)
		}
		totalAbsorbed += absorbed
		// Track absorbed IDs (all non-survivor, non-pinned members)
		for _, m := range cluster[1:] {
			if !m.Pinned {
				allAbsorbedIDs = append(allAbsorbedIDs, m.ID)
			}
		}
	}

	return totalAbsorbed, allAbsorbedIDs, nil
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

// RuleSet creates or updates a reflect rule. Returns the stored rule.
func (s *SQLiteStore) RuleSet(ctx context.Context, rule ReflectRule) (*ReflectRule, error) {
	if rule.ID == "" {
		rule.ID = s.newID()
	}
	if rule.Name == "" {
		return nil, fmt.Errorf("rule name is required")
	}
	if !validActionOps[rule.Action.Op] {
		return nil, fmt.Errorf("invalid action op %q", rule.Action.Op)
	}
	if rule.Priority == 0 {
		rule.Priority = 50
	}
	if rule.Scope == "" {
		rule.Scope = "reflect"
	}
	if rule.CreatedBy == "" {
		rule.CreatedBy = "user"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rule.CreatedAt = now

	paramsJSON, _ := json.Marshal(rule.Action.Params)

	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO reflect_rules
		(id, ns, name, priority, scope, created_by,
		 cond_tier, cond_age_gt_hours, cond_importance_lt, cond_access_lt, cond_access_gt, cond_utility_lt, cond_kind, cond_tag_includes,
		 cond_similarity_gt, action_op, action_params, rule_expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rule.ID, rule.NS, rule.Name, rule.Priority, rule.Scope, rule.CreatedBy,
		nilIfEmpty(rule.Cond.Tier), nilIfZeroF(rule.Cond.AgeGTHours), nilIfZeroF(rule.Cond.ImportanceLT),
		nilIfZero(rule.Cond.AccessLT), nilIfZero(rule.Cond.AccessGT), nilIfZeroF(rule.Cond.UtilityLT),
		nilIfEmpty(rule.Cond.Kind), nilIfEmpty(rule.Cond.TagIncludes),
		nilIfZeroF(rule.Cond.SimilarityGT),
		rule.Action.Op, string(paramsJSON), nilIfEmpty(rule.ExpiresAt), now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert rule: %w", err)
	}

	return &rule, nil
}

// RuleGet retrieves a rule by ID.
func (s *SQLiteStore) RuleGet(ctx context.Context, id string) (*ReflectRule, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, ns, name, priority, scope, created_by,
		        cond_tier, cond_age_gt_hours, cond_importance_lt, cond_access_lt, cond_access_gt, cond_utility_lt, cond_kind, cond_tag_includes,
		        cond_similarity_gt, action_op, action_params, rule_expires_at, created_at
		 FROM reflect_rules WHERE id = ?`, id)
	return scanRule(row)
}

// RuleList returns all rules matching the given namespace, ordered by priority DESC.
func (s *SQLiteStore) RuleList(ctx context.Context, ns string) ([]ReflectRule, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	where := []string{"(rule_expires_at IS NULL OR rule_expires_at > ?)"}
	args := []interface{}{now}

	if ns != "" {
		where = append(where, "(ns = '' OR ns = ?)")
		args = append(args, ns)
	}

	query := fmt.Sprintf(`SELECT id, ns, name, priority, scope, created_by,
		cond_tier, cond_age_gt_hours, cond_importance_lt, cond_access_lt, cond_access_gt, cond_utility_lt, cond_kind, cond_tag_includes,
		cond_similarity_gt, action_op, action_params, rule_expires_at, created_at
		FROM reflect_rules WHERE %s ORDER BY priority DESC`, strings.Join(where, " AND "))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rules []ReflectRule
	for rows.Next() {
		r, err := scanRuleRow(rows)
		if err != nil {
			return nil, err
		}
		rules = append(rules, *r)
	}
	return rules, nil
}

// RuleDelete removes a rule by ID.
func (s *SQLiteStore) RuleDelete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM reflect_rules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("rule not found: %s", id)
	}
	return nil
}

type ruleScanner interface {
	Scan(dest ...interface{}) error
}

func scanRule(row *sql.Row) (*ReflectRule, error) {
	var r ReflectRule
	var ns, scope, createdBy sql.NullString
	var condTier, condKind, condTag sql.NullString
	var condAgeGT, condImpLT, condUtilLT, condSimGT sql.NullFloat64
	var condAccessLT, condAccessGT sql.NullInt64
	var actionParams, expiresAt sql.NullString

	err := row.Scan(
		&r.ID, &ns, &r.Name, &r.Priority, &scope, &createdBy,
		&condTier, &condAgeGT, &condImpLT, &condAccessLT, &condAccessGT, &condUtilLT, &condKind, &condTag,
		&condSimGT, &r.Action.Op, &actionParams, &expiresAt, &r.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	fillRule(&r, ns, scope, createdBy, condTier, condAgeGT, condImpLT, condAccessLT, condAccessGT, condUtilLT, condKind, condTag, condSimGT, actionParams, expiresAt)
	return &r, nil
}

func scanRuleRow(row ruleScanner) (*ReflectRule, error) {
	var r ReflectRule
	var ns, scope, createdBy sql.NullString
	var condTier, condKind, condTag sql.NullString
	var condAgeGT, condImpLT, condUtilLT, condSimGT sql.NullFloat64
	var condAccessLT, condAccessGT sql.NullInt64
	var actionParams, expiresAt sql.NullString

	err := row.Scan(
		&r.ID, &ns, &r.Name, &r.Priority, &scope, &createdBy,
		&condTier, &condAgeGT, &condImpLT, &condAccessLT, &condAccessGT, &condUtilLT, &condKind, &condTag,
		&condSimGT, &r.Action.Op, &actionParams, &expiresAt, &r.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	fillRule(&r, ns, scope, createdBy, condTier, condAgeGT, condImpLT, condAccessLT, condAccessGT, condUtilLT, condKind, condTag, condSimGT, actionParams, expiresAt)
	return &r, nil
}

func fillRule(r *ReflectRule, ns, scope, createdBy, condTier sql.NullString, condAgeGT, condImpLT sql.NullFloat64, condAccessLT, condAccessGT sql.NullInt64, condUtilLT sql.NullFloat64, condKind, condTag sql.NullString, condSimGT sql.NullFloat64, actionParams, expiresAt sql.NullString) {
	if ns.Valid {
		r.NS = ns.String
	}
	if scope.Valid {
		r.Scope = scope.String
	}
	if createdBy.Valid {
		r.CreatedBy = createdBy.String
	}
	if condTier.Valid {
		r.Cond.Tier = condTier.String
	}
	if condAgeGT.Valid {
		r.Cond.AgeGTHours = condAgeGT.Float64
	}
	if condImpLT.Valid {
		r.Cond.ImportanceLT = condImpLT.Float64
	}
	if condAccessLT.Valid {
		r.Cond.AccessLT = int(condAccessLT.Int64)
	}
	if condAccessGT.Valid {
		r.Cond.AccessGT = int(condAccessGT.Int64)
	}
	if condUtilLT.Valid {
		r.Cond.UtilityLT = condUtilLT.Float64
	}
	if condKind.Valid {
		r.Cond.Kind = condKind.String
	}
	if condTag.Valid {
		r.Cond.TagIncludes = condTag.String
	}
	if condSimGT.Valid {
		r.Cond.SimilarityGT = condSimGT.Float64
	}
	if actionParams.Valid && actionParams.String != "" && actionParams.String != "null" {
		json.Unmarshal([]byte(actionParams.String), &r.Action.Params)
	}
	if expiresAt.Valid {
		r.ExpiresAt = expiresAt.String
	}
}

func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func nilIfZero(n int) interface{} {
	if n == 0 {
		return nil
	}
	return n
}

func nilIfZeroF(f float64) interface{} {
	if f == 0 {
		return nil
	}
	return f
}

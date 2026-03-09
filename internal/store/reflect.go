package store

import (
	"context"
	"database/sql"
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
	Tier          string  `json:"tier,omitempty"`
	AgeGTHours    float64 `json:"age_gt_hours,omitempty"`
	ImportanceLT  float64 `json:"importance_lt,omitempty"`
	AccessLT      int     `json:"access_lt,omitempty"`
	AccessGT      int     `json:"access_gt,omitempty"`
	UtilityLT     float64 `json:"utility_lt,omitempty"`
	Kind          string  `json:"kind,omitempty"`
	TagIncludes   string  `json:"tag_includes,omitempty"`
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
	Errors            []string `json:"errors,omitempty"`
}

// Valid action operations.
var validActionOps = map[string]bool{
	"DECAY": true, "DELETE": true, "PROMOTE": true, "DEMOTE": true,
	"ARCHIVE": true, "TTL_SET": true, "PIN": true,
}

// builtinRules are seeded on startup with ON CONFLICT IGNORE semantics.
var builtinRules = []ReflectRule{
	{
		ID:        "sys-pin-identity",
		Name:      "Protect identity-tier memories from decay/demotion",
		Scope:     "reflect",
		Priority:  1,
		CreatedBy: "system",
		Cond:      RuleCond{Tier: "identity"},
		Action:    RuleAction{Op: "PIN"},
	},
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
}

// seedBuiltinRules inserts built-in rules if they don't already exist.
func (s *SQLiteStore) seedBuiltinRules() {
	now := time.Now().UTC().Format(time.RFC3339)
	for _, r := range builtinRules {
		paramsJSON, _ := json.Marshal(r.Action.Params)
		s.db.Exec(`INSERT OR IGNORE INTO reflect_rules
			(id, ns, name, priority, scope, created_by,
			 cond_tier, cond_age_gt_hours, cond_importance_lt, cond_access_lt, cond_access_gt, cond_utility_lt, cond_kind, cond_tag_includes,
			 action_op, action_params, rule_expires_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.NS, r.Name, r.Priority, r.Scope, r.CreatedBy,
			nilIfEmpty(r.Cond.Tier), nilIfZeroF(r.Cond.AgeGTHours), nilIfZeroF(r.Cond.ImportanceLT),
			nilIfZero(r.Cond.AccessLT), nilIfZero(r.Cond.AccessGT), nilIfZeroF(r.Cond.UtilityLT),
			nilIfEmpty(r.Cond.Kind), nilIfEmpty(r.Cond.TagIncludes),
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
		       m.importance, m.utility_count, m.tier, m.est_tokens
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

	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("scan: %v", err))
			continue
		}
		result.MemoriesEvaluated++

		ageHours := now.Sub(m.CreatedAt).Hours()
		utilityRatio := 0.0
		if m.AccessCount > 0 {
			utilityRatio = float64(m.UtilityCount) / float64(m.AccessCount)
		}

		// Evaluate rules in priority order (already sorted by RuleList)
		for _, rule := range rules {
			if !ruleMatches(rule, m, ageHours, utilityRatio) {
				continue
			}
			// Check for conflicts: PIN/PRESERVE beats destructive ops
			actions = append(actions, memAction{id: m.ID, action: rule.Action, rule: rule.Name})
			break // first matching rule wins per memory
		}
	}

	if p.DryRun {
		for _, a := range actions {
			result.RulesApplied++
			switch a.action.Op {
			case "DECAY":
				result.Decayed++
			case "DELETE":
				result.Deleted++
			case "PROMOTE":
				result.Promoted++
			case "DEMOTE":
				result.Demoted++
			case "ARCHIVE":
				result.Archived++
			}
		}
		return result, nil
	}

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

	return result, nil
}

func ruleMatches(rule ReflectRule, m model.Memory, ageHours, utilityRatio float64) bool {
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
		 action_op, action_params, rule_expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rule.ID, rule.NS, rule.Name, rule.Priority, rule.Scope, rule.CreatedBy,
		nilIfEmpty(rule.Cond.Tier), nilIfZeroF(rule.Cond.AgeGTHours), nilIfZeroF(rule.Cond.ImportanceLT),
		nilIfZero(rule.Cond.AccessLT), nilIfZero(rule.Cond.AccessGT), nilIfZeroF(rule.Cond.UtilityLT),
		nilIfEmpty(rule.Cond.Kind), nilIfEmpty(rule.Cond.TagIncludes),
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
		        action_op, action_params, rule_expires_at, created_at
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
		action_op, action_params, rule_expires_at, created_at
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
	var condAgeGT, condImpLT, condUtilLT sql.NullFloat64
	var condAccessLT, condAccessGT sql.NullInt64
	var actionParams, expiresAt sql.NullString

	err := row.Scan(
		&r.ID, &ns, &r.Name, &r.Priority, &scope, &createdBy,
		&condTier, &condAgeGT, &condImpLT, &condAccessLT, &condAccessGT, &condUtilLT, &condKind, &condTag,
		&r.Action.Op, &actionParams, &expiresAt, &r.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	fillRule(&r, ns, scope, createdBy, condTier, condAgeGT, condImpLT, condAccessLT, condAccessGT, condUtilLT, condKind, condTag, actionParams, expiresAt)
	return &r, nil
}

func scanRuleRow(row ruleScanner) (*ReflectRule, error) {
	var r ReflectRule
	var ns, scope, createdBy sql.NullString
	var condTier, condKind, condTag sql.NullString
	var condAgeGT, condImpLT, condUtilLT sql.NullFloat64
	var condAccessLT, condAccessGT sql.NullInt64
	var actionParams, expiresAt sql.NullString

	err := row.Scan(
		&r.ID, &ns, &r.Name, &r.Priority, &scope, &createdBy,
		&condTier, &condAgeGT, &condImpLT, &condAccessLT, &condAccessGT, &condUtilLT, &condKind, &condTag,
		&r.Action.Op, &actionParams, &expiresAt, &r.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	fillRule(&r, ns, scope, createdBy, condTier, condAgeGT, condImpLT, condAccessLT, condAccessGT, condUtilLT, condKind, condTag, actionParams, expiresAt)
	return &r, nil
}

func fillRule(r *ReflectRule, ns, scope, createdBy, condTier sql.NullString, condAgeGT, condImpLT sql.NullFloat64, condAccessLT, condAccessGT sql.NullInt64, condUtilLT sql.NullFloat64, condKind, condTag sql.NullString, actionParams, expiresAt sql.NullString) {
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

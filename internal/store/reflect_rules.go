package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

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
		 cond_tier, cond_age_gt_hours, cond_unaccessed_gt_hours, cond_importance_lt, cond_access_lt, cond_access_gt, cond_utility_lt, cond_kind, cond_tag_includes,
		 cond_similarity_gt, action_op, action_params, rule_expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rule.ID, rule.NS, rule.Name, rule.Priority, rule.Scope, rule.CreatedBy,
		nilIfEmpty(rule.Cond.Tier), nilIfZeroF(rule.Cond.AgeGTHours), nilIfZeroF(rule.Cond.UnaccessedGTHours),
		nilIfZeroF(rule.Cond.ImportanceLT),
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
		        cond_tier, cond_age_gt_hours, cond_unaccessed_gt_hours, cond_importance_lt, cond_access_lt, cond_access_gt, cond_utility_lt, cond_kind, cond_tag_includes,
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
		cond_tier, cond_age_gt_hours, cond_unaccessed_gt_hours, cond_importance_lt, cond_access_lt, cond_access_gt, cond_utility_lt, cond_kind, cond_tag_includes,
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
	var condAgeGT, condUnaccessedGT, condImpLT, condUtilLT, condSimGT sql.NullFloat64
	var condAccessLT, condAccessGT sql.NullInt64
	var actionParams, expiresAt sql.NullString

	err := row.Scan(
		&r.ID, &ns, &r.Name, &r.Priority, &scope, &createdBy,
		&condTier, &condAgeGT, &condUnaccessedGT, &condImpLT, &condAccessLT, &condAccessGT, &condUtilLT, &condKind, &condTag,
		&condSimGT, &r.Action.Op, &actionParams, &expiresAt, &r.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	fillRule(&r, ns, scope, createdBy, condTier, condAgeGT, condUnaccessedGT, condImpLT, condAccessLT, condAccessGT, condUtilLT, condKind, condTag, condSimGT, actionParams, expiresAt)
	return &r, nil
}

func scanRuleRow(row ruleScanner) (*ReflectRule, error) {
	var r ReflectRule
	var ns, scope, createdBy sql.NullString
	var condTier, condKind, condTag sql.NullString
	var condAgeGT, condUnaccessedGT, condImpLT, condUtilLT, condSimGT sql.NullFloat64
	var condAccessLT, condAccessGT sql.NullInt64
	var actionParams, expiresAt sql.NullString

	err := row.Scan(
		&r.ID, &ns, &r.Name, &r.Priority, &scope, &createdBy,
		&condTier, &condAgeGT, &condUnaccessedGT, &condImpLT, &condAccessLT, &condAccessGT, &condUtilLT, &condKind, &condTag,
		&condSimGT, &r.Action.Op, &actionParams, &expiresAt, &r.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	fillRule(&r, ns, scope, createdBy, condTier, condAgeGT, condUnaccessedGT, condImpLT, condAccessLT, condAccessGT, condUtilLT, condKind, condTag, condSimGT, actionParams, expiresAt)
	return &r, nil
}

func fillRule(r *ReflectRule, ns, scope, createdBy, condTier sql.NullString, condAgeGT, condUnaccessedGT, condImpLT sql.NullFloat64, condAccessLT, condAccessGT sql.NullInt64, condUtilLT sql.NullFloat64, condKind, condTag sql.NullString, condSimGT sql.NullFloat64, actionParams, expiresAt sql.NullString) {
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
	if condUnaccessedGT.Valid {
		r.Cond.UnaccessedGTHours = condUnaccessedGT.Float64
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

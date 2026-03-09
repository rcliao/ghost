package store

import (
	"context"
	"testing"
	"time"

	"github.com/rcliao/ghost/internal/model"
)

func TestReflectDryRun(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Create an old STM memory with low access count — should match sys-decay-unaccessed
	s.db.Exec(`INSERT INTO memories (id, ns, key, content, kind, version, created_at, priority, access_count, importance, tier, est_tokens)
		VALUES ('m1', 'test', 'old-mem', 'old content', 'semantic', 1, ?, 'normal', 1, 0.5, 'stm', 25)`,
		time.Now().Add(-96*time.Hour).UTC().Format(time.RFC3339))

	result, err := s.Reflect(ctx, ReflectParams{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.MemoriesEvaluated < 1 {
		t.Errorf("expected at least 1 memory evaluated, got %d", result.MemoriesEvaluated)
	}
	if result.Decayed < 1 {
		t.Errorf("expected at least 1 decayed (dry-run), got %d", result.Decayed)
	}
}

func TestReflectDecay(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Insert old STM memory
	s.db.Exec(`INSERT INTO memories (id, ns, key, content, kind, version, created_at, priority, access_count, importance, tier, est_tokens)
		VALUES ('m1', 'test', 'old-mem', 'old content', 'semantic', 1, ?, 'normal', 1, 0.8, 'stm', 25)`,
		time.Now().Add(-96*time.Hour).UTC().Format(time.RFC3339))

	result, err := s.Reflect(ctx, ReflectParams{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Decayed < 1 {
		t.Errorf("expected at least 1 decayed, got %d", result.Decayed)
	}

	// Check importance was reduced
	var importance float64
	s.db.QueryRow(`SELECT importance FROM memories WHERE id = 'm1'`).Scan(&importance)
	if importance >= 0.8 {
		t.Errorf("expected importance < 0.8 after decay, got %f", importance)
	}
}

func TestReflectPromote(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Insert STM memory with high access count and old enough
	s.db.Exec(`INSERT INTO memories (id, ns, key, content, kind, version, created_at, priority, access_count, importance, tier, est_tokens)
		VALUES ('m1', 'test', 'popular', 'popular content', 'semantic', 1, ?, 'normal', 5, 0.7, 'stm', 30)`,
		time.Now().Add(-48*time.Hour).UTC().Format(time.RFC3339))

	result, err := s.Reflect(ctx, ReflectParams{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Promoted < 1 {
		t.Errorf("expected at least 1 promoted, got %d", result.Promoted)
	}

	// Check tier changed
	var tier string
	s.db.QueryRow(`SELECT tier FROM memories WHERE id = 'm1'`).Scan(&tier)
	if tier != "ltm" {
		t.Errorf("expected tier 'ltm', got %q", tier)
	}
}

func TestRuleSetAndList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rule, err := s.RuleSet(ctx, ReflectRule{
		Name:     "test-rule",
		Priority: 75,
		Cond:     RuleCond{Tier: "stm", AgeGTHours: 48},
		Action:   RuleAction{Op: "DECAY", Params: map[string]any{"factor": 0.9}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rule.ID == "" {
		t.Error("expected generated ID")
	}
	if rule.Name != "test-rule" {
		t.Errorf("expected name 'test-rule', got %q", rule.Name)
	}

	// List should include system rules + our new rule
	rules, err := s.RuleList(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) < 7 { // 6 system + 1 user
		t.Errorf("expected at least 7 rules, got %d", len(rules))
	}

	// First rule should be highest priority
	if rules[0].Priority < rules[len(rules)-1].Priority {
		t.Error("expected rules sorted by priority DESC")
	}
}

func TestRuleGetAndDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rule, _ := s.RuleSet(ctx, ReflectRule{
		Name:   "to-delete",
		Action: RuleAction{Op: "DELETE"},
	})

	got, err := s.RuleGet(ctx, rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "to-delete" {
		t.Errorf("expected name 'to-delete', got %q", got.Name)
	}

	if err := s.RuleDelete(ctx, rule.ID); err != nil {
		t.Fatal(err)
	}

	_, err = s.RuleGet(ctx, rule.ID)
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestRuleMatchesConditions(t *testing.T) {
	tests := []struct {
		name     string
		rule     ReflectRule
		mem      model.Memory
		ageH     float64
		utilR    float64
		expected bool
	}{
		{
			name:     "tier match",
			rule:     ReflectRule{Cond: RuleCond{Tier: "stm"}},
			mem:      model.Memory{Tier: "stm"},
			expected: true,
		},
		{
			name:     "tier mismatch",
			rule:     ReflectRule{Cond: RuleCond{Tier: "ltm"}},
			mem:      model.Memory{Tier: "stm"},
			expected: false,
		},
		{
			name:     "age condition met",
			rule:     ReflectRule{Cond: RuleCond{AgeGTHours: 24}},
			mem:      model.Memory{},
			ageH:     48,
			expected: true,
		},
		{
			name:     "age condition not met",
			rule:     ReflectRule{Cond: RuleCond{AgeGTHours: 72}},
			mem:      model.Memory{},
			ageH:     48,
			expected: false,
		},
		{
			name:     "access_lt met",
			rule:     ReflectRule{Cond: RuleCond{AccessLT: 5}},
			mem:      model.Memory{AccessCount: 2},
			expected: true,
		},
		{
			name:     "access_lt not met",
			rule:     ReflectRule{Cond: RuleCond{AccessLT: 5}},
			mem:      model.Memory{AccessCount: 10},
			expected: false,
		},
		{
			name:     "combined conditions",
			rule:     ReflectRule{Cond: RuleCond{Tier: "stm", AgeGTHours: 24, AccessLT: 3}},
			mem:      model.Memory{Tier: "stm", AccessCount: 1},
			ageH:     48,
			expected: true,
		},
		{
			name:     "tag includes match",
			rule:     ReflectRule{Cond: RuleCond{TagIncludes: "important"}},
			mem:      model.Memory{Tags: []string{"important", "other"}},
			expected: true,
		},
		{
			name:     "tag includes no match",
			rule:     ReflectRule{Cond: RuleCond{TagIncludes: "important"}},
			mem:      model.Memory{Tags: []string{"other"}},
			expected: false,
		},
		{
			name:     "utility_lt skipped when utility_count is 0",
			rule:     ReflectRule{Cond: RuleCond{AccessGT: 5, UtilityLT: 0.2}},
			mem:      model.Memory{AccessCount: 10, UtilityCount: 0},
			utilR:    0.0,
			expected: false, // should NOT match — utility tracking never engaged
		},
		{
			name:     "utility_lt matches when utility tracking engaged",
			rule:     ReflectRule{Cond: RuleCond{AccessGT: 5, UtilityLT: 0.2}},
			mem:      model.Memory{AccessCount: 10, UtilityCount: 1},
			utilR:    0.1,
			expected: true, // 1/10 = 0.1 < 0.2 → match
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ruleMatches(tt.rule, tt.mem, tt.ageH, tt.utilR)
			if got != tt.expected {
				t.Errorf("ruleMatches() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestReflectSensoryDecay(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Sensory memory older than 4h with no accesses → should be deleted
	s.db.Exec(`INSERT INTO memories (id, ns, key, content, kind, version, created_at, priority, access_count, importance, tier, est_tokens)
		VALUES ('s1', 'test', 'old-sensory', 'fleeting observation', 'episodic', 1, ?, 'normal', 0, 0.3, 'sensory', 20)`,
		time.Now().Add(-5*time.Hour).UTC().Format(time.RFC3339))

	result, err := s.Reflect(ctx, ReflectParams{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted < 1 {
		t.Errorf("expected sensory memory to be deleted, got deleted=%d", result.Deleted)
	}

	// Verify soft-deleted
	var deletedAt *string
	s.db.QueryRow(`SELECT deleted_at FROM memories WHERE id = 's1'`).Scan(&deletedAt)
	if deletedAt == nil {
		t.Error("expected sensory memory to have deleted_at set")
	}
}

func TestReflectSensoryPromote(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Sensory memory older than 1h with access > 0 → should promote to stm
	s.db.Exec(`INSERT INTO memories (id, ns, key, content, kind, version, created_at, priority, access_count, importance, tier, est_tokens)
		VALUES ('s1', 'test', 'attended-sensory', 'noticed observation', 'episodic', 1, ?, 'normal', 2, 0.4, 'sensory', 20)`,
		time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339))

	result, err := s.Reflect(ctx, ReflectParams{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Promoted < 1 {
		t.Errorf("expected sensory memory to be promoted, got promoted=%d", result.Promoted)
	}

	var tier string
	s.db.QueryRow(`SELECT tier FROM memories WHERE id = 's1'`).Scan(&tier)
	if tier != "stm" {
		t.Errorf("expected tier 'stm' after promotion, got %q", tier)
	}
}

func TestReflectPinnedProtection(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Pinned memory that is old and unaccessed — should be protected
	s.db.Exec(`INSERT INTO memories (id, ns, key, content, kind, version, created_at, priority, access_count, importance, tier, est_tokens, pinned)
		VALUES ('id1', 'agent:test', 'core-self', 'I am helpful', 'semantic', 1, ?, 'normal', 0, 0.5, 'stm', 20, 1)`,
		time.Now().Add(-720*time.Hour).UTC().Format(time.RFC3339)) // 30 days old, 0 accesses

	_, err := s.Reflect(ctx, ReflectParams{})
	if err != nil {
		t.Fatal(err)
	}

	// Should NOT be decayed, demoted, or deleted
	var tier string
	var importance float64
	var deletedAt *string
	s.db.QueryRow(`SELECT tier, importance, deleted_at FROM memories WHERE id = 'id1'`).Scan(&tier, &importance, &deletedAt)
	if tier != "stm" {
		t.Errorf("pinned memory tier changed to %q — should have been protected", tier)
	}
	if importance != 0.5 {
		t.Errorf("pinned memory importance changed to %f — should have been protected", importance)
	}
	if deletedAt != nil {
		t.Error("pinned memory was deleted — should have been protected")
	}
}

func TestReflectDemoteStaleLTM(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// LTM memory older than 7 days with < 2 accesses → should demote to dormant
	s.db.Exec(`INSERT INTO memories (id, ns, key, content, kind, version, created_at, priority, access_count, importance, tier, est_tokens)
		VALUES ('l1', 'test', 'stale-ltm', 'old fact', 'semantic', 1, ?, 'normal', 1, 0.6, 'ltm', 20)`,
		time.Now().Add(-200*time.Hour).UTC().Format(time.RFC3339)) // ~8 days

	result, err := s.Reflect(ctx, ReflectParams{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Demoted < 1 {
		t.Errorf("expected stale LTM to be demoted, got demoted=%d", result.Demoted)
	}

	var tier string
	s.db.QueryRow(`SELECT tier FROM memories WHERE id = 'l1'`).Scan(&tier)
	if tier != "dormant" {
		t.Errorf("expected tier 'dormant' after demotion, got %q", tier)
	}
}

func TestReflectPruneLowUtility(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Memory accessed 10 times but only useful once (utility ratio 0.1 < 0.2) → should be deleted
	s.db.Exec(`INSERT INTO memories (id, ns, key, content, kind, version, created_at, priority, access_count, importance, utility_count, tier, est_tokens)
		VALUES ('u1', 'test', 'low-util', 'unhelpful memory', 'semantic', 1, ?, 'normal', 10, 0.5, 1, 'stm', 20)`,
		time.Now().Add(-48*time.Hour).UTC().Format(time.RFC3339))

	result, err := s.Reflect(ctx, ReflectParams{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted < 1 {
		t.Errorf("expected low-utility memory to be deleted, got deleted=%d", result.Deleted)
	}
}

func TestReflectNamespaceScoped(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Two old STM memories in different namespaces
	s.db.Exec(`INSERT INTO memories (id, ns, key, content, kind, version, created_at, priority, access_count, importance, tier, est_tokens)
		VALUES ('m1', 'proj-a', 'old-a', 'content a', 'semantic', 1, ?, 'normal', 1, 0.5, 'stm', 20)`,
		time.Now().Add(-96*time.Hour).UTC().Format(time.RFC3339))
	s.db.Exec(`INSERT INTO memories (id, ns, key, content, kind, version, created_at, priority, access_count, importance, tier, est_tokens)
		VALUES ('m2', 'proj-b', 'old-b', 'content b', 'semantic', 1, ?, 'normal', 1, 0.5, 'stm', 20)`,
		time.Now().Add(-96*time.Hour).UTC().Format(time.RFC3339))

	// Reflect only proj-a namespace
	result, err := s.Reflect(ctx, ReflectParams{NS: "proj-a"})
	if err != nil {
		t.Fatal(err)
	}
	if result.MemoriesEvaluated != 1 {
		t.Errorf("expected 1 memory evaluated (scoped to proj-a), got %d", result.MemoriesEvaluated)
	}

	// proj-b should be untouched
	var importance float64
	s.db.QueryRow(`SELECT importance FROM memories WHERE id = 'm2'`).Scan(&importance)
	if importance != 0.5 {
		t.Errorf("proj-b memory importance changed to %f — should be untouched", importance)
	}
}

func TestReflectFirstMatchWins(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Identity memory that also matches decay conditions — PIN should win (priority 1 > 10)
	s.db.Exec(`INSERT INTO memories (id, ns, key, content, kind, version, created_at, priority, access_count, importance, tier, est_tokens)
		VALUES ('id1', 'identity', 'pinned', 'core truth', 'semantic', 1, ?, 'normal', 0, 0.5, 'identity', 20)`,
		time.Now().Add(-200*time.Hour).UTC().Format(time.RFC3339))

	result, err := s.Reflect(ctx, ReflectParams{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}

	// PIN is a no-op, so no actions should count for this memory
	if result.Decayed > 0 || result.Deleted > 0 || result.Demoted > 0 {
		t.Errorf("identity memory should be PINned: decayed=%d deleted=%d demoted=%d",
			result.Decayed, result.Deleted, result.Demoted)
	}
}

func TestRuleMatchesKindCondition(t *testing.T) {
	rule := ReflectRule{Cond: RuleCond{Kind: "procedural"}}

	if !ruleMatches(rule, model.Memory{Kind: "procedural"}, 0, 0) {
		t.Error("expected kind=procedural to match")
	}
	if ruleMatches(rule, model.Memory{Kind: "semantic"}, 0, 0) {
		t.Error("expected kind=semantic to NOT match procedural rule")
	}
}

func TestRuleMatchesImportanceLTCondition(t *testing.T) {
	rule := ReflectRule{Cond: RuleCond{ImportanceLT: 0.3}}

	if !ruleMatches(rule, model.Memory{Importance: 0.1}, 0, 0) {
		t.Error("expected importance 0.1 < 0.3 to match")
	}
	if ruleMatches(rule, model.Memory{Importance: 0.5}, 0, 0) {
		t.Error("expected importance 0.5 >= 0.3 to NOT match")
	}
}

func TestRuleInvalidAction(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.RuleSet(ctx, ReflectRule{
		Name:   "bad-action",
		Action: RuleAction{Op: "INVALID"},
	})
	if err == nil {
		t.Error("expected error for invalid action op")
	}
}

func TestBuiltinRulesSeeded(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rules, err := s.RuleList(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) < 6 {
		t.Errorf("expected at least 6 built-in rules, got %d", len(rules))
	}

	// Check specific built-in rule exists
	found := false
	for _, r := range rules {
		if r.ID == "sys-promote-to-ltm" {
			found = true
			if r.Action.Op != "PROMOTE" {
				t.Errorf("expected PROMOTE op, got %q", r.Action.Op)
			}
		}
	}
	if !found {
		t.Error("sys-promote-to-ltm not found in rules")
	}
}

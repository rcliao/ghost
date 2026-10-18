package store

import (
	"context"
	"path/filepath"
	"testing"
)

// TestReconcileBuiltinRules reproduces the 2026-10-18 production finding: two
// live stores were still running their March rule rows because seeding is
// INSERT OR IGNORE. One store demoted every ltm memory 7 days after creation
// (age>168h, no access condition); the other promoted on access>3. Reopening
// the store must bring seeder-owned rows back to the current builtins, and
// must leave user-edited rules alone.
func TestReconcileBuiltinRules(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}

	// Stale shapes copied from the live stores.
	stale := []string{
		`UPDATE reflect_rules SET cond_age_gt_hours = 168, cond_unaccessed_gt_hours = NULL, cond_access_lt = NULL
			WHERE id = 'sys-demote-stale-ltm'`,
		`UPDATE reflect_rules SET cond_access_gt = 3, cond_age_gt_hours = 24, name = 'Promote accessed STM to LTM'
			WHERE id = 'sys-promote-to-ltm'`,
		`UPDATE reflect_rules SET cond_age_gt_hours = 72, cond_access_lt = 3 WHERE id = 'sys-decay-unaccessed'`,
	}
	for _, q := range stale {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	// A hand-tuned builtin: RuleSet stamps it created_by='user'.
	tuned := ReflectRule{
		ID: "sys-archive-old-episodic", Name: "Archive old episodic after 60 days", Priority: 45,
		Cond:   RuleCond{Tier: "stm", Kind: "episodic", AgeGTHours: 1440, AccessLT: 5},
		Action: RuleAction{Op: "ARCHIVE"},
	}
	if _, err := s.RuleSet(ctx, tuned); err != nil {
		t.Fatal(err)
	}
	// A non-builtin rule written by an agent loop must also be untouched.
	if _, err := s.RuleSet(ctx, ReflectRule{
		ID: "sys-archive-auto-summary", Name: "Archive stale auto summaries", Priority: 60, CreatedBy: "evolve-cycle-21",
		Cond:   RuleCond{Tier: "dormant", AgeGTHours: 720, TagIncludes: "auto-generated"},
		Action: RuleAction{Op: "ARCHIVE"},
	}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	want := map[string]RuleCond{}
	for _, r := range builtinRules {
		want[r.ID] = r.Cond
	}
	for _, id := range []string{"sys-demote-stale-ltm", "sys-promote-to-ltm", "sys-decay-unaccessed"} {
		got, err := s.RuleGet(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Cond != want[id] {
			t.Errorf("%s: cond = %+v, want builtin %+v", id, got.Cond, want[id])
		}
		if got.CreatedBy != "system" {
			t.Errorf("%s: created_by = %q, want system", id, got.CreatedBy)
		}
	}

	got, err := s.RuleGet(ctx, "sys-archive-old-episodic")
	if err != nil {
		t.Fatal(err)
	}
	if got.Cond.AgeGTHours != 1440 || got.Name != tuned.Name {
		t.Errorf("user-tuned rule was overwritten: %+v", got)
	}
	got, err = s.RuleGet(ctx, "sys-archive-auto-summary")
	if err != nil {
		t.Fatal(err)
	}
	if got.Cond.TagIncludes != "auto-generated" || got.CreatedBy != "evolve-cycle-21" {
		t.Errorf("agent-created rule was changed: %+v", got)
	}
}

// TestReconciledDemoteSparesUsedLTM checks the behaviour the stale umbreon row
// broke: an ltm memory created long ago but accessed recently must not be
// demoted, while one untouched for over a week still is.
func TestReconciledDemoteSparesUsedLTM(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if _, err := s.db.Exec(`UPDATE reflect_rules SET cond_age_gt_hours = 168, cond_unaccessed_gt_hours = NULL
		WHERE id = 'sys-demote-stale-ltm'`); err != nil {
		t.Fatal(err)
	}
	s.reconcileBuiltinRules()

	used, err := s.Put(ctx, PutParams{NS: "agent:t", Key: "used-fact", Content: "soy milk ratio is 1:12", Tier: "ltm"})
	if err != nil {
		t.Fatal(err)
	}
	idle, err := s.Put(ctx, PutParams{NS: "agent:t", Key: "idle-fact", Content: "old parking spot was level 3", Tier: "ltm"})
	if err != nil {
		t.Fatal(err)
	}
	// Both created ten days ago; only one accessed an hour ago.
	if _, err := s.db.Exec(`UPDATE memories SET created_at = strftime('%Y-%m-%dT%H:%M:%SZ','now','-10 days'), last_accessed_at = strftime('%Y-%m-%dT%H:%M:%SZ','now','-1 hours'), access_count = 10 WHERE id = ?`, used.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE memories SET created_at = strftime('%Y-%m-%dT%H:%M:%SZ','now','-10 days'), last_accessed_at = strftime('%Y-%m-%dT%H:%M:%SZ','now','-9 days'), access_count = 1 WHERE id = ?`, idle.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Reflect(ctx, ReflectParams{NS: "agent:t"}); err != nil {
		t.Fatal(err)
	}
	for id, wantTier := range map[string]string{used.ID: "ltm", idle.ID: "dormant"} {
		var tier string
		if err := s.db.QueryRow(`SELECT tier FROM memories WHERE id = ?`, id).Scan(&tier); err != nil {
			t.Fatal(err)
		}
		if tier != wantTier {
			t.Errorf("memory %s: tier = %s, want %s", id, tier, wantTier)
		}
	}
}

// TestDemoteSparesUsedButQuietLTM pins the demote default chosen on
// 2026-10-18: an ltm memory that earned use but has been quiet for a week
// stays ltm. The bare unaccessed>168h default would demote it (and, on the
// pikamini store, 4,039 others in one pass) into the invisible dormant tier.
func TestDemoteSparesUsedButQuietLTM(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	used, err := s.Put(ctx, PutParams{NS: "agent:t", Key: "quiet-fact", Content: "family home is in the north end", Tier: "ltm"})
	if err != nil {
		t.Fatal(err)
	}
	unused, err := s.Put(ctx, PutParams{NS: "agent:t", Key: "never-used", Content: "one-off parking note", Tier: "ltm"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE memories SET created_at = strftime('%Y-%m-%dT%H:%M:%SZ','now','-30 days'),
		last_accessed_at = strftime('%Y-%m-%dT%H:%M:%SZ','now','-9 days'), access_count = 5 WHERE id = ?`, used.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE memories SET created_at = strftime('%Y-%m-%dT%H:%M:%SZ','now','-30 days'),
		last_accessed_at = NULL, access_count = 0 WHERE id = ?`, unused.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reflect(ctx, ReflectParams{NS: "agent:t"}); err != nil {
		t.Fatal(err)
	}
	for id, wantTier := range map[string]string{used.ID: "ltm", unused.ID: "dormant"} {
		var tier string
		if err := s.db.QueryRow(`SELECT tier FROM memories WHERE id = ?`, id).Scan(&tier); err != nil {
			t.Fatal(err)
		}
		if tier != wantTier {
			t.Errorf("memory %s: tier = %s, want %s", id, tier, wantTier)
		}
	}
}

package store

import (
	"context"
	"testing"
)

func easeOf(t *testing.T, s *SQLiteStore, ns, key string) float64 {
	t.Helper()
	var ease float64
	err := s.db.QueryRow(
		`SELECT ease FROM memories WHERE ns=? AND key=? AND deleted_at IS NULL ORDER BY version DESC LIMIT 1`,
		ns, key).Scan(&ease)
	if err != nil {
		t.Fatalf("read ease %s: %v", key, err)
	}
	return ease
}

func tierOf(t *testing.T, s *SQLiteStore, ns, key string) string {
	t.Helper()
	var tier string
	err := s.db.QueryRow(
		`SELECT tier FROM memories WHERE ns=? AND key=? AND deleted_at IS NULL ORDER BY version DESC LIMIT 1`,
		ns, key).Scan(&tier)
	if err != nil {
		t.Fatalf("read tier %s: %v", key, err)
	}
	return tier
}

// TestReflectEaseShieldsUsefulFromDemotion is the core D2 test: two equally-stale
// LTM memories, one proven useful and one not. After reflect, the useless one is
// demoted to dormant on the 7-day rule; the useful one's ease raises its
// effective threshold so it stays LTM (searchable).
func TestReflectEaseShieldsUsefulFromDemotion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Both LTM, idle 300h (> 168h stale threshold), differ only in utility.
	corpus := []SeedMemory{
		{NS: "t", Key: "useful", Kind: "semantic", Tier: "ltm", Importance: 0.6,
			BackdateHours: 300, AccessCount: 15, UtilityCount: 12}, // ease 3.4 → threshold 571h
		{NS: "t", Key: "useless", Kind: "semantic", Tier: "ltm", Importance: 0.6,
			BackdateHours: 300, AccessCount: 15, UtilityCount: 0}, // ease 1.0 → threshold 168h
	}
	if _, err := SeedStore(ctx, s, corpus); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// This tests the ease-scaling mechanism, not the shipped default (which
	// also requires access<2), so pin a bare unaccessed>168h demote rule.
	if _, err := s.RuleSet(ctx, ReflectRule{
		ID: "sys-demote-stale-ltm", Name: "Demote LTM not accessed in 7 days", Priority: 50,
		Cond:   RuleCond{Tier: "ltm", UnaccessedGTHours: 168},
		Action: RuleAction{Op: "DEMOTE", Params: map[string]any{"to_tier": "dormant"}},
	}); err != nil {
		t.Fatalf("rule set: %v", err)
	}

	res, err := s.Reflect(ctx, ReflectParams{NS: "t"})
	if err != nil {
		t.Fatalf("reflect: %v", err)
	}

	if got := tierOf(t, s, "t", "useless"); got != "dormant" {
		t.Errorf("useless memory should be demoted to dormant, got %q (demoted=%d)", got, res.Demoted)
	}
	if got := tierOf(t, s, "t", "useful"); got != "ltm" {
		t.Errorf("proven-useful memory should stay ltm (ease shield), got %q", got)
	}

	// Ease persisted from utility.
	if e := easeOf(t, s, "t", "useful"); e < 3.3 || e > 3.5 {
		t.Errorf("useful ease should be ~3.4, got %.2f", e)
	}
	if e := easeOf(t, s, "t", "useless"); e != 1.0 {
		t.Errorf("useless ease should be 1.0, got %.2f", e)
	}
}

// TestReflectEaseCap verifies ease saturates at easeMax.
func TestReflectEaseCap(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := SeedStore(ctx, s, []SeedMemory{
		{NS: "t", Key: "sticky", Kind: "semantic", Tier: "ltm", Importance: 0.6, AccessCount: 100, UtilityCount: 100},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reflect(ctx, ReflectParams{NS: "t"}); err != nil {
		t.Fatal(err)
	}
	if e := easeOf(t, s, "t", "sticky"); e != easeMax {
		t.Errorf("ease should cap at %.1f, got %.2f", easeMax, e)
	}
}

// TestReflectEaseDryRunNoWrite confirms dry-run does not persist ease.
func TestReflectEaseDryRunNoWrite(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := SeedStore(ctx, s, []SeedMemory{
		{NS: "t", Key: "m", Kind: "semantic", Tier: "ltm", Importance: 0.6, AccessCount: 10, UtilityCount: 10},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := s.Reflect(ctx, ReflectParams{NS: "t", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.EaseUpdated != 0 {
		t.Errorf("dry-run should not update ease, reported %d", res.EaseUpdated)
	}
	if e := easeOf(t, s, "t", "m"); e != 1.0 {
		t.Errorf("dry-run must leave ease at default 1.0, got %.2f", e)
	}
}

// TestEaseFromUtility unit-checks the derivation.
func TestEaseFromUtility(t *testing.T) {
	cases := map[int]float64{0: 1.0, 1: 1.2, 5: 2.0, 15: 4.0, 100: easeMax}
	for util, want := range cases {
		if got := easeFromUtility(util); got != want {
			t.Errorf("easeFromUtility(%d)=%.2f want %.2f", util, got, want)
		}
	}
}

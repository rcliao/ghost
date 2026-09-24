package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rcliao/ghost/internal/model"
)

// Matches is exercised on the synthetic fixture: the two built-in rules must
// fire on the causal shapes and stay silent on the restatements.
func TestBuiltinPairRulesOnFixture(t *testing.T) {
	fx := loadPairFixture(t)
	byName := map[string]PairRule{}
	for _, r := range builtinPairRules {
		if err := r.Validate(); err != nil {
			t.Fatalf("builtin rule invalid: %v", err)
		}
		byName[r.ID] = r
	}
	shared := byName["sys-pair-causal-shared-entities"]
	cue := byName["sys-pair-causal-cue"]

	want := map[string]struct{ shared, cue bool }{
		"rule-from-incident":            {true, true}, // rina+solaris both count without a frequency table; plus a correction cue
		"decision-then-schedule":        {false, false},
		"restatement":                   {false, false}, // same day, high overlap
		"date-words-are-not-entities":   {false, false},
		"trip-booked-then-tool-built":   {false, false},
		"same-session-summarised-twice": {false, false},
	}
	for _, c := range fx.Cases {
		f := NewPairFeatures(c.Older.memory(t), c.Newer.memory(t), nil)
		w, ok := want[c.Name]
		if !ok {
			t.Fatalf("fixture case %q has no expectation in this test", c.Name)
		}
		if got := shared.Cond.Matches(f); got != w.shared {
			t.Errorf("%s: shared-entities rule fired=%v want %v (features %+v)", c.Name, got, w.shared, f)
		}
		if got := cue.Cond.Matches(f); got != w.cue {
			t.Errorf("%s: cue rule fired=%v want %v (features %+v)", c.Name, got, w.cue, f)
		}
	}
}

func TestPairRuleValidate(t *testing.T) {
	base := PairRule{ID: "r", Name: "n", Cond: PairCond{SameUser: -1, KeyPrefixMatch: -1}, Action: PairAction{Op: PairOpPropose, Rel: "caused_by"}}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := base
	bad.Action.Op = PairOpAssert
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "min_precision") {
		t.Errorf("assert without measured precision must be rejected, got %v", err)
	}
	bad.MinPrecision = 0.9
	if err := bad.Validate(); err != nil {
		t.Errorf("assert with precision should validate: %v", err)
	}
	for _, rel := range []string{"relates_to", "merged_into", "banana"} {
		r := base
		r.Action.Rel = rel
		if err := r.Validate(); err == nil {
			t.Errorf("relation %q must be rejected", rel)
		}
	}
	r := base
	r.Cond.Cue = "sometimes"
	if err := r.Validate(); err == nil {
		t.Error("unknown cue must be rejected")
	}
}

func TestPairCondTriState(t *testing.T) {
	f := PairFeatures{SameUser: true, KeyPrefixMatch: false, NewerCue: "causal", DaysApart: 10}
	if !(PairCond{SameUser: -1, KeyPrefixMatch: -1}).Matches(f) {
		t.Error("ignore/ignore must match")
	}
	if (PairCond{SameUser: 0, KeyPrefixMatch: -1}).Matches(f) {
		t.Error("same_user=0 must reject a same-user pair")
	}
	if !(PairCond{SameUser: 1, KeyPrefixMatch: 0, Cue: "causal"}).Matches(f) {
		t.Error("exact tri-state and cue must match")
	}
	if (PairCond{SameUser: -1, KeyPrefixMatch: -1, Cue: "none"}).Matches(f) {
		t.Error("cue=none must reject a cued pair")
	}
}

// seedPairCorpus writes memories at controlled clock times so DaysApart is real.
func seedPairCorpus(t *testing.T, s *SQLiteStore, ns string) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	put := func(day int, key, content string) {
		s.SetClock(func() time.Time { return base.AddDate(0, 0, day) })
		if _, err := s.Put(ctx, PutParams{NS: ns, Key: key, Content: content, Tier: "stm"}); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}
	put(0, "health-shoulder-cause-corrected", "Correction from Rina: the shoulder ache was NOT from gardening, it was from installing the Solaris patio lights overhead. Diagnosis changed to posture fatigue.")
	put(9, "behavioral-reread-before-citing", "RULE: re-read the record before citing a date. Failed today: told Rina the ache came from gardening, but she had corrected that cause (Solaris lights). Root cause: stale summary.")
	put(1, "rina-kefir-stopped", "Rina decided to stop drinking kefir. Probiotics covered by the Floravita capsule; calcium by tofu and greens.")
	put(14, "rina-supplement-schedule", "Rina's supplement timing: enzyme before lunch, Floravita after any meal. Floravita replaces kefir's probiotic role now that she avoids dairy.")
	put(2, "learning-eavesdrop", "Do not proactively reference group chat information that Rina told another agent. Even though visible, surfacing it feels like eavesdropping.")
	put(2, "learning-no-eavesdrop", "Do not proactively reference group chat info that Rina said to another agent. Even though visible, surfacing it feels like eavesdropping.")
	put(3, "briefing-2026-07-04", "Briefing: Floravita launched a new capsule; Solaris lights recalled a batch. Rina unaffected.")
	s.SetClock(func() time.Time { return base.AddDate(0, 0, 30) })
}

func TestRunPairRulesWritesEventsAndSkipsRepeats(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedPairCorpus(t, s, "agent:test")

	dry, err := s.RunPairRules(ctx, RunPairRulesParams{NS: "agent:test", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(dry.Firings) == 0 {
		t.Fatalf("expected the cue rule to fire on the correction pair; scanned=%d pairs=%d", dry.MemoriesScanned, dry.PairsEvaluated)
	}
	if dry.MemoriesScanned != 6 {
		t.Errorf("briefing- rows must be skipped: scanned %d", dry.MemoriesScanned)
	}
	events, _ := s.ListRuleEvents(ctx, ListRuleEventsParams{NS: "agent:test"})
	if len(events) != 0 {
		t.Errorf("dry run must write no events, got %d", len(events))
	}
	var sawCorrection bool
	for _, f := range dry.Firings {
		if f.Features.OlderKey == "health-shoulder-cause-corrected" && f.Features.NewerKey == "behavioral-reread-before-citing" {
			sawCorrection = true
			if f.Op != PairOpPropose || f.Rel != "caused_by" || f.EdgeWritten {
				t.Errorf("unexpected firing %+v", f)
			}
		}
		if f.Features.NewerKey == "learning-no-eavesdrop" {
			t.Errorf("restatement pair must not be proposed: %+v", f.Features)
		}
	}
	if !sawCorrection {
		t.Errorf("correction→rule pair not proposed; firings: %+v", dry.Firings)
	}

	real, err := s.RunPairRules(ctx, RunPairRulesParams{NS: "agent:test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(real.Firings) != len(dry.Firings) {
		t.Errorf("real run fired %d, dry run %d", len(real.Firings), len(dry.Firings))
	}
	events, err = s.ListRuleEvents(ctx, ListRuleEventsParams{NS: "agent:test", Unreviewed: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != len(real.Firings) {
		t.Errorf("events %d != firings %d", len(events), len(real.Firings))
	}
	for _, e := range events {
		if e.Source != ruleEventSourcePair || e.FromKey == "" || e.ToKey == "" || !strings.Contains(e.Features, `"days_apart"`) {
			t.Errorf("event incomplete: %+v", e)
		}
	}

	again, err := s.RunPairRules(ctx, RunPairRulesParams{NS: "agent:test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Firings) != 0 || again.Skipped == 0 {
		t.Errorf("second run must skip evented pairs: firings=%d skipped=%d", len(again.Firings), again.Skipped)
	}
}

func TestAssertRuleWritesEdgeAndDisagreeRemovesIt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedPairCorpus(t, s, "agent:test")

	assert := PairRule{
		ID: "test-assert-causal", Name: "assert caused_by on cue pairs", Enabled: true, Priority: 99,
		Cond:         PairCond{MinSharedEntities: 1, MaxEntityDF: 40, MinDaysApart: 7, MaxJaccard: 0.25, Cue: "any-cue", SameUser: -1, KeyPrefixMatch: -1},
		Action:       PairAction{Op: PairOpAssert, Rel: "caused_by"},
		MinPrecision: 0.85,
	}
	if err := s.PutPairRule(ctx, assert); err != nil {
		t.Fatal(err)
	}
	res, err := s.RunPairRules(ctx, RunPairRulesParams{NS: "agent:test", RuleIDs: []string{assert.ID}})
	if err != nil {
		t.Fatal(err)
	}
	var ev PairFiring
	for _, f := range res.Firings {
		if f.Features.NewerKey == "behavioral-reread-before-citing" {
			ev = f
		}
	}
	if ev.EventID == "" || !ev.EdgeWritten {
		t.Fatalf("assert rule should have written an edge: %+v", res.Firings)
	}
	edges, _ := s.GetEdges(ctx, ev.NewerID)
	if !hasEdge(edges, ev.NewerID, ev.OlderID, "caused_by") {
		t.Fatalf("caused_by edge newer→older missing after assert: %+v", edges)
	}

	if err := s.ReviewRuleEvent(ctx, ev.EventID, "maybe", "tester"); err == nil {
		t.Error("bad verdict must be rejected")
	}
	if err := s.ReviewRuleEvent(ctx, ev.EventID, VerdictDisagree, "tester"); err != nil {
		t.Fatal(err)
	}
	edges, _ = s.GetEdges(ctx, ev.NewerID)
	if hasEdge(edges, ev.NewerID, ev.OlderID, "caused_by") {
		t.Errorf("disagree must remove the asserted edge: %+v", edges)
	}
	events, _ := s.ListRuleEvents(ctx, ListRuleEventsParams{RuleID: assert.ID})
	var reviewed *RuleEvent
	for i := range events {
		if events[i].ID == ev.EventID {
			reviewed = &events[i]
		}
	}
	if reviewed == nil || !reviewed.Reviewed || reviewed.Verdict != VerdictDisagree || reviewed.ReviewedBy != "tester" {
		t.Errorf("review not recorded: %+v", reviewed)
	}
	for _, e := range mustList(t, s, ListRuleEventsParams{RuleID: assert.ID, Unreviewed: true}) {
		if e.ID == ev.EventID {
			t.Errorf("reviewed event still listed as unreviewed")
		}
	}
}

// Agreeing with a PROPOSAL is what creates the edge; disagreeing later removes it.
func TestAgreeOnProposalWritesEdge(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedPairCorpus(t, s, "agent:test")
	res, err := s.RunPairRules(ctx, RunPairRulesParams{NS: "agent:test"})
	if err != nil {
		t.Fatal(err)
	}
	var ev PairFiring
	for _, f := range res.Firings {
		if f.Features.OlderKey == "health-shoulder-cause-corrected" && f.Features.NewerKey == "behavioral-reread-before-citing" {
			ev = f
		}
	}
	if ev.EventID == "" || ev.EdgeWritten {
		t.Fatalf("expected an unwritten proposal for the correction pair: %+v", res.Firings)
	}
	if err := s.ReviewRuleEvent(ctx, ev.EventID, VerdictAgree, "tester"); err != nil {
		t.Fatal(err)
	}
	edges, _ := s.GetEdges(ctx, ev.NewerID)
	if !hasEdge(edges, ev.NewerID, ev.OlderID, "caused_by") {
		t.Fatalf("agree must write caused_by newer→older: %+v", edges)
	}
	events := mustList(t, s, ListRuleEventsParams{RuleID: ev.RuleID})
	for _, e := range events {
		if e.ID == ev.EventID && !e.EdgeWritten {
			t.Error("event must record edge_written after agree")
		}
	}
	if !s.reviewedEdgeSet(ctx)[ev.NewerID+"|"+ev.OlderID+"|caused_by"] {
		t.Error("reviewed edge set must contain the agreed edge, keyed as stored")
	}
	if err := s.ReviewRuleEvent(ctx, ev.EventID, VerdictDisagree, "tester"); err != nil {
		t.Fatal(err)
	}
	edges, _ = s.GetEdges(ctx, ev.NewerID)
	if hasEdge(edges, ev.NewerID, ev.OlderID, "caused_by") {
		t.Error("a later disagree must remove the edge agree wrote")
	}
}

// A re-put gives a memory a new id. Verdicts must follow it: the reserved
// handling and disagree must still find the live edge, a second run must fire
// nothing, and purging the old version must not cascade the trace away.
func TestRuleEventsFollowVersionChange(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedPairCorpus(t, s, "agent:test")
	res, err := s.RunPairRules(ctx, RunPairRulesParams{NS: "agent:test"})
	if err != nil {
		t.Fatal(err)
	}
	var ev PairFiring
	for _, f := range res.Firings {
		if f.Features.OlderKey == "health-shoulder-cause-corrected" && f.Features.NewerKey == "behavioral-reread-before-citing" {
			ev = f
		}
	}
	if ev.EventID == "" {
		t.Fatal("correction pair not proposed")
	}
	if err := s.ReviewRuleEvent(ctx, ev.EventID, VerdictAgree, "tester"); err != nil {
		t.Fatal(err)
	}
	// Re-put both sides with the same content: new versions, new ids.
	for _, key := range []string{"behavioral-reread-before-citing", "health-shoulder-cause-corrected"} {
		var m []model.Memory
		m, err = s.Get(ctx, GetParams{NS: "agent:test", Key: key})
		if err != nil || len(m) == 0 {
			t.Fatalf("get %s: %v", key, err)
		}
		if _, err := s.Put(ctx, PutParams{NS: "agent:test", Key: key, Content: m[0].Content + " (edited)", Tier: "stm"}); err != nil {
			t.Fatal(err)
		}
	}
	live, err := s.GetEdgesByNSKey(ctx, "agent:test", "behavioral-reread-before-citing")
	if err != nil {
		t.Fatal(err)
	}
	var edge *Edge
	for i := range live {
		if live[i].Rel == "caused_by" {
			edge = &live[i]
		}
	}
	if edge == nil {
		t.Fatalf("caused_by edge lost on re-put: %+v", live)
	}
	if !s.reviewedEdgeSet(ctx)[edge.FromID+"|"+edge.ToID+"|caused_by"] {
		t.Errorf("agree verdict no longer matches the live edge after re-put")
	}
	again, err := s.RunPairRules(ctx, RunPairRulesParams{NS: "agent:test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range again.Firings {
		if f.Features.NewerKey == "behavioral-reread-before-citing" && f.Features.OlderKey == "health-shoulder-cause-corrected" {
			t.Errorf("pair proposed again after re-put: %+v", f)
		}
	}
	if _, err := s.PurgeDeleted(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if evs := mustList(t, s, ListRuleEventsParams{RuleID: ev.RuleID}); len(evs) == 0 {
		t.Error("purging old versions cascaded the review trace away")
	}
	if err := s.ReviewRuleEvent(ctx, ev.EventID, VerdictDisagree, "tester"); err != nil {
		t.Fatal(err)
	}
	live, _ = s.GetEdgesByNSKey(ctx, "agent:test", "behavioral-reread-before-citing")
	for _, e := range live {
		if e.Rel == "caused_by" {
			t.Errorf("disagree after re-put did not remove the live edge")
		}
	}
}

// The MaxPairs window must advance past pairs already handled, or a queue can
// never refill after review.
func TestRunPairRulesWindowAdvances(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedPairCorpus(t, s, "agent:test")
	full, _ := s.RunPairRules(ctx, RunPairRulesParams{NS: "agent:test", DryRun: true})
	fired, evaluated := len(full.Firings), full.PairsEvaluated
	if fired < 2 {
		t.Skipf("corpus fires %d; need >= 2", fired)
	}
	// A window one wider than the non-matching pairs holds at least one
	// undecided match per run. Decided pairs leave the window, so each run
	// must fire at least once until every match is recorded.
	window := evaluated - fired + 1
	total := 0
	for i := 0; i < fired; i++ {
		r, err := s.RunPairRules(ctx, RunPairRulesParams{NS: "agent:test", MaxPairs: window})
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Firings) == 0 {
			t.Fatalf("run %d fired nothing: the window did not advance past decided pairs", i)
		}
		total += len(r.Firings)
	}
	if total != fired {
		t.Errorf("windowed runs fired %d in total, a full run fires %d", total, fired)
	}
}

func TestPairRuleCRUDAndSeeding(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rules, err := s.ListPairRules(ctx, "agent:x")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != len(builtinPairRules) {
		t.Fatalf("expected %d seeded rules, got %d", len(builtinPairRules), len(rules))
	}
	if rules[0].Priority < rules[len(rules)-1].Priority {
		t.Error("rules must list highest priority first")
	}
	r := PairRule{ID: "caller-1", NS: "agent:x", Name: "x", Enabled: true, Cond: PairCond{SameUser: -1, KeyPrefixMatch: -1}, Action: PairAction{Op: PairOpPropose, Rel: "refines"}}
	if err := s.PutPairRule(ctx, r); err != nil {
		t.Fatal(err)
	}
	other, _ := s.ListPairRules(ctx, "agent:y")
	if len(other) != len(builtinPairRules) {
		t.Errorf("namespaced rule leaked into another namespace")
	}
	mine, _ := s.ListPairRules(ctx, "agent:x")
	if len(mine) != len(builtinPairRules)+1 {
		t.Errorf("namespaced rule missing from its namespace")
	}
	if err := s.DeletePairRule(ctx, "caller-1"); err != nil {
		t.Fatal(err)
	}
	mine, _ = s.ListPairRules(ctx, "agent:x")
	if len(mine) != len(builtinPairRules) {
		t.Errorf("delete failed")
	}
}

func hasEdge(edges []Edge, from, to, rel string) bool {
	for _, e := range edges {
		if e.FromID == from && e.ToID == to && e.Rel == rel {
			return true
		}
	}
	return false
}

func mustList(t *testing.T, s *SQLiteStore, p ListRuleEventsParams) []RuleEvent {
	t.Helper()
	events, err := s.ListRuleEvents(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

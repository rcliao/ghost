package store

import (
	"context"
	"testing"
)

func TestProposeScoreOrdersCueAndSpecificityFirst(t *testing.T) {
	plain := PairFeatures{SharedEntities: []SharedEntity{{"x", 20}}, MinSharedDF: 20, DaysApart: 10}
	cued := plain
	cued.NewerCue = "causal"
	corrected := plain
	corrected.NewerCue = "correction"
	rare := plain
	rare.SharedEntities = []SharedEntity{{"x", 2}, {"y", 3}}
	rare.MinSharedDF = 2

	if !(corrected.ProposeScore() > cued.ProposeScore() && cued.ProposeScore() > plain.ProposeScore()) {
		t.Errorf("cue ordering wrong: correction=%.2f causal=%.2f none=%.2f", corrected.ProposeScore(), cued.ProposeScore(), plain.ProposeScore())
	}
	if !(rare.ProposeScore() > plain.ProposeScore()) {
		t.Errorf("more and rarer shared entities must score higher: %.2f vs %.2f", rare.ProposeScore(), plain.ProposeScore())
	}
	if plain.ProposeScore() != plain.ProposeScore() {
		t.Error("score must be deterministic")
	}
	sibling := rare
	sibling.KeyPrefixMatch = true
	overlapping := rare
	overlapping.Jaccard = 0.25
	if !(sibling.ProposeScore() < rare.ProposeScore() && overlapping.ProposeScore() < rare.ProposeScore()) {
		t.Errorf("sibling shapes must rank below causal shapes: sibling=%.2f overlap=%.2f rare=%.2f", sibling.ProposeScore(), overlapping.ProposeScore(), rare.ProposeScore())
	}
	if sibling.ProposeScore() <= 0 {
		t.Errorf("penalties push down, not out: %.2f", sibling.ProposeScore())
	}
}

func TestLooksLikeEntity(t *testing.T) {
	for text, want := range map[string]bool{
		"a/c": false, "ui": false, "r&t": false, "dt-9": false, "x1": false,
		"solaris": true, "port ellery": true, "d3k2": false, "kirkland signature": true, "瑞爾特": true,
	} {
		if got := looksLikeEntity(text); got != want {
			t.Errorf("looksLikeEntity(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestRunPairRulesRanksAndCapsProposals(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedPairCorpus(t, s, "agent:test")

	all, err := s.RunPairRules(ctx, RunPairRulesParams{NS: "agent:test", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Firings) < 2 {
		t.Skipf("corpus yields %d firings; need >= 2 to test ranking", len(all.Firings))
	}
	for i := 1; i < len(all.Firings); i++ {
		if all.Firings[i-1].Score < all.Firings[i].Score {
			t.Errorf("firings not ranked by score: %.2f before %.2f", all.Firings[i-1].Score, all.Firings[i].Score)
		}
	}
	if all.Firings[0].Features.NewerKey != "behavioral-reread-before-citing" {
		t.Errorf("the correction-cued pair should head the queue, got %s -> %s", all.Firings[0].Features.OlderKey, all.Firings[0].Features.NewerKey)
	}

	capped, err := s.RunPairRules(ctx, RunPairRulesParams{NS: "agent:test", MaxProposals: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(capped.Firings) != 1 || capped.Capped != len(all.Firings)-1 {
		t.Errorf("cap: firings=%d capped=%d (all=%d)", len(capped.Firings), capped.Capped, len(all.Firings))
	}
	if capped.Firings[0].Score != all.Firings[0].Score {
		t.Error("cap must keep the highest-scored proposal")
	}
	events, err := s.ListRuleEvents(ctx, ListRuleEventsParams{NS: "agent:test", Unreviewed: true, ByScore: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Score != capped.Firings[0].Score {
		t.Errorf("event should carry the score: %+v", events)
	}

	// Capped-out proposals were not evented, so a later run with a bigger cap
	// records them rather than losing them.
	rest, err := s.RunPairRules(ctx, RunPairRulesParams{NS: "agent:test", MaxProposals: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(rest.Firings) != len(all.Firings)-1 {
		t.Errorf("second run should record the remaining %d proposals, got %d", len(all.Firings)-1, len(rest.Firings))
	}
}

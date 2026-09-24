package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// A caused_by edge a reviewer AGREED with must survive production packing.
//
// Measured on the production snapshot (2026-09-23, pairproto --pullthrough):
// sixteen Jev-accepted caused_by edges pulled their cause through 0/16 times
// at the production budget, because caused_by is accompany-class — carried
// only when there is room — and the near-duplicate wall never leaves room.
// The August eval (#100) put the same ceiling at 2/10 relations surviving
// budget 250. Reserve class is the one lever measured to work, and reserve
// must stay scarce: an edge a person or agent has reviewed and agreed with
// is the principled reason to admit one.
//
// Three arms on the caused_by corpus case, same query, same budget:
//   control     — no edge:             neighbour must be ABSENT (else the case measures nothing)
//   unreviewed  — edge, no verdict:    absent at the tight budget today; reported, not asserted
//   reviewed    — edge + agree verdict: must be PRESENT — this is the assertion
//
// Red before the store consults the review trace; green after.

const reviewedEdgeTightBudget = 250

type reviewedArm string

const (
	armControl    reviewedArm = "control"
	armUnreviewed reviewedArm = "unreviewed"
	armReviewed   reviewedArm = "reviewed"
)

func reviewedCausedByCase(t *testing.T) graphCase {
	t.Helper()
	for _, c := range graphCorpus() {
		if c.rel == "caused_by" {
			return c
		}
	}
	t.Fatal("graph corpus has no caused_by case")
	return graphCase{}
}

// buildReviewedCase mirrors buildGraphCase (seed, later neighbour, ten
// near-duplicate fillers closer to the query than the neighbour) and adds the
// review trace for the reviewed arm through the real API: a propose event on
// the pair, then ReviewRuleEvent(agree).
func buildReviewedCase(t *testing.T, c graphCase, arm reviewedArm, budget int) *ContextResult {
	t.Helper()
	ctx := context.Background()
	s := newTestStore(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return base })

	seedMem, err := s.Put(ctx, PutParams{NS: "agent:graph", Key: "seed", Content: c.seedContent, Kind: "semantic", Tier: "ltm", Importance: 0.7})
	if err != nil {
		t.Fatal(err)
	}
	s.SetClock(func() time.Time { return base.AddDate(0, 0, 10) })
	nbMem, err := s.Put(ctx, PutParams{NS: "agent:graph", Key: "neighbour", Content: c.neighbourContent, Kind: "semantic", Tier: "ltm", Importance: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		_, _ = s.Put(ctx, PutParams{NS: "agent:graph", Key: fmt.Sprintf("filler-%d", i),
			Content: fmt.Sprintf("%s (note %d, no change reported this week).", c.seedContent, i),
			Kind:    "semantic", Tier: "ltm", Importance: 0.45})
	}

	if arm != armControl {
		// Natural direction for caused_by: the effect (seed, what the query
		// finds) --caused_by--> its cause (neighbour).
		if _, err := s.CreateEdge(ctx, EdgeParams{FromNS: "agent:graph", FromKey: "seed", ToNS: "agent:graph", ToKey: "neighbour", Rel: "caused_by"}); err != nil {
			t.Fatal(err)
		}
	}
	if arm == armReviewed {
		f := NewPairFeatures(nbMem, seedMem, nil)
		id, err := s.insertRuleEvent(ctx, ruleEventSourcePair, PairFiring{
			RuleID: "sys-pair-causal-cue", OlderID: nbMem.ID, NewerID: seedMem.ID, Features: f,
			Op: PairOpPropose, Rel: "caused_by", Score: f.ProposeScore(), EdgeWritten: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.ReviewRuleEvent(ctx, id, VerdictAgree, "eval"); err != nil {
			t.Fatal(err)
		}
	}
	s.SetClock(func() time.Time { return base.AddDate(0, 0, 20) })

	// Production shape: the MinScore floor is on.
	res, err := s.Context(ctx, ContextParams{NS: "agent:graph", Query: c.query, Budget: budget, MinScore: 0.3})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestEvalReviewedEdgeSurvivesTightBudget(t *testing.T) {
	c := reviewedCausedByCase(t)
	for _, budget := range []int{reviewedEdgeTightBudget, 700} {
		control := buildReviewedCase(t, c, armControl, budget)
		unrev := buildReviewedCase(t, c, armUnreviewed, budget)
		rev := buildReviewedCase(t, c, armReviewed, budget)
		hc, hu, hr := hasMarker(control, c.neighbourMarker), hasMarker(unrev, c.neighbourMarker), hasMarker(rev, c.neighbourMarker)
		t.Logf("budget=%-4d neighbour present: control=%-5v unreviewed=%-5v reviewed=%-5v  reserved_stage=%d  keys(reviewed)=%s",
			budget, hc, hu, hr, rev.Stages["packed_reserved"], renderKeys(rev))
		if hc {
			t.Fatalf("budget %d: control arm reaches the neighbour by similarity; the case cannot measure an edge", budget)
		}
		if budget == reviewedEdgeTightBudget {
			if !hr {
				t.Errorf("budget %d: a caused_by edge with an AGREE verdict did not carry its cause into context (unreviewed=%v)", budget, hu)
			}
			if hu {
				t.Errorf("budget %d: the UNREVIEWED edge carried its cause; the treatment is not what made the difference", budget)
			}
		}
	}
}

// The reviewed treatment must be narrow: a DISAGREE verdict, or an edge nobody
// reviewed, gets no reservation. Otherwise every auto-linked edge would claim
// budget and the class would stop being scarce (TestNonReserveClassIsNotPromoted).
func TestEvalReviewedEdgeReservationIsScarce(t *testing.T) {
	c := reviewedCausedByCase(t)
	// packed_reserved never sees edge arrivals (viaEdge is counted first), so
	// observe the flag where it is set: edge_marked_reserved.
	unrev := buildReviewedCase(t, c, armUnreviewed, reviewedEdgeTightBudget)
	if unrev.Stages["edge_marked_reserved"] != 0 {
		t.Errorf("an unreviewed caused_by edge must not be marked reserved: stage=%d", unrev.Stages["edge_marked_reserved"])
	}
	rev := buildReviewedCase(t, c, armReviewed, reviewedEdgeTightBudget)
	if rev.Stages["edge_marked_reserved"] == 0 {
		t.Errorf("the reviewed edge must be marked reserved; stages=%v", rev.Stages)
	}
}

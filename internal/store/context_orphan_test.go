package store

import (
	"context"
	"testing"
)

// Orphaned edge passengers: edge expansion runs before the MinScore floor, and
// typed-edge arrivals are floor-exempt, so before the fix a seed dropped by the
// floor left its neighbours behind. Measured on an eval fixture: the deploy
// query returned ONLY "run make migrate first" (via=edge, score 0.03), reached
// through a refines edge from the dropped staging-deploy memory.

const orphanQuery = "I need to ship this service to staging. Give me the exact steps and commands, in order."

// The seed scores ~0.14 on this query (weak term overlap, so the
// relevance-confident rescue does not apply); the neighbour ~0.03.
func orphanStore(t *testing.T, edges [][3]string, extra ...householdMemory) *SQLiteStore {
	t.Helper()
	mems := []householdMemory{
		{key: "staging", content: "Staging deploy for the service: run make deploy ENV=staging from the repo root."},
		{key: "migrate", content: "Run make migrate against the database first, every time."},
		{key: "backup", content: "Snapshot the primary with the nightly backup script beforehand."},
		{key: "filler", content: "The office coffee machine descaling schedule is on the fridge."},
	}
	return stressStore(t, append(mems, extra...), edges)
}

func orphanContext(t *testing.T, s *SQLiteStore, minScore float64) (*ContextResult, map[string]string) {
	t.Helper()
	res, err := s.Context(context.Background(), ContextParams{
		NS: "agent:home", Query: orphanQuery, Budget: 2000, MinScore: minScore,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res, viaByKey(t, res)
}

func TestContextOrphanEdgePassengerDropped(t *testing.T) {
	s := orphanStore(t, [][3]string{{"staging", "migrate", "refines"}})
	res, via := orphanContext(t, s, 0.55)
	if _, ok := via["staging"]; ok {
		t.Fatalf("fixture broken: seed survived the 0.55 floor (%v)", via)
	}
	if _, ok := via["migrate"]; ok {
		t.Errorf("refines neighbour returned without the seed that pulled it in: %v", via)
	}
	if got := res.Stages["orphan_edge_dropped"]; got != 1 {
		t.Errorf("stages orphan_edge_dropped = %d, want 1 (%v)", got, res.Stages)
	}
}

func TestContextEdgePassengerKeptWhenSeedSurvives(t *testing.T) {
	s := orphanStore(t, [][3]string{{"staging", "migrate", "refines"}})
	res, via := orphanContext(t, s, 0.1)
	if via["staging"] != ViaSearch {
		t.Fatalf("fixture broken: seed should pass a 0.1 floor as a search hit (%v)", via)
	}
	if via["migrate"] != ViaEdge {
		t.Errorf("refines neighbour of a surviving seed via = %q, want edge (%v)", via["migrate"], via)
	}
	if _, ok := res.Stages["orphan_edge_dropped"]; ok {
		t.Errorf("nothing should be orphaned when the seed survives: %v", res.Stages)
	}
}

// Reserve-class arrivals keep their guarantee while their seed survives, and go
// down with it when it does not: a prerequisite of a memory that is not shown
// qualifies nothing in context.
func TestContextOrphanReservedFollowsSeed(t *testing.T) {
	s := orphanStore(t, [][3]string{{"staging", "migrate", "depends_on"}})
	if _, via := orphanContext(t, s, 0.1); via["migrate"] != ViaReserved {
		t.Errorf("depends_on neighbour of a surviving seed via = %q, want reserved (%v)", via["migrate"], via)
	}
	if _, via := orphanContext(t, s, 0.55); len(via) != 0 {
		t.Errorf("depends_on neighbour of a dropped seed survived: %v", via)
	}
}

// A hop-2 arrival survives only if its chain reaches a surviving seed.
func TestContextOrphanMultiHopChain(t *testing.T) {
	s := orphanStore(t, [][3]string{
		{"staging", "migrate", "depends_on"},
		{"migrate", "backup", "depends_on"},
	})
	if _, via := orphanContext(t, s, 0.1); via["backup"] == "" || via["migrate"] == "" {
		t.Errorf("chain from a surviving seed lost a hop: %v", via)
	}
	res, via := orphanContext(t, s, 0.55)
	if len(via) != 0 {
		t.Errorf("chain from a dropped seed survived: %v", via)
	}
	if got := res.Stages["orphan_edge_dropped"]; got != 2 {
		t.Errorf("orphan_edge_dropped = %d, want 2 (%v)", got, res.Stages)
	}
}

// A passenger reached from a dropped seed AND from a pinned memory (always in
// the result) survives: one surviving source is enough.
func TestContextOrphanAnySurvivingSourceKeeps(t *testing.T) {
	s := orphanStore(t,
		[][3]string{{"staging", "migrate", "refines"}, {"runbook", "migrate", "refines"}},
		householdMemory{key: "runbook", content: "Release runbook owner is the platform team.", pinned: true})
	_, via := orphanContext(t, s, 0.55)
	if _, ok := via["staging"]; ok {
		t.Fatalf("fixture broken: seed survived the 0.55 floor (%v)", via)
	}
	if via["runbook"] != ViaPinned {
		t.Fatalf("fixture broken: runbook should be pinned (%v)", via)
	}
	if via["migrate"] != ViaEdge {
		t.Errorf("passenger of a pinned memory dropped because another source fell: %v", via)
	}
}

// No floor, nothing dropped: output identical to the pre-fix behaviour.
func TestContextOrphanNoFloorUnchanged(t *testing.T) {
	s := orphanStore(t, [][3]string{{"staging", "migrate", "refines"}})
	res, via := orphanContext(t, s, 0)
	if via["staging"] != ViaSearch || via["migrate"] != ViaEdge {
		t.Errorf("no-floor context changed: %v", via)
	}
	if _, ok := res.Stages["orphan_edge_dropped"]; ok {
		t.Errorf("orphan pruning ran without a floor: %v", res.Stages)
	}
}

func TestDropOrphanedEdgeArrivals(t *testing.T) {
	cand := func(id string, from ...string) contextCandidate {
		c := contextCandidate{via: ViaEdge}
		c.memory.ID = id
		if from != nil {
			c.arrivedFrom = from
		} else {
			c.via = ViaSearch
		}
		return c
	}
	keep := []contextCandidate{
		cand("seed"),              // surviving direct hit
		cand("a", "seed"),         // hop 1 from a survivor
		cand("b", "a"),            // hop 2 via a survivor
		cand("c", "gone", "seed"), // one dropped source, one surviving
		cand("d", "gone"),         // orphan
		cand("x", "y"),            // cycle x<->y with no surviving root
		cand("y", "x", "gone"),
		cand("p", "pin"), // passenger of a pinned memory
	}
	got, dropped := dropOrphanedEdgeArrivals(keep, map[string]bool{"pin": true})
	var ids []string
	for _, c := range got {
		ids = append(ids, c.memory.ID)
	}
	want := []string{"seed", "a", "b", "c", "p"}
	if dropped != 3 || len(ids) != len(want) {
		t.Fatalf("kept %v (dropped %d), want %v (dropped 3)", ids, dropped, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("kept %v, want %v (order must be preserved)", ids, want)
		}
	}
}

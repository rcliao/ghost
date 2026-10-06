package store

import (
	"context"
	"fmt"
	"testing"
)

// knobFixture is a small household graph exercising every arrival path: a
// pinned memory, a direct search hit (the seed), a reserve-class contradicts
// neighbour, a typed accompany-class neighbour (refines), and a relates_to
// tangent.
func knobFixture(t *testing.T) *SQLiteStore {
	t.Helper()
	return stressStore(t, []householdMemory{
		{key: "allergy", content: "The youngest is allergic to peanuts; always check labels.", pinned: true},
		{key: "dentist", content: "Need to book the twice-yearly dentist appointment for the kids."},
		{key: "moved", content: "The pediatric practice moved across town and no longer takes walk-ins."},
		{key: "clinic", content: "The clinic on Elm prefers morning slots and closes early on Fridays."},
		{key: "tangent", content: "The garden hose nozzle cracked over the winter and drips at the join."},
	}, [][3]string{
		{"dentist", "moved", "contradicts"},
		{"dentist", "clinic", "refines"},
		{"dentist", "tangent", "relates_to"},
	})
}

func knobContext(t *testing.T, s *SQLiteStore) *ContextResult {
	t.Helper()
	res, err := s.Context(context.Background(), ContextParams{
		NS: "agent:home", Query: "book the dentist appointment", Budget: 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// knobTrace renders the packed output as key/via/score lines, in order.
func knobTrace(res *ContextResult) []string {
	var out []string
	for _, m := range res.Memories {
		out = append(out, fmt.Sprintf("%s/%s/%.6f", m.Key, m.Via, m.Score))
	}
	return out
}

// knobBaseline is the packed output of knobFixture on origin/main (94c046b),
// before the knobs existed. With no knob env set the output must match it
// exactly — keys, order, via and score.
var knobBaseline = []string{
	"allergy/pinned/0.600000",
	"moved/reserved/0.430000",
	"dentist/search/0.540000",
	"clinic/edge/0.130000",
	"tangent/edge/0.080000",
}

func clearEdgeKnobs(t *testing.T) {
	t.Helper()
	t.Setenv("GHOST_EDGE_EXPANSION", "")
	t.Setenv("GHOST_EDGE_MIN_SCORE", "")
	t.Setenv("GHOST_PPR", "")
}

func equalTrace(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestEdgeKnobsDefaultUnchanged(t *testing.T) {
	clearEdgeKnobs(t)
	if !DefaultEdgeExpansion().Enabled {
		t.Fatal("edge expansion disabled with no env set")
	}
	got := knobTrace(knobContext(t, knobFixture(t)))
	if !equalTrace(got, knobBaseline) {
		t.Errorf("default context changed:\n got  %v\n want %v", got, knobBaseline)
	}
	// Values that are not an "off" spelling leave expansion on and output identical.
	for _, v := range []string{"on", "1", "true", "yes"} {
		t.Setenv("GHOST_EDGE_EXPANSION", v)
		if got := knobTrace(knobContext(t, knobFixture(t))); !equalTrace(got, knobBaseline) {
			t.Errorf("GHOST_EDGE_EXPANSION=%s changed output: %v", v, got)
		}
	}
	// A zero / unparsable floor is off.
	t.Setenv("GHOST_EDGE_EXPANSION", "")
	for _, v := range []string{"0", "nope", "-1"} {
		t.Setenv("GHOST_EDGE_MIN_SCORE", v)
		if got := knobTrace(knobContext(t, knobFixture(t))); !equalTrace(got, knobBaseline) {
			t.Errorf("GHOST_EDGE_MIN_SCORE=%s changed output: %v", v, got)
		}
	}
}

func TestEdgeKnobsExpansionOff(t *testing.T) {
	for _, v := range []string{"off", "0", "false", "OFF", " False "} {
		t.Run(v, func(t *testing.T) {
			clearEdgeKnobs(t)
			t.Setenv("GHOST_EDGE_EXPANSION", v)
			if DefaultEdgeExpansion().Enabled {
				t.Fatalf("GHOST_EDGE_EXPANSION=%q left expansion enabled", v)
			}
			res := knobContext(t, knobFixture(t))
			via := viaByKey(t, res)
			if via["allergy"] != ViaPinned {
				t.Errorf("pinned lost: %v", via)
			}
			if via["dentist"] != ViaSearch {
				t.Errorf("direct hit lost: %v", via)
			}
			for _, m := range res.Memories {
				if m.Via == ViaEdge || m.Via == ViaReserved {
					t.Errorf("%q arrived via %s with expansion off", m.Key, m.Via)
				}
			}
		})
	}
}

func TestEdgeKnobsMinScore(t *testing.T) {
	clearEdgeKnobs(t)
	// 0.1 sits between tangent (0.08) and clinic (0.13).
	t.Setenv("GHOST_EDGE_MIN_SCORE", "0.1")
	res := knobContext(t, knobFixture(t))
	via := viaByKey(t, res)
	if _, ok := via["tangent"]; ok {
		t.Errorf("low-score edge arrival survived the floor: %v", via)
	}
	if via["clinic"] != ViaEdge {
		t.Errorf("higher-score edge arrival dropped: %v", via)
	}
	if via["moved"] != ViaReserved || via["dentist"] != ViaSearch || via["allergy"] != ViaPinned {
		t.Errorf("non-passenger lost: %v", via)
	}
	if res.Stages["edge_min_score_dropped"] != 1 {
		t.Errorf("edge_min_score_dropped = %d, want 1", res.Stages["edge_min_score_dropped"])
	}
	// Remaining order and scores are untouched.
	want := []string{knobBaseline[0], knobBaseline[1], knobBaseline[2], knobBaseline[3]}
	if got := knobTrace(res); !equalTrace(got, want) {
		t.Errorf("ranking changed beyond the drop:\n got  %v\n want %v", got, want)
	}

	// A floor above every edge arrival (and above the contradiction's own
	// 0.43, below the seed's 0.54): the reserved contradicts neighbour must
	// still survive, every plain edge arrival goes, and direct hits are not
	// judged at all.
	t.Setenv("GHOST_EDGE_MIN_SCORE", "0.5")
	via = viaByKey(t, knobContext(t, knobFixture(t)))
	if via["moved"] != ViaReserved {
		t.Errorf("reserved contradicts neighbour dropped: %v", via)
	}
	for _, k := range []string{"clinic", "tangent"} {
		if _, ok := via[k]; ok {
			t.Errorf("edge arrival %q survived a 0.5 floor: %v", k, via)
		}
	}
	if via["dentist"] != ViaSearch || via["allergy"] != ViaPinned {
		t.Errorf("direct/pinned judged by the edge floor: %v", via)
	}
}

// The PPR path force-includes contradictions without the reserved flag; the
// floor must still exempt them.
func TestEdgeKnobsMinScorePPRKeepsContradiction(t *testing.T) {
	clearEdgeKnobs(t)
	t.Setenv("GHOST_PPR", "1")
	t.Setenv("GHOST_EDGE_MIN_SCORE", "0.99")
	via := viaByKey(t, knobContext(t, knobFixture(t)))
	if _, ok := via["moved"]; !ok {
		t.Errorf("PPR contradicts neighbour dropped by the edge floor: %v", via)
	}
	for _, k := range []string{"clinic", "tangent"} {
		if _, ok := via[k]; ok {
			t.Errorf("PPR edge arrival %q survived a 0.99 floor: %v", k, via)
		}
	}
	if via["dentist"] != ViaSearch {
		t.Errorf("direct hit lost under PPR: %v", via)
	}
}

package store

import (
	"context"
	"testing"
)

// viaByKey maps each returned key to its Via, failing on any untraced memory.
func viaByKey(t *testing.T, res *ContextResult) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, m := range res.Memories {
		if m.Via == "" {
			t.Errorf("memory %q returned with empty Via", m.Key)
		}
		out[m.Key] = m.Via
	}
	return out
}

func TestFinalViaPrecedence(t *testing.T) {
	cases := []struct {
		name string
		c    contextCandidate
		sub  bool
		want string
	}{
		{"search", contextCandidate{via: ViaSearch}, false, ViaSearch},
		{"rescued", contextCandidate{via: ViaRescued}, false, ViaRescued},
		{"edge", contextCandidate{via: ViaEdge, viaEdge: true}, false, ViaEdge},
		{"reserved beats edge", contextCandidate{via: ViaEdge, viaEdge: true, reserved: true}, false, ViaReserved},
		{"reserved beats search", contextCandidate{via: ViaSearch, reserved: true}, false, ViaReserved},
		{"parent beats reserved", contextCandidate{via: ViaSearch, reserved: true}, true, ViaParent},
		{"parent arrival", contextCandidate{via: ViaParent}, false, ViaParent},
		{"untraced defaults to search", contextCandidate{}, false, ViaSearch},
	}
	for _, tc := range cases {
		if got := finalVia(tc.c, tc.sub); got != tc.want {
			t.Errorf("%s: finalVia = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestContextViaPinnedAndSearch(t *testing.T) {
	s := stressStore(t, []householdMemory{
		{key: "allergy", content: "The youngest is allergic to peanuts; always check labels.", pinned: true},
		{key: "dentist", content: "Need to book the twice-yearly dentist appointment for the kids."},
	}, nil)
	res, err := s.Context(context.Background(), ContextParams{
		NS: "agent:home", Query: "book the dentist appointment", Budget: 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	via := viaByKey(t, res)
	if via["allergy"] != ViaPinned {
		t.Errorf("pinned memory via = %q, want pinned (all: %v)", via["allergy"], via)
	}
	if via["dentist"] != ViaSearch {
		t.Errorf("direct hit with no floor via = %q, want search (all: %v)", via["dentist"], via)
	}
}

// A strong topical match whose composite cannot clear an impossible floor is
// kept only by the relevance-confident rescue — and must say so.
func TestContextViaRescued(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	s.Put(ctx, PutParams{NS: "test", Key: "fruit", Content: "apple banana cherry"})
	s.Put(ctx, PutParams{NS: "test", Key: "cars", Content: "something totally unrelated about cars"})

	res, err := s.Context(ctx, ContextParams{
		NS: "test", Query: "apple banana cherry", Budget: 4000, MinScore: 99,
	})
	if err != nil {
		t.Fatal(err)
	}
	via := viaByKey(t, res)
	if via["fruit"] != ViaRescued {
		t.Errorf("sub-floor rescued hit via = %q, want rescued (all: %v)", via["fruit"], via)
	}
	if _, ok := via["cars"]; ok {
		t.Errorf("unmatched filler survived a 99 floor: %v", via)
	}
	for _, m := range res.Memories {
		if m.Via == ViaSearch && m.Score < 99 {
			t.Errorf("%q reports search but scores %.2f under the floor", m.Key, m.Score)
		}
	}
}

// Reserve-class (depends_on) neighbours below the floor report reserved, and
// a typed accompany-class neighbour (refines) reports edge.
func TestContextViaReservedAndEdge(t *testing.T) {
	mems := []householdMemory{
		{key: "dentist", content: "Need to book the twice-yearly dentist appointment for the kids."},
		{key: "insurance", content: "The dental card on file lapsed at the end of last quarter and has not been replaced."},
		{key: "signature", content: "That replacement paperwork sits unsigned in the folder by the front door."},
		{key: "clinic", content: "The clinic on Elm prefers morning slots and closes early on Fridays."},
	}
	s := stressStore(t, mems, [][3]string{
		{"dentist", "insurance", "depends_on"},
		{"insurance", "signature", "depends_on"},
		{"dentist", "clinic", "refines"},
	})
	res, err := s.Context(context.Background(), ContextParams{
		NS: "agent:home", Query: "book the dentist appointment", Budget: 2000, MinScore: 0.3,
	})
	if err != nil {
		t.Fatal(err)
	}
	via := viaByKey(t, res)
	for _, k := range []string{"insurance", "signature"} {
		if via[k] != ViaReserved {
			t.Errorf("depends_on neighbour %q via = %q, want reserved (all: %v)", k, via[k], via)
		}
	}
	if via["clinic"] != ViaEdge {
		t.Errorf("refines neighbour via = %q, want edge (all: %v)", via["clinic"], via)
	}
	if via["dentist"] != ViaSearch && via["dentist"] != ViaRescued {
		t.Errorf("seed via = %q, want search or rescued", via["dentist"])
	}
}

// relates_to arrivals never set the internal viaEdge flag (they stay under the
// floor), yet they did arrive by an edge — Via must not misreport them as search.
func TestContextViaRelatesToArrivalIsEdge(t *testing.T) {
	s := stressStore(t, []householdMemory{
		{key: "dentist", content: "Need to book the twice-yearly dentist appointment for the kids."},
		{key: "tangent", content: "The garden hose nozzle cracked over the winter and drips at the join."},
	}, [][3]string{{"dentist", "tangent", "relates_to"}})
	res, err := s.Context(context.Background(), ContextParams{
		NS: "agent:home", Query: "book the dentist appointment", Budget: 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	via := viaByKey(t, res)
	if via["tangent"] != ViaEdge {
		t.Errorf("relates_to arrival via = %q, want edge (all: %v)", via["tangent"], via)
	}
}

func TestContextViaParentSubstitution(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	s.Put(ctx, PutParams{NS: "test", Key: "detail-1", Content: "JWT tokens use RSA256 signing for authentication"})
	s.Put(ctx, PutParams{NS: "test", Key: "detail-2", Content: "JWT authentication tokens expire after 24 hours"})
	s.Put(ctx, PutParams{NS: "test", Key: "detail-3", Content: "JWT refresh tokens for authentication are stored in httpOnly cookies"})
	s.Put(ctx, PutParams{NS: "test", Key: "auth-summary",
		Content: "Authentication overview: JWT with RSA256, 24h expiry, refresh via httpOnly cookies"})
	for _, k := range []string{"detail-1", "detail-2", "detail-3"} {
		if _, err := s.CreateEdge(ctx, EdgeParams{FromNS: "test", FromKey: "auth-summary",
			ToNS: "test", ToKey: k, Rel: "contains"}); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.Context(ctx, ContextParams{NS: "test", Query: "authentication JWT", Budget: 100})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range res.Memories {
		if len(m.SummaryOf) > 0 {
			found = true
			if m.Via != ViaParent {
				t.Errorf("substituted %q via = %q, want parent", m.Key, m.Via)
			}
		} else if m.Via == ViaParent {
			t.Errorf("%q reports parent without SummaryOf", m.Key)
		}
	}
	if !found {
		t.Fatalf("tight budget did not substitute; cannot check parent via: %+v", res.Memories)
	}
}

// Regression guard: Via is trace-only. Selection, order, and scores must be
// identical to what packing chose before the field existed. The expected order
// below was recorded from this corpus; any change means tracing leaked into
// ranking (or ranking changed — re-record deliberately, not to make this pass).
func TestContextViaDoesNotChangeSelection(t *testing.T) {
	mems := []householdMemory{
		{key: "allergy", content: "The youngest is allergic to peanuts; always check labels.", pinned: true},
		{key: "dentist", content: "Need to book the twice-yearly dentist appointment for the kids."},
		{key: "insurance", content: "The dental card on file lapsed at the end of last quarter and has not been replaced."},
		{key: "signature", content: "That replacement paperwork sits unsigned in the folder by the front door."},
	}
	for i := 0; i < 4; i++ {
		mems = append(mems, householdMemory{key: "appt-" + string(rune('0'+i)),
			content: "Appointment note: still need to book the twice-yearly dentist visit for the kids."})
	}
	s := stressStore(t, mems, [][3]string{
		{"dentist", "insurance", "depends_on"},
		{"insurance", "signature", "depends_on"},
	})
	res, err := s.Context(context.Background(), ContextParams{
		NS: "agent:home", Query: "book the dentist appointment", Budget: 800, MinScore: 0.3,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range res.Memories {
		got = append(got, m.Key+"="+m.Via)
	}
	want := expectedViaSelection
	if len(got) != len(want) {
		t.Fatalf("selection changed:\n got  %v\n want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("selection/order changed at %d:\n got  %v\n want %v", i, got, want)
		}
	}
}

// Recorded 2026-10-06; the same keys/order/scores were confirmed identical
// against origin/main (pre-Via) on this corpus across budgets 150/800/4000 and
// MinScore 0/0.3/0.55/99.
var expectedViaSelection = []string{
	"allergy=pinned", "insurance=reserved", "signature=reserved",
	"appt-3=search", "appt-2=search", "appt-1=search", "appt-0=search", "dentist=search",
}

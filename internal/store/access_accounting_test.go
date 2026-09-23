package store

import (
	"context"
	"testing"
	"time"
)

func accessAndUtility(t *testing.T, s *SQLiteStore, ns, key string) (int, int) {
	t.Helper()
	var access, utility int
	err := s.db.QueryRow(`SELECT access_count, utility_count FROM memories
		WHERE ns = ? AND key = ? AND deleted_at IS NULL ORDER BY version DESC LIMIT 1`,
		ns, key).Scan(&access, &utility)
	if err != nil {
		t.Fatalf("read counters for %s: %v", key, err)
	}
	return access, utility
}

// Search hits that the MinSpread/MinScore filter drops were never shown to the
// agent, so they must not accrue access (which drives ltm promotion and shields
// a memory from the low-utility prune).
func TestContextDoesNotTouchFilteredHits(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	keys := []string{"k1", "k2", "k3", "k4", "k5"}
	for i, k := range keys {
		s.Put(ctx, PutParams{NS: "test", Key: k, Content: "Generic stop mention " + string(rune('1'+i))})
	}

	res, err := s.Context(ctx, ContextParams{NS: "test", Query: "stop", Budget: 4000, MinSpread: 99.0})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Memories) != 1 {
		t.Fatalf("expected MinSpread to collapse to the top hit, got %d", len(res.Memories))
	}
	returned := res.Memories[0].Key
	for _, k := range keys {
		access, _ := accessAndUtility(t, s, "test", k)
		want := 0
		if k == returned {
			want = 1
		}
		if access != want {
			t.Errorf("%s: access_count=%d, want %d (returned=%s)", k, access, want, returned)
		}
	}
}

// A lone direct hit that is returned is both accessed and credited with
// utility; before, utility was only credited when 2+ memories came back.
func TestContextCreditsLoneDirectHit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	s.Put(ctx, PutParams{NS: "test", Key: "deploy-gotcha", Content: "Release tags need a manual workflow run"})

	res, err := s.Context(ctx, ContextParams{NS: "test", Query: "release tags manual workflow", Budget: 4000})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Memories) != 1 {
		t.Fatalf("expected exactly one returned memory, got %d", len(res.Memories))
	}
	access, utility := accessAndUtility(t, s, "test", "deploy-gotcha")
	if access != 1 || utility != 1 {
		t.Errorf("lone returned hit: access=%d utility=%d, want 1 and 1", access, utility)
	}
}

// An edge passenger (here a junk capture hanging off a real hit by a
// force-included contradicts edge) is returned but never matched the query. It
// must not accrue access, or three days of returns would read as rehearsal and
// promote it to ltm while the spaced-access guard shields it from the prune.
func TestContextEdgePassengerNotPromoted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Now().Add(-96 * time.Hour)
	clock := base
	s.SetClock(func() time.Time { return clock })
	s.Put(ctx, PutParams{NS: "test", Key: "real-fact", Content: "Release tags need a manual workflow run"})
	s.Put(ctx, PutParams{NS: "test", Key: "junk-capture", Content: "i think that is fine and lets go"})
	if _, err := s.CreateEdge(ctx, EdgeParams{FromNS: "test", FromKey: "real-fact", ToNS: "test", ToKey: "junk-capture", Rel: "contradicts"}); err != nil {
		t.Fatal(err)
	}
	s.db.Exec(`UPDATE memories SET created_at = ?`, base.Add(-96*time.Hour).UTC().Format(time.RFC3339))

	passengerReturned := false
	for day := 0; day < 3; day++ {
		clock = base.Add(time.Duration(day*24) * time.Hour)
		for i := 0; i < 20; i++ {
			res, err := s.Context(ctx, ContextParams{NS: "test", Query: "release tags manual workflow", Budget: 4000})
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range res.Memories {
				if m.Key == "junk-capture" {
					passengerReturned = true
				}
			}
		}
	}
	if !passengerReturned {
		t.Fatal("setup: expected the contradicts neighbour to ride along as an edge passenger")
	}
	if access, _ := accessAndUtility(t, s, "test", "junk-capture"); access != 0 {
		t.Errorf("edge passenger access_count=%d, want 0", access)
	}
	if access, utility := accessAndUtility(t, s, "test", "real-fact"); access != 60 || utility != 60 {
		t.Errorf("direct hit access=%d utility=%d, want 60 and 60", access, utility)
	}

	clock = base.Add(96 * time.Hour)
	if _, err := s.Reflect(ctx, ReflectParams{NS: "test"}); err != nil {
		t.Fatal(err)
	}
	var tier string
	s.db.QueryRow(`SELECT tier FROM memories WHERE key = 'junk-capture'`).Scan(&tier)
	if tier == "ltm" {
		t.Errorf("edge passenger was promoted to ltm")
	}
}

package store

import (
	"context"
	"testing"
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

package store

import (
	"context"
	"testing"
)

func TestCurate_Promote(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Store a memory in STM (default tier)
	s.Put(ctx, PutParams{NS: "test", Key: "fact", Content: "hello", Importance: 0.5})

	result, err := s.Curate(ctx, CurateParams{NS: "test", Key: "fact", Op: "promote"})
	if err != nil {
		t.Fatal(err)
	}
	if result.OldTier != "stm" {
		t.Errorf("old tier: want stm, got %s", result.OldTier)
	}
	if result.NewTier != "ltm" {
		t.Errorf("new tier: want ltm, got %s", result.NewTier)
	}

	// Verify the tier actually changed
	mems, _ := s.Get(ctx, GetParams{NS: "test", Key: "fact"})
	if mems[0].Tier != "ltm" {
		t.Errorf("memory tier after promote: want ltm, got %s", mems[0].Tier)
	}
}

func TestCurate_Demote(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Store then promote to LTM first (Put doesn't persist tier to DB)
	s.Put(ctx, PutParams{NS: "test", Key: "fact", Content: "hello"})
	s.Curate(ctx, CurateParams{NS: "test", Key: "fact", Op: "promote"}) // stm→ltm

	result, err := s.Curate(ctx, CurateParams{NS: "test", Key: "fact", Op: "demote"})
	if err != nil {
		t.Fatal(err)
	}
	if result.OldTier != "ltm" || result.NewTier != "stm" {
		t.Errorf("demote: want ltm→stm, got %s→%s", result.OldTier, result.NewTier)
	}
}

func TestCurate_Boost(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "fact", Content: "hello", Importance: 0.5})

	result, err := s.Curate(ctx, CurateParams{NS: "test", Key: "fact", Op: "boost"})
	if err != nil {
		t.Fatal(err)
	}
	if result.OldImportance != 0.5 {
		t.Errorf("old importance: want 0.5, got %f", result.OldImportance)
	}
	if result.NewImportance != 0.7 {
		t.Errorf("new importance: want 0.7, got %f", result.NewImportance)
	}
}

func TestCurate_BoostCapsAt1(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "fact", Content: "hello", Importance: 0.95})

	result, _ := s.Curate(ctx, CurateParams{NS: "test", Key: "fact", Op: "boost"})
	if result.NewImportance != 1.0 {
		t.Errorf("boosted importance should cap at 1.0, got %f", result.NewImportance)
	}
}

func TestCurate_Diminish(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "fact", Content: "hello", Importance: 0.5})

	result, _ := s.Curate(ctx, CurateParams{NS: "test", Key: "fact", Op: "diminish"})
	if result.NewImportance != 0.3 {
		t.Errorf("diminished importance: want 0.3, got %f", result.NewImportance)
	}
}

func TestCurate_DiminishFloorsAt01(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "fact", Content: "hello", Importance: 0.15})

	result, _ := s.Curate(ctx, CurateParams{NS: "test", Key: "fact", Op: "diminish"})
	if result.NewImportance != 0.1 {
		t.Errorf("diminished importance should floor at 0.1, got %f", result.NewImportance)
	}
}

func TestCurate_Delete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "fact", Content: "hello"})

	_, err := s.Curate(ctx, CurateParams{NS: "test", Key: "fact", Op: "delete"})
	if err != nil {
		t.Fatal(err)
	}

	// Should be soft-deleted
	mems, _ := s.Get(ctx, GetParams{NS: "test", Key: "fact"})
	if len(mems) != 0 {
		t.Errorf("memory should be deleted, but Get returned %d results", len(mems))
	}
}

func TestCurate_Archive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "fact", Content: "hello"})
	s.Curate(ctx, CurateParams{NS: "test", Key: "fact", Op: "promote"}) // stm→ltm

	result, _ := s.Curate(ctx, CurateParams{NS: "test", Key: "fact", Op: "archive"})
	if result.OldTier != "ltm" || result.NewTier != "dormant" {
		t.Errorf("archive: want ltm→dormant, got %s→%s", result.OldTier, result.NewTier)
	}
}

func TestCurate_NotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.Curate(ctx, CurateParams{NS: "test", Key: "nonexistent", Op: "promote"})
	if err == nil {
		t.Error("expected error for missing memory")
	}
}

func TestCurate_InvalidOp(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.Curate(ctx, CurateParams{NS: "test", Key: "fact", Op: "yeet"})
	if err == nil {
		t.Error("expected error for invalid op")
	}
}

func TestCurate_PromoteAtTop(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Promote stm→ltm→identity
	s.Put(ctx, PutParams{NS: "test", Key: "fact", Content: "hello"})
	s.Curate(ctx, CurateParams{NS: "test", Key: "fact", Op: "promote"}) // stm→ltm
	s.Curate(ctx, CurateParams{NS: "test", Key: "fact", Op: "promote"}) // ltm→identity

	_, err := s.Curate(ctx, CurateParams{NS: "test", Key: "fact", Op: "promote"})
	if err == nil {
		t.Error("expected error when promoting from identity tier")
	}
}

func TestCurate_Used(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "fact", Content: "hello", Importance: 0.5})
	s.Curate(ctx, CurateParams{NS: "test", Key: "fact", Op: "promote"}) // stm→ltm
	before, _ := s.Get(ctx, GetParams{NS: "test", Key: "fact"})
	b := before[0]

	// Read access counters directly: Get itself bumps them.
	accessOf := func() (int, string) {
		var n int
		var last string
		s.db.QueryRowContext(ctx,
			`SELECT access_count, COALESCE(last_accessed_at, '') FROM memories WHERE ns = 'test' AND key = 'fact' AND deleted_at IS NULL`).Scan(&n, &last)
		return n, last
	}
	accBefore, lastBefore := accessOf()

	result, err := s.Curate(ctx, CurateParams{NS: "test", Key: "fact", Op: "used"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Op != "used" || result.NS != "test" || result.Key != "fact" {
		t.Errorf("unexpected result: %+v", result)
	}
	// A reported use is not a retrieval: access counters and recency stay put.
	if accAfter, lastAfter := accessOf(); accAfter != accBefore || lastAfter != lastBefore {
		t.Errorf("used touched access tracking: access_count %d→%d, last_accessed_at %q→%q", accBefore, accAfter, lastBefore, lastAfter)
	}

	after, _ := s.Get(ctx, GetParams{NS: "test", Key: "fact"})
	a := after[0]
	if a.UsedCount != b.UsedCount+1 {
		t.Errorf("used_count: want %d, got %d", b.UsedCount+1, a.UsedCount)
	}
	// The caller-reported signal is stored apart from the retrieval-time credit.
	if a.UtilityCount != b.UtilityCount {
		t.Errorf("used must not touch utility_count: %d→%d", b.UtilityCount, a.UtilityCount)
	}
	if a.Importance != b.Importance || a.Tier != b.Tier || a.Content != b.Content ||
		a.Version != b.Version || a.ID != b.ID || a.Pinned != b.Pinned {
		t.Errorf("used changed more than used_count: before %+v after %+v", b, a)
	}

	// Second call increments again by exactly 1, still without new versions.
	s.Curate(ctx, CurateParams{NS: "test", Key: "fact", Op: "used"})
	again, _ := s.Get(ctx, GetParams{NS: "test", Key: "fact"})
	if again[0].UsedCount != b.UsedCount+2 {
		t.Errorf("used_count after 2 uses: want %d, got %d", b.UsedCount+2, again[0].UsedCount)
	}
	hist, _ := s.Get(ctx, GetParams{NS: "test", Key: "fact", History: true})
	if len(hist) != 1 {
		t.Errorf("used must not create versions: got %d versions", len(hist))
	}
}

func TestCurate_UsedOnPinnedAndLocked(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.Put(ctx, PutParams{NS: "test", Key: "pinned", Content: "p", Pinned: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, PutParams{NS: "test", Key: "locked", Content: "l", Tags: []string{LockedTag}, Pinned: true}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"pinned", "locked"} {
		before, _ := s.Get(ctx, GetParams{NS: "test", Key: key})
		if _, err := s.Curate(ctx, CurateParams{NS: "test", Key: key, Op: "used"}); err != nil {
			t.Fatalf("used on %s: %v", key, err)
		}
		after, _ := s.Get(ctx, GetParams{NS: "test", Key: key})
		if after[0].UsedCount != before[0].UsedCount+1 {
			t.Errorf("%s used_count: want %d, got %d", key, before[0].UsedCount+1, after[0].UsedCount)
		}
		if after[0].UtilityCount != before[0].UtilityCount {
			t.Errorf("%s: used touched utility_count", key)
		}
		if !after[0].Pinned || after[0].Content != before[0].Content || after[0].Version != before[0].Version {
			t.Errorf("%s: used changed protected state: before %+v after %+v", key, before[0], after[0])
		}
	}
}

func TestCurate_UsedNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Curate(context.Background(), CurateParams{NS: "test", Key: "nope", Op: "used"})
	if err == nil {
		t.Error("expected error for missing memory")
	}
}

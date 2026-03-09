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

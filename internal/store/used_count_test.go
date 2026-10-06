package store

import (
	"context"
	"path/filepath"
	"testing"
)

// A DB created before used_count existed gains the column on open, old rows
// read as 0, and curate --op used works. Reopening is idempotent.
func TestMigrateOldSchemaAddsUsedCount(t *testing.T) {
	ctx := context.Background()
	dbPath := createOldSchemaDB(t)

	for i := 0; i < 2; i++ { // second open re-runs migrate on a migrated DB
		s, err := NewSQLiteStore(dbPath)
		if err != nil {
			t.Fatalf("open #%d: %v", i+1, err)
		}
		got, err := s.Get(ctx, GetParams{NS: "test", Key: "greeting"})
		if err != nil || len(got) != 1 {
			t.Fatalf("open #%d: get old memory: %v (n=%d)", i+1, err, len(got))
		}
		if got[0].UsedCount != i {
			t.Errorf("open #%d: used_count want %d, got %d", i+1, i, got[0].UsedCount)
		}
		if _, err := s.Curate(ctx, CurateParams{NS: "test", Key: "greeting", Op: "used"}); err != nil {
			t.Fatalf("open #%d: curate used on migrated DB: %v", i+1, err)
		}
		s.Close()
	}
}

func TestExportImportRoundTripsUsedCount(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	src, err := NewSQLiteStore(filepath.Join(dir, "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	src.Put(ctx, PutParams{NS: "test", Key: "used", Content: "alpha"})
	src.Put(ctx, PutParams{NS: "test", Key: "unused", Content: "beta"})
	for i := 0; i < 3; i++ {
		if _, err := src.Curate(ctx, CurateParams{NS: "test", Key: "used", Op: "used"}); err != nil {
			t.Fatal(err)
		}
	}

	exported, err := src.ExportAll(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"used": 3, "unused": 0}
	for _, m := range exported {
		if m.UsedCount != want[m.Key] {
			t.Errorf("export %s: used_count want %d, got %d", m.Key, want[m.Key], m.UsedCount)
		}
	}

	dst, err := NewSQLiteStore(filepath.Join(dir, "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if _, err := dst.Import(ctx, exported); err != nil {
		t.Fatal(err)
	}
	for key, n := range want {
		got, err := dst.Get(ctx, GetParams{NS: "test", Key: key})
		if err != nil || len(got) != 1 {
			t.Fatalf("get %s after import: %v", key, err)
		}
		if got[0].UsedCount != n {
			t.Errorf("import %s: used_count want %d, got %d", key, n, got[0].UsedCount)
		}
		if got[0].UtilityCount != 0 {
			t.Errorf("import %s: utility_count should stay 0, got %d", key, got[0].UtilityCount)
		}
	}
}

// searchVector drops rows whose scan fails (continue), so a column-list /
// scanner mismatch there would silently return nothing. Guard it directly.
func TestSearchVectorScansUsedCount(t *testing.T) {
	s := newTestStore(t)
	s.SetEmbedder(unitEmbedder{})
	ctx := context.Background()
	if _, err := s.Put(ctx, PutParams{NS: "test", Key: "v", Content: "the quick brown fox"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Curate(ctx, CurateParams{NS: "test", Key: "v", Op: "used"}); err != nil {
		t.Fatal(err)
	}
	res, err := s.searchVector(ctx, SearchParams{Query: "the quick brown fox", NS: "test"}, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Memory.UsedCount != 1 {
		t.Fatalf("vector search: want 1 result with used_count 1, got %+v", res)
	}
}

func TestMockStoreUsedCount(t *testing.T) {
	ctx := context.Background()
	m := NewMockStore()
	m.Put(ctx, PutParams{NS: "test", Key: "k", Content: "c"})
	if _, err := m.Curate(ctx, CurateParams{NS: "test", Key: "k", Op: "used"}); err != nil {
		t.Fatal(err)
	}
	exported, _ := m.ExportAll(ctx, "test")
	if len(exported) != 1 || exported[0].UsedCount != 1 || exported[0].UtilityCount != 0 {
		t.Fatalf("mock used: want used_count 1 / utility 0, got %+v", exported)
	}

	m2 := NewMockStore()
	if _, err := m2.Import(ctx, exported); err != nil {
		t.Fatal(err)
	}
	got, _ := m2.ExportAll(ctx, "test")
	if len(got) != 1 || got[0].UsedCount != 1 {
		t.Fatalf("mock import: want used_count 1, got %+v", got)
	}
}

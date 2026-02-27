package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSearch_Basic(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSQLiteStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	// Store some memories
	s.Put(ctx, PutParams{NS: "test", Key: "golang", Content: "Go is a compiled language with goroutines"})
	s.Put(ctx, PutParams{NS: "test", Key: "python", Content: "Python is an interpreted language"})
	s.Put(ctx, PutParams{NS: "other", Key: "rust", Content: "Rust has a borrow checker"})

	// Search by content
	results, err := s.Search(ctx, SearchParams{Query: "language"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// Search with namespace filter
	results, err = s.Search(ctx, SearchParams{NS: "test", Query: "language"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// Search by key
	results, err = s.Search(ctx, SearchParams{Query: "golang"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	// No results
	results, err = s.Search(ctx, SearchParams{Query: "javascript"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestSearch_DeletedExcluded(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSQLiteStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "deleted", Content: "this should not appear"})
	s.Rm(ctx, RmParams{NS: "test", Key: "deleted"})

	results, err := s.Search(ctx, SearchParams{Query: "should not appear"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0, got %d", len(results))
	}
}

func TestStats(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "ns1", Key: "a", Content: "hello"})
	s.Put(ctx, PutParams{NS: "ns1", Key: "b", Content: "world"})
	s.Put(ctx, PutParams{NS: "ns2", Key: "c", Content: "test"})

	stats, err := s.Stats(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ActiveMemories != 3 {
		t.Fatalf("expected 3 active, got %d", stats.ActiveMemories)
	}
	if len(stats.Namespaces) != 2 {
		t.Fatalf("expected 2 namespaces, got %d", len(stats.Namespaces))
	}
	if stats.DBSizeBytes == 0 {
		t.Fatal("expected non-zero db size")
	}
}

func TestExportImport(t *testing.T) {
	dir := t.TempDir()
	s1, _ := NewSQLiteStore(filepath.Join(dir, "src.db"))
	defer s1.Close()
	ctx := context.Background()

	s1.Put(ctx, PutParams{NS: "test", Key: "a", Content: "alpha"})
	s1.Put(ctx, PutParams{NS: "test", Key: "b", Content: "beta"})

	exported, err := s1.ExportAll(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(exported) != 2 {
		t.Fatalf("expected 2 exported, got %d", len(exported))
	}

	s2, _ := NewSQLiteStore(filepath.Join(dir, "dst.db"))
	defer s2.Close()

	n, err := s2.Import(ctx, exported)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected 2 imported, got %d", n)
	}

	// Verify
	mems, _ := s2.List(ctx, ListParams{NS: "test"})
	if len(mems) != 2 {
		t.Fatalf("expected 2 mems after import, got %d", len(mems))
	}
}

func TestTTL_Expired(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	// Store with very short TTL (already expired by forcing)
	s.Put(ctx, PutParams{NS: "test", Key: "ephemeral", Content: "temp data", TTL: "1s"})
	s.Put(ctx, PutParams{NS: "test", Key: "permanent", Content: "keep this"})

	// Manually expire it
	s.db.Exec(`UPDATE memories SET expires_at = '2020-01-01T00:00:00Z' WHERE key = 'ephemeral'`)

	// List should exclude expired
	mems, _ := s.List(ctx, ListParams{NS: "test"})
	if len(mems) != 1 {
		t.Fatalf("expected 1 (non-expired), got %d", len(mems))
	}
	if mems[0].Key != "permanent" {
		t.Fatalf("expected permanent, got %s", mems[0].Key)
	}
}

func TestTTL_ParseTTL(t *testing.T) {
	tests := []struct {
		input string
		ok    bool
	}{
		{"7d", true},
		{"24h", true},
		{"30m", true},
		{"60s", true},
		{"invalid", false},
		{"", false},
		{"7x", false},
	}
	for _, tt := range tests {
		_, err := ParseTTL(tt.input)
		if tt.ok && err != nil {
			t.Errorf("ParseTTL(%q) unexpected error: %v", tt.input, err)
		}
		if !tt.ok && err == nil {
			t.Errorf("ParseTTL(%q) expected error", tt.input)
		}
	}
}

func TestBuildFTSQuery(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"language", "language"},
		{"Go language", "go OR language"},
		{"do you know who EV is", "know OR ev"},
		{"the a is", ""}, // all stop words
		{"", ""},
		{"What is Rust", "rust"},
	}
	for _, tt := range tests {
		got := buildFTSQuery(tt.input)
		if got != tt.want {
			t.Errorf("buildFTSQuery(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSearch_StopWordQuery(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSQLiteStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	// Store memories that should match on non-stop-word terms
	s.Put(ctx, PutParams{NS: "test", Key: "ev-info", Content: "EV is our beloved entity and creator"})
	s.Put(ctx, PutParams{NS: "test", Key: "other", Content: "Rust has a borrow checker"})

	// Natural language query with many stop words — should still find "EV"
	results, err := s.Search(ctx, SearchParams{Query: "do you know who EV is"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result for stop-word-heavy query, got 0")
	}
	found := false
	for _, r := range results {
		if r.Key == "ev-info" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected ev-info in results, got %v", results)
	}

	// Pure stop word query should still work (falls back to full-phrase LIKE)
	results, err = s.Search(ctx, SearchParams{Query: "is"})
	if err != nil {
		t.Fatal(err)
	}
	// Should not crash — results may or may not exist depending on LIKE
}

// Ensure unused import doesn't break
var _ = os.TempDir

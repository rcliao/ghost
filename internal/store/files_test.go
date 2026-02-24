package store

import (
	"context"
	"testing"
)

func TestPutWithFiles(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	mem, err := s.Put(ctx, PutParams{
		NS: "ns", Key: "task1", Content: "refactored auth",
		Files: []FileParam{
			{Path: "src/auth.go", Rel: "modified"},
			{Path: "src/auth_test.go", Rel: "modified"},
		},
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if len(mem.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(mem.Files))
	}
	if mem.Files[0].Path != "src/auth.go" {
		t.Errorf("files[0].path: want %q, got %q", "src/auth.go", mem.Files[0].Path)
	}
	if mem.Files[0].Rel != "modified" {
		t.Errorf("files[0].rel: want %q, got %q", "modified", mem.Files[0].Rel)
	}
}

func TestPutWithFilesDefaultRel(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	mem, err := s.Put(ctx, PutParams{
		NS: "ns", Key: "k", Content: "data",
		Files: []FileParam{{Path: "main.go"}},
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if len(mem.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(mem.Files))
	}
	if mem.Files[0].Rel != "modified" {
		t.Errorf("default rel: want %q, got %q", "modified", mem.Files[0].Rel)
	}
}

func TestGetLoadsFiles(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{
		NS: "ns", Key: "k", Content: "data",
		Files: []FileParam{
			{Path: "a.go", Rel: "created"},
			{Path: "b.go", Rel: "modified"},
		},
	})

	got, err := s.Get(ctx, GetParams{NS: "ns", Key: "k"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got[0].Files) != 2 {
		t.Fatalf("expected 2 files from get, got %d", len(got[0].Files))
	}
	// Sorted by path
	if got[0].Files[0].Path != "a.go" {
		t.Errorf("files[0].path: want %q, got %q", "a.go", got[0].Files[0].Path)
	}
	if got[0].Files[0].Rel != "created" {
		t.Errorf("files[0].rel: want %q, got %q", "created", got[0].Files[0].Rel)
	}
}

func TestGetWithoutFiles(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{NS: "ns", Key: "k", Content: "no files"})

	got, err := s.Get(ctx, GetParams{NS: "ns", Key: "k"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got[0].Files) != 0 {
		t.Errorf("expected 0 files, got %d", len(got[0].Files))
	}
}

func TestFindByFile(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{
		NS: "ns", Key: "task1", Content: "first change",
		Files: []FileParam{{Path: "shared.go", Rel: "modified"}},
	})
	s.Put(ctx, PutParams{
		NS: "ns", Key: "task2", Content: "second change",
		Files: []FileParam{
			{Path: "shared.go", Rel: "modified"},
			{Path: "other.go", Rel: "created"},
		},
	})
	s.Put(ctx, PutParams{
		NS: "ns", Key: "task3", Content: "unrelated",
		Files: []FileParam{{Path: "unrelated.go", Rel: "modified"}},
	})

	// Find by shared.go
	results, err := s.FindByFile(ctx, FindByFileParams{Path: "shared.go"})
	if err != nil {
		t.Fatalf("find by file: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results for shared.go, got %d", len(results))
	}

	// Find by other.go
	results, err = s.FindByFile(ctx, FindByFileParams{Path: "other.go"})
	if err != nil {
		t.Fatalf("find by file: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for other.go, got %d", len(results))
	}
	if results[0].Key != "task2" {
		t.Errorf("expected task2, got %q", results[0].Key)
	}
}

func TestFindByFileWithRel(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{
		NS: "ns", Key: "task1", Content: "modified it",
		Files: []FileParam{{Path: "file.go", Rel: "modified"}},
	})
	s.Put(ctx, PutParams{
		NS: "ns", Key: "task2", Content: "just read it",
		Files: []FileParam{{Path: "file.go", Rel: "read"}},
	})

	// Filter by rel=modified
	results, err := s.FindByFile(ctx, FindByFileParams{Path: "file.go", Rel: "modified"})
	if err != nil {
		t.Fatalf("find by file: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result with rel=modified, got %d", len(results))
	}
	if results[0].Key != "task1" {
		t.Errorf("expected task1, got %q", results[0].Key)
	}
}

func TestFindByFileNoResults(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{
		NS: "ns", Key: "k", Content: "data",
		Files: []FileParam{{Path: "exists.go", Rel: "modified"}},
	})

	results, err := s.FindByFile(ctx, FindByFileParams{Path: "nonexistent.go"})
	if err != nil {
		t.Fatalf("find by file: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestFindByFileIncludesFileRefs(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{
		NS: "ns", Key: "k", Content: "data",
		Files: []FileParam{
			{Path: "a.go", Rel: "modified"},
			{Path: "b.go", Rel: "created"},
		},
	})

	results, err := s.FindByFile(ctx, FindByFileParams{Path: "a.go"})
	if err != nil {
		t.Fatalf("find by file: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	// Should include all files for the memory, not just the queried one
	if len(results[0].Files) != 2 {
		t.Errorf("expected 2 file refs on result, got %d", len(results[0].Files))
	}
}

func TestGetFilesDirectly(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	mem, _ := s.Put(ctx, PutParams{
		NS: "ns", Key: "k", Content: "data",
		Files: []FileParam{
			{Path: "x.go", Rel: "modified"},
			{Path: "y.go", Rel: "deleted"},
		},
	})

	files, err := s.GetFiles(ctx, mem.ID)
	if err != nil {
		t.Fatalf("get files: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if files[0].Path != "x.go" || files[1].Path != "y.go" {
		t.Errorf("unexpected file paths: %v", files)
	}
}

func TestPutWithFilesVersioning(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// v1 links to a.go
	s.Put(ctx, PutParams{
		NS: "ns", Key: "k", Content: "v1",
		Files: []FileParam{{Path: "a.go", Rel: "modified"}},
	})

	// v2 links to b.go
	s.Put(ctx, PutParams{
		NS: "ns", Key: "k", Content: "v2",
		Files: []FileParam{{Path: "b.go", Rel: "created"}},
	})

	// Get latest should show v2's files
	got, err := s.Get(ctx, GetParams{NS: "ns", Key: "k"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got[0].Files) != 1 {
		t.Fatalf("expected 1 file on latest version, got %d", len(got[0].Files))
	}
	if got[0].Files[0].Path != "b.go" {
		t.Errorf("expected b.go, got %q", got[0].Files[0].Path)
	}

	// History should show each version's own files
	hist, err := s.Get(ctx, GetParams{NS: "ns", Key: "k", History: true})
	if err != nil {
		t.Fatalf("get history: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(hist))
	}
	// hist[0] is v2 (newest first)
	if len(hist[0].Files) != 1 || hist[0].Files[0].Path != "b.go" {
		t.Errorf("v2 files: expected [b.go], got %v", hist[0].Files)
	}
	if len(hist[1].Files) != 1 || hist[1].Files[0].Path != "a.go" {
		t.Errorf("v1 files: expected [a.go], got %v", hist[1].Files)
	}
}

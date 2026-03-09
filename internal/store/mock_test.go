package store

import (
	"context"
	"testing"
	"time"

	"github.com/rcliao/ghost/internal/model"
)

func TestMockStorePutGet(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	mem, err := s.Put(ctx, PutParams{NS: "test", Key: "k1", Content: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if mem.NS != "test" || mem.Key != "k1" || mem.Content != "hello" {
		t.Fatalf("unexpected memory: %+v", mem)
	}
	if mem.Version != 1 || mem.Kind != "episodic" || mem.Priority != "normal" {
		t.Fatalf("unexpected defaults: version=%d kind=%s priority=%s (expected episodic for stm tier)", mem.Version, mem.Kind, mem.Priority)
	}

	got, err := s.Get(ctx, GetParams{NS: "test", Key: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content != "hello" {
		t.Fatalf("expected 1 result with 'hello', got %d", len(got))
	}
}

func TestMockStoreVersioning(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "k1", Content: "v1"})
	mem2, _ := s.Put(ctx, PutParams{NS: "test", Key: "k1", Content: "v2"})

	if mem2.Version != 2 {
		t.Fatalf("expected version 2, got %d", mem2.Version)
	}
	if mem2.Supersedes == "" {
		t.Fatal("expected supersedes to be set")
	}

	// Get returns latest
	got, _ := s.Get(ctx, GetParams{NS: "test", Key: "k1"})
	if got[0].Content != "v2" {
		t.Fatalf("expected v2, got %s", got[0].Content)
	}

	// History returns all
	hist, _ := s.Get(ctx, GetParams{NS: "test", Key: "k1", History: true})
	if len(hist) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(hist))
	}
}

func TestMockStoreList(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "ns1", Key: "a", Content: "a", Kind: "semantic", Tags: []string{"go"}})
	s.Put(ctx, PutParams{NS: "ns1", Key: "b", Content: "b", Kind: "episodic"})
	s.Put(ctx, PutParams{NS: "ns2", Key: "c", Content: "c", Kind: "semantic"})

	all, _ := s.List(ctx, ListParams{})
	if len(all) != 3 {
		t.Fatalf("expected 3, got %d", len(all))
	}

	byNS, _ := s.List(ctx, ListParams{NS: "ns1"})
	if len(byNS) != 2 {
		t.Fatalf("expected 2 in ns1, got %d", len(byNS))
	}

	byKind, _ := s.List(ctx, ListParams{Kind: "episodic"})
	if len(byKind) != 1 {
		t.Fatalf("expected 1 episodic, got %d", len(byKind))
	}

	byTag, _ := s.List(ctx, ListParams{Tags: []string{"go"}})
	if len(byTag) != 1 {
		t.Fatalf("expected 1 with tag 'go', got %d", len(byTag))
	}
}

func TestMockStoreRm(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "k1", Content: "hello"})

	if err := s.Rm(ctx, RmParams{NS: "test", Key: "k1"}); err != nil {
		t.Fatal(err)
	}

	_, err := s.Get(ctx, GetParams{NS: "test", Key: "k1"})
	if err == nil {
		t.Fatal("expected error after soft delete")
	}
}

func TestMockStoreRmHard(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "k1", Content: "hello"})
	s.Rm(ctx, RmParams{NS: "test", Key: "k1", Hard: true})

	count, _ := s.MemoryCount(ctx)
	if count != 0 {
		t.Fatalf("expected 0 memories after hard delete, got %d", count)
	}
}

func TestMockStoreSearch(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "k1", Content: "the quick brown fox"})
	s.Put(ctx, PutParams{NS: "test", Key: "k2", Content: "lazy dog"})

	results, err := s.Search(ctx, SearchParams{Query: "fox"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Key != "k1" {
		t.Fatalf("expected 1 result for 'fox', got %d", len(results))
	}
}

func TestMockStoreGC(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "k1", Content: "expires", TTL: "1s"})
	s.Put(ctx, PutParams{NS: "test", Key: "k2", Content: "stays"})

	time.Sleep(2 * time.Second)

	result, err := s.GC(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.MemoriesDeleted != 1 {
		t.Fatalf("expected 1 deleted, got %d", result.MemoriesDeleted)
	}

	count, _ := s.MemoryCount(ctx)
	if count != 1 {
		t.Fatalf("expected 1 remaining, got %d", count)
	}
}

func TestMockStoreGCDryRun(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "k1", Content: "expires", TTL: "1s"})
	time.Sleep(2 * time.Second)

	result, _ := s.GCDryRun(ctx)
	if result.MemoriesDeleted != 1 {
		t.Fatalf("expected 1, got %d", result.MemoriesDeleted)
	}

	// Memory should still exist (dry run)
	count, _ := s.MemoryCount(ctx)
	if count != 0 { // expired, so MemoryCount excludes it
		t.Fatalf("expected 0 active (expired), got %d", count)
	}
}

func TestMockStoreGCStale(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "normal", Content: "old", Priority: "normal"})
	s.Put(ctx, PutParams{NS: "test", Key: "critical", Content: "protected", Priority: "critical"})

	time.Sleep(time.Millisecond) // ensure cutoff is after CreatedAt
	result, err := s.GCStale(ctx, 0) // 0 threshold = everything is stale
	if err != nil {
		t.Fatal(err)
	}
	if result.MemoriesDeleted != 1 {
		t.Fatalf("expected 1 stale deleted, got %d", result.MemoriesDeleted)
	}
	if result.ProtectedCount != 1 {
		t.Fatalf("expected 1 protected, got %d", result.ProtectedCount)
	}
}

func TestMockStoreStats(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "ns1", Key: "a", Content: "a"})
	s.Put(ctx, PutParams{NS: "ns1", Key: "b", Content: "b"})
	s.Put(ctx, PutParams{NS: "ns2", Key: "c", Content: "c"})

	st, err := s.Stats(ctx, "/tmp/test.db")
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveMemories != 3 {
		t.Fatalf("expected 3 active, got %d", st.ActiveMemories)
	}
	if len(st.Namespaces) != 2 {
		t.Fatalf("expected 2 namespaces, got %d", len(st.Namespaces))
	}
}

func TestMockStoreListNamespaces(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "ns1", Key: "a", Content: "a"})
	s.Put(ctx, PutParams{NS: "ns2", Key: "b", Content: "b"})

	ns, err := s.ListNamespaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) != 2 {
		t.Fatalf("expected 2 namespaces, got %d", len(ns))
	}
}

func TestMockStoreExportImport(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "k1", Content: "hello"})
	s.Put(ctx, PutParams{NS: "test", Key: "k2", Content: "world"})

	exported, err := s.ExportAll(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(exported) != 2 {
		t.Fatalf("expected 2 exported, got %d", len(exported))
	}

	s2 := NewMockStore()
	imported, err := s2.Import(ctx, exported)
	if err != nil {
		t.Fatal(err)
	}
	if imported != 2 {
		t.Fatalf("expected 2 imported, got %d", imported)
	}
}

func TestMockStoreLink(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "a", Content: "a"})
	s.Put(ctx, PutParams{NS: "test", Key: "b", Content: "b"})

	link, err := s.Link(ctx, LinkParams{
		FromNS: "test", FromKey: "a",
		ToNS: "test", ToKey: "b",
		Rel: "relates_to",
	})
	if err != nil {
		t.Fatal(err)
	}
	if link.Rel != "relates_to" {
		t.Fatalf("expected relates_to, got %s", link.Rel)
	}

	links, err := s.GetLinks(ctx, link.FromID)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(links))
	}
}

func TestMockStoreLinkInvalidRel(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	_, err := s.Link(ctx, LinkParams{Rel: "bad"})
	if err == nil {
		t.Fatal("expected error for invalid rel")
	}
}

func TestMockStoreFiles(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	mem, err := s.Put(ctx, PutParams{
		NS: "test", Key: "k1", Content: "with files",
		Files: []FileParam{
			{Path: "/foo/bar.go", Rel: "modified"},
			{Path: "/foo/baz.go"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	files, err := s.GetFiles(ctx, mem.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}

	found, err := s.FindByFile(ctx, FindByFileParams{Path: "/foo/bar.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("expected 1 memory for bar.go, got %d", len(found))
	}
}

func TestMockStoreContext(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "k1", Content: "relevant content about golang"})

	result, err := s.Context(ctx, ContextParams{Query: "golang"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Memories) != 1 {
		t.Fatalf("expected 1 context memory, got %d", len(result.Memories))
	}
}

func TestMockStoreClose(t *testing.T) {
	s := NewMockStore()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMockStoreGetNotFound(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	_, err := s.Get(ctx, GetParams{NS: "x", Key: "y"})
	if err == nil {
		t.Fatal("expected not found error")
	}
}

func TestMockStoreRmNotFound(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	err := s.Rm(ctx, RmParams{NS: "x", Key: "y"})
	if err == nil {
		t.Fatal("expected not found error")
	}
}

func TestMockStorePutWithTTL(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	mem, err := s.Put(ctx, PutParams{NS: "test", Key: "k1", Content: "ttl", TTL: "1h"})
	if err != nil {
		t.Fatal(err)
	}
	if mem.ExpiresAt == nil {
		t.Fatal("expected ExpiresAt to be set")
	}
}

func TestMockStorePutInvalidTTL(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	_, err := s.Put(ctx, PutParams{NS: "test", Key: "k1", Content: "bad", TTL: "bad"})
	if err == nil {
		t.Fatal("expected error for invalid TTL")
	}
}

func TestMockStoreExportByNamespace(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "ns1", Key: "a", Content: "a"})
	s.Put(ctx, PutParams{NS: "ns2", Key: "b", Content: "b"})

	exported, _ := s.ExportAll(ctx, "ns1")
	if len(exported) != 1 {
		t.Fatalf("expected 1 exported from ns1, got %d", len(exported))
	}
}

func TestMockStoreRmAllVersions(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "k1", Content: "v1"})
	s.Put(ctx, PutParams{NS: "test", Key: "k1", Content: "v2"})

	s.Rm(ctx, RmParams{NS: "test", Key: "k1", AllVersions: true})

	_, err := s.Get(ctx, GetParams{NS: "test", Key: "k1"})
	if err == nil {
		t.Fatal("expected not found after deleting all versions")
	}
}

func TestMockStoreLinkRemove(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "a", Content: "a"})
	s.Put(ctx, PutParams{NS: "test", Key: "b", Content: "b"})

	link, _ := s.Link(ctx, LinkParams{
		FromNS: "test", FromKey: "a",
		ToNS: "test", ToKey: "b",
		Rel: "relates_to",
	})

	s.Link(ctx, LinkParams{
		FromNS: "test", FromKey: "a",
		ToNS: "test", ToKey: "b",
		Rel: "relates_to", Remove: true,
	})

	links, _ := s.GetLinks(ctx, link.FromID)
	if len(links) != 0 {
		t.Fatalf("expected 0 links after remove, got %d", len(links))
	}
}

func TestMockStoreFindByFileWithRel(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{
		NS: "test", Key: "k1", Content: "c1",
		Files: []FileParam{{Path: "/a.go", Rel: "modified"}},
	})
	s.Put(ctx, PutParams{
		NS: "test", Key: "k2", Content: "c2",
		Files: []FileParam{{Path: "/a.go", Rel: "read"}},
	})

	// Filter by rel
	found, _ := s.FindByFile(ctx, FindByFileParams{Path: "/a.go", Rel: "modified"})
	if len(found) != 1 || found[0].Key != "k1" {
		t.Fatalf("expected 1 modified result, got %d", len(found))
	}

	// No filter
	all, _ := s.FindByFile(ctx, FindByFileParams{Path: "/a.go"})
	if len(all) != 2 {
		t.Fatalf("expected 2 results, got %d", len(all))
	}
}

func TestMockStoreMemoryCount(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "a", Content: "a"})
	s.Put(ctx, PutParams{NS: "test", Key: "b", Content: "b"})

	count, err := s.MemoryCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2, got %d", count)
	}
}

func TestMockStoreImplementsInterface(t *testing.T) {
	// Compile-time check is already in mock.go, this just confirms at test time
	var _ Store = (*MockStore)(nil)
	var _ Store = NewMockStore()
}

func TestMockStoreGetByVersion(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "k1", Content: "v1"})
	s.Put(ctx, PutParams{NS: "test", Key: "k1", Content: "v2"})

	got, err := s.Get(ctx, GetParams{NS: "test", Key: "k1", Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Content != "v1" {
		t.Fatalf("expected v1, got %s", got[0].Content)
	}
}

func TestMockStoreGCStaleDryRun(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "normal", Content: "old", Priority: "normal"})
	s.Put(ctx, PutParams{NS: "test", Key: "high", Content: "protected", Priority: "high"})

	// Small sleep to ensure cutoff is after all CreatedAt timestamps
	time.Sleep(time.Millisecond)

	result, _ := s.GCStaleDryRun(ctx, 0)
	if result.MemoriesDeleted != 1 {
		t.Fatalf("expected 1 stale, got %d", result.MemoriesDeleted)
	}
	if result.ProtectedCount != 1 {
		t.Fatalf("expected 1 protected, got %d", result.ProtectedCount)
	}

	// Confirm nothing actually deleted
	count, _ := s.MemoryCount(ctx)
	if count != 2 {
		t.Fatalf("expected 2 still active after dry run, got %d", count)
	}
}

func TestMockStoreSearchByKey(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "golang-tips", Content: "use gofmt"})

	results, _ := s.Search(ctx, SearchParams{Query: "golang"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result matching key, got %d", len(results))
	}
}

func TestMockStoreFileDefaultRel(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	mem, _ := s.Put(ctx, PutParams{
		NS: "test", Key: "k1", Content: "c",
		Files: []FileParam{{Path: "/a.go"}},
	})

	files, _ := s.GetFiles(ctx, mem.ID)
	if len(files) != 1 || files[0].Rel != "modified" {
		t.Fatalf("expected default rel 'modified', got %+v", files)
	}
}

func TestMockStoreHardDeleteAllVersions(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "k1", Content: "v1"})
	s.Put(ctx, PutParams{NS: "test", Key: "k1", Content: "v2"})

	s.Rm(ctx, RmParams{NS: "test", Key: "k1", Hard: true, AllVersions: true})

	count, _ := s.MemoryCount(ctx)
	if count != 0 {
		t.Fatalf("expected 0, got %d", count)
	}
}

func TestMockStoreHistory(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "k1", Content: "v1"})
	s.Put(ctx, PutParams{NS: "test", Key: "k1", Content: "v2"})
	s.Put(ctx, PutParams{NS: "test", Key: "k1", Content: "v3"})

	hist, err := s.History(ctx, HistoryParams{NS: "test", Key: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 3 {
		t.Fatalf("expected 3 versions, got %d", len(hist))
	}
	// Should be ordered by version ascending
	for i, m := range hist {
		if m.Version != i+1 {
			t.Errorf("version[%d]: want %d, got %d", i, i+1, m.Version)
		}
	}
}

func TestMockStoreHistoryIncludesDeleted(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "test", Key: "k1", Content: "v1"})
	s.Put(ctx, PutParams{NS: "test", Key: "k1", Content: "v2"})
	s.Rm(ctx, RmParams{NS: "test", Key: "k1"}) // soft-delete v2

	hist, err := s.History(ctx, HistoryParams{NS: "test", Key: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 {
		t.Fatalf("expected 2 versions (including deleted), got %d", len(hist))
	}

	hasDeleted := false
	for _, m := range hist {
		if m.DeletedAt != nil {
			hasDeleted = true
		}
	}
	if !hasDeleted {
		t.Error("expected at least one deleted version in history")
	}
}

func TestMockStoreHistoryNotFound(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	_, err := s.History(ctx, HistoryParams{NS: "x", Key: "y"})
	if err == nil {
		t.Fatal("expected not found error")
	}
}

func TestMockStoreListTags(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "ns", Key: "a", Content: "x", Tags: []string{"deploy", "infra"}})
	s.Put(ctx, PutParams{NS: "ns", Key: "b", Content: "y", Tags: []string{"deploy"}})
	s.Put(ctx, PutParams{NS: "ns", Key: "c", Content: "z"})

	tags, err := s.ListTags(ctx, "")
	if err != nil {
		t.Fatal(err)
	}

	tagMap := map[string]int{}
	for _, ti := range tags {
		tagMap[ti.Tag] = ti.Count
	}
	if tagMap["deploy"] != 2 {
		t.Errorf("expected deploy=2, got %d", tagMap["deploy"])
	}
	if tagMap["infra"] != 1 {
		t.Errorf("expected infra=1, got %d", tagMap["infra"])
	}
}

func TestMockStoreListTagsByNamespace(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "proj", Key: "a", Content: "x", Tags: []string{"go"}})
	s.Put(ctx, PutParams{NS: "notes", Key: "b", Content: "y", Tags: []string{"personal"}})

	tags, _ := s.ListTags(ctx, "proj")
	if len(tags) != 1 || tags[0].Tag != "go" {
		t.Fatalf("expected [go], got %v", tags)
	}
}

func TestMockStoreRenameTag(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "ns", Key: "a", Content: "x", Tags: []string{"old", "keep"}})
	s.Put(ctx, PutParams{NS: "ns", Key: "b", Content: "y", Tags: []string{"old"}})

	count, err := s.RenameTag(ctx, "old", "new", "")
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2, got %d", count)
	}

	tags, _ := s.ListTags(ctx, "")
	tagMap := map[string]int{}
	for _, ti := range tags {
		tagMap[ti.Tag] = ti.Count
	}
	if _, ok := tagMap["old"]; ok {
		t.Error("expected old tag to be gone")
	}
	if tagMap["new"] != 2 {
		t.Errorf("expected new=2, got %d", tagMap["new"])
	}
}

func TestMockStoreRemoveTag(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "ns", Key: "a", Content: "x", Tags: []string{"deploy", "infra"}})
	s.Put(ctx, PutParams{NS: "ns", Key: "b", Content: "y", Tags: []string{"deploy"}})

	count, err := s.RemoveTag(ctx, "deploy", "")
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2, got %d", count)
	}

	tags, _ := s.ListTags(ctx, "")
	tagMap := map[string]int{}
	for _, ti := range tags {
		tagMap[ti.Tag] = ti.Count
	}
	if _, ok := tagMap["deploy"]; ok {
		t.Error("expected deploy to be gone")
	}
	if tagMap["infra"] != 1 {
		t.Errorf("expected infra=1, got %d", tagMap["infra"])
	}
}

func TestMockStoreRemoveTagByNamespace(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "proj", Key: "a", Content: "x", Tags: []string{"deploy"}})
	s.Put(ctx, PutParams{NS: "notes", Key: "b", Content: "y", Tags: []string{"deploy"}})

	count, _ := s.RemoveTag(ctx, "deploy", "proj")
	if count != 1 {
		t.Fatalf("expected 1, got %d", count)
	}

	// notes should still have deploy
	tags, _ := s.ListTags(ctx, "notes")
	if len(tags) != 1 || tags[0].Tag != "deploy" {
		t.Fatalf("expected deploy in notes, got %v", tags)
	}
}

func TestMockStoreImportPreservesFields(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	mems := []model.Memory{
		{NS: "ns1", Key: "k1", Content: "c1", Kind: "episodic", Priority: "high", Tags: []string{"t1"}, Meta: `{"x":1}`},
	}

	n, err := s.Import(ctx, mems)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1, got %d", n)
	}

	got, _ := s.Get(ctx, GetParams{NS: "ns1", Key: "k1"})
	if got[0].Kind != "episodic" || got[0].Priority != "high" || got[0].Meta != `{"x":1}` {
		t.Fatalf("fields not preserved: %+v", got[0])
	}
}

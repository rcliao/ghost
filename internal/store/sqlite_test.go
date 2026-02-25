package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dir := t.TempDir()
	s, err := NewSQLiteStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPutAndGet(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	mem, err := s.Put(ctx, PutParams{
		NS: "test", Key: "hello", Content: "world", Kind: "semantic", Priority: "normal",
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if mem.Version != 1 {
		t.Errorf("expected version 1, got %d", mem.Version)
	}
	if mem.ID == "" {
		t.Error("expected non-empty ID")
	}

	got, err := s.Get(ctx, GetParams{NS: "test", Key: "hello"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if got[0].Content != "world" {
		t.Errorf("expected 'world', got %q", got[0].Content)
	}
	// Access count incremented after read, verify with a second get
	got2, _ := s.Get(ctx, GetParams{NS: "test", Key: "hello"})
	if got2[0].AccessCount != 1 {
		t.Errorf("expected access_count 1 after second get, got %d", got2[0].AccessCount)
	}
}

func TestVersioning(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{NS: "ns", Key: "k", Content: "v1"})
	m2, _ := s.Put(ctx, PutParams{NS: "ns", Key: "k", Content: "v2"})

	if m2.Version != 2 {
		t.Errorf("expected version 2, got %d", m2.Version)
	}
	if m2.Supersedes == "" {
		t.Error("expected supersedes to be set")
	}

	// Get latest
	got, _ := s.Get(ctx, GetParams{NS: "ns", Key: "k"})
	if got[0].Content != "v2" {
		t.Errorf("expected 'v2', got %q", got[0].Content)
	}

	// Get history
	hist, _ := s.Get(ctx, GetParams{NS: "ns", Key: "k", History: true})
	if len(hist) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(hist))
	}

	// Get specific version
	v1, _ := s.Get(ctx, GetParams{NS: "ns", Key: "k", Version: 1})
	if v1[0].Content != "v1" {
		t.Errorf("expected 'v1', got %q", v1[0].Content)
	}
}

func TestList(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{NS: "ns", Key: "a", Content: "alpha"})
	s.Put(ctx, PutParams{NS: "ns", Key: "b", Content: "beta"})
	s.Put(ctx, PutParams{NS: "other", Key: "c", Content: "gamma"})

	// List all
	all, _ := s.List(ctx, ListParams{})
	if len(all) != 3 {
		t.Errorf("expected 3, got %d", len(all))
	}

	// List by namespace
	nsOnly, _ := s.List(ctx, ListParams{NS: "ns"})
	if len(nsOnly) != 2 {
		t.Errorf("expected 2, got %d", len(nsOnly))
	}
}

func TestListShowsLatestVersion(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{NS: "ns", Key: "k", Content: "v1"})
	s.Put(ctx, PutParams{NS: "ns", Key: "k", Content: "v2"})

	list, _ := s.List(ctx, ListParams{NS: "ns"})
	if len(list) != 1 {
		t.Fatalf("expected 1 (latest only), got %d", len(list))
	}
	if list[0].Content != "v2" {
		t.Errorf("expected latest 'v2', got %q", list[0].Content)
	}
}

func TestSoftDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{NS: "ns", Key: "k", Content: "data"})
	err := s.Rm(ctx, RmParams{NS: "ns", Key: "k"})
	if err != nil {
		t.Fatalf("rm: %v", err)
	}

	_, err = s.Get(ctx, GetParams{NS: "ns", Key: "k"})
	if err == nil {
		t.Error("expected error after soft delete")
	}
}

func TestHardDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{NS: "ns", Key: "k", Content: "data"})
	err := s.Rm(ctx, RmParams{NS: "ns", Key: "k", Hard: true})
	if err != nil {
		t.Fatalf("rm hard: %v", err)
	}

	_, err = s.Get(ctx, GetParams{NS: "ns", Key: "k"})
	if err == nil {
		t.Error("expected error after hard delete")
	}
}

func TestDeleteAllVersions(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{NS: "ns", Key: "k", Content: "v1"})
	s.Put(ctx, PutParams{NS: "ns", Key: "k", Content: "v2"})

	s.Rm(ctx, RmParams{NS: "ns", Key: "k", AllVersions: true})

	_, err := s.Get(ctx, GetParams{NS: "ns", Key: "k", History: true})
	if err == nil {
		t.Error("expected error after deleting all versions")
	}
}

func TestTags(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{NS: "ns", Key: "a", Content: "x", Tags: []string{"deploy", "infra"}})
	s.Put(ctx, PutParams{NS: "ns", Key: "b", Content: "y", Tags: []string{"deploy"}})
	s.Put(ctx, PutParams{NS: "ns", Key: "c", Content: "z"})

	list, _ := s.List(ctx, ListParams{NS: "ns", Tags: []string{"deploy"}})
	if len(list) != 2 {
		t.Errorf("expected 2 with 'deploy' tag, got %d", len(list))
	}

	list, _ = s.List(ctx, ListParams{NS: "ns", Tags: []string{"infra"}})
	if len(list) != 1 {
		t.Errorf("expected 1 with 'infra' tag, got %d", len(list))
	}
}

func TestDBPathCreation(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sub", "dir", "test.db")
	s, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	s.Close()

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("expected db file to be created")
	}
}

func TestPutWithPriorityAndKind(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	mem, _ := s.Put(ctx, PutParams{
		NS: "ns", Key: "k", Content: "data",
		Kind: "procedural", Priority: "critical",
	})
	if mem.Kind != "procedural" {
		t.Errorf("expected kind 'procedural', got %q", mem.Kind)
	}
	if mem.Priority != "critical" {
		t.Errorf("expected priority 'critical', got %q", mem.Priority)
	}

	got, _ := s.Get(ctx, GetParams{NS: "ns", Key: "k"})
	if got[0].Kind != "procedural" || got[0].Priority != "critical" {
		t.Error("kind/priority not persisted correctly")
	}
}

// createOldSchemaDB creates a SQLite database with the pre-expires_at schema
// (no expires_at column on memories, no embedding column on chunks) and
// inserts seed data, returning the path to the DB file.
func createOldSchemaDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "old.db")

	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatalf("open old db: %v", err)
	}
	defer db.Close()

	// Old schema: no expires_at on memories, no embedding on chunks
	oldSchema := `
	CREATE TABLE memories (
		id          TEXT PRIMARY KEY,
		ns          TEXT NOT NULL,
		key         TEXT NOT NULL,
		content     TEXT NOT NULL,
		kind        TEXT NOT NULL DEFAULT 'semantic',
		tags        TEXT,
		version     INTEGER NOT NULL DEFAULT 1,
		supersedes  TEXT,
		created_at  TEXT NOT NULL,
		deleted_at  TEXT,
		priority    TEXT NOT NULL DEFAULT 'normal',
		access_count INTEGER NOT NULL DEFAULT 0,
		last_accessed_at TEXT,
		meta        TEXT
	);
	CREATE INDEX idx_memories_ns_key ON memories(ns, key);

	CREATE TABLE chunks (
		id          TEXT PRIMARY KEY,
		memory_id   TEXT NOT NULL REFERENCES memories(id),
		seq         INTEGER NOT NULL,
		text        TEXT NOT NULL,
		start_line  INTEGER,
		end_line    INTEGER
	);
	CREATE INDEX idx_chunks_memory ON chunks(memory_id);

	CREATE TABLE memory_links (
		from_id    TEXT NOT NULL REFERENCES memories(id),
		to_id      TEXT NOT NULL REFERENCES memories(id),
		rel        TEXT NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY (from_id, to_id, rel)
	);
	`
	if _, err := db.Exec(oldSchema); err != nil {
		t.Fatalf("create old schema: %v", err)
	}

	// Seed data
	_, err = db.Exec(`INSERT INTO memories (id, ns, key, content, kind, version, created_at, priority, access_count)
		VALUES ('OLD001', 'test', 'greeting', 'hello world', 'semantic', 1, '2025-01-01T00:00:00Z', 'normal', 0)`)
	if err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	_, err = db.Exec(`INSERT INTO chunks (id, memory_id, seq, text, start_line, end_line)
		VALUES ('CHK001', 'OLD001', 0, 'hello world', 0, 0)`)
	if err != nil {
		t.Fatalf("seed chunk: %v", err)
	}

	return dbPath
}

func TestGC(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// Insert a memory with a TTL that's already expired (1s TTL, then we manipulate expires_at)
	mem, err := s.Put(ctx, PutParams{NS: "ns", Key: "ephemeral", Content: "temp", TTL: "1s"})
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// Insert a memory with no TTL (should survive GC)
	_, err = s.Put(ctx, PutParams{NS: "ns", Key: "permanent", Content: "keep"})
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// Manually set expires_at to the past so GC picks it up
	_, err = s.db.ExecContext(ctx,
		`UPDATE memories SET expires_at = '2000-01-01T00:00:00Z' WHERE id = ?`, mem.ID)
	if err != nil {
		t.Fatalf("update expires_at: %v", err)
	}

	// Run GC
	result, err := s.GC(ctx)
	if err != nil {
		t.Fatalf("gc: %v", err)
	}
	if result.MemoriesDeleted != 1 {
		t.Errorf("expected 1 deleted, got %d", result.MemoriesDeleted)
	}
	if result.ChunksFreed != 1 {
		t.Errorf("expected 1 chunk freed, got %d", result.ChunksFreed)
	}

	// Verify expired memory is gone
	_, err = s.Get(ctx, GetParams{NS: "ns", Key: "ephemeral"})
	if err == nil {
		t.Error("expected error getting expired memory after GC")
	}

	// Verify permanent memory survives
	got, err := s.Get(ctx, GetParams{NS: "ns", Key: "permanent"})
	if err != nil {
		t.Fatalf("get permanent: %v", err)
	}
	if got[0].Content != "keep" {
		t.Errorf("expected 'keep', got %q", got[0].Content)
	}

	// Verify chunks were cleaned up
	var chunkCount int
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chunks WHERE memory_id = ?`, mem.ID).Scan(&chunkCount)
	if err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if chunkCount != 0 {
		t.Errorf("expected 0 chunks for deleted memory, got %d", chunkCount)
	}
}

func TestGCDryRun(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// Insert a memory with TTL and set it to expired
	mem, err := s.Put(ctx, PutParams{NS: "ns", Key: "ephemeral", Content: "temp", TTL: "1s"})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE memories SET expires_at = '2000-01-01T00:00:00Z' WHERE id = ?`, mem.ID)
	if err != nil {
		t.Fatalf("update expires_at: %v", err)
	}

	// Insert a permanent memory
	_, err = s.Put(ctx, PutParams{NS: "ns", Key: "permanent", Content: "keep"})
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// Dry run should report 1
	result, err := s.GCDryRun(ctx)
	if err != nil {
		t.Fatalf("gc dry-run: %v", err)
	}
	if result.MemoriesDeleted != 1 {
		t.Errorf("expected 1 would_delete, got %d", result.MemoriesDeleted)
	}
	if result.ChunksFreed != 1 {
		t.Errorf("expected 1 chunk, got %d", result.ChunksFreed)
	}

	// Verify nothing was actually deleted
	var memCount int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memories`).Scan(&memCount)
	if err != nil {
		t.Fatalf("count memories: %v", err)
	}
	if memCount != 2 {
		t.Errorf("expected 2 memories still present, got %d", memCount)
	}
}

func TestGCNoExpired(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// Insert memories with no TTL
	s.Put(ctx, PutParams{NS: "ns", Key: "a", Content: "alpha"})
	s.Put(ctx, PutParams{NS: "ns", Key: "b", Content: "beta"})

	result, err := s.GC(ctx)
	if err != nil {
		t.Fatalf("gc: %v", err)
	}
	if result.MemoriesDeleted != 0 {
		t.Errorf("expected 0 deleted, got %d", result.MemoriesDeleted)
	}
}

func TestMigrateOldSchemaAddsExpiresAt(t *testing.T) {
	ctx := context.Background()

	// Create a DB with old schema (no expires_at, no embedding)
	dbPath := createOldSchemaDB(t)

	// Open with NewSQLiteStore, which triggers migrate()
	s, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore on old DB: %v", err)
	}
	defer s.Close()

	// Verify old data survived migration
	got, err := s.Get(ctx, GetParams{NS: "test", Key: "greeting"})
	if err != nil {
		t.Fatalf("get old memory: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if got[0].Content != "hello world" {
		t.Errorf("expected 'hello world', got %q", got[0].Content)
	}
	if got[0].ExpiresAt != nil {
		t.Errorf("expected nil expires_at for old data, got %v", got[0].ExpiresAt)
	}

	// Verify new data can use expires_at (put with TTL)
	mem, err := s.Put(ctx, PutParams{
		NS: "test", Key: "ephemeral", Content: "temp data", TTL: "1h",
	})
	if err != nil {
		t.Fatalf("put with TTL: %v", err)
	}
	if mem.ExpiresAt == nil {
		t.Error("expected expires_at to be set for TTL memory")
	}

	// Verify listing works with migrated schema
	list, err := s.List(ctx, ListParams{NS: "test"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 memories, got %d", len(list))
	}

	// Verify search works (FTS5 was set up by migration)
	results, err := s.Search(ctx, SearchParams{Query: "hello", NS: "test"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected search to find 'hello world' after migration")
	}

	// Verify the expires_at column exists by querying it directly
	var expiresAt sql.NullString
	err = s.db.QueryRowContext(ctx,
		`SELECT expires_at FROM memories WHERE id = 'OLD001'`).Scan(&expiresAt)
	if err != nil {
		t.Fatalf("query expires_at column: %v", err)
	}
	if expiresAt.Valid {
		t.Errorf("expected NULL expires_at for old record, got %q", expiresAt.String)
	}

	// Verify the embedding column was added to chunks
	var embeddingCol sql.NullString
	err = s.db.QueryRowContext(ctx,
		`SELECT embedding FROM chunks WHERE id = 'CHK001'`).Scan(&embeddingCol)
	if err != nil {
		t.Fatalf("query embedding column: %v", err)
	}
	if embeddingCol.Valid {
		t.Errorf("expected NULL embedding for old chunk, got %q", embeddingCol.String)
	}
}

func TestNewSQLiteStoreAutoGCsExpired(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "autogc.db")

	// Create a store, seed data, then close it
	s1, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	ctx := context.Background()

	// Insert a memory with TTL and backdate its expires_at to the past
	expired, err := s1.Put(ctx, PutParams{NS: "ns", Key: "expired1", Content: "gone soon", TTL: "1s"})
	if err != nil {
		t.Fatalf("put expired: %v", err)
	}
	s1.db.ExecContext(ctx, `UPDATE memories SET expires_at = '2000-01-01T00:00:00Z' WHERE id = ?`, expired.ID)

	// Insert another expired memory
	expired2, err := s1.Put(ctx, PutParams{NS: "ns", Key: "expired2", Content: "also gone", TTL: "1s"})
	if err != nil {
		t.Fatalf("put expired2: %v", err)
	}
	s1.db.ExecContext(ctx, `UPDATE memories SET expires_at = '2000-01-01T00:00:00Z' WHERE id = ?`, expired2.ID)

	// Insert a permanent memory (no TTL)
	_, err = s1.Put(ctx, PutParams{NS: "ns", Key: "permanent", Content: "keep me"})
	if err != nil {
		t.Fatalf("put permanent: %v", err)
	}

	// Verify all 3 rows exist before closing
	var total int
	s1.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memories`).Scan(&total)
	if total != 3 {
		t.Fatalf("expected 3 memories seeded, got %d", total)
	}
	s1.Close()

	// Re-open — NewSQLiteStore should auto-GC the expired rows
	s2, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("re-open store: %v", err)
	}
	defer s2.Close()

	// Expired memories should be gone (hard-deleted by GC)
	var remaining int
	s2.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memories`).Scan(&remaining)
	if remaining != 1 {
		t.Errorf("expected 1 memory after auto-GC, got %d", remaining)
	}

	// Chunks for expired memories should also be gone
	var chunkCount int
	s2.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks WHERE memory_id IN (?, ?)`, expired.ID, expired2.ID).Scan(&chunkCount)
	if chunkCount != 0 {
		t.Errorf("expected 0 chunks for expired memories, got %d", chunkCount)
	}

	// Permanent memory should still be accessible
	got, err := s2.Get(ctx, GetParams{NS: "ns", Key: "permanent"})
	if err != nil {
		t.Fatalf("get permanent: %v", err)
	}
	if got[0].Content != "keep me" {
		t.Errorf("expected 'keep me', got %q", got[0].Content)
	}
}

func TestGCStale(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// Insert a normal-priority memory and backdate its created_at to 60 days ago
	m1, err := s.Put(ctx, PutParams{NS: "ns", Key: "old-normal", Content: "stale data", Priority: "normal"})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	sixtyDaysAgo := time.Now().UTC().Add(-60 * 24 * time.Hour).Format(time.RFC3339)
	s.db.ExecContext(ctx, `UPDATE memories SET created_at = ?, last_accessed_at = NULL WHERE id = ?`, sixtyDaysAgo, m1.ID)

	// Insert a high-priority memory also backdated (should be skipped)
	m2, err := s.Put(ctx, PutParams{NS: "ns", Key: "old-high", Content: "important", Priority: "high"})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	s.db.ExecContext(ctx, `UPDATE memories SET created_at = ?, last_accessed_at = NULL WHERE id = ?`, sixtyDaysAgo, m2.ID)

	// Insert a critical-priority memory also backdated (should be skipped)
	m3, err := s.Put(ctx, PutParams{NS: "ns", Key: "old-critical", Content: "critical", Priority: "critical"})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	s.db.ExecContext(ctx, `UPDATE memories SET created_at = ?, last_accessed_at = NULL WHERE id = ?`, sixtyDaysAgo, m3.ID)

	// Insert a recent normal-priority memory (should survive)
	_, err = s.Put(ctx, PutParams{NS: "ns", Key: "recent", Content: "fresh", Priority: "normal"})
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// Run GCStale with 30-day threshold
	result, err := s.GCStale(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("gc stale: %v", err)
	}
	if result.MemoriesDeleted != 1 {
		t.Errorf("expected 1 soft-deleted, got %d", result.MemoriesDeleted)
	}
	if result.ProtectedCount != 2 {
		t.Errorf("expected 2 protected (high+critical), got %d", result.ProtectedCount)
	}

	// Verify stale normal memory is soft-deleted (not found by Get)
	_, err = s.Get(ctx, GetParams{NS: "ns", Key: "old-normal"})
	if err == nil {
		t.Error("expected error getting stale normal memory")
	}

	// Verify high-priority memory survives
	got, err := s.Get(ctx, GetParams{NS: "ns", Key: "old-high"})
	if err != nil {
		t.Fatalf("get old-high: %v", err)
	}
	if got[0].Content != "important" {
		t.Errorf("expected 'important', got %q", got[0].Content)
	}

	// Verify critical-priority memory survives
	got, err = s.Get(ctx, GetParams{NS: "ns", Key: "old-critical"})
	if err != nil {
		t.Fatalf("get old-critical: %v", err)
	}
	if got[0].Content != "critical" {
		t.Errorf("expected 'critical', got %q", got[0].Content)
	}

	// Verify recent memory survives
	got, err = s.Get(ctx, GetParams{NS: "ns", Key: "recent"})
	if err != nil {
		t.Fatalf("get recent: %v", err)
	}
	if got[0].Content != "fresh" {
		t.Errorf("expected 'fresh', got %q", got[0].Content)
	}
}

func TestGCStaleUsesLastAccessedAt(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// Insert a memory created 60 days ago but accessed recently
	m, err := s.Put(ctx, PutParams{NS: "ns", Key: "old-but-accessed", Content: "accessed", Priority: "normal"})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	sixtyDaysAgo := time.Now().UTC().Add(-60 * 24 * time.Hour).Format(time.RFC3339)
	recentAccess := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	s.db.ExecContext(ctx, `UPDATE memories SET created_at = ?, last_accessed_at = ? WHERE id = ?`, sixtyDaysAgo, recentAccess, m.ID)

	// GCStale with 30d threshold should NOT delete it (accessed recently)
	result, err := s.GCStale(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("gc stale: %v", err)
	}
	if result.MemoriesDeleted != 0 {
		t.Errorf("expected 0 deleted (accessed recently), got %d", result.MemoriesDeleted)
	}

	// Verify memory still accessible
	got, err := s.Get(ctx, GetParams{NS: "ns", Key: "old-but-accessed"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got[0].Content != "accessed" {
		t.Errorf("expected 'accessed', got %q", got[0].Content)
	}
}

func TestGCStaleDryRun(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// Insert a stale normal-priority memory
	m, err := s.Put(ctx, PutParams{NS: "ns", Key: "stale", Content: "old", Priority: "normal"})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	sixtyDaysAgo := time.Now().UTC().Add(-60 * 24 * time.Hour).Format(time.RFC3339)
	s.db.ExecContext(ctx, `UPDATE memories SET created_at = ?, last_accessed_at = NULL WHERE id = ?`, sixtyDaysAgo, m.ID)

	// Insert a stale high-priority memory (should be skipped)
	m2, err := s.Put(ctx, PutParams{NS: "ns", Key: "stale-high", Content: "important", Priority: "high"})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	s.db.ExecContext(ctx, `UPDATE memories SET created_at = ?, last_accessed_at = NULL WHERE id = ?`, sixtyDaysAgo, m2.ID)

	// Dry run should report 1 (only the normal one)
	result, err := s.GCStaleDryRun(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("gc stale dry-run: %v", err)
	}
	if result.MemoriesDeleted != 1 {
		t.Errorf("expected 1 would_delete, got %d", result.MemoriesDeleted)
	}
	if result.ProtectedCount != 1 {
		t.Errorf("expected 1 protected (high), got %d", result.ProtectedCount)
	}

	// Verify nothing was actually deleted
	var memCount int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memories WHERE deleted_at IS NULL`).Scan(&memCount)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if memCount != 2 {
		t.Errorf("expected 2 active memories, got %d", memCount)
	}
}

func TestListPrefixMatch(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{NS: "reflect:agent-memory", Key: "k1", Content: "c1"})
	s.Put(ctx, PutParams{NS: "reflect:other", Key: "k2", Content: "c2"})
	s.Put(ctx, PutParams{NS: "project", Key: "k3", Content: "c3"})

	// Prefix match
	results, err := s.List(ctx, ListParams{NS: "reflect:*"})
	if err != nil {
		t.Fatalf("list prefix: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results for reflect:*, got %d", len(results))
	}

	// Exact match
	results, _ = s.List(ctx, ListParams{NS: "project"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result for exact project, got %d", len(results))
	}
}

func TestSearchPrefixMatch(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{NS: "reflect:agent-memory", Key: "k1", Content: "hello world"})
	s.Put(ctx, PutParams{NS: "reflect:other", Key: "k2", Content: "hello there"})
	s.Put(ctx, PutParams{NS: "project", Key: "k3", Content: "hello project"})

	results, err := s.Search(ctx, SearchParams{NS: "reflect:*", Query: "hello"})
	if err != nil {
		t.Fatalf("search prefix: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results for reflect:* search, got %d", len(results))
	}
}

func TestRmNamespace(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{NS: "reflect:agent-memory", Key: "k1", Content: "c1"})
	s.Put(ctx, PutParams{NS: "reflect:other", Key: "k2", Content: "c2"})
	s.Put(ctx, PutParams{NS: "project", Key: "k3", Content: "c3"})

	// Soft delete by prefix
	count, err := s.RmNamespace(ctx, "reflect:*", false)
	if err != nil {
		t.Fatalf("RmNamespace: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 deleted, got %d", count)
	}

	// Verify reflect:* memories are gone
	results, _ := s.List(ctx, ListParams{NS: "reflect:*"})
	if len(results) != 0 {
		t.Fatalf("expected 0 reflect:* after rm, got %d", len(results))
	}

	// Verify project is still there
	results, _ = s.List(ctx, ListParams{NS: "project"})
	if len(results) != 1 {
		t.Fatalf("expected project to survive, got %d", len(results))
	}
}

func TestRmNamespaceExact(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{NS: "ns1", Key: "a", Content: "a"})
	s.Put(ctx, PutParams{NS: "ns1", Key: "b", Content: "b"})
	s.Put(ctx, PutParams{NS: "ns2", Key: "c", Content: "c"})

	count, err := s.RmNamespace(ctx, "ns1", false)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2 deleted, got %d", count)
	}

	remaining, _ := s.MemoryCount(ctx)
	if remaining != 1 {
		t.Fatalf("expected 1 remaining, got %d", remaining)
	}
}

func TestRmNamespaceHard(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{NS: "ns1", Key: "a", Content: "a"})
	s.Put(ctx, PutParams{NS: "ns1", Key: "b", Content: "b"})

	count, err := s.RmNamespace(ctx, "ns1", true)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2 hard-deleted, got %d", count)
	}

	// Should be truly gone (not just soft-deleted)
	var total int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memories`).Scan(&total)
	if total != 0 {
		t.Fatalf("expected 0 memories after hard delete, got %d", total)
	}
}

func TestPutValidatesNamespace(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	_, err := s.Put(ctx, PutParams{NS: "bad ns", Key: "k", Content: "c"})
	if err == nil {
		t.Fatal("expected error for invalid namespace")
	}

	_, err = s.Put(ctx, PutParams{NS: ":leading", Key: "k", Content: "c"})
	if err == nil {
		t.Fatal("expected error for leading colon")
	}

	// Valid namespace should work
	_, err = s.Put(ctx, PutParams{NS: "valid-ns:sub", Key: "k", Content: "c"})
	if err != nil {
		t.Fatalf("unexpected error for valid ns: %v", err)
	}
}

func TestHistory(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{NS: "ns", Key: "k", Content: "v1"})
	s.Put(ctx, PutParams{NS: "ns", Key: "k", Content: "v2"})
	s.Put(ctx, PutParams{NS: "ns", Key: "k", Content: "v3"})

	hist, err := s.History(ctx, HistoryParams{NS: "ns", Key: "k"})
	if err != nil {
		t.Fatalf("history: %v", err)
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
	if hist[0].Content != "v1" || hist[2].Content != "v3" {
		t.Errorf("unexpected content ordering: %q, %q", hist[0].Content, hist[2].Content)
	}
}

func TestHistoryIncludesDeleted(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{NS: "ns", Key: "k", Content: "v1"})
	s.Put(ctx, PutParams{NS: "ns", Key: "k", Content: "v2"})
	s.Rm(ctx, RmParams{NS: "ns", Key: "k"}) // soft-delete v2

	hist, err := s.History(ctx, HistoryParams{NS: "ns", Key: "k"})
	if err != nil {
		t.Fatalf("history: %v", err)
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

func TestHistoryNotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	_, err := s.History(ctx, HistoryParams{NS: "x", Key: "y"})
	if err == nil {
		t.Fatal("expected not found error")
	}
}

func TestListTags(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{NS: "ns", Key: "a", Content: "x", Tags: []string{"deploy", "infra"}})
	s.Put(ctx, PutParams{NS: "ns", Key: "b", Content: "y", Tags: []string{"deploy", "docs"}})
	s.Put(ctx, PutParams{NS: "ns", Key: "c", Content: "z"}) // no tags

	tags, err := s.ListTags(ctx, "")
	if err != nil {
		t.Fatalf("list tags: %v", err)
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
	if tagMap["docs"] != 1 {
		t.Errorf("expected docs=1, got %d", tagMap["docs"])
	}
}

func TestListTagsByNamespace(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{NS: "proj", Key: "a", Content: "x", Tags: []string{"go", "deploy"}})
	s.Put(ctx, PutParams{NS: "notes", Key: "b", Content: "y", Tags: []string{"personal", "deploy"}})

	tags, err := s.ListTags(ctx, "proj")
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}

	tagMap := map[string]int{}
	for _, ti := range tags {
		tagMap[ti.Tag] = ti.Count
	}

	if tagMap["go"] != 1 {
		t.Errorf("expected go=1 in proj, got %d", tagMap["go"])
	}
	if tagMap["deploy"] != 1 {
		t.Errorf("expected deploy=1 in proj, got %d", tagMap["deploy"])
	}
	if _, ok := tagMap["personal"]; ok {
		t.Error("expected personal tag to NOT appear in proj namespace")
	}
}

func TestListTagsLatestVersionOnly(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// v1 has "old" tag, v2 replaces with "new"
	s.Put(ctx, PutParams{NS: "ns", Key: "k", Content: "v1", Tags: []string{"old"}})
	s.Put(ctx, PutParams{NS: "ns", Key: "k", Content: "v2", Tags: []string{"new"}})

	tags, err := s.ListTags(ctx, "")
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}

	tagMap := map[string]int{}
	for _, ti := range tags {
		tagMap[ti.Tag] = ti.Count
	}

	if _, ok := tagMap["old"]; ok {
		t.Error("expected old tag to NOT appear (superseded by v2)")
	}
	if tagMap["new"] != 1 {
		t.Errorf("expected new=1, got %d", tagMap["new"])
	}
}

func TestRenameTag(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{NS: "ns", Key: "a", Content: "x", Tags: []string{"deploy", "infra"}})
	s.Put(ctx, PutParams{NS: "ns", Key: "b", Content: "y", Tags: []string{"deploy"}})
	s.Put(ctx, PutParams{NS: "ns", Key: "c", Content: "z", Tags: []string{"other"}})

	count, err := s.RenameTag(ctx, "deploy", "release", "")
	if err != nil {
		t.Fatalf("rename tag: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 affected, got %d", count)
	}

	// Verify old tag is gone, new tag is present
	tags, _ := s.ListTags(ctx, "")
	tagMap := map[string]int{}
	for _, ti := range tags {
		tagMap[ti.Tag] = ti.Count
	}
	if _, ok := tagMap["deploy"]; ok {
		t.Error("expected deploy tag to be gone after rename")
	}
	if tagMap["release"] != 2 {
		t.Errorf("expected release=2, got %d", tagMap["release"])
	}
	if tagMap["infra"] != 1 {
		t.Errorf("expected infra=1 (unchanged), got %d", tagMap["infra"])
	}
}

func TestRenameTagDeduplicates(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// Memory has both "old" and "new" — rename should deduplicate
	s.Put(ctx, PutParams{NS: "ns", Key: "a", Content: "x", Tags: []string{"old", "new"}})

	count, err := s.RenameTag(ctx, "old", "new", "")
	if err != nil {
		t.Fatalf("rename tag: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 affected, got %d", count)
	}

	// Verify only one "new" tag
	got, _ := s.Get(ctx, GetParams{NS: "ns", Key: "a"})
	newCount := 0
	for _, t := range got[0].Tags {
		if t == "new" {
			newCount++
		}
	}
	if newCount != 1 {
		t.Errorf("expected exactly 1 'new' tag, got %d in %v", newCount, got[0].Tags)
	}
}

func TestRenameTagNotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{NS: "ns", Key: "a", Content: "x", Tags: []string{"keep"}})

	count, err := s.RenameTag(ctx, "missing", "new", "")
	if err != nil {
		t.Fatalf("rename tag: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 affected for missing tag, got %d", count)
	}
}

func TestRemoveTag(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{NS: "ns", Key: "a", Content: "x", Tags: []string{"deploy", "infra"}})
	s.Put(ctx, PutParams{NS: "ns", Key: "b", Content: "y", Tags: []string{"deploy"}})
	s.Put(ctx, PutParams{NS: "ns", Key: "c", Content: "z", Tags: []string{"other"}})

	count, err := s.RemoveTag(ctx, "deploy", "")
	if err != nil {
		t.Fatalf("remove tag: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 affected, got %d", count)
	}

	// Verify deploy is gone
	tags, _ := s.ListTags(ctx, "")
	tagMap := map[string]int{}
	for _, ti := range tags {
		tagMap[ti.Tag] = ti.Count
	}
	if _, ok := tagMap["deploy"]; ok {
		t.Error("expected deploy to be gone after remove")
	}
	if tagMap["infra"] != 1 {
		t.Errorf("expected infra=1 (unchanged), got %d", tagMap["infra"])
	}
}

func TestRemoveTagSetsNullWhenEmpty(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{NS: "ns", Key: "a", Content: "x", Tags: []string{"only"}})

	count, err := s.RemoveTag(ctx, "only", "")
	if err != nil {
		t.Fatalf("remove tag: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 affected, got %d", count)
	}

	// Verify memory still exists with no tags
	got, err := s.Get(ctx, GetParams{NS: "ns", Key: "a"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got[0].Tags) != 0 {
		t.Errorf("expected empty tags, got %v", got[0].Tags)
	}
}

func TestRemoveTagByNamespace(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{NS: "proj", Key: "a", Content: "x", Tags: []string{"deploy"}})
	s.Put(ctx, PutParams{NS: "notes", Key: "b", Content: "y", Tags: []string{"deploy"}})

	count, err := s.RemoveTag(ctx, "deploy", "proj")
	if err != nil {
		t.Fatalf("remove tag: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 affected (only proj), got %d", count)
	}

	// Verify notes still has deploy
	tags, _ := s.ListTags(ctx, "notes")
	found := false
	for _, ti := range tags {
		if ti.Tag == "deploy" {
			found = true
		}
	}
	if !found {
		t.Error("expected deploy tag to still exist in notes namespace")
	}
}

func TestExportPrefixMatch(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.Put(ctx, PutParams{NS: "reflect:a", Key: "k1", Content: "c1"})
	s.Put(ctx, PutParams{NS: "reflect:b", Key: "k2", Content: "c2"})
	s.Put(ctx, PutParams{NS: "project", Key: "k3", Content: "c3"})

	results, err := s.ExportAll(ctx, "reflect:*")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 exported for reflect:*, got %d", len(results))
	}
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	_ "modernc.org/sqlite"

	"github.com/rcliao/agent-memory/internal/chunker"
	"github.com/rcliao/agent-memory/internal/embedding"
	"github.com/rcliao/agent-memory/internal/model"
)

// SQLiteStore implements Store using SQLite.
type SQLiteStore struct {
	db       *sql.DB
	entropy  *rand.Rand
	embedder embedding.Embedder
}

// NewSQLiteStore opens or creates a SQLite database at the given path.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=foreign_keys(on)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	s := &SQLiteStore{
		db:       db,
		entropy:  rand.New(rand.NewSource(time.Now().UnixNano())),
		embedder: embedding.NewFromEnv(),
	}

	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	// Auto-GC: silently delete expired memories on startup
	s.GC(context.Background())

	return s, nil
}

func (s *SQLiteStore) newID() string {
	return ulid.MustNew(ulid.Timestamp(time.Now()), s.entropy).String()
}

func (s *SQLiteStore) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS memories (
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
		meta        TEXT,
		expires_at  TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_memories_ns_key ON memories(ns, key);
	CREATE INDEX IF NOT EXISTS idx_memories_ns_kind ON memories(ns, kind);
	CREATE INDEX IF NOT EXISTS idx_memories_created ON memories(created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_memories_deleted ON memories(deleted_at);
	CREATE INDEX IF NOT EXISTS idx_memories_priority ON memories(ns, priority);

	CREATE TABLE IF NOT EXISTS chunks (
		id          TEXT PRIMARY KEY,
		memory_id   TEXT NOT NULL REFERENCES memories(id),
		seq         INTEGER NOT NULL,
		text        TEXT NOT NULL,
		start_line  INTEGER,
		end_line    INTEGER,
		embedding   TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_chunks_memory ON chunks(memory_id);

	CREATE TABLE IF NOT EXISTS memory_links (
		from_id    TEXT NOT NULL REFERENCES memories(id),
		to_id      TEXT NOT NULL REFERENCES memories(id),
		rel        TEXT NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY (from_id, to_id, rel)
	);
	CREATE INDEX IF NOT EXISTS idx_links_to ON memory_links(to_id);

	CREATE TABLE IF NOT EXISTS memory_files (
		memory_id  TEXT NOT NULL REFERENCES memories(id),
		path       TEXT NOT NULL,
		rel        TEXT NOT NULL DEFAULT 'modified',
		created_at TEXT NOT NULL,
		PRIMARY KEY (memory_id, path)
	);
	CREATE INDEX IF NOT EXISTS idx_memory_files_path ON memory_files(path);

	CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
		text,
		content=chunks,
		content_rowid=rowid
	);
	`
	_, err := s.db.Exec(schema)
	if err != nil {
		return err
	}

	// Schema upgrades for older databases
	s.db.Exec(`ALTER TABLE memories ADD COLUMN expires_at TEXT`)
	s.db.Exec(`ALTER TABLE chunks ADD COLUMN embedding TEXT`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_memories_expires ON memories(expires_at)`)
	s.db.Exec(`CREATE TABLE IF NOT EXISTS memory_files (
		memory_id TEXT NOT NULL REFERENCES memories(id),
		path      TEXT NOT NULL,
		rel       TEXT NOT NULL DEFAULT 'modified',
		created_at TEXT NOT NULL,
		PRIMARY KEY (memory_id, path)
	)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_memory_files_path ON memory_files(path)`)

	// FTS5 triggers for automatic sync
	s.db.Exec(`CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN
		INSERT INTO chunks_fts(rowid, text) VALUES (new.rowid, new.text);
	END`)
	s.db.Exec(`CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
		INSERT INTO chunks_fts(chunks_fts, rowid, text) VALUES('delete', old.rowid, old.text);
	END`)
	s.db.Exec(`CREATE TRIGGER IF NOT EXISTS chunks_au AFTER UPDATE ON chunks BEGIN
		INSERT INTO chunks_fts(chunks_fts, rowid, text) VALUES('delete', old.rowid, old.text);
		INSERT INTO chunks_fts(rowid, text) VALUES (new.rowid, new.text);
	END`)

	// Backfill FTS for any existing chunks not yet indexed
	s.db.Exec(`INSERT OR IGNORE INTO chunks_fts(rowid, text) SELECT rowid, text FROM chunks`)

	return nil
}

func (s *SQLiteStore) Put(ctx context.Context, p PutParams) (*model.Memory, error) {
	now := time.Now().UTC()
	id := s.newID()

	kind := p.Kind
	if kind == "" {
		kind = "semantic"
	}
	priority := p.Priority
	if priority == "" {
		priority = "normal"
	}

	var tagsJSON *string
	if len(p.Tags) > 0 {
		b, _ := json.Marshal(p.Tags)
		s := string(b)
		tagsJSON = &s
	}

	var metaPtr *string
	if p.Meta != "" {
		metaPtr = &p.Meta
	}

	var expiresAt *string
	if p.TTL != "" {
		d, err := ParseTTL(p.TTL)
		if err != nil {
			return nil, fmt.Errorf("invalid ttl: %w", err)
		}
		exp := now.Add(d).Format(time.RFC3339)
		expiresAt = &exp
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Check for existing latest version
	var prevID string
	var prevVersion int
	err = tx.QueryRowContext(ctx,
		`SELECT id, version FROM memories
		 WHERE ns = ? AND key = ? AND deleted_at IS NULL
		 ORDER BY version DESC LIMIT 1`, p.NS, p.Key).Scan(&prevID, &prevVersion)

	version := 1
	var supersedes *string
	if err == nil {
		version = prevVersion + 1
		supersedes = &prevID
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO memories (id, ns, key, content, kind, tags, version, supersedes, created_at, priority, access_count, meta, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		id, p.NS, p.Key, p.Content, kind, tagsJSON, version, supersedes,
		now.Format(time.RFC3339), priority, metaPtr, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("insert memory: %w", err)
	}

	// Chunk the content
	chunks := chunker.Chunk(p.Content, chunker.DefaultOptions())
	for i, c := range chunks {
		chunkID := s.newID()

		// Generate embedding if provider is configured
		var embeddingJSON *string
		if s.embedder != nil {
			vec, err := s.embedder.Embed(ctx, c.Text)
			if err == nil && len(vec) > 0 {
				b, _ := json.Marshal(vec)
				str := string(b)
				embeddingJSON = &str
			}
			// Silently skip embedding errors — FTS5 still works
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO chunks (id, memory_id, seq, text, start_line, end_line, embedding)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			chunkID, id, i, c.Text, c.StartLine, c.EndLine, embeddingJSON)
		if err != nil {
			return nil, fmt.Errorf("insert chunk: %w", err)
		}
	}

	// Insert file references
	var fileRefs []model.FileRef
	for _, f := range p.Files {
		rel := f.Rel
		if rel == "" {
			rel = "modified"
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO memory_files (memory_id, path, rel, created_at) VALUES (?, ?, ?, ?)`,
			id, f.Path, rel, now.Format(time.RFC3339))
		if err != nil {
			return nil, fmt.Errorf("insert file ref: %w", err)
		}
		fileRefs = append(fileRefs, model.FileRef{Path: f.Path, Rel: rel})
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	mem := &model.Memory{
		ID:         id,
		NS:         p.NS,
		Key:        p.Key,
		Content:    p.Content,
		Kind:       kind,
		Tags:       p.Tags,
		Version:    version,
		CreatedAt:  now,
		Priority:   priority,
		Meta:       p.Meta,
		ChunkCount: len(chunks),
		Files:      fileRefs,
	}
	if expiresAt != nil {
		t, _ := time.Parse(time.RFC3339, *expiresAt)
		mem.ExpiresAt = &t
	}
	if supersedes != nil {
		mem.Supersedes = *supersedes
	}

	return mem, nil
}

func (s *SQLiteStore) Get(ctx context.Context, p GetParams) ([]model.Memory, error) {
	var query string
	var args []interface{}

	now := time.Now().UTC().Format(time.RFC3339)

	if p.History {
		// History shows all versions including expired (for audit)
		query = `SELECT id, ns, key, content, kind, tags, version, supersedes,
				        created_at, deleted_at, priority, access_count, last_accessed_at, meta, expires_at
				 FROM memories WHERE ns = ? AND key = ? AND deleted_at IS NULL
				 ORDER BY version DESC`
		args = []interface{}{p.NS, p.Key}
	} else if p.Version > 0 {
		query = `SELECT id, ns, key, content, kind, tags, version, supersedes,
				        created_at, deleted_at, priority, access_count, last_accessed_at, meta, expires_at
				 FROM memories WHERE ns = ? AND key = ? AND version = ? AND deleted_at IS NULL
				   AND (expires_at IS NULL OR expires_at > ?)
				 LIMIT 1`
		args = []interface{}{p.NS, p.Key, p.Version, now}
	} else {
		query = `SELECT id, ns, key, content, kind, tags, version, supersedes,
				        created_at, deleted_at, priority, access_count, last_accessed_at, meta, expires_at
				 FROM memories WHERE ns = ? AND key = ? AND deleted_at IS NULL
				   AND (expires_at IS NULL OR expires_at > ?)
				 ORDER BY version DESC LIMIT 1`
		args = []interface{}{p.NS, p.Key, now}
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var memories []model.Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		memories = append(memories, m)
	}

	if len(memories) == 0 {
		return nil, fmt.Errorf("memory not found: %s/%s", p.NS, p.Key)
	}

	// Update access tracking for the latest
	if !p.History {
		now := time.Now().UTC().Format(time.RFC3339)
		s.db.ExecContext(ctx,
			`UPDATE memories SET access_count = access_count + 1, last_accessed_at = ? WHERE id = ?`,
			now, memories[0].ID)
	}

	// Load file references
	if err := s.loadFilesForMemories(ctx, memories); err != nil {
		return nil, err
	}

	return memories, nil
}

func (s *SQLiteStore) List(ctx context.Context, p ListParams) ([]model.Memory, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = 20
	}

	// Build a query that returns only the latest version of each ns+key
	now := time.Now().UTC().Format(time.RFC3339)
	where := []string{"m.deleted_at IS NULL", "(m.expires_at IS NULL OR m.expires_at > ?)"}
	args := []interface{}{now}

	if p.NS != "" {
		where = append(where, "m.ns = ?")
		args = append(args, p.NS)
	}
	if p.Kind != "" {
		where = append(where, "m.kind = ?")
		args = append(args, p.Kind)
	}

	// Tag filtering
	for _, tag := range p.Tags {
		where = append(where, "m.tags LIKE ?")
		args = append(args, "%\""+tag+"\"%")
	}

	query := fmt.Sprintf(`
		SELECT m.id, m.ns, m.key, m.content, m.kind, m.tags, m.version, m.supersedes,
		       m.created_at, m.deleted_at, m.priority, m.access_count, m.last_accessed_at, m.meta, m.expires_at
		FROM memories m
		INNER JOIN (
			SELECT ns, key, MAX(version) AS max_ver
			FROM memories WHERE deleted_at IS NULL
			GROUP BY ns, key
		) latest ON m.ns = latest.ns AND m.key = latest.key AND m.version = latest.max_ver
		WHERE %s
		ORDER BY m.created_at DESC
		LIMIT ?`, strings.Join(where, " AND "))
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var memories []model.Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		memories = append(memories, m)
	}

	return memories, nil
}

func (s *SQLiteStore) Rm(ctx context.Context, p RmParams) error {
	if p.Hard {
		if p.AllVersions {
			// Delete chunks first
			_, err := s.db.ExecContext(ctx,
				`DELETE FROM chunks WHERE memory_id IN (SELECT id FROM memories WHERE ns = ? AND key = ?)`,
				p.NS, p.Key)
			if err != nil {
				return err
			}
			_, err = s.db.ExecContext(ctx, `DELETE FROM memories WHERE ns = ? AND key = ?`, p.NS, p.Key)
			return err
		}
		// Hard delete latest only
		var id string
		err := s.db.QueryRowContext(ctx,
			`SELECT id FROM memories WHERE ns = ? AND key = ? AND deleted_at IS NULL ORDER BY version DESC LIMIT 1`,
			p.NS, p.Key).Scan(&id)
		if err != nil {
			return fmt.Errorf("memory not found: %s/%s", p.NS, p.Key)
		}
		s.db.ExecContext(ctx, `DELETE FROM chunks WHERE memory_id = ?`, id)
		_, err = s.db.ExecContext(ctx, `DELETE FROM memories WHERE id = ?`, id)
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if p.AllVersions {
		_, err := s.db.ExecContext(ctx,
			`UPDATE memories SET deleted_at = ? WHERE ns = ? AND key = ? AND deleted_at IS NULL`,
			now, p.NS, p.Key)
		return err
	}

	// Soft-delete latest version only
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM memories WHERE ns = ? AND key = ? AND deleted_at IS NULL ORDER BY version DESC LIMIT 1`,
		p.NS, p.Key).Scan(&id)
	if err != nil {
		return fmt.Errorf("memory not found: %s/%s", p.NS, p.Key)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE memories SET deleted_at = ? WHERE id = ?`, now, id)
	return err
}

// GCResult holds the result of an expired-memory garbage collection.
type GCResult struct {
	MemoriesDeleted int64
	ChunksFreed     int64
}

// GCStaleResult holds the result of a stale-memory garbage collection.
type GCStaleResult struct {
	MemoriesDeleted int64
	ProtectedCount  int64
}

// GCStaleDryRun counts stale memories (not accessed within the given duration)
// without deleting them. Memories with priority "high" or "critical" are skipped.
func (s *SQLiteStore) GCStaleDryRun(ctx context.Context, staleThreshold time.Duration) (GCStaleResult, error) {
	cutoff := time.Now().UTC().Add(-staleThreshold).Format(time.RFC3339)
	var result GCStaleResult

	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories
		 WHERE deleted_at IS NULL
		   AND priority NOT IN ('high', 'critical')
		   AND COALESCE(last_accessed_at, created_at) < ?`, cutoff).Scan(&result.MemoriesDeleted)
	if err != nil {
		return result, err
	}

	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories
		 WHERE deleted_at IS NULL
		   AND priority IN ('high', 'critical')
		   AND COALESCE(last_accessed_at, created_at) < ?`, cutoff).Scan(&result.ProtectedCount)
	if err != nil {
		return result, err
	}

	return result, nil
}

// GCStale soft-deletes memories not accessed within the given duration.
// Memories with priority "high" or "critical" are skipped.
func (s *SQLiteStore) GCStale(ctx context.Context, staleThreshold time.Duration) (GCStaleResult, error) {
	now := time.Now().UTC()
	cutoff := now.Add(-staleThreshold).Format(time.RFC3339)
	nowStr := now.Format(time.RFC3339)
	var result GCStaleResult

	// Count protected memories (high/critical that are stale but skipped)
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories
		 WHERE deleted_at IS NULL
		   AND priority IN ('high', 'critical')
		   AND COALESCE(last_accessed_at, created_at) < ?`, cutoff).Scan(&result.ProtectedCount)
	if err != nil {
		return result, err
	}

	res, err := s.db.ExecContext(ctx,
		`UPDATE memories SET deleted_at = ?
		 WHERE deleted_at IS NULL
		   AND priority NOT IN ('high', 'critical')
		   AND COALESCE(last_accessed_at, created_at) < ?`, nowStr, cutoff)
	if err != nil {
		return result, fmt.Errorf("soft-delete stale memories: %w", err)
	}

	result.MemoriesDeleted, err = res.RowsAffected()
	if err != nil {
		return result, err
	}

	return result, nil
}

// GCDryRun counts expired memories and their chunks without deleting.
func (s *SQLiteStore) GCDryRun(ctx context.Context) (GCResult, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	var result GCResult

	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories WHERE expires_at IS NOT NULL AND expires_at < ?`, now).Scan(&result.MemoriesDeleted)
	if err != nil {
		return result, err
	}

	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chunks WHERE memory_id IN (
			SELECT id FROM memories WHERE expires_at IS NOT NULL AND expires_at < ?
		)`, now).Scan(&result.ChunksFreed)
	if err != nil {
		return result, err
	}

	return result, nil
}

// GC deletes expired memories (where expires_at < now) and their chunks.
func (s *SQLiteStore) GC(ctx context.Context) (GCResult, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	var result GCResult

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()

	// Count chunks to be deleted
	err = tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chunks WHERE memory_id IN (
			SELECT id FROM memories WHERE expires_at IS NOT NULL AND expires_at < ?
		)`, now).Scan(&result.ChunksFreed)
	if err != nil {
		return result, err
	}

	// Delete chunks belonging to expired memories
	_, err = tx.ExecContext(ctx,
		`DELETE FROM chunks WHERE memory_id IN (
			SELECT id FROM memories WHERE expires_at IS NOT NULL AND expires_at < ?
		)`, now)
	if err != nil {
		return result, fmt.Errorf("delete expired chunks: %w", err)
	}

	// Delete expired memories
	res, err := tx.ExecContext(ctx,
		`DELETE FROM memories WHERE expires_at IS NOT NULL AND expires_at < ?`, now)
	if err != nil {
		return result, fmt.Errorf("delete expired memories: %w", err)
	}

	result.MemoriesDeleted, err = res.RowsAffected()
	if err != nil {
		return result, err
	}

	if err := tx.Commit(); err != nil {
		return result, err
	}

	return result, nil
}

// MemoryCount returns the number of active (non-deleted, non-expired) memories.
func (s *SQLiteStore) MemoryCount(ctx context.Context) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	var count int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories WHERE deleted_at IS NULL AND (expires_at IS NULL OR expires_at > ?)`, now).Scan(&count)
	return count, err
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

type scanner interface {
	Scan(dest ...interface{}) error
}

func scanMemory(row scanner) (model.Memory, error) {
	var m model.Memory
	var tagsJSON, supersedes, deletedAt, lastAccessed, meta, expiresAt sql.NullString
	var createdAt string

	err := row.Scan(
		&m.ID, &m.NS, &m.Key, &m.Content, &m.Kind, &tagsJSON,
		&m.Version, &supersedes, &createdAt, &deletedAt,
		&m.Priority, &m.AccessCount, &lastAccessed, &meta, &expiresAt,
	)
	if err != nil {
		return m, err
	}

	m.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	if supersedes.Valid {
		m.Supersedes = supersedes.String
	}
	if deletedAt.Valid {
		t, _ := time.Parse(time.RFC3339, deletedAt.String)
		m.DeletedAt = &t
	}
	if lastAccessed.Valid {
		t, _ := time.Parse(time.RFC3339, lastAccessed.String)
		m.LastAccessedAt = &t
	}
	if meta.Valid {
		m.Meta = meta.String
	}
	if tagsJSON.Valid {
		json.Unmarshal([]byte(tagsJSON.String), &m.Tags)
	}
	if expiresAt.Valid {
		t, _ := time.Parse(time.RFC3339, expiresAt.String)
		m.ExpiresAt = &t
	}

	return m, nil
}

// ParseTTL parses a TTL string like "7d", "24h", "30m" into a time.Duration.
var ttlRegex = regexp.MustCompile(`^(\d+)([dhms])$`)

func ParseTTL(s string) (time.Duration, error) {
	m := ttlRegex.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("invalid format %q (use e.g. 7d, 24h, 30m, 60s)", s)
	}
	n, _ := strconv.Atoi(m[1])
	switch m[2] {
	case "d":
		return time.Duration(n) * 24 * time.Hour, nil
	case "h":
		return time.Duration(n) * time.Hour, nil
	case "m":
		return time.Duration(n) * time.Minute, nil
	case "s":
		return time.Duration(n) * time.Second, nil
	}
	return 0, fmt.Errorf("unknown unit %q", m[2])
}

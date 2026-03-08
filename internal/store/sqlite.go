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

	"github.com/rcliao/ghost/internal/chunker"
	"github.com/rcliao/ghost/internal/embedding"
	"github.com/rcliao/ghost/internal/model"
)

// Compile-time check: SQLiteStore implements Store.
var _ Store = (*SQLiteStore)(nil)

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

	// Phase 2 columns
	s.db.Exec(`ALTER TABLE memories ADD COLUMN importance REAL NOT NULL DEFAULT 0.5`)
	s.db.Exec(`ALTER TABLE memories ADD COLUMN utility_count INTEGER NOT NULL DEFAULT 0`)
	s.db.Exec(`ALTER TABLE memories ADD COLUMN tier TEXT NOT NULL DEFAULT 'stm'`)
	s.db.Exec(`ALTER TABLE memories ADD COLUMN est_tokens INTEGER NOT NULL DEFAULT 0`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_memories_tier ON memories(tier)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_memories_importance ON memories(importance DESC)`)

	// Phase 3: reflect_rules table
	s.db.Exec(`CREATE TABLE IF NOT EXISTS reflect_rules (
		id              TEXT PRIMARY KEY,
		ns              TEXT NOT NULL DEFAULT '',
		name            TEXT NOT NULL,
		priority        INTEGER NOT NULL DEFAULT 50,
		scope           TEXT NOT NULL DEFAULT 'reflect',
		created_by      TEXT NOT NULL DEFAULT 'system',
		cond_tier       TEXT,
		cond_age_gt_hours REAL,
		cond_importance_lt REAL,
		cond_access_lt  INTEGER,
		cond_access_gt  INTEGER,
		cond_utility_lt REAL,
		cond_kind       TEXT,
		cond_tag_includes TEXT,
		action_op       TEXT NOT NULL,
		action_params   TEXT,
		rule_expires_at TEXT,
		created_at      TEXT NOT NULL
	)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_rules_ns ON reflect_rules(ns, scope)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_rules_priority ON reflect_rules(priority DESC)`)
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

	// Seed built-in reflect rules
	s.seedBuiltinRules()

	return nil
}

func tierOrDefault(tier string) string {
	switch tier {
	case "stm", "ltm", "identity", "dormant":
		return tier
	default:
		return "stm"
	}
}

func (s *SQLiteStore) Put(ctx context.Context, p PutParams) (*model.Memory, error) {
	if err := ValidateNS(p.NS); err != nil {
		return nil, fmt.Errorf("invalid namespace: %w", err)
	}

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

	importance := p.Importance
	if importance <= 0 {
		importance = 0.5
	}
	estTokens := estimateTokens(p.Content)

	_, err = tx.ExecContext(ctx,
		`INSERT INTO memories (id, ns, key, content, kind, tags, version, supersedes, created_at, priority, access_count, meta, expires_at, importance, est_tokens)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)`,
		id, p.NS, p.Key, p.Content, kind, tagsJSON, version, supersedes,
		now.Format(time.RFC3339), priority, metaPtr, expiresAt, importance, estTokens)
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
		Importance: importance,
		Tier:       tierOrDefault(p.Tier),
		EstTokens:  estTokens,
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
				        created_at, deleted_at, priority, access_count, last_accessed_at, meta, expires_at,
				        importance, utility_count, tier, est_tokens
				 FROM memories WHERE ns = ? AND key = ? AND deleted_at IS NULL
				 ORDER BY version DESC`
		args = []interface{}{p.NS, p.Key}
	} else if p.Version > 0 {
		query = `SELECT id, ns, key, content, kind, tags, version, supersedes,
				        created_at, deleted_at, priority, access_count, last_accessed_at, meta, expires_at,
				        importance, utility_count, tier, est_tokens
				 FROM memories WHERE ns = ? AND key = ? AND version = ? AND deleted_at IS NULL
				   AND (expires_at IS NULL OR expires_at > ?)
				 LIMIT 1`
		args = []interface{}{p.NS, p.Key, p.Version, now}
	} else {
		query = `SELECT id, ns, key, content, kind, tags, version, supersedes,
				        created_at, deleted_at, priority, access_count, last_accessed_at, meta, expires_at,
				        importance, utility_count, tier, est_tokens
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

func (s *SQLiteStore) History(ctx context.Context, p HistoryParams) ([]model.Memory, error) {
	query := `SELECT id, ns, key, content, kind, tags, version, supersedes,
			         created_at, deleted_at, priority, access_count, last_accessed_at, meta, expires_at,
				        importance, utility_count, tier, est_tokens
			  FROM memories WHERE ns = ? AND key = ?
			  ORDER BY version ASC`

	rows, err := s.db.QueryContext(ctx, query, p.NS, p.Key)
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
		nsf := ParseNSFilter(p.NS)
		clause, nsArgs := nsf.SQL("m.ns")
		if clause != "" {
			where = append(where, clause)
			args = append(args, nsArgs...)
		}
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
		       m.created_at, m.deleted_at, m.priority, m.access_count, m.last_accessed_at, m.meta, m.expires_at,
		       m.importance, m.utility_count, m.tier, m.est_tokens
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

// RmNamespace soft-deletes (or hard-deletes) all memories in the given namespace.
// Supports prefix matching (e.g., "reflect:*" deletes all under "reflect:").
func (s *SQLiteStore) RmNamespace(ctx context.Context, ns string, hard bool) (int64, error) {
	nsf := ParseNSFilter(ns)
	clause, args := nsf.SQL("ns")
	if clause == "" {
		return 0, fmt.Errorf("namespace filter cannot be empty")
	}

	if hard {
		// Delete chunks first
		_, err := s.db.ExecContext(ctx,
			`DELETE FROM chunks WHERE memory_id IN (SELECT id FROM memories WHERE `+clause+`)`, args...)
		if err != nil {
			return 0, fmt.Errorf("delete chunks: %w", err)
		}
		// Delete file refs
		_, err = s.db.ExecContext(ctx,
			`DELETE FROM memory_files WHERE memory_id IN (SELECT id FROM memories WHERE `+clause+`)`, args...)
		if err != nil {
			return 0, fmt.Errorf("delete file refs: %w", err)
		}
		// Delete links
		_, err = s.db.ExecContext(ctx,
			`DELETE FROM memory_links WHERE from_id IN (SELECT id FROM memories WHERE `+clause+`) OR to_id IN (SELECT id FROM memories WHERE `+clause+`)`,
			append(args, args...)...)
		if err != nil {
			return 0, fmt.Errorf("delete links: %w", err)
		}
		// Delete memories
		res, err := s.db.ExecContext(ctx,
			`DELETE FROM memories WHERE `+clause, args...)
		if err != nil {
			return 0, fmt.Errorf("delete memories: %w", err)
		}
		return res.RowsAffected()
	}

	// Soft delete
	now := time.Now().UTC().Format(time.RFC3339)
	allArgs := append([]interface{}{now}, args...)
	res, err := s.db.ExecContext(ctx,
		`UPDATE memories SET deleted_at = ? WHERE deleted_at IS NULL AND `+clause, allArgs...)
	if err != nil {
		return 0, fmt.Errorf("soft-delete namespace: %w", err)
	}
	return res.RowsAffected()
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

	// Also prune low-utility memories: accessed 5+ times but useful <20%
	utilRes, err := s.db.ExecContext(ctx,
		`UPDATE memories SET deleted_at = ?
		 WHERE deleted_at IS NULL
		   AND access_count >= 5
		   AND utility_count > 0
		   AND CAST(utility_count AS REAL) / CAST(access_count AS REAL) < 0.2`, nowStr)
	if err == nil {
		n, _ := utilRes.RowsAffected()
		result.MemoriesDeleted += n
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

// ListTags returns all distinct tags with counts from active memories,
// optionally filtered by namespace.
func (s *SQLiteStore) ListTags(ctx context.Context, ns string) ([]TagInfo, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	where := []string{
		"m.deleted_at IS NULL",
		"(m.expires_at IS NULL OR m.expires_at > ?)",
		"m.tags IS NOT NULL",
	}
	args := []interface{}{now}

	if ns != "" {
		nsf := ParseNSFilter(ns)
		clause, nsArgs := nsf.SQL("m.ns")
		if clause != "" {
			where = append(where, clause)
			args = append(args, nsArgs...)
		}
	}

	// Get latest version of each ns+key that has tags
	query := fmt.Sprintf(`
		SELECT m.tags FROM memories m
		INNER JOIN (
			SELECT ns, key, MAX(version) AS max_ver
			FROM memories WHERE deleted_at IS NULL
			GROUP BY ns, key
		) latest ON m.ns = latest.ns AND m.key = latest.key AND m.version = latest.max_ver
		WHERE %s`, strings.Join(where, " AND "))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()

	tagCounts := map[string]int{}
	for rows.Next() {
		var tagsJSON string
		if err := rows.Scan(&tagsJSON); err != nil {
			return nil, err
		}
		var tags []string
		if err := json.Unmarshal([]byte(tagsJSON), &tags); err != nil {
			continue // skip malformed tags
		}
		for _, t := range tags {
			tagCounts[t]++
		}
	}

	var result []TagInfo
	for tag, count := range tagCounts {
		result = append(result, TagInfo{Tag: tag, Count: count})
	}
	return result, nil
}

// RenameTag renames a tag across all active memories, returning the count of affected memories.
func (s *SQLiteStore) RenameTag(ctx context.Context, oldTag, newTag, ns string) (int, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	where := []string{
		"deleted_at IS NULL",
		"(expires_at IS NULL OR expires_at > ?)",
		"tags LIKE ?",
	}
	args := []interface{}{now, `%"` + oldTag + `"%`}

	if ns != "" {
		nsf := ParseNSFilter(ns)
		clause, nsArgs := nsf.SQL("ns")
		if clause != "" {
			where = append(where, clause)
			args = append(args, nsArgs...)
		}
	}

	query := fmt.Sprintf(`SELECT id, tags FROM memories WHERE %s`, strings.Join(where, " AND "))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("rename tag query: %w", err)
	}
	defer rows.Close()

	type update struct {
		id      string
		newTags string
	}
	var updates []update

	for rows.Next() {
		var id, tagsJSON string
		if err := rows.Scan(&id, &tagsJSON); err != nil {
			return 0, err
		}
		var tags []string
		if err := json.Unmarshal([]byte(tagsJSON), &tags); err != nil {
			continue
		}
		changed := false
		seen := map[string]bool{}
		var newTags []string
		for _, t := range tags {
			if t == oldTag {
				t = newTag
				changed = true
			}
			if !seen[t] {
				seen[t] = true
				newTags = append(newTags, t)
			}
		}
		if changed {
			b, _ := json.Marshal(newTags)
			updates = append(updates, update{id: id, newTags: string(b)})
		}
	}

	if len(updates) == 0 {
		return 0, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	for _, u := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE memories SET tags = ? WHERE id = ?`, u.newTags, u.id); err != nil {
			return 0, fmt.Errorf("rename tag update: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(updates), nil
}

// RemoveTag removes a tag from all active memories, returning the count of affected memories.
func (s *SQLiteStore) RemoveTag(ctx context.Context, tag, ns string) (int, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	where := []string{
		"deleted_at IS NULL",
		"(expires_at IS NULL OR expires_at > ?)",
		"tags LIKE ?",
	}
	args := []interface{}{now, `%"` + tag + `"%`}

	if ns != "" {
		nsf := ParseNSFilter(ns)
		clause, nsArgs := nsf.SQL("ns")
		if clause != "" {
			where = append(where, clause)
			args = append(args, nsArgs...)
		}
	}

	query := fmt.Sprintf(`SELECT id, tags FROM memories WHERE %s`, strings.Join(where, " AND "))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("remove tag query: %w", err)
	}
	defer rows.Close()

	type update struct {
		id      string
		newTags *string // nil means set to NULL
	}
	var updates []update

	for rows.Next() {
		var id, tagsJSON string
		if err := rows.Scan(&id, &tagsJSON); err != nil {
			return 0, err
		}
		var tags []string
		if err := json.Unmarshal([]byte(tagsJSON), &tags); err != nil {
			continue
		}
		var newTags []string
		for _, t := range tags {
			if t != tag {
				newTags = append(newTags, t)
			}
		}
		if len(newTags) == len(tags) {
			continue // tag wasn't in this memory
		}
		if len(newTags) == 0 {
			updates = append(updates, update{id: id, newTags: nil})
		} else {
			b, _ := json.Marshal(newTags)
			s := string(b)
			updates = append(updates, update{id: id, newTags: &s})
		}
	}

	if len(updates) == 0 {
		return 0, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	for _, u := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE memories SET tags = ? WHERE id = ?`, u.newTags, u.id); err != nil {
			return 0, fmt.Errorf("remove tag update: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(updates), nil
}

// estimateTokens returns a rough token count for content.
func estimateTokens(content string) int {
	return (len(content) / 4) + 20
}

// UtilityInc increments the utility_count for a memory.
func (s *SQLiteStore) UtilityInc(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE memories SET utility_count = utility_count + 1 WHERE id = ? AND deleted_at IS NULL`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("memory not found: %s", id)
	}
	return nil
}

// touchMemories batch-updates access_count and last_accessed_at for the given memory IDs.
func (s *SQLiteStore) touchMemories(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	now := time.Now().UTC().Format(time.RFC3339)
	args := []interface{}{now}
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := s.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE memories SET access_count = access_count + 1, last_accessed_at = ? WHERE id IN (%s) AND deleted_at IS NULL`, placeholders),
		args...,
	)
	return err
}

// Peek returns a lightweight index of memory state for lazy discovery.
func (s *SQLiteStore) Peek(ctx context.Context, ns string) (*PeekResult, error) {
	result := &PeekResult{
		NS:             ns,
		MemoryCounts:   map[string]int{},
		TotalEstTokens: map[string]int{},
	}

	// Build WHERE clause for namespace filter
	where := "deleted_at IS NULL AND (expires_at IS NULL OR expires_at > ?)"
	args := []interface{}{time.Now().UTC().Format(time.RFC3339)}
	if ns != "" {
		nsf := ParseNSFilter(ns)
		clause, nsArgs := nsf.SQL("ns")
		if clause != "" {
			where += " AND " + clause
			args = append(args, nsArgs...)
		}
	}

	// 1. Memory counts and token totals by tier
	rows, err := s.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT tier, COUNT(*), COALESCE(SUM(est_tokens), 0)
			FROM memories WHERE %s GROUP BY tier`, where), args...)
	if err != nil {
		return nil, fmt.Errorf("peek tier counts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tier string
		var count, tokens int
		if err := rows.Scan(&tier, &count, &tokens); err != nil {
			return nil, err
		}
		result.MemoryCounts[tier] = count
		result.TotalEstTokens[tier] = tokens
	}

	// 2. Identity summary (first identity-tier memory)
	var identContent sql.NullString
	s.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT content FROM memories WHERE %s AND tier = 'identity'
			ORDER BY importance DESC, created_at DESC LIMIT 1`, where), args...).Scan(&identContent)
	if identContent.Valid {
		result.IdentitySummary = truncate(identContent.String, 200)
	}

	// 3. Recent topics: top-5 tags by recency
	tagRows, err := s.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT tags FROM memories WHERE %s AND tags IS NOT NULL
			ORDER BY created_at DESC LIMIT 20`, where), args...)
	if err == nil {
		defer tagRows.Close()
		tagSeen := map[string]bool{}
		var topics []string
		for tagRows.Next() && len(topics) < 5 {
			var tagsJSON string
			if tagRows.Scan(&tagsJSON) != nil {
				continue
			}
			var tags []string
			if json.Unmarshal([]byte(tagsJSON), &tags) != nil {
				continue
			}
			for _, t := range tags {
				if !tagSeen[t] && len(topics) < 5 {
					tagSeen[t] = true
					topics = append(topics, t)
				}
			}
		}
		result.RecentTopics = topics
	}
	if result.RecentTopics == nil {
		result.RecentTopics = []string{}
	}

	// 4. Top-5 by importance
	stubRows, err := s.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT id, key, kind, tier, importance, est_tokens, content
			FROM memories WHERE %s
			ORDER BY importance DESC, created_at DESC LIMIT 5`, where), args...)
	if err != nil {
		return nil, fmt.Errorf("peek high importance: %w", err)
	}
	defer stubRows.Close()
	for stubRows.Next() {
		var stub MemoryStub
		var content string
		if err := stubRows.Scan(&stub.ID, &stub.Key, &stub.Kind, &stub.Tier,
			&stub.Importance, &stub.EstTokens, &content); err != nil {
			return nil, err
		}
		stub.Summary = truncate(content, 80)
		result.HighImportance = append(result.HighImportance, stub)
	}
	if result.HighImportance == nil {
		result.HighImportance = []MemoryStub{}
	}

	return result, nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

type scanner interface {
	Scan(dest ...interface{}) error
}

func scanMemory(row scanner) (model.Memory, error) {
	var m model.Memory
	var tagsJSON, supersedes, deletedAt, lastAccessed, meta, expiresAt, tier sql.NullString
	var createdAt string
	var importance sql.NullFloat64
	var utilityCount, estTokens sql.NullInt64

	err := row.Scan(
		&m.ID, &m.NS, &m.Key, &m.Content, &m.Kind, &tagsJSON,
		&m.Version, &supersedes, &createdAt, &deletedAt,
		&m.Priority, &m.AccessCount, &lastAccessed, &meta, &expiresAt,
		&importance, &utilityCount, &tier, &estTokens,
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
	if importance.Valid {
		m.Importance = importance.Float64
	} else {
		m.Importance = 0.5
	}
	if utilityCount.Valid {
		m.UtilityCount = int(utilityCount.Int64)
	}
	if tier.Valid {
		m.Tier = tier.String
	} else {
		m.Tier = "stm"
	}
	if estTokens.Valid {
		m.EstTokens = int(estTokens.Int64)
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

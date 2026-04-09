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
	"time"

	"github.com/oklog/ulid/v2"
	_ "modernc.org/sqlite"

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
	reranker embedding.Reranker
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
		reranker: embedding.NewRerankerFromEnv(),
	}

	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	// Auto-GC: silently delete expired memories on startup
	s.GC(context.Background())

	return s, nil
}

// SetEmbedder overrides the embedder used for vector operations.
func (s *SQLiteStore) SetEmbedder(e embedding.Embedder) {
	s.embedder = e
}

// SetReranker overrides the reranker used for cross-encoder reranking.
func (s *SQLiteStore) SetReranker(r embedding.Reranker) {
	s.reranker = r
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

	// Phase 4: pinned column for chronic accessibility (replaces tier=identity)
	s.db.Exec(`ALTER TABLE memories ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_memories_pinned ON memories(pinned)`)
	// Migrate: existing identity-tier memories become pinned LTM
	s.db.Exec(`UPDATE memories SET pinned = 1, tier = 'ltm' WHERE tier = 'identity' AND pinned = 0`)

	// Phase 5: similarity condition for reflect rules
	s.db.Exec(`ALTER TABLE reflect_rules ADD COLUMN cond_similarity_gt REAL`)

	// Phase 6: unaccessed_gt_hours condition for reflect rules (time since last access)
	s.db.Exec(`ALTER TABLE reflect_rules ADD COLUMN cond_unaccessed_gt_hours REAL`)

	// Phase 7: memory_edges table (DAG-based retrieval)
	s.db.Exec(`CREATE TABLE IF NOT EXISTS memory_edges (
		from_id          TEXT NOT NULL REFERENCES memories(id),
		to_id            TEXT NOT NULL REFERENCES memories(id),
		rel              TEXT NOT NULL,
		weight           REAL NOT NULL DEFAULT 0.5,
		access_count     INTEGER NOT NULL DEFAULT 0,
		last_accessed_at TEXT,
		created_at       TEXT NOT NULL,
		PRIMARY KEY (from_id, to_id, rel)
	)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_edges_to ON memory_edges(to_id)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_edges_weight ON memory_edges(weight DESC)`)

	// Migrate existing memory_links data into memory_edges
	s.db.Exec(`INSERT OR IGNORE INTO memory_edges (from_id, to_id, rel, weight, access_count, last_accessed_at, created_at)
		SELECT from_id, to_id, rel,
		       CASE rel
		           WHEN 'contradicts' THEN 0.9
		           WHEN 'refines' THEN 0.8
		           WHEN 'depends_on' THEN 0.7
		           WHEN 'relates_to' THEN 0.5
		           WHEN 'merged_into' THEN 0.0
		           ELSE 0.5
		       END,
		       0, NULL, created_at
		FROM memory_links`)

	// Phase 8: migrate sys-merge-similar to link_only strategy (non-destructive)
	s.db.Exec(`UPDATE reflect_rules SET action_params = '{"strategy":"link_only"}', name = 'link similar STM memories'
		WHERE id = 'sys-merge-similar' AND action_params = '{"strategy":"keep_highest_importance"}'`)

	// Phase 10: fix sensory rule priorities — decay (delete >4h) must fire before
	// promote (>1h, >1 access). Higher priority number = fires first (DESC order).
	s.db.Exec(`UPDATE reflect_rules SET priority = 95 WHERE id = 'sys-decay-sensory'`)
	s.db.Exec(`UPDATE reflect_rules SET priority = 90 WHERE id = 'sys-promote-sensory'`)

	// Phase 9: make sys-prune-low-utility safer — demote instead of delete,
	// require 20+ accesses instead of 5. With zero utility tracking across the DB,
	// the old rule (AccessGT:5, UtilityLT:0.2, DELETE) would delete nearly everything.
	s.db.Exec(`UPDATE reflect_rules
		SET name = 'Delete heavily-accessed but never-useful memories',
		    cond_access_gt = 20,
		    cond_utility_lt = 0.05,
		    action_op = 'DEMOTE',
		    action_params = '{"to_tier":"dormant"}'
		WHERE id = 'sys-prune-low-utility' AND cond_access_gt = 5`)

	// Seed built-in reflect rules
	s.seedBuiltinRules()

	return nil
}

func tierOrDefault(tier string) string {
	switch tier {
	case "sensory", "stm", "ltm", "dormant":
		return tier
	case "identity":
		return "ltm" // backward compat: identity tier mapped to ltm + pinned
	default:
		return "stm"
	}
}

// estimateTokens returns a rough token count for content.
func estimateTokens(content string) int {
	return (len(content) / 4) + 20
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
	var utilityCount, estTokens, pinned sql.NullInt64

	err := row.Scan(
		&m.ID, &m.NS, &m.Key, &m.Content, &m.Kind, &tagsJSON,
		&m.Version, &supersedes, &createdAt, &deletedAt,
		&m.Priority, &m.AccessCount, &lastAccessed, &meta, &expiresAt,
		&importance, &utilityCount, &tier, &estTokens, &pinned,
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
	if pinned.Valid && pinned.Int64 != 0 {
		m.Pinned = true
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

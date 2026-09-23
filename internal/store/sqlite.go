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
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	_ "modernc.org/sqlite"

	"github.com/rcliao/ghost/internal/embedding"
	"github.com/rcliao/ghost/internal/model"
	"strings"
)

// Compile-time check: SQLiteStore implements Store.
var _ Store = (*SQLiteStore)(nil)

// SQLiteStore implements Store using SQLite.
type SQLiteStore struct {
	db       *sql.DB
	entropy  *rand.Rand
	embedder embedding.Embedder
	// contractReadOnly: the DB was written under a NEWER store contract than
	// this binary supports — writes are refused (see contract.go).
	contractReadOnly  bool
	reranker          embedding.Reranker
	rerankErrOnce     sync.Once        // one-shot warn when the reranker fails (see rerankMaxP)
	pinOverflowWarned sync.Map         // ns -> bool, one-shot warn when pinned exceeds the pin budget
	nowFn             func() time.Time // injectable clock; nil means time.Now
}

// now is the store's clock. Retrieval scoring, write timestamps, and lifecycle
// decisions all read time through it so tests and evals can freeze the clock
// (see SetClock) — without this, rank ties near decay boundaries flip with the
// wall clock and the same eval gives different scores at different times of day.
func (s *SQLiteStore) now() time.Time {
	if s.nowFn != nil {
		return s.nowFn()
	}
	return time.Now()
}

// SetClock overrides the store's time source. Pass nil to restore time.Now.
// Intended for tests/evals; production callers should leave it unset.
func (s *SQLiteStore) SetClock(fn func() time.Time) { s.nowFn = fn }

// NewSQLiteStore opens or creates a SQLite database at the given path.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	registerCJKSegmentUDF()
	// busy_timeout: the daemon, CLI, and MCP server write the same DB —
	// without a wait, a concurrent writer gets an instant SQLITE_BUSY
	// (observed as "consolidate exchanges failed ... database is locked").
	//
	// _txlock=immediate: busy_timeout alone was NOT enough. Every write path
	// here uses BeginTx(ctx, nil), which SQLite begins DEFERRED — the write
	// lock is taken lazily, on the first write statement. A transaction that
	// reads first and writes second therefore has to UPGRADE, and if another
	// writer committed in between, SQLite fails that upgrade with an instant
	// SQLITE_BUSY that busy_timeout does not retry (retrying could not
	// preserve the snapshot the transaction already read from). Taking the
	// write lock up front makes the wait apply, which is what busy_timeout
	// was there for in the first place.
	//
	// Measured on the two live daemon stores 2026-08-05: 22 swallowed write
	// failures — 6 thread summaries, 6 exchange logs, 3 distilled same-day
	// facts, plus scheduler and media-note writes. Each was logged at WARN
	// and dropped, so the memory simply never existed.
	db, err := sql.Open("sqlite", dbPath+"?_txlock=immediate&_pragma=journal_mode(wal)&_pragma=foreign_keys(on)&_pragma=busy_timeout(5000)")
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

	// Contract handshake: stamp fresh/older DBs; refuse writes when the DB
	// is newer than this binary (see contract.go).
	s.checkContract()

	// Auto-GC: delete expired memories on startup. Failures must be visible —
	// a swallowed FK error here once left months of expired memories in place.
	if _, err := s.GC(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "ghost: startup GC failed: %v\n", err)
	}

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
		tokenize='porter unicode61'
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

	// Write provenance (2026-08): the PERSON a memory originated from and how
	// it entered the store. Nullable on purpose — unknown origin stays NULL,
	// never backfilled.
	s.db.Exec(`ALTER TABLE memories ADD COLUMN source_user TEXT`)
	s.db.Exec(`ALTER TABLE memories ADD COLUMN source_kind TEXT`)
	s.db.Exec(`ALTER TABLE memories ADD COLUMN source_scope TEXT`)
	// Source identity: declared alias → canonical person-id mapping,
	// resolved at read; memory rows stay verbatim (source-identity-design.md).
	s.db.Exec(`CREATE TABLE IF NOT EXISTS source_aliases (
		ns         TEXT NOT NULL,
		alias      TEXT NOT NULL COLLATE NOCASE,
		canonical  TEXT NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY (ns, alias)
	)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_memories_source_user ON memories(ns, source_user)`)
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
		INSERT INTO chunks_fts(rowid, text) VALUES (new.rowid, cjk_segment(new.text));
	END`)
	s.db.Exec(`CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
		DELETE FROM chunks_fts WHERE rowid = old.rowid;
	END`)
	s.db.Exec(`CREATE TRIGGER IF NOT EXISTS chunks_au AFTER UPDATE ON chunks BEGIN
		DELETE FROM chunks_fts WHERE rowid = old.rowid;
		INSERT INTO chunks_fts(rowid, text) VALUES (new.rowid, cjk_segment(new.text));
	END`)

	// Backfill FTS for any existing chunks not yet indexed
	s.db.Exec(`INSERT OR IGNORE INTO chunks_fts(rowid, text) SELECT rowid, cjk_segment(text) FROM chunks`)

	// Phase 11: porter stemming for FTS. Pre-existing DBs built chunks_fts
	// with the default unicode61 tokenizer, so "deploys"/"deployed" never
	// matched "deploy" — measured +0.125 multi-hop recall on the in-repo
	// report, all other suites and the LongMemEval_S gate unchanged. Porter
	// is deliberately conservative: it does NOT unify deployment↔deploy or
	// bundling↔bundler (suffix rules gate on stem measure) — closing those
	// needs query-side expansion, not a tokenizer. Detect the old table via
	// its stored DDL and rebuild (content-external FTS: repopulating from
	// chunks is cheap and lossless).
	// Phase 12: convert legacy JSON-text embeddings to the binary blob
	// format (see embedding_codec.go). One-time, in-place, same column —
	// measured ~300ms/query of JSON decode on a 58k-chunk DB before this.
	s.migrateEmbeddingBlobs()

	var ftsSQL string
	if err := s.db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE name = 'chunks_fts'`).Scan(&ftsSQL); err == nil &&
		(!strings.Contains(ftsSQL, "porter") || strings.Contains(ftsSQL, "content=chunks")) {
		// Phase 13 unifies with Phase 11: the FTS index is standalone
		// (stores cjk_segment-ed text — CJK runs as overlapping bigrams so
		// Chinese terms are matchable; see cjk.go) with porter for English
		// stemming. Old DBs (unicode61 external-content, or porter
		// external-content from Phase 11) are rebuilt once from chunks.
		s.db.Exec(`DROP TABLE chunks_fts`)
		s.db.Exec(`CREATE VIRTUAL TABLE chunks_fts USING fts5(
			text,
			tokenize='porter unicode61'
		)`)
		s.db.Exec(`INSERT INTO chunks_fts(rowid, text) SELECT rowid, cjk_segment(text) FROM chunks`)
	}

	// Phase 14: recreate stale FTS triggers. CREATE TRIGGER IF NOT EXISTS never
	// replaces, so DBs from before the standalone-bigram rebuild kept trigger
	// bodies that (a) use the external-content 'delete' command — an SQL error
	// on the standalone table, silently breaking every chunk DELETE/UPDATE and
	// therefore expired-chunk GC — and (b) index new text without cjk_segment,
	// leaving new CJK content invisible to FTS. Found live in the agent DBs by
	// the shell wiring session (2026-07-14). Detect stale DDL, recreate, and
	// reindex (rows inserted by the stale trigger lack bigram segmentation).
	var adDDL, aiDDL string
	s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='chunks_ad'`).Scan(&adDDL)
	s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='chunks_ai'`).Scan(&aiDDL)
	if (adDDL != "" && strings.Contains(adDDL, "'delete'")) || (aiDDL != "" && !strings.Contains(aiDDL, "cjk_segment")) {
		s.db.Exec(`DROP TRIGGER IF EXISTS chunks_ai`)
		s.db.Exec(`DROP TRIGGER IF EXISTS chunks_ad`)
		s.db.Exec(`DROP TRIGGER IF EXISTS chunks_au`)
		s.db.Exec(`CREATE TRIGGER chunks_ai AFTER INSERT ON chunks BEGIN
			INSERT INTO chunks_fts(rowid, text) VALUES (new.rowid, cjk_segment(new.text));
		END`)
		s.db.Exec(`CREATE TRIGGER chunks_ad AFTER DELETE ON chunks BEGIN
			DELETE FROM chunks_fts WHERE rowid = old.rowid;
		END`)
		s.db.Exec(`CREATE TRIGGER chunks_au AFTER UPDATE ON chunks BEGIN
			DELETE FROM chunks_fts WHERE rowid = old.rowid;
			INSERT INTO chunks_fts(rowid, text) VALUES (new.rowid, cjk_segment(new.text));
		END`)
		s.db.Exec(`DELETE FROM chunks_fts`)
		s.db.Exec(`INSERT INTO chunks_fts(rowid, text) SELECT rowid, cjk_segment(text) FROM chunks`)
	}

	// Phase 4: pinned column for chronic accessibility (replaces tier=identity)
	s.db.Exec(`ALTER TABLE memories ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_memories_pinned ON memories(pinned)`)
	// Migrate: existing identity-tier memories become pinned LTM
	s.db.Exec(`UPDATE memories SET pinned = 1, tier = 'ltm' WHERE tier = 'identity' AND pinned = 0`)

	// Phase 6: spaced-repetition ease — a per-memory decay-resistance factor
	// derived from proven usefulness (utility). Higher ease → survives idle
	// stretches longer before demotion. Default 1.0 (neutral); existing rows
	// keep neutral decay until reflect recomputes ease from their utility.
	s.db.Exec(`ALTER TABLE memories ADD COLUMN ease REAL NOT NULL DEFAULT 1.0`)

	// Phase 9: bi-temporal event-time validity (see bitemporal.go). NULLs mean
	// "valid since created_at, still valid" — existing rows are unaffected and
	// the columns stay inert unless GHOST_BITEMPORAL=1 stamps them.
	s.db.Exec(`ALTER TABLE memories ADD COLUMN valid_from TEXT`)
	s.db.Exec(`ALTER TABLE memories ADD COLUMN valid_to TEXT`)

	// Phase 10: per-access log (see access_log.go) — the raw timestamps that
	// access_count/last_accessed_at compress away. Pruned by GC (retention +
	// per-memory cap), so it stays small.
	s.db.Exec(`CREATE TABLE IF NOT EXISTS memory_accesses (
		memory_id TEXT NOT NULL,
		accessed_at TEXT NOT NULL
	)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_memory_accesses_memory
		ON memory_accesses(memory_id, accessed_at)`)

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

	// Pair rules + the rule-event audit trace (docs/research/pair-rules-design.md).
	// Additive: an older binary ignores both tables; the contract version is unchanged.
	s.db.Exec(`CREATE TABLE IF NOT EXISTS pair_rules (
		id TEXT PRIMARY KEY,
		ns TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		priority INTEGER NOT NULL DEFAULT 0,
		created_by TEXT NOT NULL DEFAULT 'system',
		cond_min_shared_entities INTEGER NOT NULL DEFAULT 0,
		cond_max_entity_df INTEGER NOT NULL DEFAULT 0,
		cond_min_days_apart REAL NOT NULL DEFAULT 0,
		cond_max_jaccard REAL NOT NULL DEFAULT 0,
		cond_cue TEXT NOT NULL DEFAULT '',
		cond_same_user INTEGER NOT NULL DEFAULT -1,
		cond_key_prefix_match INTEGER NOT NULL DEFAULT -1,
		action_op TEXT NOT NULL,
		action_rel TEXT NOT NULL,
		min_precision REAL NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL
	)`)
	s.db.Exec(`CREATE TABLE IF NOT EXISTS rule_events (
		id TEXT PRIMARY KEY,
		source TEXT NOT NULL,
		rule_id TEXT NOT NULL,
		from_id TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
		to_id TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
		features TEXT NOT NULL DEFAULT '{}',
		action_op TEXT NOT NULL,
		action_rel TEXT NOT NULL DEFAULT '',
		score REAL NOT NULL DEFAULT 0,
		edge_written INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		reviewed INTEGER NOT NULL DEFAULT 0,
		verdict TEXT,
		reviewed_by TEXT,
		reviewed_at TEXT
	)`)
	s.db.Exec(`ALTER TABLE rule_events ADD COLUMN score REAL NOT NULL DEFAULT 0`) // idempotent: errors ignored
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_rule_events_pair ON rule_events(source, rule_id, from_id, to_id)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_rule_events_review ON rule_events(reviewed, created_at)`)

	// Seed built-in reflect rules
	s.seedBuiltinRules()
	s.seedBuiltinPairRules()

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
	var tagsJSON, supersedes, deletedAt, lastAccessed, meta, expiresAt, tier, sourceUser, sourceKind, sourceScope sql.NullString
	var createdAt string
	var importance sql.NullFloat64
	var utilityCount, estTokens, pinned sql.NullInt64

	err := row.Scan(
		&m.ID, &m.NS, &m.Key, &m.Content, &m.Kind, &tagsJSON,
		&m.Version, &supersedes, &createdAt, &deletedAt,
		&m.Priority, &m.AccessCount, &lastAccessed, &meta, &expiresAt,
		&importance, &utilityCount, &tier, &estTokens, &pinned,
		&sourceUser, &sourceKind, &sourceScope,
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
	if sourceUser.Valid {
		m.SourceUser = sourceUser.String
	}
	if sourceKind.Valid {
		m.SourceKind = sourceKind.String
	}
	if sourceScope.Valid {
		m.SourceScope = sourceScope.String
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

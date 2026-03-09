// Package store provides the memory storage interface and SQLite implementation.
package store

import (
	"context"
	"time"

	"github.com/rcliao/ghost/internal/model"
)

// PutParams holds parameters for storing a memory.
type PutParams struct {
	NS         string
	Key        string
	Content    string
	Kind       string
	Tags       []string
	Priority   string
	Importance float64 // 0.0-1.0; 0 means use default (0.5)
	Tier       string  // "sensory", "stm", "ltm", "dormant"; empty defaults to "stm"
	Pinned     bool    // always loaded in context, exempt from decay
	Meta       string
	TTL        string // e.g. "7d", "24h", "30m"
	Files      []FileParam
}

// FileParam specifies a file to link to a memory.
type FileParam struct {
	Path string
	Rel  string // modified, created, deleted, read (default: modified)
}

// GetParams holds parameters for retrieving a memory.
type GetParams struct {
	NS      string
	Key     string
	History bool
	Version int // 0 means latest
}

// ListParams holds parameters for listing memories.
type ListParams struct {
	NS       string
	Kind     string
	Tags     []string
	Limit    int
	KeysOnly bool
}

// RmParams holds parameters for deleting a memory.
type RmParams struct {
	NS          string
	Key         string
	AllVersions bool
	Hard        bool
}

// HistoryParams holds parameters for retrieving the full version history.
type HistoryParams struct {
	NS  string
	Key string
}

// TagInfo holds information about a tag used across memories.
type TagInfo struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"` // number of active memories with this tag
}

// PeekResult is a lightweight memory index for lazy discovery.
type PeekResult struct {
	NS              string         `json:"ns"`
	PinnedSummary   string         `json:"pinned_summary,omitempty"`
	RecentTopics    []string       `json:"recent_topics"`
	MemoryCounts    map[string]int `json:"memory_counts"`
	HighImportance  []MemoryStub   `json:"high_importance"`
	TotalEstTokens  map[string]int `json:"total_est_tokens"`
}

// MemoryStub is a lightweight reference to a memory for peek results.
type MemoryStub struct {
	ID         string  `json:"id"`
	Key        string  `json:"key"`
	Kind       string  `json:"kind"`
	Tier       string  `json:"tier"`
	Importance float64 `json:"importance"`
	EstTokens  int     `json:"est_tokens"`
	Summary    string  `json:"summary"`
}

// Store defines the memory storage interface.
type Store interface {
	// Put stores or updates a memory. Returns the created memory.
	Put(ctx context.Context, p PutParams) (*model.Memory, error)

	// Get retrieves a memory by namespace and key.
	// Returns a slice (single element normally, multiple with History=true).
	Get(ctx context.Context, p GetParams) ([]model.Memory, error)

	// History returns all versions of a memory (including deleted) for audit.
	History(ctx context.Context, p HistoryParams) ([]model.Memory, error)

	// List lists memories matching the given filters.
	List(ctx context.Context, p ListParams) ([]model.Memory, error)

	// Rm soft-deletes (or hard-deletes) a memory.
	Rm(ctx context.Context, p RmParams) error

	// Search finds memories whose content or chunks match a query.
	Search(ctx context.Context, p SearchParams) ([]SearchResult, error)

	// GC deletes expired memories and their chunks.
	GC(ctx context.Context) (GCResult, error)

	// GCDryRun counts expired memories and chunks without deleting.
	GCDryRun(ctx context.Context) (GCResult, error)

	// GCStale soft-deletes memories not accessed within the threshold.
	GCStale(ctx context.Context, staleThreshold time.Duration) (GCStaleResult, error)

	// GCStaleDryRun counts stale memories without deleting.
	GCStaleDryRun(ctx context.Context, staleThreshold time.Duration) (GCStaleResult, error)

	// MemoryCount returns the number of active memories.
	MemoryCount(ctx context.Context) (int64, error)

	// Stats returns database statistics.
	Stats(ctx context.Context, dbPath string) (*Stats, error)

	// ListNamespaces returns all namespaces with counts.
	ListNamespaces(ctx context.Context) ([]NamespaceStats, error)

	// RmNamespace soft-deletes all memories in the given namespace (supports prefix matching).
	RmNamespace(ctx context.Context, ns string, hard bool) (int64, error)

	// ExportAll returns all non-deleted memories, optionally filtered by namespace.
	ExportAll(ctx context.Context, ns string) ([]model.Memory, error)

	// Import stores memories from an export.
	Import(ctx context.Context, memories []model.Memory) (int, error)

	// Link creates or removes a relation between two memories.
	Link(ctx context.Context, p LinkParams) (*Link, error)

	// GetLinks returns all links for a memory.
	GetLinks(ctx context.Context, memoryID string) ([]Link, error)

	// Context assembles relevant memories within a token budget.
	Context(ctx context.Context, p ContextParams) (*ContextResult, error)

	// GetFiles returns all file references for a memory.
	GetFiles(ctx context.Context, memoryID string) ([]model.FileRef, error)

	// FindByFile returns memories linked to a given file path.
	FindByFile(ctx context.Context, p FindByFileParams) ([]model.Memory, error)

	// ListTags returns all distinct tags with counts, optionally filtered by namespace.
	ListTags(ctx context.Context, ns string) ([]TagInfo, error)

	// RenameTag renames a tag across all matching memories, returning the count of affected memories.
	RenameTag(ctx context.Context, oldTag, newTag, ns string) (int, error)

	// RemoveTag removes a tag from all matching memories, returning the count of affected memories.
	RemoveTag(ctx context.Context, tag, ns string) (int, error)

	// UtilityInc increments the utility_count for a memory, signaling that
	// a retrieval of this memory actually contributed to task success.
	UtilityInc(ctx context.Context, id string) error

	// Peek returns a lightweight index of memory state for lazy discovery.
	Peek(ctx context.Context, ns string) (*PeekResult, error)

	// Curate applies a lifecycle action to a single memory identified by ns+key.
	// Supported ops: promote, demote, boost, diminish, archive, delete.
	Curate(ctx context.Context, p CurateParams) (*CurateResult, error)

	// Reflect evaluates rules against memories and applies tier/importance changes.
	Reflect(ctx context.Context, p ReflectParams) (*ReflectResult, error)

	// RuleSet creates or updates a reflect rule.
	RuleSet(ctx context.Context, rule ReflectRule) (*ReflectRule, error)

	// RuleGet retrieves a rule by ID.
	RuleGet(ctx context.Context, id string) (*ReflectRule, error)

	// RuleList returns all rules matching the given namespace.
	RuleList(ctx context.Context, ns string) ([]ReflectRule, error)

	// RuleDelete removes a rule by ID.
	RuleDelete(ctx context.Context, id string) error

	// Close closes the store.
	Close() error
}

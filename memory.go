// Package memory provides a public API for the ghost memory store.
//
// This package re-exports the core types and constructor from internal packages
// so that external Go modules can use ghost as a library.
package memory

import (
	"context"
	"time"

	"github.com/rcliao/ghost/internal/model"
	"github.com/rcliao/ghost/internal/store"
)

// Memory represents a stored memory entry.
type Memory = model.Memory

// FileRef represents a link between a memory and a file on disk.
type FileRef = model.FileRef

// PutParams holds parameters for storing a memory.
type PutParams = store.PutParams

// FileParam specifies a file to link to a memory.
type FileParam = store.FileParam

// GetParams holds parameters for retrieving a memory.
type GetParams = store.GetParams

// ListParams holds parameters for listing memories.
type ListParams = store.ListParams

// RmParams holds parameters for deleting a memory.
type RmParams = store.RmParams

// SearchParams holds parameters for searching memories.
type SearchParams = store.SearchParams

// SearchResult wraps a memory with optional match info.
type SearchResult = store.SearchResult

// ContextParams holds parameters for context assembly.
type ContextParams = store.ContextParams

// ContextMemory is a scored memory for context output.
type ContextMemory = store.ContextMemory

// ContextResult is the assembled context response.
type ContextResult = store.ContextResult

// PeekResult is a lightweight memory index for lazy discovery.
type PeekResult = store.PeekResult

// MemoryStub is a lightweight reference to a memory for peek results.
type MemoryStub = store.MemoryStub

// Store defines the memory storage interface.
type Store interface {
	Put(ctx context.Context, p PutParams) (*Memory, error)
	Get(ctx context.Context, p GetParams) ([]Memory, error)
	List(ctx context.Context, p ListParams) ([]Memory, error)
	Rm(ctx context.Context, p RmParams) error
	Search(ctx context.Context, p SearchParams) ([]SearchResult, error)
	Context(ctx context.Context, p ContextParams) (*ContextResult, error)
	Peek(ctx context.Context, ns string) (*PeekResult, error)
	MemoryCount(ctx context.Context) (int64, error)
	GC(ctx context.Context) (GCResult, error)
	GCStale(ctx context.Context, staleThreshold time.Duration) (GCStaleResult, error)
	UtilityInc(ctx context.Context, id string) error
	CreateEdge(ctx context.Context, p EdgeParams) (*Edge, error)
	DeleteEdge(ctx context.Context, p EdgeParams) error
	GetEdges(ctx context.Context, memoryID string) ([]Edge, error)
	GetEdgesByNSKey(ctx context.Context, ns, key string) ([]Edge, error)
	GetSimilarClusters(ctx context.Context, ns string) ([]MemoryCluster, error)
	Consolidate(ctx context.Context, p ConsolidateParams) (*ConsolidateResult, error)
	Expand(ctx context.Context, p ExpandParams) (*ExpandResult, error)
	Reflect(ctx context.Context, p ReflectParams) (*ReflectResult, error)
	RuleSet(ctx context.Context, rule ReflectRule) (*ReflectRule, error)
	RuleGet(ctx context.Context, id string) (*ReflectRule, error)
	RuleList(ctx context.Context, ns string) ([]ReflectRule, error)
	RuleDelete(ctx context.Context, id string) error
	Close() error
}

// GCResult holds garbage collection results.
type GCResult = store.GCResult

// GCStaleResult holds stale GC results.
type GCStaleResult = store.GCStaleResult

// CurateParams holds parameters for a single-memory lifecycle action.
type CurateParams = store.CurateParams

// CurateResult describes what happened after curation.
type CurateResult = store.CurateResult

// ReflectParams controls a reflect cycle.
type ReflectParams = store.ReflectParams

// ReflectResult summarizes what the reflect cycle did.
type ReflectResult = store.ReflectResult

// ReflectRule defines a condition→action pair for the reflect engine.
type ReflectRule = store.ReflectRule

// RuleCond holds rule conditions.
type RuleCond = store.RuleCond

// RuleAction holds rule actions.
type RuleAction = store.RuleAction

// Edge represents a weighted, typed relation between two memories.
type Edge = store.Edge

// EdgeParams holds parameters for creating or removing an edge.
type EdgeParams = store.EdgeParams

// EdgeExpansionConfig controls edge expansion in context assembly.
type EdgeExpansionConfig = store.EdgeExpansionConfig

// MemoryCluster represents a group of similar memories connected by edges.
type MemoryCluster = store.MemoryCluster

// ConsolidateParams holds parameters for creating a consolidation.
type ConsolidateParams = store.ConsolidateParams

// ConsolidateResult wraps the result of a consolidate operation.
type ConsolidateResult = store.ConsolidateResult

// ExpandParams holds parameters for expanding a consolidation node.
type ExpandParams = store.ExpandParams

// ExpandResult contains expandable nodes or a single node's children.
type ExpandResult = store.ExpandResult

// NewSQLiteStore opens or creates a SQLite-backed memory store at the given path.
func NewSQLiteStore(dbPath string) (Store, error) {
	return store.NewSQLiteStore(dbPath)
}

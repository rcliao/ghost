// Package store provides the memory storage interface and SQLite implementation.
package store

import (
	"context"

	"github.com/rcliao/agent-memory/internal/model"
)

// PutParams holds parameters for storing a memory.
type PutParams struct {
	NS       string
	Key      string
	Content  string
	Kind     string
	Tags     []string
	Priority string
	Meta     string
	TTL      string // e.g. "7d", "24h", "30m"
	Files    []FileParam
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

// Store defines the memory storage interface.
type Store interface {
	// Put stores or updates a memory. Returns the created memory.
	Put(ctx context.Context, p PutParams) (*model.Memory, error)

	// Get retrieves a memory by namespace and key.
	// Returns a slice (single element normally, multiple with History=true).
	Get(ctx context.Context, p GetParams) ([]model.Memory, error)

	// List lists memories matching the given filters.
	List(ctx context.Context, p ListParams) ([]model.Memory, error)

	// Rm soft-deletes (or hard-deletes) a memory.
	Rm(ctx context.Context, p RmParams) error

	// Close closes the store.
	Close() error
}

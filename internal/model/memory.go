// Package model defines the core memory data types.
package model

import "time"

// Memory represents a stored memory entry.
type Memory struct {
	ID             string     `json:"id"`
	NS             string     `json:"ns"`
	Key            string     `json:"key"`
	Content        string     `json:"content"`
	Kind           string     `json:"kind"`
	Tags           []string   `json:"tags,omitempty"`
	Version        int        `json:"version"`
	Supersedes     string     `json:"supersedes,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	DeletedAt      *time.Time `json:"deleted_at,omitempty"`
	Priority       string     `json:"priority"`
	AccessCount    int        `json:"access_count"`
	LastAccessedAt *time.Time `json:"last_accessed_at,omitempty"`
	Meta           string     `json:"meta,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	Importance     float64    `json:"importance"`
	UtilityCount   int        `json:"utility_count"`
	Tier           string     `json:"tier"`
	EstTokens      int        `json:"est_tokens"`
	ChunkCount     int        `json:"chunks,omitempty"`
	Files          []FileRef  `json:"files,omitempty"`
}

// FileRef represents a link between a memory and a file on disk.
type FileRef struct {
	Path string `json:"path"`
	Rel  string `json:"rel"`
}

// Chunk represents an internal text chunk of a memory.
type Chunk struct {
	ID        string `json:"id"`
	MemoryID  string `json:"memory_id"`
	Seq       int    `json:"seq"`
	Text      string `json:"text"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
}

// ValidKinds are the allowed memory kinds.
var ValidKinds = map[string]bool{
	"semantic":   true,
	"episodic":   true,
	"procedural": true,
}

// ValidPriorities are the allowed priority levels.
var ValidPriorities = map[string]bool{
	"low":      true,
	"normal":   true,
	"high":     true,
	"critical": true,
}

// ValidFileRels are the allowed file reference relationship types.
var ValidFileRels = map[string]bool{
	"modified": true,
	"created":  true,
	"deleted":  true,
	"read":     true,
}

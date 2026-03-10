package store

import (
	"context"
	"fmt"
	"time"
)

// LinkParams holds parameters for creating/removing a link.
type LinkParams struct {
	FromNS  string // namespace of source
	FromKey string // key of source (or raw ID)
	ToNS    string
	ToKey   string
	Rel     string // relates_to | contradicts | depends_on | refines
	Remove  bool
}

// Link represents a relation between two memories.
type Link struct {
	FromID    string `json:"from_id"`
	ToID      string `json:"to_id"`
	Rel       string `json:"rel"`
	CreatedAt string `json:"created_at"`
}

var validRels = map[string]bool{
	"relates_to":  true,
	"contradicts": true,
	"depends_on":  true,
	"refines":     true,
	"merged_into": true,
}

// Link creates or removes a relation between two memories.
func (s *SQLiteStore) Link(ctx context.Context, p LinkParams) (*Link, error) {
	if !validRels[p.Rel] {
		return nil, fmt.Errorf("invalid relation %q (valid: relates_to, contradicts, depends_on, refines)", p.Rel)
	}

	fromID, err := s.resolveMemoryID(ctx, p.FromNS, p.FromKey)
	if err != nil {
		return nil, fmt.Errorf("resolve from: %w", err)
	}
	toID, err := s.resolveMemoryID(ctx, p.ToNS, p.ToKey)
	if err != nil {
		return nil, fmt.Errorf("resolve to: %w", err)
	}

	if p.Remove {
		_, err := s.db.ExecContext(ctx,
			`DELETE FROM memory_links WHERE from_id = ? AND to_id = ? AND rel = ?`,
			fromID, toID, p.Rel)
		if err != nil {
			return nil, err
		}
		return &Link{FromID: fromID, ToID: toID, Rel: p.Rel}, nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO memory_links (from_id, to_id, rel, created_at) VALUES (?, ?, ?, ?)`,
		fromID, toID, p.Rel, now)
	if err != nil {
		return nil, err
	}

	return &Link{FromID: fromID, ToID: toID, Rel: p.Rel, CreatedAt: now}, nil
}

// GetLinks returns all links for a memory.
func (s *SQLiteStore) GetLinks(ctx context.Context, memoryID string) ([]Link, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT from_id, to_id, rel, created_at FROM memory_links
		 WHERE from_id = ? OR to_id = ?`, memoryID, memoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var links []Link
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.FromID, &l.ToID, &l.Rel, &l.CreatedAt); err != nil {
			return nil, err
		}
		links = append(links, l)
	}
	return links, nil
}

// resolveMemoryID finds the latest memory ID for a ns:key pair.
func (s *SQLiteStore) resolveMemoryID(ctx context.Context, ns, key string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM memories WHERE ns = ? AND key = ? AND deleted_at IS NULL
		 ORDER BY version DESC LIMIT 1`, ns, key).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("memory not found: %s:%s", ns, key)
	}
	return id, nil
}

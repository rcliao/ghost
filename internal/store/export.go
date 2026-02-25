package store

import (
	"context"
	"strings"

	"github.com/rcliao/agent-memory/internal/model"
)

// ExportAll returns all non-deleted memories, optionally filtered by namespace.
func (s *SQLiteStore) ExportAll(ctx context.Context, ns string) ([]model.Memory, error) {
	where := []string{"deleted_at IS NULL"}
	args := []interface{}{}

	if ns != "" {
		nsf := ParseNSFilter(ns)
		clause, nsArgs := nsf.SQL("ns")
		if clause != "" {
			where = append(where, clause)
			args = append(args, nsArgs...)
		}
	}

	query := `SELECT id, ns, key, content, kind, tags, version, supersedes,
	                 created_at, deleted_at, priority, access_count, last_accessed_at, meta, expires_at
	          FROM memories WHERE ` + strings.Join(where, " AND ") + ` ORDER BY ns, key, version`

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

// Import stores memories from an export. Skips duplicates (same ns+key+version).
func (s *SQLiteStore) Import(ctx context.Context, memories []model.Memory) (int, error) {
	imported := 0
	for _, m := range memories {
		_, err := s.Put(ctx, PutParams{
			NS:       m.NS,
			Key:      m.Key,
			Content:  m.Content,
			Kind:     m.Kind,
			Tags:     m.Tags,
			Priority: m.Priority,
			Meta:     m.Meta,
		})
		if err != nil {
			return imported, err
		}
		imported++
	}
	return imported, nil
}

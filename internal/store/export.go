package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/rcliao/ghost/internal/model"
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
	                 created_at, deleted_at, priority, access_count, last_accessed_at, meta, expires_at,
	                 importance, utility_count, tier, est_tokens, pinned, source_user, source_kind, source_scope, used_count
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
		mem, err := s.Put(ctx, PutParams{
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
		// Carry the caller-reported use signal across export/import. MAX so a
		// Put that dedups onto an existing row never lowers its count.
		if m.UsedCount > 0 && mem != nil {
			if _, err := s.db.ExecContext(ctx,
				`UPDATE memories SET used_count = MAX(used_count, ?) WHERE id = ?`, m.UsedCount, mem.ID); err != nil {
				return imported, fmt.Errorf("import %s/%s: set used_count: %w", m.NS, m.Key, err)
			}
		}
		imported++
	}
	return imported, nil
}

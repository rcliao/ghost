package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

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

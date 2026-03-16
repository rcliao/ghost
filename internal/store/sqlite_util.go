package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// UtilityInc increments the utility_count for a memory.
func (s *SQLiteStore) UtilityInc(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE memories SET utility_count = utility_count + 1 WHERE id = ? AND deleted_at IS NULL`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("memory not found: %s", id)
	}
	return nil
}

// BackfillEmbeddings generates embeddings for all chunks that don't have one.
// Returns the number of chunks updated. The callback is called after each chunk
// with (completed, total, skipped) counts for progress reporting.
func (s *SQLiteStore) BackfillEmbeddings(ctx context.Context, progressFn func(done, total, skipped int)) (int, error) {
	if s.embedder == nil {
		return 0, fmt.Errorf("no embedding provider configured")
	}

	// Count chunks needing embeddings
	var total int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chunks c
		 JOIN memories m ON c.memory_id = m.id
		 WHERE c.embedding IS NULL AND m.deleted_at IS NULL`).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("count chunks: %w", err)
	}
	if total == 0 {
		return 0, nil
	}

	// Fetch chunks in batches
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.rowid, c.text FROM chunks c
		 JOIN memories m ON c.memory_id = m.id
		 WHERE c.embedding IS NULL AND m.deleted_at IS NULL
		 ORDER BY c.rowid`)
	if err != nil {
		return 0, fmt.Errorf("query chunks: %w", err)
	}
	defer rows.Close()

	updated := 0
	skipped := 0
	for rows.Next() {
		var rowid int64
		var text string
		if err := rows.Scan(&rowid, &text); err != nil {
			return updated, fmt.Errorf("scan chunk: %w", err)
		}

		vec, err := s.embedder.Embed(ctx, text)
		if err != nil || len(vec) == 0 {
			skipped++
			if progressFn != nil {
				progressFn(updated, total, skipped)
			}
			continue
		}

		b, _ := json.Marshal(vec)
		_, err = s.db.ExecContext(ctx,
			`UPDATE chunks SET embedding = ? WHERE rowid = ?`, string(b), rowid)
		if err != nil {
			return updated, fmt.Errorf("update chunk: %w", err)
		}
		updated++

		if progressFn != nil {
			progressFn(updated, total, skipped)
		}
	}

	return updated, nil
}

// touchMemories batch-updates access_count and last_accessed_at for the given memory IDs.
func (s *SQLiteStore) touchMemories(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	now := time.Now().UTC().Format(time.RFC3339)
	args := []interface{}{now}
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := s.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE memories SET access_count = access_count + 1, last_accessed_at = ? WHERE id IN (%s) AND deleted_at IS NULL`, placeholders),
		args...,
	)
	return err
}

package store

import (
	"context"
	"fmt"
	"time"
)

// GCResult holds the result of an expired-memory garbage collection.
type GCResult struct {
	MemoriesDeleted int64
	ChunksFreed     int64
}

// GCStaleResult holds the result of a stale-memory garbage collection.
type GCStaleResult struct {
	MemoriesDeleted int64
	ProtectedCount  int64
}

// GCStaleDryRun counts stale memories (not accessed within the given duration)
// without deleting them. Memories with priority "high" or "critical" are skipped.
func (s *SQLiteStore) GCStaleDryRun(ctx context.Context, staleThreshold time.Duration) (GCStaleResult, error) {
	cutoff := time.Now().UTC().Add(-staleThreshold).Format(time.RFC3339)
	var result GCStaleResult

	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories
		 WHERE deleted_at IS NULL
		   AND priority NOT IN ('high', 'critical')
		   AND COALESCE(last_accessed_at, created_at) < ?`, cutoff).Scan(&result.MemoriesDeleted)
	if err != nil {
		return result, err
	}

	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories
		 WHERE deleted_at IS NULL
		   AND priority IN ('high', 'critical')
		   AND COALESCE(last_accessed_at, created_at) < ?`, cutoff).Scan(&result.ProtectedCount)
	if err != nil {
		return result, err
	}

	return result, nil
}

// GCStale soft-deletes memories not accessed within the given duration.
// Memories with priority "high" or "critical" are skipped.
func (s *SQLiteStore) GCStale(ctx context.Context, staleThreshold time.Duration) (GCStaleResult, error) {
	now := time.Now().UTC()
	cutoff := now.Add(-staleThreshold).Format(time.RFC3339)
	nowStr := now.Format(time.RFC3339)
	var result GCStaleResult

	// Count protected memories (high/critical that are stale but skipped)
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories
		 WHERE deleted_at IS NULL
		   AND priority IN ('high', 'critical')
		   AND COALESCE(last_accessed_at, created_at) < ?`, cutoff).Scan(&result.ProtectedCount)
	if err != nil {
		return result, err
	}

	res, err := s.db.ExecContext(ctx,
		`UPDATE memories SET deleted_at = ?
		 WHERE deleted_at IS NULL
		   AND priority NOT IN ('high', 'critical')
		   AND COALESCE(last_accessed_at, created_at) < ?`, nowStr, cutoff)
	if err != nil {
		return result, fmt.Errorf("soft-delete stale memories: %w", err)
	}

	result.MemoriesDeleted, err = res.RowsAffected()
	if err != nil {
		return result, err
	}

	// Also prune low-utility memories: accessed 5+ times but useful <20%
	utilRes, err := s.db.ExecContext(ctx,
		`UPDATE memories SET deleted_at = ?
		 WHERE deleted_at IS NULL
		   AND access_count >= 5
		   AND utility_count > 0
		   AND CAST(utility_count AS REAL) / CAST(access_count AS REAL) < 0.2`, nowStr)
	if err == nil {
		n, _ := utilRes.RowsAffected()
		result.MemoriesDeleted += n
	}

	return result, nil
}

// GCDryRun counts expired memories and their chunks without deleting.
func (s *SQLiteStore) GCDryRun(ctx context.Context) (GCResult, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	var result GCResult

	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories WHERE expires_at IS NOT NULL AND expires_at < ?`, now).Scan(&result.MemoriesDeleted)
	if err != nil {
		return result, err
	}

	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chunks WHERE memory_id IN (
			SELECT id FROM memories WHERE expires_at IS NOT NULL AND expires_at < ?
		)`, now).Scan(&result.ChunksFreed)
	if err != nil {
		return result, err
	}

	return result, nil
}

// GC deletes expired memories (where expires_at < now) and their chunks.
func (s *SQLiteStore) GC(ctx context.Context) (GCResult, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	var result GCResult

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()

	// Count chunks to be deleted
	err = tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chunks WHERE memory_id IN (
			SELECT id FROM memories WHERE expires_at IS NOT NULL AND expires_at < ?
		)`, now).Scan(&result.ChunksFreed)
	if err != nil {
		return result, err
	}

	// Delete edges referencing expired memories
	_, err = tx.ExecContext(ctx,
		`DELETE FROM memory_edges WHERE from_id IN (
			SELECT id FROM memories WHERE expires_at IS NOT NULL AND expires_at < ?
		) OR to_id IN (
			SELECT id FROM memories WHERE expires_at IS NOT NULL AND expires_at < ?
		)`, now, now)
	if err != nil {
		return result, fmt.Errorf("delete expired edges: %w", err)
	}

	// Delete chunks belonging to expired memories
	_, err = tx.ExecContext(ctx,
		`DELETE FROM chunks WHERE memory_id IN (
			SELECT id FROM memories WHERE expires_at IS NOT NULL AND expires_at < ?
		)`, now)
	if err != nil {
		return result, fmt.Errorf("delete expired chunks: %w", err)
	}

	// Delete expired memories
	res, err := tx.ExecContext(ctx,
		`DELETE FROM memories WHERE expires_at IS NOT NULL AND expires_at < ?`, now)
	if err != nil {
		return result, fmt.Errorf("delete expired memories: %w", err)
	}

	result.MemoriesDeleted, err = res.RowsAffected()
	if err != nil {
		return result, err
	}

	if err := tx.Commit(); err != nil {
		return result, err
	}

	return result, nil
}

// MemoryCount returns the number of active (non-deleted, non-expired) memories.
func (s *SQLiteStore) MemoryCount(ctx context.Context) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	var count int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories WHERE deleted_at IS NULL AND (expires_at IS NULL OR expires_at > ?)`, now).Scan(&count)
	return count, err
}

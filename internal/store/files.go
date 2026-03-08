package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rcliao/ghost/internal/model"
)

// FindByFileParams holds parameters for finding memories linked to a file.
type FindByFileParams struct {
	Path  string
	Rel   string // optional: filter by relationship type
	Limit int
}

// GetFiles returns all file references for a memory.
func (s *SQLiteStore) GetFiles(ctx context.Context, memoryID string) ([]model.FileRef, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT path, rel FROM memory_files WHERE memory_id = ? ORDER BY path`, memoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []model.FileRef
	for rows.Next() {
		var f model.FileRef
		if err := rows.Scan(&f.Path, &f.Rel); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, nil
}

// loadFilesForMemories populates the Files field on each memory.
func (s *SQLiteStore) loadFilesForMemories(ctx context.Context, memories []model.Memory) error {
	if len(memories) == 0 {
		return nil
	}

	ids := make([]string, len(memories))
	args := make([]interface{}, len(memories))
	for i, m := range memories {
		ids[i] = "?"
		args[i] = m.ID
	}

	query := fmt.Sprintf(
		`SELECT memory_id, path, rel FROM memory_files WHERE memory_id IN (%s) ORDER BY path`,
		strings.Join(ids, ","))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	fileMap := map[string][]model.FileRef{}
	for rows.Next() {
		var memID string
		var f model.FileRef
		if err := rows.Scan(&memID, &f.Path, &f.Rel); err != nil {
			return err
		}
		fileMap[memID] = append(fileMap[memID], f)
	}

	for i := range memories {
		if files, ok := fileMap[memories[i].ID]; ok {
			memories[i].Files = files
		}
	}
	return nil
}

// FindByFile returns memories linked to a given file path.
func (s *SQLiteStore) FindByFile(ctx context.Context, p FindByFileParams) ([]model.Memory, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = 20
	}

	now := time.Now().UTC().Format(time.RFC3339)
	where := []string{
		"m.deleted_at IS NULL",
		"(m.expires_at IS NULL OR m.expires_at > ?)",
		"mf.path = ?",
	}
	args := []interface{}{now, p.Path}

	if p.Rel != "" {
		where = append(where, "mf.rel = ?")
		args = append(args, p.Rel)
	}

	query := fmt.Sprintf(`
		SELECT m.id, m.ns, m.key, m.content, m.kind, m.tags, m.version, m.supersedes,
		       m.created_at, m.deleted_at, m.priority, m.access_count, m.last_accessed_at, m.meta, m.expires_at,
		       m.importance, m.utility_count, m.tier, m.est_tokens
		FROM memories m
		INNER JOIN memory_files mf ON mf.memory_id = m.id
		INNER JOIN (
			SELECT ns, key, MAX(version) AS max_ver
			FROM memories WHERE deleted_at IS NULL
			GROUP BY ns, key
		) latest ON m.ns = latest.ns AND m.key = latest.key AND m.version = latest.max_ver
		WHERE %s
		ORDER BY m.created_at DESC
		LIMIT ?`, strings.Join(where, " AND "))
	args = append(args, limit)

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

	// Load file refs for all returned memories
	if err := s.loadFilesForMemories(ctx, memories); err != nil {
		return nil, err
	}

	return memories, nil
}

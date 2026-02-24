package cli

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

type gcResult struct {
	Mode        string `json:"mode"`
	Deleted     int64  `json:"deleted"`
	WouldDelete int64  `json:"would_delete"`
	ChunksFreed int64  `json:"chunks_freed"`
	Protected   int64  `json:"protected"`
	Remaining   int64  `json:"remaining"`
	DBSizeBytes int64  `json:"db_size_bytes"`
}

func parseGCResult(t *testing.T, out string) gcResult {
	t.Helper()
	var r gcResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("unmarshal gc result: %v\nraw: %s", err, out)
	}
	return r
}

func TestGCDeletesExpired(t *testing.T) {
	db := tempDB(t)

	// Put a memory with TTL
	_, err := executeCmd(t, "put", "--db", db, "-n", "ns", "-k", "temp", "--ttl", "1s", "temporary")
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// Put a permanent memory
	_, err = executeCmd(t, "put", "--db", db, "-n", "ns", "-k", "perm", "permanent")
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// Manually set expires_at to the past
	rawDB, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	_, err = rawDB.Exec(`UPDATE memories SET expires_at = '2000-01-01T00:00:00Z' WHERE key = 'temp'`)
	if err != nil {
		t.Fatalf("update expires_at: %v", err)
	}
	rawDB.Close()

	// Run GC — auto-GC on store open already deletes expired memories,
	// so the explicit gc reports 0.
	out, err := executeCmd(t, "gc", "--db", db)
	if err != nil {
		t.Fatalf("gc: %v", err)
	}

	r := parseGCResult(t, out)
	if r.Mode != "expired" {
		t.Errorf("expected mode=expired, got %q", r.Mode)
	}
	if r.Deleted != 0 {
		t.Errorf("expected 0 deleted (auto-GC already cleaned), got %d", r.Deleted)
	}
	if r.Remaining != 1 {
		t.Errorf("expected 1 remaining, got %d", r.Remaining)
	}
	if r.DBSizeBytes <= 0 {
		t.Errorf("expected positive db_size_bytes, got %d", r.DBSizeBytes)
	}

	// Verify expired memory is gone (cleaned by auto-GC on store open)
	verifyDB, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatalf("open verify db: %v", err)
	}
	var expiredCount int
	verifyDB.QueryRow(`SELECT COUNT(*) FROM memories WHERE key = 'temp'`).Scan(&expiredCount)
	verifyDB.Close()
	if expiredCount != 0 {
		t.Errorf("expected expired memory to be gone, but found %d", expiredCount)
	}

	// Verify permanent memory still exists
	getOut, err := executeCmd(t, "get", "--db", db, "-n", "ns", "-k", "perm")
	if err != nil {
		t.Fatalf("get perm: %v", err)
	}
	if getOut == "" {
		t.Error("expected permanent memory to survive GC")
	}
}

func TestGCNothingToDelete(t *testing.T) {
	db := tempDB(t)

	// Put a permanent memory
	_, err := executeCmd(t, "put", "--db", db, "-n", "ns", "-k", "perm", "permanent")
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	out, err := executeCmd(t, "gc", "--db", db)
	if err != nil {
		t.Fatalf("gc: %v", err)
	}

	r := parseGCResult(t, out)
	if r.Deleted != 0 {
		t.Errorf("expected 0 deleted, got %d", r.Deleted)
	}
	if r.Remaining != 1 {
		t.Errorf("expected 1 remaining, got %d", r.Remaining)
	}
}

func TestGCDryRun(t *testing.T) {
	db := tempDB(t)

	// Put a memory with TTL
	_, err := executeCmd(t, "put", "--db", db, "-n", "ns", "-k", "temp", "--ttl", "1s", "temporary")
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// Put a permanent memory
	_, err = executeCmd(t, "put", "--db", db, "-n", "ns", "-k", "perm", "permanent")
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// Manually set expires_at to the past
	rawDB, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	_, err = rawDB.Exec(`UPDATE memories SET expires_at = '2000-01-01T00:00:00Z' WHERE key = 'temp'`)
	if err != nil {
		t.Fatalf("update expires_at: %v", err)
	}
	rawDB.Close()

	// Dry run opens the store — auto-GC already deletes expired memories,
	// so dry-run reports 0 remaining.
	out, err := executeCmd(t, "gc", "--db", db, "--dry-run")
	if err != nil {
		t.Fatalf("gc dry-run: %v", err)
	}

	r := parseGCResult(t, out)
	if r.Mode != "expired_dry_run" {
		t.Errorf("expected mode=expired_dry_run, got %q", r.Mode)
	}
	if r.WouldDelete != 0 {
		t.Errorf("expected would_delete=0 (auto-GC already cleaned), got %d", r.WouldDelete)
	}
	if r.Remaining != 1 {
		t.Errorf("expected 1 remaining, got %d", r.Remaining)
	}

	// Verify expired memory is gone (auto-GC on store open)
	verifyDB, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatalf("open verify db: %v", err)
	}
	var expiredCount int
	verifyDB.QueryRow(`SELECT COUNT(*) FROM memories WHERE key = 'temp'`).Scan(&expiredCount)
	verifyDB.Close()
	if expiredCount != 0 {
		t.Errorf("expected expired memory to be gone, but found %d", expiredCount)
	}
}

func TestGCDryRunNothingExpired(t *testing.T) {
	db := tempDB(t)

	_, err := executeCmd(t, "put", "--db", db, "-n", "ns", "-k", "perm", "permanent")
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	out, err := executeCmd(t, "gc", "--db", db, "--dry-run")
	if err != nil {
		t.Fatalf("gc dry-run: %v", err)
	}

	r := parseGCResult(t, out)
	if r.WouldDelete != 0 {
		t.Errorf("expected would_delete=0, got %d", r.WouldDelete)
	}
	if r.Remaining != 1 {
		t.Errorf("expected 1 remaining, got %d", r.Remaining)
	}
}

func TestGCEmptyDB(t *testing.T) {
	db := tempDB(t)

	// Run GC on fresh empty database
	out, err := executeCmd(t, "gc", "--db", db)
	if err != nil {
		t.Fatalf("gc: %v", err)
	}

	r := parseGCResult(t, out)
	if r.Deleted != 0 {
		t.Errorf("expected 0 deleted, got %d", r.Deleted)
	}
	if r.Remaining != 0 {
		t.Errorf("expected 0 remaining, got %d", r.Remaining)
	}
}

func TestGCStaleDeletesOldNormalMemory(t *testing.T) {
	db := tempDB(t)

	// Put a normal-priority memory
	_, err := executeCmd(t, "put", "--db", db, "-n", "ns", "-k", "old", "-p", "normal", "stale data")
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// Put a high-priority memory
	_, err = executeCmd(t, "put", "--db", db, "-n", "ns", "-k", "important", "-p", "high", "important data")
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// Put a recent normal-priority memory
	_, err = executeCmd(t, "put", "--db", db, "-n", "ns", "-k", "recent", "-p", "normal", "fresh data")
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// Backdate old and important memories to 60 days ago
	rawDB, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	sixtyDaysAgo := time.Now().UTC().Add(-60 * 24 * time.Hour).Format(time.RFC3339)
	_, err = rawDB.Exec(`UPDATE memories SET created_at = ?, last_accessed_at = NULL WHERE key IN ('old', 'important')`, sixtyDaysAgo)
	if err != nil {
		t.Fatalf("backdate: %v", err)
	}
	rawDB.Close()

	// Run gc --stale 30d
	out, err := executeCmd(t, "gc", "--db", db, "--stale", "30d")
	if err != nil {
		t.Fatalf("gc stale: %v", err)
	}

	r := parseGCResult(t, out)
	if r.Mode != "stale" {
		t.Errorf("expected mode=stale, got %q", r.Mode)
	}
	if r.Deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", r.Deleted)
	}
	if r.Protected != 1 {
		t.Errorf("expected 1 protected (high), got %d", r.Protected)
	}
	if r.Remaining != 2 {
		t.Errorf("expected 2 remaining, got %d", r.Remaining)
	}

	// Verify high-priority memory still exists
	getOut, err := executeCmd(t, "get", "--db", db, "-n", "ns", "-k", "important")
	if err != nil {
		t.Fatalf("get important: %v", err)
	}
	if getOut == "" {
		t.Error("expected high-priority memory to survive --stale")
	}

	// Verify recent memory still exists
	getOut, err = executeCmd(t, "get", "--db", db, "-n", "ns", "-k", "recent")
	if err != nil {
		t.Fatalf("get recent: %v", err)
	}
	if getOut == "" {
		t.Error("expected recent memory to survive --stale")
	}
}

func TestGCStaleDryRunCLI(t *testing.T) {
	db := tempDB(t)

	// Put a normal-priority memory
	_, err := executeCmd(t, "put", "--db", db, "-n", "ns", "-k", "old", "-p", "normal", "stale data")
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// Backdate to 60 days ago
	rawDB, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	sixtyDaysAgo := time.Now().UTC().Add(-60 * 24 * time.Hour).Format(time.RFC3339)
	_, err = rawDB.Exec(`UPDATE memories SET created_at = ?, last_accessed_at = NULL WHERE key = 'old'`, sixtyDaysAgo)
	if err != nil {
		t.Fatalf("backdate: %v", err)
	}
	rawDB.Close()

	// Dry run should report 1
	out, err := executeCmd(t, "gc", "--db", db, "--stale", "30d", "--dry-run")
	if err != nil {
		t.Fatalf("gc stale dry-run: %v", err)
	}

	r := parseGCResult(t, out)
	if r.WouldDelete != 1 {
		t.Errorf("expected would_delete=1, got %d", r.WouldDelete)
	}
	if r.Remaining != 1 {
		t.Errorf("expected 1 remaining, got %d", r.Remaining)
	}

	// Verify memory was NOT deleted — real gc --stale should still find it
	gcOut, err := executeCmd(t, "gc", "--db", db, "--stale", "30d")
	if err != nil {
		t.Fatalf("gc stale after dry-run: %v", err)
	}
	gcR := parseGCResult(t, gcOut)
	if gcR.Deleted != 1 {
		t.Errorf("expected 1 deleted after dry-run, got %d", gcR.Deleted)
	}
}

func TestGCStaleSkipsCritical(t *testing.T) {
	db := tempDB(t)

	// Put a critical-priority memory
	_, err := executeCmd(t, "put", "--db", db, "-n", "ns", "-k", "crit", "-p", "critical", "critical data")
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// Backdate to 60 days ago
	rawDB, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	sixtyDaysAgo := time.Now().UTC().Add(-60 * 24 * time.Hour).Format(time.RFC3339)
	_, err = rawDB.Exec(`UPDATE memories SET created_at = ?, last_accessed_at = NULL WHERE key = 'crit'`, sixtyDaysAgo)
	if err != nil {
		t.Fatalf("backdate: %v", err)
	}
	rawDB.Close()

	// gc --stale 30d should delete 0 (critical is protected)
	out, err := executeCmd(t, "gc", "--db", db, "--stale", "30d")
	if err != nil {
		t.Fatalf("gc stale: %v", err)
	}

	r := parseGCResult(t, out)
	if r.Deleted != 0 {
		t.Errorf("expected 0 deleted (critical protected), got %d", r.Deleted)
	}
	if r.Protected != 1 {
		t.Errorf("expected 1 protected (critical), got %d", r.Protected)
	}
}

func TestGCDeletesMultipleExpired(t *testing.T) {
	db := tempDB(t)

	// Put 3 memories with TTL
	for _, key := range []string{"temp1", "temp2", "temp3"} {
		_, err := executeCmd(t, "put", "--db", db, "-n", "ns", "-k", key, "--ttl", "1s", "temporary "+key)
		if err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}

	// Put a permanent memory
	_, err := executeCmd(t, "put", "--db", db, "-n", "ns", "-k", "perm", "permanent")
	if err != nil {
		t.Fatalf("put perm: %v", err)
	}

	// Expire all temp memories
	rawDB, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	_, err = rawDB.Exec(`UPDATE memories SET expires_at = '2000-01-01T00:00:00Z' WHERE key LIKE 'temp%'`)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	rawDB.Close()

	// Run GC — auto-GC on store open already deletes the 3 expired memories,
	// so explicit gc reports 0.
	out, err := executeCmd(t, "gc", "--db", db)
	if err != nil {
		t.Fatalf("gc: %v", err)
	}

	r := parseGCResult(t, out)
	if r.Deleted != 0 {
		t.Errorf("expected 0 deleted (auto-GC already cleaned), got %d", r.Deleted)
	}
	if r.Remaining != 1 {
		t.Errorf("expected 1 remaining, got %d", r.Remaining)
	}

	// Verify expired memories are gone (cleaned by auto-GC)
	verifyDB, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatalf("open verify db: %v", err)
	}
	var expiredCount int
	verifyDB.QueryRow(`SELECT COUNT(*) FROM memories WHERE key LIKE 'temp%'`).Scan(&expiredCount)
	verifyDB.Close()
	if expiredCount != 0 {
		t.Errorf("expected 0 expired memories, got %d", expiredCount)
	}

	// Verify permanent memory survived
	getOut, err := executeCmd(t, "get", "--db", db, "-n", "ns", "-k", "perm")
	if err != nil {
		t.Fatalf("get perm: %v", err)
	}
	if getOut == "" {
		t.Error("expected permanent memory to survive GC")
	}
}

func TestGCStaleSkipsHighPriority(t *testing.T) {
	db := tempDB(t)

	// Put a high-priority memory
	_, err := executeCmd(t, "put", "--db", db, "-n", "ns", "-k", "important", "-p", "high", "important data")
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// Backdate to 60 days ago
	rawDB, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	sixtyDaysAgo := time.Now().UTC().Add(-60 * 24 * time.Hour).Format(time.RFC3339)
	_, err = rawDB.Exec(`UPDATE memories SET created_at = ?, last_accessed_at = NULL WHERE key = 'important'`, sixtyDaysAgo)
	if err != nil {
		t.Fatalf("backdate: %v", err)
	}
	rawDB.Close()

	// gc --stale 30d should delete 0 (high priority is protected)
	out, err := executeCmd(t, "gc", "--db", db, "--stale", "30d")
	if err != nil {
		t.Fatalf("gc stale: %v", err)
	}

	r := parseGCResult(t, out)
	if r.Deleted != 0 {
		t.Errorf("expected 0 deleted (high priority protected), got %d", r.Deleted)
	}
	if r.Protected != 1 {
		t.Errorf("expected 1 protected (high), got %d", r.Protected)
	}
}

func TestGCStaleUsesCreatedAtWhenNoLastAccess(t *testing.T) {
	db := tempDB(t)

	// Put a memory (will have NULL last_accessed_at since never read via get)
	_, err := executeCmd(t, "put", "--db", db, "-n", "ns", "-k", "never-accessed", "old data")
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// Backdate created_at to 90 days ago, confirm last_accessed_at is NULL
	rawDB, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	ninetyDaysAgo := time.Now().UTC().Add(-90 * 24 * time.Hour).Format(time.RFC3339)
	_, err = rawDB.Exec(`UPDATE memories SET created_at = ?, last_accessed_at = NULL WHERE key = 'never-accessed'`, ninetyDaysAgo)
	if err != nil {
		t.Fatalf("backdate: %v", err)
	}
	rawDB.Close()

	// gc --stale 30d should detect this via COALESCE(last_accessed_at, created_at)
	out, err := executeCmd(t, "gc", "--db", db, "--stale", "30d")
	if err != nil {
		t.Fatalf("gc stale: %v", err)
	}

	r := parseGCResult(t, out)
	if r.Deleted != 1 {
		t.Errorf("expected 1 deleted (created_at used as fallback), got %d", r.Deleted)
	}
}

func TestGCStaleRespectsLastAccessedAt(t *testing.T) {
	db := tempDB(t)

	// Put a memory
	_, err := executeCmd(t, "put", "--db", db, "-n", "ns", "-k", "recently-accessed", "data")
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// Set created_at to 90 days ago but last_accessed_at to 5 days ago
	rawDB, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	ninetyDaysAgo := time.Now().UTC().Add(-90 * 24 * time.Hour).Format(time.RFC3339)
	fiveDaysAgo := time.Now().UTC().Add(-5 * 24 * time.Hour).Format(time.RFC3339)
	_, err = rawDB.Exec(`UPDATE memories SET created_at = ?, last_accessed_at = ? WHERE key = 'recently-accessed'`, ninetyDaysAgo, fiveDaysAgo)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	rawDB.Close()

	// gc --stale 30d should NOT delete (last_accessed_at is within 30 days)
	out, err := executeCmd(t, "gc", "--db", db, "--stale", "30d")
	if err != nil {
		t.Fatalf("gc stale: %v", err)
	}

	r := parseGCResult(t, out)
	if r.Deleted != 0 {
		t.Errorf("expected 0 deleted (last_accessed_at is recent), got %d", r.Deleted)
	}
}

func TestGCStaleDryRunWithMixedPriorities(t *testing.T) {
	db := tempDB(t)

	// Put memories with different priorities
	for _, tc := range []struct{ key, priority string }{
		{"low1", "low"},
		{"normal1", "normal"},
		{"high1", "high"},
		{"critical1", "critical"},
	} {
		_, err := executeCmd(t, "put", "--db", db, "-n", "ns", "-k", tc.key, "-p", tc.priority, "data")
		if err != nil {
			t.Fatalf("put %s: %v", tc.key, err)
		}
	}

	// Backdate all to 60 days ago
	rawDB, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	sixtyDaysAgo := time.Now().UTC().Add(-60 * 24 * time.Hour).Format(time.RFC3339)
	_, err = rawDB.Exec(`UPDATE memories SET created_at = ?, last_accessed_at = NULL`, sixtyDaysAgo)
	if err != nil {
		t.Fatalf("backdate: %v", err)
	}
	rawDB.Close()

	// Dry run should report 2 would_delete (low + normal) and 2 protected (high + critical)
	out, err := executeCmd(t, "gc", "--db", db, "--stale", "30d", "--dry-run")
	if err != nil {
		t.Fatalf("gc stale dry-run: %v", err)
	}

	r := parseGCResult(t, out)
	if r.Mode != "stale_dry_run" {
		t.Errorf("expected mode=stale_dry_run, got %q", r.Mode)
	}
	if r.WouldDelete != 2 {
		t.Errorf("expected would_delete=2 (low+normal), got %d", r.WouldDelete)
	}
	if r.Protected != 2 {
		t.Errorf("expected protected=2 (high+critical), got %d", r.Protected)
	}
	if r.Remaining != 4 {
		t.Errorf("expected 4 remaining (dry-run doesn't delete), got %d", r.Remaining)
	}
}

func TestGCTextFormat(t *testing.T) {
	db := tempDB(t)

	_, err := executeCmd(t, "put", "--db", db, "-n", "ns", "-k", "perm", "permanent")
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// Text format should output a human-readable line, not JSON
	out, err := executeCmd(t, "gc", "--db", db, "--format", "text")
	if err != nil {
		t.Fatalf("gc text: %v", err)
	}

	if !strings.Contains(out, "gc:") {
		t.Errorf("expected text output starting with 'gc:', got %q", out)
	}
	if !strings.Contains(out, "1 remaining") {
		t.Errorf("expected '1 remaining' in text output, got %q", out)
	}
	// Should not be valid JSON
	var probe map[string]interface{}
	if json.Unmarshal([]byte(out), &probe) == nil {
		t.Error("text format should not be valid JSON")
	}
}

func TestGCTextFormatStale(t *testing.T) {
	db := tempDB(t)

	_, err := executeCmd(t, "put", "--db", db, "-n", "ns", "-k", "old", "-p", "normal", "stale data")
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	rawDB, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	sixtyDaysAgo := time.Now().UTC().Add(-60 * 24 * time.Hour).Format(time.RFC3339)
	_, err = rawDB.Exec(`UPDATE memories SET created_at = ?, last_accessed_at = NULL WHERE key = 'old'`, sixtyDaysAgo)
	if err != nil {
		t.Fatalf("backdate: %v", err)
	}
	rawDB.Close()

	out, err := executeCmd(t, "gc", "--db", db, "--stale", "30d", "--format", "text")
	if err != nil {
		t.Fatalf("gc stale text: %v", err)
	}

	if !strings.Contains(out, "stale") {
		t.Errorf("expected 'stale' in text output, got %q", out)
	}
	if !strings.Contains(out, "protected") {
		t.Errorf("expected 'protected' in text output, got %q", out)
	}
}

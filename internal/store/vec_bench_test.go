package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"sort"
	"testing"
	"time"

	_ "modernc.org/sqlite/vec" // test-only: installs sqlite-vec on every connection in this binary
)

// TestVecBench asks whether sqlite-vec's vec0 table beats ghost's Go-side
// brute-force cosine scan on a REAL store, before any production code adopts
// it. sqlite-vec documents no index strategy, so the only way to know whether
// vec0 is an index or a faster scan is to measure. Gated; a measurement, not
// a pass/fail test.
//
//	GHOST_BENCH_LATENCY_DB=/path/to/snapshot.db GHOST_NS=agent:x \
//	  GHOST_EMBED_PROVIDER=local GHOST_EMBED_MODEL_LOCAL=... \
//	  go test ./internal/store/ -run TestVecBench -v
func TestVecBench(t *testing.T) {
	dbPath := os.Getenv("GHOST_BENCH_LATENCY_DB")
	if dbPath == "" {
		t.Skip("GHOST_BENCH_LATENCY_DB not set")
	}
	ns := os.Getenv("GHOST_NS")
	s, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.embedder == nil {
		t.Skip("no embedder; set GHOST_EMBED_PROVIDER=local")
	}
	ctx := context.Background()

	var ver string
	_ = s.db.QueryRow(`SELECT vec_version()`).Scan(&ver)
	t.Logf("sqlite-vec %s", ver)

	// Load every embedded chunk in the namespace once, as the scan does.
	// Same population Search scans: latest versions, dormant and sensory
	// excluded (Search sets ExcludeTiers on entry). Measuring over every tier
	// overstated the scan by ~4x on the first run.
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, c.embedding FROM chunks c JOIN memories m ON m.id = c.memory_id
		WHERE m.ns = ? AND m.deleted_at IS NULL AND c.embedding IS NOT NULL
		  AND COALESCE(m.tier,'stm') NOT IN ('dormant','sensory')`, ns)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		id  string
		vec []float32
	}
	var all []row
	dims := 0
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			t.Fatal(err)
		}
		v, err := decodeEmbedding(raw)
		if err != nil {
			continue
		}
		f := make([]float32, len(v))
		for i, x := range v {
			f[i] = float32(x)
		}
		dims = len(f)
		all = append(all, row{id, f})
	}
	rows.Close()
	t.Logf("chunks=%d dims=%d", len(all), dims)
	if len(all) == 0 {
		t.Skip("no embedded chunks")
	}

	// Build the vec0 table in a scratch copy's schema (this DB IS a scratch
	// copy: the harness never points the bench at a live store).
	if _, err := s.db.Exec(`DROP TABLE IF EXISTS vec_bench`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(fmt.Sprintf(`CREATE VIRTUAL TABLE vec_bench USING vec0(chunk_id TEXT PRIMARY KEY, embedding float[%d] distance_metric=cosine)`, dims)); err != nil {
		t.Fatalf("create vec0: %v", err)
	}
	start := time.Now()
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO vec_bench(chunk_id, embedding) VALUES (?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range all {
		if _, err := stmt.Exec(r.id, f32bytes(r.vec)); err != nil {
			t.Fatal(err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	t.Logf("vec0 build: %d rows in %s", len(all), time.Since(start).Round(time.Millisecond))

	queries := []string{
		"what did we decide about the deploy pipeline",
		"user preferences for communication style",
		"grocery list and meal planning",
		"what happened with the performance incident",
		"優勝美地 行程 早上 出發",
		"yogurt dairy probiotic plan",
	}
	const k = 50
	var scanTimes, vecTimes []time.Duration
	overlapSum := 0.0
	for _, q := range queries {
		qv, err := s.embedder.Embed(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		// Arm 1: ghost's scan, as Search runs it.
		t0 := time.Now()
		scan, err := s.searchVector(ctx, SearchParams{NS: ns, Query: q, ExcludeTiers: []string{"dormant", "sensory"}}, nil, k)
		scanTimes = append(scanTimes, time.Since(t0))
		if err != nil {
			t.Fatal(err)
		}
		// Arm 2: vec0 KNN over the same vectors.
		qf := make([]float32, len(qv))
		for i, x := range qv {
			qf[i] = float32(x)
		}
		t1 := time.Now()
		vrows, err := s.db.Query(`SELECT chunk_id, distance FROM vec_bench WHERE embedding MATCH ? ORDER BY distance LIMIT ?`, f32bytes(qf), k)
		if err != nil {
			t.Fatal(err)
		}
		vecIDs := map[string]bool{}
		for vrows.Next() {
			var id string
			var d float64
			_ = vrows.Scan(&id, &d)
			vecIDs[id] = true
		}
		vrows.Close()
		vecTimes = append(vecTimes, time.Since(t1))
		// Overlap of memory sets (scan returns memories; map chunk ids to memory ids).
		memOfChunk := map[string]string{}
		crow, _ := s.db.Query(`SELECT id, memory_id FROM chunks WHERE id IN (`+placeholders(len(vecIDs))+`)`, anyKeys(vecIDs)...)
		for crow.Next() {
			var cid, mid string
			_ = crow.Scan(&cid, &mid)
			memOfChunk[cid] = mid
		}
		crow.Close()
		vecMems := map[string]bool{}
		for cid := range vecIDs {
			vecMems[memOfChunk[cid]] = true
		}
		hit := 0
		for _, r := range scan {
			if vecMems[r.ID] {
				hit++
			}
		}
		ov := 0.0
		if len(scan) > 0 {
			ov = float64(hit) / float64(len(scan))
		}
		overlapSum += ov
		t.Logf("%-46q scan=%-7s vec0=%-7s scan_hits=%d overlap=%.2f", q, scanTimes[len(scanTimes)-1].Round(time.Millisecond), vecTimes[len(vecTimes)-1].Round(time.Millisecond), len(scan), ov)
	}
	t.Logf("SUMMARY chunks=%d  scan p50=%s  vec0 p50=%s  mean overlap of top-%d memory sets=%.2f",
		len(all), p50(scanTimes), p50(vecTimes), k, overlapSum/float64(len(queries)))
	_, _ = s.db.Exec(`DROP TABLE IF EXISTS vec_bench`)
}

func f32bytes(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func placeholders(n int) string {
	if n == 0 {
		return "''"
	}
	s := "?"
	for i := 1; i < n; i++ {
		s += ",?"
	}
	return s
}

func anyKeys(m map[string]bool) []any {
	out := make([]any, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func p50(d []time.Duration) time.Duration {
	if len(d) == 0 {
		return 0
	}
	c := append([]time.Duration(nil), d...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	return c[len(c)/2].Round(time.Millisecond)
}

var _ = sql.ErrNoRows

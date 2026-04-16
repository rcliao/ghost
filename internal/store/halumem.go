package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// ── HaluMem benchmark loader (scaffold) ──────────────────────────
//
// HaluMem (arxiv 2511.03506, github.com/MemTensor/HaluMem) is the first
// operation-level hallucination benchmark for agent memory systems.
//
// Three evaluation tasks:
//   1. Memory Extraction  — Memory Integrity + Memory Accuracy
//   2. Memory Updating    — accuracy/hallucination/omission during updates
//   3. Question Answering — accuracy/hallucination/omission in downstream QA
//
// Dataset files (multi-stage pipeline, github.com/MemTensor/HaluMem/tree/main/data):
//   - stage4_1_events2memories.jsonl — gold memory points
//   - stage5_1_dialogue_generation.jsonl — conversational turns
//   - Various intermediate stages (persona, events, merged_events)
//
// Full integration requires their Python evaluation harness
// (eval/evaluation.py) which interfaces with Mem0/Zep/MemOS/Supermemory/
// Memobase via adapter scripts (eval_*.py). To add Ghost as a supported
// memory system, either:
//   (a) Expose Ghost via HTTP and write eval_ghost.py in their repo; or
//   (b) Port their evaluation tasks to Go (~3 tasks × gold+eval metrics)
//
// This file provides a minimal loader for the QA task so Ghost can measure
// its own scores independently when the dataset is available locally.

// HaluMemMemoryPoint is one gold memory point from stage4 output.
type HaluMemMemoryPoint struct {
	UserID  string `json:"user_id"`
	EventID string `json:"event_id"`
	Memory  string `json:"memory"` // the canonical memory fact
	// Additional fields vary by stage; accept them as raw
	Raw map[string]json.RawMessage `json:"-"`
}

// HaluMemQA is one evaluation query for the QA task.
type HaluMemQA struct {
	UserID   string   `json:"user_id"`
	Question string   `json:"question"`
	Answer   string   `json:"answer"`
	Evidence []string `json:"evidence,omitempty"` // referenced memory IDs
	Category string   `json:"category,omitempty"`
}

// LoadHaluMemJSONL reads a line-delimited JSON file and returns parsed entries.
// Callers provide the target type T via a factory; unknown fields preserved
// in Raw when possible.
func LoadHaluMemJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var out []T
	scanner := bufio.NewScanner(f)
	// Allow very long lines (dialogue turns can be >64KB)
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var v T
		if err := json.Unmarshal(line, &v); err != nil {
			continue // skip malformed lines rather than abort
		}
		out = append(out, v)
	}
	return out, scanner.Err()
}

// ── Status ────────────────────────────────────────────────────────
//
// As of 2026-04, Ghost's HaluMem integration is scaffolding only:
// - JSONL loaders work for HaluMemMemoryPoint and HaluMemQA
// - No ingestion pipeline, no evaluation harness yet
//
// Next steps for full integration (non-trivial):
// 1. Ingest stage4_1_events2memories.jsonl memories via BatchBenchInsert
// 2. For each QA in evaluation set, run ghost.Search + LLM-generate
// 3. Score via their three hallucination metrics (not just accuracy):
//    - Accuracy: correct answer present
//    - Hallucination: incorrect facts introduced
//    - Omission: correct facts missing
// 4. Compare Ghost to published baselines (Mem0, Zep, MemOS, etc.)
//
// See eval.md for integration progress and planned scope.

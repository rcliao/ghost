package store

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ── HaluMem benchmark loader + retrieval harness ──────────────────
//
// HaluMem (arxiv 2511.03506, github.com/MemTensor/HaluMem) decomposes the
// agent memory workflow into three operations: extraction, update, QA.
// The full benchmark uses LLM judges on each, against the IAAR-Shanghai/HaluMem
// dataset on Hugging Face. This file implements a retrieval-only harness
// for the QA task: ingest gold memory_points → search per question → score
// retrieved memories against the question's evidence memory_content.
//
// No LLM in Ghost's loop. The hallucination/omission rates require LLM-judge;
// we measure pure retrieval (Recall@K, MRR) here as a proxy for the
// retrieval-and-update half of the pipeline. The extraction and judge tasks
// are still out of scope for this harness.
//
// Dataset:
//   curl -L https://huggingface.co/datasets/IAAR-Shanghai/HaluMem/resolve/main/HaluMem-Medium.jsonl \
//     -o testdata/halumem/HaluMem-Medium.jsonl

// HaluMemMemoryPoint is one gold memory fact in a session.
type HaluMemMemoryPoint struct {
	Index           int     `json:"index"`
	MemoryContent   string  `json:"memory_content"`
	MemoryType      string  `json:"memory_type"`
	IsUpdate        string  `json:"is_update"` // "True" / "False"
	OriginalMemories []string `json:"original_memories,omitempty"`
	Timestamp       string  `json:"timestamp"`
	EventSource     int     `json:"event_source"`
	Importance      float64 `json:"importance"`
	MemorySource    string  `json:"memory_source"`
}

// HaluMemQA is one evaluation query attached to a session.
type HaluMemQA struct {
	Question     string                  `json:"question"`
	Answer       string                  `json:"answer"`
	Evidence     []HaluMemEvidence       `json:"evidence"`
	Difficulty   string                  `json:"difficulty"`
	QuestionType string                  `json:"question_type"`
}

// HaluMemEvidence points to a memory_point by its content (the dataset doesn't
// expose stable IDs, so we match on canonical content string).
type HaluMemEvidence struct {
	MemoryContent string `json:"memory_content"`
	MemoryType    string `json:"memory_type"`
}

// HaluMemSession bundles memory_points + dialogue + questions for one session.
type HaluMemSession struct {
	StartTime       string                 `json:"start_time"`
	EndTime         string                 `json:"end_time"`
	MemoryPoints    []HaluMemMemoryPoint   `json:"memory_points"`
	Questions       []HaluMemQA            `json:"questions"`
	// Dialogue, token counts, etc. are not needed for retrieval-only eval.
}

// HaluMemUser is one record (one user) from the dataset.
type HaluMemUser struct {
	UUID        string           `json:"uuid"`
	PersonaInfo string           `json:"persona_info"`
	Sessions    []HaluMemSession `json:"sessions"`
}

// LoadHaluMemJSONL reads a line-delimited JSON file and returns parsed entries.
func LoadHaluMemJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var out []T
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, 32*1024*1024) // HaluMem records are large
	lineN := 0
	for scanner.Scan() {
		lineN++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var v T
		if err := json.Unmarshal(line, &v); err != nil {
			return out, fmt.Errorf("line %d (len=%d): %w", lineN, len(line), err)
		}
		out = append(out, v)
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("scanner err after line %d: %w", lineN, err)
	}
	return out, nil
}

// ── Retrieval-only QA harness ─────────────────────────────────────

// HaluMemConfig controls the QA-retrieval benchmark run.
type HaluMemConfig struct {
	DatasetPath  string
	UserLimit    int  // max users to evaluate (0 = all)
	PerTypeLimit int  // max questions per question_type (0 = no cap)
	TopK         []int
	NS           string
	EmbedCachePath string
	ProgressFunc func(done, total int)
	// SkipBoundary skips Memory Boundary questions (evidence=0, test of
	// abstention). Retrieval recall isn't meaningful for them.
	SkipBoundary bool

	// LLM-judge E2E (optional). When LLM is set, each question goes through
	// Ghost retrieve → compress → answer → judge for Accuracy (C),
	// Hallucination (H), and Omission (O). Disabled if nil.
	LLM      LLMClient
	Judge    LLMClient // judge LLM (defaults to LLM)
	JudgeTopK int      // top-K memories to feed the answerer (default 5)
}

// HaluMemReport holds aggregate benchmark results.
type HaluMemReport struct {
	Timestamp time.Time                      `json:"timestamp"`
	Dataset   string                         `json:"dataset"`
	Total     int                            `json:"total"`
	ByType    map[string]*LongMemEvalTypeAgg `json:"by_question_type"`
	Overall   map[string]float64             `json:"overall"`
	Results   []HaluMemResult                `json:"results,omitempty"`
}

// HaluMemResult holds per-question retrieval outcome.
type HaluMemResult struct {
	UUID         string             `json:"uuid"`
	QuestionType string             `json:"question_type"`
	Difficulty   string             `json:"difficulty"`
	Question     string             `json:"question"`
	Answer       string             `json:"answer"`
	EvidenceN    int                `json:"evidence_n"`
	GoldRank     int                `json:"gold_rank"` // 1-based rank of first evidence hit
	Metrics      map[string]float64 `json:"metrics"`
}

// normalizeMemoryContent trims and lowercases for content-based matching.
func normalizeMemoryContent(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// RunHaluMemRetrieval ingests each user's gold memory_points as Ghost memories,
// then issues each QA query and measures retrieval against the question's
// evidence (matched on memory_content).
//
// Each user is isolated in its own SQLiteStore (built by newStore) so memories
// from one persona don't leak into another's queries.
func RunHaluMemRetrieval(cfg HaluMemConfig, newStore func() (*SQLiteStore, func(), error)) (*HaluMemReport, error) {
	users, err := LoadHaluMemJSONL[HaluMemUser](cfg.DatasetPath)
	if err != nil {
		return nil, err
	}
	if cfg.UserLimit > 0 && cfg.UserLimit < len(users) {
		users = users[:cfg.UserLimit]
	}
	if cfg.NS == "" {
		cfg.NS = "bench:halumem"
	}
	if len(cfg.TopK) == 0 {
		cfg.TopK = []int{5, 10}
	}

	report := &HaluMemReport{
		Timestamp: time.Now(),
		Dataset:   filepath.Base(cfg.DatasetPath),
		ByType:    make(map[string]*LongMemEvalTypeAgg),
		Overall:   make(map[string]float64),
	}

	ctx := context.Background()
	maxK := 0
	for _, k := range cfg.TopK {
		if k > maxK {
			maxK = k
		}
	}
	if maxK < 20 {
		maxK = 20
	}

	// Total evaluable questions (pre-count for progress).
	totalEval := 0
	typeSeen := map[string]int{}
	for _, u := range users {
		for _, s := range u.Sessions {
			for _, q := range s.Questions {
				if cfg.SkipBoundary && q.QuestionType == "Memory Boundary" {
					continue
				}
				if cfg.PerTypeLimit > 0 && typeSeen[q.QuestionType] >= cfg.PerTypeLimit {
					continue
				}
				typeSeen[q.QuestionType]++
				totalEval++
			}
		}
	}

	doneCounts := map[string]int{}
	done := 0

	for _, u := range users {
		store, cleanup, err := newStore()
		if err != nil {
			return nil, fmt.Errorf("create store for %s: %w", u.UUID, err)
		}

		// Ingest all memory_points across all sessions for this user.
		// Key = session_index + memory_index so updates have distinct keys.
		// (We don't model supersedes — the cross-encoder/rerank should rely on
		// retrieval order to handle conflicting memories.)
		var batch []BenchSession
		for sIdx, s := range u.Sessions {
			for _, mp := range s.MemoryPoints {
				key := fmt.Sprintf("s%d_m%d", sIdx, mp.Index)
				batch = append(batch, BenchSession{
					Key:     key,
					Content: mp.MemoryContent,
				})
			}
		}
		if len(batch) > 0 {
			if err := store.BatchBenchInsert(ctx, cfg.NS, batch); err != nil {
				cleanup()
				return nil, fmt.Errorf("ingest %s: %w", u.UUID, err)
			}
		}

		// Build content → key index for evidence matching.
		contentToKeys := make(map[string][]string, len(batch))
		for _, b := range batch {
			c := normalizeMemoryContent(b.Content)
			contentToKeys[c] = append(contentToKeys[c], b.Key)
		}

		// Evaluate each QA.
		userTypeSeen := map[string]int{}
		for _, s := range u.Sessions {
			for _, qa := range s.Questions {
				if cfg.SkipBoundary && qa.QuestionType == "Memory Boundary" {
					continue
				}
				if cfg.PerTypeLimit > 0 && doneCounts[qa.QuestionType] >= cfg.PerTypeLimit {
					continue
				}
				if len(qa.Evidence) == 0 && !cfg.SkipBoundary && qa.QuestionType != "Memory Boundary" {
					// Edge case: a question with no evidence that isn't a
					// boundary type. Skip — no ground truth to score against.
					continue
				}
				userTypeSeen[qa.QuestionType]++

				// Map evidence content → expected keys.
				wantKeys := map[string]bool{}
				for _, ev := range qa.Evidence {
					c := normalizeMemoryContent(ev.MemoryContent)
					for _, k := range contentToKeys[c] {
						wantKeys[k] = true
					}
				}

				results, err := store.Search(ctx, SearchParams{
					NS:         cfg.NS,
					Query:      qa.Question,
					Limit:      maxK,
					IncludeAll: true,
				})
				if err != nil {
					cleanup()
					return nil, fmt.Errorf("search: %w", err)
				}
				retrieved := make([]string, len(results))
				for i, r := range results {
					retrieved[i] = r.Key
				}

				relList := make([]string, 0, len(wantKeys))
				for k := range wantKeys {
					relList = append(relList, k)
				}
				sort.Strings(relList)

				if _, ok := report.ByType[qa.QuestionType]; !ok {
					report.ByType[qa.QuestionType] = &LongMemEvalTypeAgg{Metrics: make(map[string]float64)}
				}
				agg := report.ByType[qa.QuestionType]
				agg.Count++

				for _, k := range cfg.TopK {
					recall := RecallAtK(retrieved, relList, k)
					ndcg := NDCGAtK(retrieved, relList, k)
					precision := PrecisionAtK(retrieved, relList, k)
					rkey := fmt.Sprintf("recall@%d", k)
					nkey := fmt.Sprintf("ndcg@%d", k)
					pkey := fmt.Sprintf("precision@%d", k)
					agg.Metrics[rkey] += recall
					agg.Metrics[nkey] += ndcg
					agg.Metrics[pkey] += precision
					report.Overall[rkey] += recall
					report.Overall[nkey] += ndcg
					report.Overall[pkey] += precision
				}
				mrr := MRR(retrieved, wantKeys)
				agg.Metrics["mrr"] += mrr
				report.Overall["mrr"] += mrr

				// Optional LLM-judge E2E pass: retrieve → compress → answer →
				// judge for Accuracy / Hallucination / Omission.
				if cfg.LLM != nil {
					topK := cfg.JudgeTopK
					if topK <= 0 {
						topK = 5
					}
					if topK > len(results) {
						topK = len(results)
					}
					userMsg := compressContext(ctx, cfg.LLM, qa.Question, results[:topK]) + qa.Question
					answer, errA := cfg.LLM.Generate(ctx, e2eSystemPrompt, userMsg)
					judge := cfg.Judge
					if judge == nil {
						judge = cfg.LLM
					}
					var refMems []string
					for _, ev := range qa.Evidence {
						refMems = append(refMems, ev.MemoryContent)
					}
					if errA == nil {
						c, h, o := haluMemJudge(ctx, judge, qa.Question, qa.Answer, refMems, answer)
						agg.Metrics["judge_correct"] += c
						agg.Metrics["judge_hallucination"] += h
						agg.Metrics["judge_omission"] += o
						report.Overall["judge_correct"] += c
						report.Overall["judge_hallucination"] += h
						report.Overall["judge_omission"] += o
					}
				}

				goldRank := 0
				for i, k := range retrieved {
					if wantKeys[k] {
						goldRank = i + 1
						break
					}
				}
				report.Results = append(report.Results, HaluMemResult{
					UUID:         u.UUID,
					QuestionType: qa.QuestionType,
					Difficulty:   qa.Difficulty,
					Question:     qa.Question,
					Answer:       qa.Answer,
					EvidenceN:    len(qa.Evidence),
					GoldRank:     goldRank,
					Metrics: map[string]float64{
						"mrr":      mrr,
						"recall@5": RecallAtK(retrieved, relList, 5),
					},
				})

				doneCounts[qa.QuestionType]++
				report.Total++
				done++
				if cfg.ProgressFunc != nil && (done%25 == 0 || done == totalEval) {
					cfg.ProgressFunc(done, totalEval)
				}
			}
		}
		cleanup()
	}

	if report.Total > 0 {
		for k := range report.Overall {
			report.Overall[k] /= float64(report.Total)
		}
	}
	for _, agg := range report.ByType {
		if agg.Count > 0 {
			for k := range agg.Metrics {
				agg.Metrics[k] /= float64(agg.Count)
			}
		}
	}
	return report, nil
}

// haluMemJudgeSystemPrompt scores a memory system's QA response on three axes
// matching HaluMem's QA-evaluation framework: Accuracy (does it answer correctly),
// Hallucination (does it introduce wrong facts), Omission (does it miss correct facts).
//
// Output is three lines: `correct: yes|no`, `hallucination: yes|no`, `omission: yes|no`.
// Strict format keeps the parser cheap and the judge focused.
const haluMemJudgeSystemPrompt = `You are evaluating a memory-system answer against a gold reference.

You will see:
  - Question:   the user's question
  - Reference:  the canonical correct answer
  - Key memories: the gold memory facts the reference draws from
  - Response:   the memory system's answer

Score the response on three independent axes:

  1. correct        — yes if the response answers the question consistently with the Reference (paraphrasing OK), no otherwise.
  2. hallucination  — yes if the response asserts a fact that is NOT supported by the Key memories or contradicts them. No if the response stays grounded in the memories OR honestly says it doesn't know.
  3. omission       — yes if the response leaves out a fact from the Key memories that is necessary to fully answer the Question. No if it covers the needed facts.

Output exactly three lines in this format, lowercase:

correct: yes
hallucination: no
omission: no

No commentary, no preamble.`

// haluMemJudge calls the LLM-as-judge to score one response on
// Accuracy / Hallucination / Omission. Returns three 0..1 scores
// (1 means the axis flag fired for this question; aggregate across
// many questions to get the published HaluMem rates).
// On parse failure or LLM error, all three are 0 — caller can detect
// by aggregating a separate "judged" count if needed.
func haluMemJudge(ctx context.Context, judge LLMClient, question, reference string, keyMemories []string, response string) (correct, hallucination, omission float64) {
	if judge == nil {
		return 0, 0, 0
	}
	keyMems := strings.Join(keyMemories, "\n")
	if keyMems == "" {
		keyMems = "(none)"
	}
	msg := fmt.Sprintf("Question: %s\n\nReference: %s\n\nKey memories:\n%s\n\nResponse: %s\n\nScores:",
		question, reference, keyMems, response)
	out, err := judge.Generate(ctx, haluMemJudgeSystemPrompt, msg)
	if err != nil {
		return 0, 0, 0
	}
	parse := func(line, key string) float64 {
		line = strings.ToLower(strings.TrimSpace(line))
		if !strings.HasPrefix(line, key) {
			return 0
		}
		val := strings.TrimSpace(strings.TrimPrefix(line, key+":"))
		val = strings.TrimSpace(strings.TrimPrefix(val, key))
		if strings.HasPrefix(val, "yes") {
			return 1
		}
		return 0
	}
	for _, line := range strings.Split(out, "\n") {
		correct += parse(line, "correct")
		hallucination += parse(line, "hallucination")
		omission += parse(line, "omission")
	}
	// Clamp in case the judge double-emitted a label.
	if correct > 1 {
		correct = 1
	}
	if hallucination > 1 {
		hallucination = 1
	}
	if omission > 1 {
		omission = 1
	}
	return
}

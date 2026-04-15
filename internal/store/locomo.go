package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ── LoCoMo dataset types ───────────────────────────────────────────

// LoCoMoEntry represents one conversation from the LoCoMo dataset.
type LoCoMoEntry struct {
	SampleID     string                     `json:"sample_id"`
	Conversation map[string]json.RawMessage `json:"conversation"`
	QA           []LoCoMoQA                 `json:"qa"`
}

// LoCoMoTurn is a single dialogue turn.
type LoCoMoTurn struct {
	Speaker string `json:"speaker"`
	DiaID   string `json:"dia_id"`
	Text    string `json:"text"`
}

// LoCoMoQA is a question-answer pair with evidence.
type LoCoMoQA struct {
	Question          string          `json:"question"`
	RawAnswer         json.RawMessage `json:"answer,omitempty"`
	Answer            string          `json:"-"`
	AdversarialAnswer string          `json:"adversarial_answer,omitempty"`
	Evidence          []string        `json:"evidence"`
	Category          int             `json:"category"`
}

// LoCoMoConfig controls the benchmark run.
type LoCoMoConfig struct {
	DatasetPath    string
	Limit          int    // max QA pairs to evaluate (0 = all)
	PerCatLimit    int    // max QA per category (0 = no limit)
	TopK           []int
	NS             string
	EmbedCachePath string
	ExpandEdges    bool // if true, build entity edges and expand during search
	MultiQuery     bool // if true, decompose complex queries into sub-queries
	PRF            bool // if true, run pseudo-relevance feedback for multi-hop
	MMR            bool // if true, diversify top results via MMR
	ProgressFunc   func(done, total int)
}

// LoCoMoReport holds aggregate benchmark results.
type LoCoMoReport struct {
	Timestamp time.Time                       `json:"timestamp"`
	Dataset   string                          `json:"dataset"`
	Total     int                             `json:"total"`
	ByCat     map[string]*LongMemEvalTypeAgg  `json:"by_category"` // reuse type agg
	Overall   map[string]float64              `json:"overall"`
}

var diaIDRe = regexp.MustCompile(`^D(\d+):(\d+)$`)

// evidenceToSessions extracts unique session numbers from dia_id evidence list.
// "D1:3" → "session_1", "D15:7" → "session_15"
func evidenceToSessions(evidence []string) []string {
	seen := make(map[string]bool)
	var sessions []string
	for _, diaID := range evidence {
		m := diaIDRe.FindStringSubmatch(diaID)
		if m == nil {
			continue
		}
		sessionKey := "session_" + m[1]
		if !seen[sessionKey] {
			seen[sessionKey] = true
			sessions = append(sessions, sessionKey)
		}
	}
	return sessions
}

// parseLoCoMoSessions extracts ordered sessions from a conversation map.
func parseLoCoMoSessions(conv map[string]json.RawMessage) (sessions []struct {
	key     string
	date    string
	content string
}) {
	// Find all session keys
	sessionRe := regexp.MustCompile(`^session_(\d+)$`)
	type sessionInfo struct {
		num  int
		key  string
		date string
	}
	var infos []sessionInfo

	for k := range conv {
		m := sessionRe.FindStringSubmatch(k)
		if m == nil {
			continue
		}
		num, _ := strconv.Atoi(m[1])
		dateKey := k + "_date_time"
		var date string
		if raw, ok := conv[dateKey]; ok {
			json.Unmarshal(raw, &date)
		}
		infos = append(infos, sessionInfo{num: num, key: k, date: date})
	}

	sort.Slice(infos, func(i, j int) bool { return infos[i].num < infos[j].num })

	for _, info := range infos {
		var turns []LoCoMoTurn
		if err := json.Unmarshal(conv[info.key], &turns); err != nil {
			continue
		}
		var sb strings.Builder
		for _, t := range turns {
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(t.Speaker)
			sb.WriteString(": ")
			sb.WriteString(t.Text)
		}
		if sb.Len() > 0 {
			sessions = append(sessions, struct {
				key     string
				date    string
				content string
			}{key: info.key, date: info.date, content: sb.String()})
		}
	}
	return
}

// categoryName maps LoCoMo category numbers to readable names.
func categoryName(cat int) string {
	switch cat {
	case 1:
		return "single-hop"
	case 2:
		return "temporal"
	case 3:
		return "multi-hop"
	case 4:
		return "open-domain"
	case 5:
		return "adversarial"
	default:
		return fmt.Sprintf("cat-%d", cat)
	}
}

// LoadLoCoMo reads the LoCoMo dataset JSON.
func LoadLoCoMo(path string) ([]LoCoMoEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read dataset: %w", err)
	}
	var entries []LoCoMoEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse dataset: %w", err)
	}
	// Normalize answer field
	for i := range entries {
		for j := range entries[i].QA {
			qa := &entries[i].QA[j]
			if len(qa.RawAnswer) > 0 {
				var s string
				if err := json.Unmarshal(qa.RawAnswer, &s); err == nil {
					qa.Answer = s
				} else {
					qa.Answer = strings.Trim(string(qa.RawAnswer), "\"")
				}
			}
		}
	}
	return entries, nil
}

// RunLoCoMo executes the LoCoMo retrieval benchmark.
// For each conversation, ingests all sessions, then evaluates each QA pair.
func RunLoCoMo(cfg LoCoMoConfig, newStore func() (*SQLiteStore, func(), error)) (*LoCoMoReport, error) {
	entries, err := LoadLoCoMo(cfg.DatasetPath)
	if err != nil {
		return nil, err
	}

	if cfg.NS == "" {
		cfg.NS = "bench:locomo"
	}
	if len(cfg.TopK) == 0 {
		cfg.TopK = []int{5, 10}
	}

	// Collect all QA pairs across conversations with their store references
	type qaWithStore struct {
		qa       LoCoMoQA
		storeIdx int
	}

	report := &LoCoMoReport{
		Timestamp: time.Now(),
		Dataset:   filepath.Base(cfg.DatasetPath),
		ByCat:     make(map[string]*LongMemEvalTypeAgg),
		Overall:   make(map[string]float64),
	}

	ctx := context.Background()
	maxK := 0
	for _, k := range cfg.TopK {
		if k > maxK {
			maxK = k
		}
	}

	// Count total evaluable QA pairs
	evalTotal := 0
	for _, entry := range entries {
		for _, qa := range entry.QA {
			if qa.Category == 5 { // skip adversarial
				continue
			}
			evalTotal++
		}
	}
	if cfg.PerCatLimit > 0 {
		// Estimate: 4 categories × limit
		est := 4 * cfg.PerCatLimit * len(entries)
		if est < evalTotal {
			evalTotal = est
		}
	}

	evalDone := 0
	catCounts := make(map[int]int)

	// Process each conversation
	for _, entry := range entries {
		sessions := parseLoCoMoSessions(entry.Conversation)
		if len(sessions) == 0 {
			continue
		}

		store, cleanup, err := newStore()
		if err != nil {
			return nil, fmt.Errorf("create store for %s: %w", entry.SampleID, err)
		}

		// Ingest sessions via batch insert (single transaction, batched embeddings).
		// Note: session dates/ordering not passed as CreatedAt — triggers temporal
		// scoring that helps open-domain but hurts multi-hop. Kept neutral.
		var batchSessions []BenchSession
		for _, sess := range sessions {
			batchSessions = append(batchSessions, BenchSession{
				Key: sess.key, Content: sess.content,
			})
		}
		if err := store.BatchBenchInsert(ctx, cfg.NS, batchSessions); err != nil {
			cleanup()
			return nil, fmt.Errorf("batch ingest %s: %w", entry.SampleID, err)
		}

		// Build entity-based edges for multi-hop expansion
		if cfg.ExpandEdges {
			store.BenchBuildEdges(ctx, cfg.NS)
		}

		// Evaluate each QA pair
		for _, qa := range entry.QA {
			if qa.Category == 5 { // skip adversarial for retrieval eval
				continue
			}
			if cfg.PerCatLimit > 0 && catCounts[qa.Category] >= cfg.PerCatLimit {
				continue
			}

			evidenceSessions := evidenceToSessions(qa.Evidence)
			if len(evidenceSessions) == 0 {
				continue
			}

			searchLimit := maxK
			if searchLimit < 50 {
				searchLimit = 50
			}
			results, err := store.Search(ctx, SearchParams{
				NS:          cfg.NS,
				Query:       qa.Question,
				Limit:       searchLimit,
				IncludeAll:  true,
				ExpandEdges: cfg.ExpandEdges,
				MultiQuery:  cfg.MultiQuery,
				PRF:         cfg.PRF,
				MMR:         cfg.MMR,
			})
			if err != nil {
				cleanup()
				return nil, fmt.Errorf("search %s: %w", entry.SampleID, err)
			}

			retrieved := make([]string, len(results))
			for ri, r := range results {
				retrieved[ri] = r.Key
			}

			catName := categoryName(qa.Category)
			if _, ok := report.ByCat[catName]; !ok {
				report.ByCat[catName] = &LongMemEvalTypeAgg{Metrics: make(map[string]float64)}
			}

			for _, k := range cfg.TopK {
				recall := RecallAtK(retrieved, evidenceSessions, k)
				ndcg := NDCGAtK(retrieved, evidenceSessions, k)
				precision := PrecisionAtK(retrieved, evidenceSessions, k)
				report.ByCat[catName].Metrics[fmt.Sprintf("recall@%d", k)] += recall
				report.ByCat[catName].Metrics[fmt.Sprintf("ndcg@%d", k)] += ndcg
				report.ByCat[catName].Metrics[fmt.Sprintf("precision@%d", k)] += precision
				report.Overall[fmt.Sprintf("recall@%d", k)] += recall
				report.Overall[fmt.Sprintf("ndcg@%d", k)] += ndcg
				report.Overall[fmt.Sprintf("precision@%d", k)] += precision
			}

			relSet := make(map[string]bool, len(evidenceSessions))
			for _, s := range evidenceSessions {
				relSet[s] = true
			}
			mrr := MRR(retrieved, relSet)
			report.ByCat[catName].Metrics["mrr"] += mrr
			report.Overall["mrr"] += mrr

			report.ByCat[catName].Count++
			catCounts[qa.Category]++
			evalDone++
			report.Total++

			if cfg.ProgressFunc != nil && (evalDone%50 == 0 || evalDone == evalTotal) {
				cfg.ProgressFunc(evalDone, evalTotal)
			}
		}

		cleanup()
	}

	// Average all metrics
	if report.Total > 0 {
		for metric := range report.Overall {
			report.Overall[metric] /= float64(report.Total)
		}
	}
	for _, agg := range report.ByCat {
		if agg.Count > 0 {
			for metric := range agg.Metrics {
				agg.Metrics[metric] /= float64(agg.Count)
			}
		}
	}

	return report, nil
}

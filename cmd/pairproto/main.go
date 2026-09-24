// pairproto is a THROWAWAY prototype (not for commit): does a deterministic
// pair-feature extractor + one rule produce better typed-edge candidates than
// cosine neighbours? Baseline (2026-09-23, relates_to pairs → Jev): 0 causal
// in 100. This generates pairs by rule, prints the feature distribution, and
// optionally sends a sample to Jev with the same criteria.
//
//	go run ./cmd/pairproto --db snap.db --ns agent:pikamini [--jev N]
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/rcliao/ghost/internal/model"
	"github.com/rcliao/ghost/internal/store"
	_ "modernc.org/sqlite"
)

type mem struct {
	id, key, content string
	created          time.Time
	m                *model.Memory
	ents             map[string]bool
}

type pair struct {
	a, b      *mem
	shared    []string
	daysGap   float64
	jac       float64
	cue       bool
	relatesTo bool
}

func main() {
	dbPath := flag.String("db", "", "snapshot db (copied, never live)")
	ns := flag.String("ns", "agent:pikamini", "namespace")
	minDays := flag.Float64("min-days", 7, "minimum days apart")
	maxJac := flag.Float64("max-jaccard", 0.25, "maximum token overlap (excludes restatements)")
	maxDF := flag.Int("max-df", 40, "ignore entities appearing in more than this many memories")
	sample := flag.Int("sample", 100, "pairs to print/classify")
	jev := flag.Int("jev", 0, "send this many sampled pairs to Jev (needs TYPESAFE_API_KEY)")
	seed := flag.Int64("seed", 1, "sample seed")
	minShared := flag.Int("min-shared", 1, "minimum shared entities")
	excl := flag.String("exclude-prefix", "briefing-,hb-,heartbeat-,exchange-,session-pikamini-,skill-", "comma-separated key prefixes to skip (agent self-maintenance, not memory of the human)")
	requireCue := flag.Bool("require-cue", false, "keep only pairs whose newer side has a cue")
	fromRules := flag.Bool("from-rules", false, "take candidates from the store's RunPairRules (dry-run) instead of this prototype's own enumeration")
	pullThrough := flag.String("pullthrough", "", "TSV of accepted pairs (older, newer, p): write caused_by edges on a copy and measure whether the cause becomes reachable via Context (control vs edges)")
	budget := flag.Int("budget", 2000, "pullthrough: Context token budget")
	minScore := flag.Float64("min-score", 0.3, "pullthrough: Context MinScore (production uses 0.3)")
	qr := flag.Int("query-runes", 80, "pullthrough: query length in runes taken from the newer memory")
	reviewed := flag.Bool("reviewed", false, "pullthrough: also record an AGREE verdict for each edge in rule_events (the reviewed arm)")
	flag.Parse()
	if *dbPath == "" {
		fmt.Fprintln(os.Stderr, "--db required")
		os.Exit(2)
	}
	if *fromRules {
		runFromRules(*dbPath, *ns, *sample, *jev, *seed)
		return
	}
	if *pullThrough != "" {
		queryRunes = *qr
		reviewedArm = *reviewed
		runPullThrough(*dbPath, *ns, *pullThrough, *budget, *minScore)
		return
	}
	db, err := sql.Open("sqlite", "file:"+*dbPath+"?mode=ro")
	must(err)
	defer db.Close()

	rows, err := db.Query(`SELECT m.id, m.key, m.content, m.created_at FROM memories m
		JOIN (SELECT ns, key, MAX(version) mv FROM memories WHERE ns=? AND deleted_at IS NULL GROUP BY ns,key) h
		  ON h.ns=m.ns AND h.key=m.key AND h.mv=m.version
		WHERE m.deleted_at IS NULL`, *ns)
	must(err)
	prefixes := strings.Split(*excl, ",")
	var mems []*mem
	for rows.Next() {
		var m mem
		var created string
		must(rows.Scan(&m.id, &m.key, &m.content, &created))
		skip := false
		for _, pf := range prefixes {
			if pf != "" && strings.HasPrefix(m.key, pf) {
				skip = true
			}
		}
		if skip {
			continue
		}
		m.created, _ = time.Parse(time.RFC3339, created)
		m.m = &model.Memory{ID: m.id, Key: m.key, Content: m.content, CreatedAt: m.created}
		mems = append(mems, &m)
	}
	rows.Close()

	// relates_to pairs, to report overlap with the cosine pool
	rel := map[string]bool{}
	er, err := db.Query(`SELECT from_id, to_id FROM memory_edges WHERE rel='relates_to'`)
	must(err)
	for er.Next() {
		var a, b string
		must(er.Scan(&a, &b))
		rel[a+"|"+b] = true
		rel[b+"|"+a] = true
	}
	er.Close()

	// entity document frequency from the STORE's extractor (the code under test)
	var corpus []*model.Memory
	for _, m := range mems {
		corpus = append(corpus, m.m)
	}
	df := store.EntityDF(corpus)
	for _, m := range mems {
		m.ents = map[string]bool{}
		for e := range df {
			_ = e
		}
	}
	// per-memory entity sets, via a single-memory DF (cheap and identical to the store's view)
	for _, m := range mems {
		for e := range store.EntityDF([]*model.Memory{m.m}) {
			m.ents[e] = true
		}
	}
	index := map[string][]*mem{}
	dropped := 0
	for _, m := range mems {
		for e := range m.ents {
			if df[e] > *maxDF || df[e] < 2 {
				if df[e] > *maxDF {
					dropped++
				}
				continue
			}
			index[e] = append(index[e], m)
		}
	}

	seen := map[string]bool{}
	var pairs []pair
	for e, list := range index {
		for i := 0; i < len(list); i++ {
			for j := i + 1; j < len(list); j++ {
				a, b := list[i], list[j]
				if a.created.After(b.created) {
					a, b = b, a
				}
				k := a.id + "|" + b.id
				if seen[k] {
					continue
				}
				pf := store.NewPairFeatures(a.m, b.m, df)
				if pf.DaysApart < *minDays || pf.Jaccard > *maxJac {
					continue
				}
				seen[k] = true
				var shared []string
				for _, se := range pf.SharedEntities {
					if se.DF <= *maxDF {
						shared = append(shared, se.Text)
					}
				}
				_ = e
				hasCue := pf.NewerCue != ""
				if len(shared) < *minShared || (*requireCue && !hasCue) {
					continue
				}
				pairs = append(pairs, pair{a: a, b: b, shared: shared, daysGap: pf.DaysApart, jac: pf.Jaccard,
					cue: hasCue, relatesTo: rel[k]})
			}
		}
	}

	fmt.Printf("memories=%d entities(indexed)=%d pairs=%d  (min-days=%.0f max-jaccard=%.2f max-df=%d)\n",
		len(mems), len(index), len(pairs), *minDays, *maxJac, *maxDF)
	inCos, withCue, multi := 0, 0, 0
	for _, p := range pairs {
		if p.relatesTo {
			inCos++
		}
		if p.cue {
			withCue++
		}
		if len(p.shared) > 1 {
			multi++
		}
	}
	fmt.Printf("  already relates_to: %d   newer side has change/cause cue: %d   share >1 entity: %d\n", inCos, withCue, multi)

	// Rank: cue first, then more shared entities, then closer in time. Sample within the top band.
	sort.Slice(pairs, func(i, j int) bool {
		pi, pj := pairs[i], pairs[j]
		if pi.cue != pj.cue {
			return pi.cue
		}
		if len(pi.shared) != len(pj.shared) {
			return len(pi.shared) > len(pj.shared)
		}
		return pi.daysGap < pj.daysGap
	})
	top := pairs
	if len(top) > *sample*5 {
		top = top[:*sample*5]
	}
	r := rand.New(rand.NewSource(*seed))
	r.Shuffle(len(top), func(i, j int) { top[i], top[j] = top[j], top[i] })
	if len(top) > *sample {
		top = top[:*sample]
	}

	if *jev == 0 {
		for i, p := range top {
			if i >= 15 {
				break
			}
			fmt.Printf("  [%s] --%.0fd--> [%s]  shared=%v jac=%.2f cue=%v\n", p.a.key, p.daysGap, p.b.key, p.shared, p.jac, p.cue)
		}
		return
	}

	key := os.Getenv("TYPESAFE_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "TYPESAFE_API_KEY not set")
		os.Exit(2)
	}
	counts := map[string]int{}
	n := 0
	for _, p := range top {
		if n >= *jev {
			break
		}
		n++
		msg := fmt.Sprintf("Memory A (key: %s, %s):\n%s\n\nMemory B (key: %s, %s):\n%s",
			p.a.key, p.a.created.Format("2006-01-02"), trunc(p.a.content, 1500),
			p.b.key, p.b.created.Format("2006-01-02"), trunc(p.b.content, 1500))
		ch, prob, err := askJev(key, msg)
		if err != nil {
			counts["error"]++
			if counts["error"] == 1 {
				fmt.Fprintln(os.Stderr, "jev:", strings.ReplaceAll(err.Error(), key, "[redacted]"))
			}
			continue
		}
		counts[ch]++
		if ch != "none" && ch != "restates" {
			fmt.Printf("  %-9s p=%.2f  [%s] -> [%s]  shared=%v gap=%.0fd\n", ch, prob, p.a.key, p.b.key, p.shared, p.daysGap)
		}
	}
	fmt.Printf("jev on %d rule-generated pairs: %v\n", n, counts)
}

var queryRunes = 80
var reviewedArm = false

// runPullThrough mirrors TestEvalGraphPullThrough on real data: for each
// accepted pair, the query is the NEWER memory's own opening text (the
// situation the agent is in), and we ask whether the OLDER cause is packed —
// with the caused_by edge vs without. Pinned are excluded as the shell does
// (they live in the system prompt); MinScore matches production.
func runPullThrough(dbPath, ns, tsv string, budget int, minScore float64) {
	type pair struct{ older, newer, p string }
	var pairs []pair
	raw, err := os.ReadFile(tsv)
	must(err)
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Split(strings.TrimSpace(line), "\t")
		if len(f) >= 2 && f[0] != "" {
			p := ""
			if len(f) > 2 {
				p = f[2]
			}
			pairs = append(pairs, pair{f[0], f[1], p})
		}
	}
	control := dbPath + ".control"
	treat := dbPath + ".edges"
	for _, dst := range []string{control, treat} {
		b, err := os.ReadFile(dbPath)
		must(err)
		must(os.WriteFile(dst, b, 0o600))
	}
	ctx := context.Background()
	stC, err := store.NewSQLiteStore(control)
	must(err)
	defer stC.Close()
	stE, err := store.NewSQLiteStore(treat)
	must(err)
	defer stE.Close()
	written, agreed := 0, 0
	rw, err := sql.Open("sqlite", treat)
	must(err)
	defer rw.Close()
	idOf := func(key string) string {
		var id string
		_ = rw.QueryRow(`SELECT id FROM memories WHERE ns=? AND key=? AND deleted_at IS NULL ORDER BY version DESC LIMIT 1`, ns, key).Scan(&id)
		return id
	}
	for i, p := range pairs {
		if _, err := stE.CreateEdge(ctx, store.EdgeParams{FromNS: ns, FromKey: p.newer, ToNS: ns, ToKey: p.older, Rel: "caused_by"}); err == nil {
			written++
		}
		if reviewedArm {
			// The harness stands in for a reviewer: an AGREE row keyed as the
			// store keys pair-rule events (from = older, to = newer).
			older, newer := idOf(p.older), idOf(p.newer)
			if older != "" && newer != "" {
				if _, err := rw.Exec(`INSERT INTO rule_events (id, source, rule_id, from_id, to_id, features, action_op, action_rel, score, edge_written, created_at, reviewed, verdict, reviewed_by, reviewed_at)
					VALUES (?, 'pair_rule', 'harness', ?, ?, '{}', 'propose', 'caused_by', 0, 1, ?, 1, 'agree', 'harness', ?)`,
					fmt.Sprintf("harness-%03d", i), older, newer, time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339)); err == nil {
					agreed++
				}
			}
		}
	}
	fmt.Printf("pairs=%d edges_written=%d agreed=%d budget=%d min_score=%.2f exclude_pinned=true\n", len(pairs), written, agreed, budget, minScore)
	ro, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	must(err)
	defer ro.Close()
	contentOf := func(key string) string {
		var c string
		_ = ro.QueryRow(`SELECT content FROM memories WHERE ns=? AND key=? AND deleted_at IS NULL ORDER BY version DESC LIMIT 1`, ns, key).Scan(&c)
		return c
	}
	bracket := regexp.MustCompile(`\[[^\]]*\]`)
	ws := regexp.MustCompile(`\s+`)
	queryFrom := func(text string) string {
		t := ws.ReplaceAllString(bracket.ReplaceAllString(text, ""), " ")
		t = strings.TrimSpace(t)
		// Rune-safe cut at the length of a real prompt; a byte cut splits CJK.
		if r := []rune(t); len(r) > queryRunes {
			t = string(r[:queryRunes])
		}
		return t
	}
	rank := func(st *store.SQLiteStore, q, key string) (int, bool, int) {
		res, err := st.Context(ctx, store.ContextParams{NS: ns, Query: q, Budget: budget, ExcludePinned: true, MinScore: minScore})
		if err != nil {
			return 0, false, 0
		}
		r, seeded := 0, false
		for i, m := range res.Memories {
			if m.Key == key {
				r = i + 1
			}
		}
		_ = seeded
		return r, false, len(res.Memories)
	}
	pulled, both, lifted, absent, regress := 0, 0, 0, 0, 0
	for _, p := range pairs {
		q := queryFrom(contentOf(p.newer))
		if q == "" {
			continue
		}
		rc, _, nc := rank(stC, q, p.older)
		re, _, ne := rank(stE, q, p.older)
		sc, _, _ := rank(stC, q, p.newer)
		// Pool rank: is the seed even among the 50 search candidates Context draws from?
		poolRank := 0
		if sr, err := stC.Search(ctx, store.SearchParams{NS: ns, Query: q, Limit: 50}); err == nil {
			for i, r := range sr {
				if r.Key == p.newer {
					poolRank = i + 1
					break
				}
			}
		}
		verdict := ""
		switch {
		case re > 0 && rc == 0:
			verdict = "PULLED THROUGH"
			pulled++
		case rc > 0 && re == 0:
			verdict = "REGRESSION"
			regress++
		case rc > 0 && re > 0:
			both++
			if re < rc {
				lifted++
			}
			verdict = fmt.Sprintf("both (rank %d -> %d)", rc, re)
		default:
			verdict = "absent in both"
			absent++
		}
		fmt.Printf("%-24s p=%-5s packed=%d/%d seed_pool=%-2d seed_packed=%-2d  %s <- %s\n", verdict, p.p, nc, ne, poolRank, sc, p.older, p.newer)
	}
	fmt.Printf("\nSUMMARY: pulled_through=%d present_both=%d (rank lifted %d) absent_both=%d regression=%d of %d\n", pulled, both, lifted, absent, regress, len(pairs))
}

// runFromRules is the gate for the shipped path: the store's own rules choose
// the candidates, Jev classifies them, and the distribution is compared with
// the prototype's. Opens the snapshot with the real store (dry-run writes nothing).
func runFromRules(dbPath, ns string, sample, jevN int, seed int64) {
	st, err := store.NewSQLiteStore(dbPath)
	must(err)
	defer st.Close()
	res, err := st.RunPairRules(context.Background(), store.RunPairRulesParams{NS: ns, MaxPairs: 20000, DryRun: true})
	must(err)
	byRule := map[string]int{}
	for _, f := range res.Firings {
		byRule[f.RuleID]++
	}
	fmt.Printf("store rules: scanned=%d pairs=%d firings=%d skipped=%d by_rule=%v\n", res.MemoriesScanned, res.PairsEvaluated, len(res.Firings), res.Skipped, byRule)
	// Firings arrive ranked (asserts, then proposals by ProposeScore). The gate
	// takes the head of the queue — what a reviewer would see first.
	fir := res.Firings
	if len(fir) > sample {
		fir = fir[:sample]
	}
	_ = seed
	if jevN == 0 {
		for i, f := range fir {
			if i >= 15 {
				break
			}
			fmt.Printf("  [%s] --%.0fd--> [%s]  rule=%s cue=%q\n", f.Features.OlderKey, f.Features.DaysApart, f.Features.NewerKey, f.RuleID, f.Features.NewerCue)
		}
		return
	}
	key := os.Getenv("TYPESAFE_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "TYPESAFE_API_KEY not set")
		os.Exit(2)
	}
	ro, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	must(err)
	defer ro.Close()
	content := func(id string) (string, time.Time) {
		var c, created string
		if err := ro.QueryRow(`SELECT content, created_at FROM memories WHERE id = ?`, id).Scan(&c, &created); err != nil {
			return "", time.Time{}
		}
		ts, _ := time.Parse(time.RFC3339, created)
		return c, ts
	}
	counts := map[string]int{}
	n := 0
	for _, f := range fir {
		if n >= jevN {
			break
		}
		n++
		ac, at := content(f.OlderID)
		bc, bt := content(f.NewerID)
		msg := fmt.Sprintf("Memory A (key: %s, %s):\n%s\n\nMemory B (key: %s, %s):\n%s",
			f.Features.OlderKey, at.Format("2006-01-02"), trunc(ac, 1500), f.Features.NewerKey, bt.Format("2006-01-02"), trunc(bc, 1500))
		ch, prob, err := askJev(key, msg)
		if err != nil {
			counts["error"]++
			if counts["error"] == 1 {
				fmt.Fprintln(os.Stderr, "jev:", strings.ReplaceAll(err.Error(), key, "[redacted]"))
			}
			continue
		}
		counts[ch]++
		if ch != "none" && ch != "restates" {
			fmt.Printf("  %-9s p=%.2f  [%s] -> [%s]  rule=%s\n", ch, prob, f.Features.OlderKey, f.Features.NewerKey, f.RuleID)
		}
	}
	fmt.Printf("jev on %d store-rule pairs: %v\n", n, counts)
}

const sysPrompt = `You are an expert at identifying reasoning relationships between two pieces of text from a user's memory. Memory A is OLDER, Memory B is NEWER. Decide which relationship holds between them. Be strict: only choose a reasoning relation when the logical link is clear from the text. Generic topical similarity is "none".`

var criteria = map[string]string{
	"caused_by": "B happened or was decided BECAUSE of A (A is a cause, reason, or trigger of B)",
	"prevents":  "A prevents or rules out B, or B was done to prevent something A describes",
	"implies":   "A logically implies B and B is NOT merely a restatement of A (a new conclusion follows)",
	"restates":  "A and B state the same fact or event in different words (paraphrase, subset, or update of the same content)",
	"none":      "No reasoning relationship, or only generic topical similarity",
}

func askJev(key, state string) (string, float64, error) {
	body, _ := json.Marshal(map[string]any{
		"state":     map[string]string{"memories": state},
		"model":     "jev-latest",
		"questions": map[string]any{"rel": map[string]any{"type": "choice", "instructions": sysPrompt, "criteria": criteria}},
	})
	req, _ := http.NewRequestWithContext(context.Background(), "POST", "https://api.typesafe.ai/v1/systemone", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, trunc(string(raw), 200))
	}
	var out struct {
		Answers map[string]struct {
			Choice        string             `json:"choice"`
			Probabilities map[string]float64 `json:"probabilities"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", 0, err
	}
	a := out.Answers["rel"]
	return a.Choice, a.Probabilities[a.Choice], nil
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

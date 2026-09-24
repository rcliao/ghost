package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rcliao/ghost/internal/model"
)

// Pair rules are relationship logic as data. A rule is a condition over
// PairFeatures and an action: PROPOSE a relation (surface the pair for a
// caller or an out-of-band classifier to decide) or ASSERT it (write the
// edge). Every firing is recorded in rule_events so it can be reviewed and,
// for an assert, reversed. See docs/research/pair-rules-design.md.
//
// The store never decides caused_by / prevents / implies itself: rules narrow
// the candidate pool deterministically, which measured 8-12 caused_by per 100
// candidates against 0 for cosine neighbours; a caller labels the rest.

// PairRule is one persisted rule.
type PairRule struct {
	ID        string     `json:"id"`
	NS        string     `json:"ns"` // empty = every namespace
	Name      string     `json:"name"`
	Enabled   bool       `json:"enabled"`
	Priority  int        `json:"priority"` // higher fires first
	CreatedBy string     `json:"created_by"`
	Cond      PairCond   `json:"cond"`
	Action    PairAction `json:"action"`
	// MinPrecision is the precision measured for this rule on the graded
	// fixture. An ASSERT rule must carry >= AssertMinPrecision; PROPOSE rules
	// may leave it 0.
	MinPrecision float64 `json:"min_precision,omitempty"`
	CreatedAt    string  `json:"created_at,omitempty"`
}

// PairCond is AND-joined. Zero values mean "not constrained"; the tri-state
// fields use -1 ignore, 0 must be false, 1 must be true.
type PairCond struct {
	MinSharedEntities int     `json:"min_shared_entities,omitempty"`
	MaxEntityDF       int     `json:"max_entity_df,omitempty"` // shared entities counted only up to this document frequency
	MinDaysApart      float64 `json:"min_days_apart,omitempty"`
	MaxJaccard        float64 `json:"max_jaccard,omitempty"` // 0 = unconstrained
	Cue               string  `json:"cue,omitempty"`         // "" any | "any-cue" | "correction" | "causal" | "none"
	SameUser          int     `json:"same_user"`             // -1 ignore
	KeyPrefixMatch    int     `json:"key_prefix_match"`      // -1 ignore
}

// PairAction is what a matching rule does.
type PairAction struct {
	Op  string `json:"op"`  // "propose" | "assert"
	Rel string `json:"rel"` // a relation from validEdgeRels
}

const (
	PairOpPropose = "propose"
	PairOpAssert  = "assert"
	// AssertMinPrecision is the measured precision a rule needs before it may
	// write edges on its own.
	AssertMinPrecision = 0.8
)

// Matches reports whether the features satisfy the condition.
func (c PairCond) Matches(f PairFeatures) bool {
	if c.MinSharedEntities > 0 && f.SharedWithin(c.MaxEntityDF) < c.MinSharedEntities {
		return false
	}
	if c.MinDaysApart > 0 && f.DaysApart < c.MinDaysApart {
		return false
	}
	if c.MaxJaccard > 0 && f.Jaccard > c.MaxJaccard {
		return false
	}
	switch c.Cue {
	case "", "any":
	case "any-cue":
		if f.NewerCue == "" {
			return false
		}
	case "none":
		if f.NewerCue != "" {
			return false
		}
	default:
		if f.NewerCue != c.Cue {
			return false
		}
	}
	if c.SameUser >= 0 && f.SameUser != (c.SameUser == 1) {
		return false
	}
	if c.KeyPrefixMatch >= 0 && f.KeyPrefixMatch != (c.KeyPrefixMatch == 1) {
		return false
	}
	return true
}

// Validate rejects rules the store must not run.
func (r PairRule) Validate() error {
	if r.ID == "" || r.Name == "" {
		return fmt.Errorf("pair rule needs id and name")
	}
	if r.Action.Op != PairOpPropose && r.Action.Op != PairOpAssert {
		return fmt.Errorf("pair rule %s: op must be propose or assert, got %q", r.ID, r.Action.Op)
	}
	if !validEdgeRels[r.Action.Rel] || r.Action.Rel == "relates_to" || r.Action.Rel == "merged_into" {
		return fmt.Errorf("pair rule %s: %q is not a relation a pair rule may produce", r.ID, r.Action.Rel)
	}
	if r.Action.Op == PairOpAssert && r.MinPrecision < AssertMinPrecision {
		return fmt.Errorf("pair rule %s: assert needs min_precision >= %.2f measured on the fixture, got %.2f", r.ID, AssertMinPrecision, r.MinPrecision)
	}
	switch r.Cond.Cue {
	case "", "any", "any-cue", "none", "correction", "causal":
	default:
		return fmt.Errorf("pair rule %s: unknown cue %q", r.ID, r.Cond.Cue)
	}
	return nil
}

// builtinPairRules are the two generators measured on 2026-09-23. Both PROPOSE;
// nothing asserts until a rule's precision has been measured on the fixture.
var builtinPairRules = []PairRule{
	{
		ID: "sys-pair-causal-shared-entities", Name: "propose caused_by: two shared specific entities, a week apart, not a restatement",
		Enabled: true, Priority: 50, CreatedBy: "system",
		Cond:   PairCond{MinSharedEntities: 2, MaxEntityDF: 40, MinDaysApart: 7, MaxJaccard: 0.25, SameUser: -1, KeyPrefixMatch: -1},
		Action: PairAction{Op: PairOpPropose, Rel: "caused_by"},
	},
	{
		ID: "sys-pair-causal-cue", Name: "propose caused_by: a shared specific entity and a correction or causal cue on the newer side",
		Enabled: true, Priority: 40, CreatedBy: "system",
		Cond:   PairCond{MinSharedEntities: 1, MaxEntityDF: 40, MinDaysApart: 7, MaxJaccard: 0.25, Cue: "any-cue", SameUser: -1, KeyPrefixMatch: -1},
		Action: PairAction{Op: PairOpPropose, Rel: "caused_by"},
	},
}

// DefaultPairSkipPrefixes are key prefixes of agent self-maintenance rows —
// briefings, heartbeats, raw exchanges — which are not memory of the human
// and swamped the candidate pool when included.
var DefaultPairSkipPrefixes = []string{"briefing-", "hb-", "heartbeat-", "exchange-", "session-pikamini-", "skill-", "auto-summary-"}

func (s *SQLiteStore) seedBuiltinPairRules() {
	now := s.now().UTC().Format(time.RFC3339)
	for _, r := range builtinPairRules {
		s.db.Exec(`INSERT OR IGNORE INTO pair_rules
			(id, ns, name, enabled, priority, created_by,
			 cond_min_shared_entities, cond_max_entity_df, cond_min_days_apart, cond_max_jaccard, cond_cue, cond_same_user, cond_key_prefix_match,
			 action_op, action_rel, min_precision, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.NS, r.Name, boolInt(r.Enabled), r.Priority, r.CreatedBy,
			r.Cond.MinSharedEntities, r.Cond.MaxEntityDF, r.Cond.MinDaysApart, r.Cond.MaxJaccard, r.Cond.Cue, r.Cond.SameUser, r.Cond.KeyPrefixMatch,
			r.Action.Op, r.Action.Rel, r.MinPrecision, now)
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ListPairRules returns rules for a namespace plus the global ones, highest
// priority first. ns "" returns everything.
func (s *SQLiteStore) ListPairRules(ctx context.Context, ns string) ([]PairRule, error) {
	q := `SELECT id, ns, name, enabled, priority, created_by,
		cond_min_shared_entities, cond_max_entity_df, cond_min_days_apart, cond_max_jaccard, cond_cue, cond_same_user, cond_key_prefix_match,
		action_op, action_rel, min_precision, created_at FROM pair_rules`
	var args []any
	if ns != "" {
		q += ` WHERE ns = '' OR ns = ?`
		args = append(args, ns)
	}
	q += ` ORDER BY priority DESC, id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PairRule
	for rows.Next() {
		var r PairRule
		var enabled int
		if err := rows.Scan(&r.ID, &r.NS, &r.Name, &enabled, &r.Priority, &r.CreatedBy,
			&r.Cond.MinSharedEntities, &r.Cond.MaxEntityDF, &r.Cond.MinDaysApart, &r.Cond.MaxJaccard, &r.Cond.Cue, &r.Cond.SameUser, &r.Cond.KeyPrefixMatch,
			&r.Action.Op, &r.Action.Rel, &r.MinPrecision, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Enabled = enabled == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// PutPairRule inserts or replaces a rule after validation.
func (s *SQLiteStore) PutPairRule(ctx context.Context, r PairRule) error {
	if s.contractReadOnly {
		return s.errContractNewer()
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if r.CreatedBy == "" {
		r.CreatedBy = "caller"
	}
	now := s.now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO pair_rules
		(id, ns, name, enabled, priority, created_by,
		 cond_min_shared_entities, cond_max_entity_df, cond_min_days_apart, cond_max_jaccard, cond_cue, cond_same_user, cond_key_prefix_match,
		 action_op, action_rel, min_precision, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.NS, r.Name, boolInt(r.Enabled), r.Priority, r.CreatedBy,
		r.Cond.MinSharedEntities, r.Cond.MaxEntityDF, r.Cond.MinDaysApart, r.Cond.MaxJaccard, r.Cond.Cue, r.Cond.SameUser, r.Cond.KeyPrefixMatch,
		r.Action.Op, r.Action.Rel, r.MinPrecision, now)
	return err
}

// DeletePairRule removes a rule; its past events stay as history.
func (s *SQLiteStore) DeletePairRule(ctx context.Context, id string) error {
	if s.contractReadOnly {
		return s.errContractNewer()
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM pair_rules WHERE id = ?`, id)
	return err
}

// RunPairRulesParams controls one evaluation run over a namespace.
type RunPairRulesParams struct {
	NS           string
	MaxPairs     int      // candidate pairs to evaluate (default 500)
	MaxEntityDF  int      // entities more common than this do not generate pairs (default 40)
	SkipPrefixes []string // nil = DefaultPairSkipPrefixes; empty slice = skip nothing
	DryRun       bool     // evaluate and report, write nothing
	RuleIDs      []string // optional: only these rules
	// MaxProposals caps how many PROPOSE firings a run records, highest
	// ProposeScore first (default 200). ASSERT firings are never capped: a
	// rule that may write edges has measured precision and fires wherever it
	// matches. A proposal queue nobody can review is not a queue.
	MaxProposals int
}

// PairFiring is one rule firing on one pair.
type PairFiring struct {
	EventID     string       `json:"event_id,omitempty"`
	RuleID      string       `json:"rule_id"`
	OlderID     string       `json:"older_id"`
	NewerID     string       `json:"newer_id"`
	Features    PairFeatures `json:"features"`
	Op          string       `json:"op"`
	Rel         string       `json:"rel"`
	Score       float64      `json:"score"`
	EdgeWritten bool         `json:"edge_written"`
}

// RunPairRulesResult summarises a run.
type RunPairRulesResult struct {
	MemoriesScanned int          `json:"memories_scanned"`
	PairsEvaluated  int          `json:"pairs_evaluated"`
	Firings         []PairFiring `json:"firings"` // ranked: asserts first, then proposals by score
	Skipped         int          `json:"skipped"` // pairs already evented for the rule or already typed
	Capped          int          `json:"capped"`  // proposals dropped by MaxProposals
	DryRun          bool         `json:"dry_run"`
}

type pairMem struct {
	id, key, content, sourceUser, sourceScope string
	created                                   time.Time
	ents                                      map[string]bool
}

// RunPairRules enumerates candidate pairs from the namespace's entity index,
// evaluates enabled rules against each pair's features, records a rule_events
// row per firing, and writes an edge for ASSERT rules. Idempotent: a pair that
// already carries a typed edge, or that a rule already fired on, is skipped.
func (s *SQLiteStore) RunPairRules(ctx context.Context, p RunPairRulesParams) (*RunPairRulesResult, error) {
	if s.contractReadOnly && !p.DryRun {
		return nil, s.errContractNewer()
	}
	if p.NS == "" {
		return nil, fmt.Errorf("ns is required")
	}
	if p.MaxPairs <= 0 {
		p.MaxPairs = 500
	}
	if p.MaxEntityDF <= 0 {
		p.MaxEntityDF = 40
	}
	if p.MaxProposals <= 0 {
		p.MaxProposals = 200
	}
	skip := p.SkipPrefixes
	if skip == nil {
		skip = DefaultPairSkipPrefixes
	}
	rules, err := s.ListPairRules(ctx, p.NS)
	if err != nil {
		return nil, err
	}
	var active []PairRule
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		if len(p.RuleIDs) > 0 && !containsStr(p.RuleIDs, r.ID) {
			continue
		}
		if err := r.Validate(); err != nil {
			return nil, err
		}
		active = append(active, r)
	}
	result := &RunPairRulesResult{DryRun: p.DryRun}
	if len(active) == 0 {
		return result, nil
	}

	mems, err := s.loadPairMemories(ctx, p.NS, skip)
	if err != nil {
		return nil, err
	}
	result.MemoriesScanned = len(mems)

	// Entity index within the discriminating frequency band.
	df := make(map[string]int)
	for _, m := range mems {
		for e := range m.ents {
			df[e]++
		}
	}
	index := make(map[string][]*pairMem)
	for _, m := range mems {
		for e := range m.ents {
			if df[e] >= 2 && df[e] <= p.MaxEntityDF {
				index[e] = append(index[e], m)
			}
		}
	}
	entities := make([]string, 0, len(index))
	for e := range index {
		entities = append(entities, e)
	}
	sort.Strings(entities) // deterministic order → deterministic MaxPairs cut

	typed, err := s.typedPairSet(ctx)
	if err != nil {
		return nil, err
	}
	evented, err := s.eventedPairSet(ctx)
	if err != nil {
		return nil, err
	}

	// Pass 1: evaluate every pair, collect matches. Nothing is written yet so
	// proposals can be ranked and capped as a set.
	seen := make(map[string]bool)
	var matched []PairFiring
	for _, e := range entities {
		list := index[e]
		for i := 0; i < len(list) && result.PairsEvaluated < p.MaxPairs; i++ {
			for j := i + 1; j < len(list) && result.PairsEvaluated < p.MaxPairs; j++ {
				a, b := list[i], list[j]
				if b.created.Before(a.created) {
					a, b = b, a
				}
				k := a.id + "|" + b.id
				if seen[k] {
					continue
				}
				seen[k] = true
				if typed[k] || typed[b.id+"|"+a.id] {
					result.Skipped++
					continue
				}
				// A pair some active rule already fired on is decided: it leaves
				// the window without being counted, so later runs advance past
				// it and the queue can refill after review. (A pair that matched
				// no rule is undecided and is evaluated again — rules change.)
				decided := false
				for _, r := range active {
					if evented[r.ID+"|"+k] {
						decided = true
						break
					}
				}
				if decided {
					result.Skipped++
					continue
				}
				result.PairsEvaluated++
				f := NewPairFeatures(a.model(), b.model(), df)
				for _, r := range active {
					if !r.Cond.Matches(f) {
						continue
					}
					matched = append(matched, PairFiring{RuleID: r.ID, OlderID: a.id, NewerID: b.id, Features: f,
						Op: r.Action.Op, Rel: r.Action.Rel, Score: f.ProposeScore()})
					break // highest-priority matching rule wins for this pair
				}
			}
		}
	}

	// Pass 2: rank. Asserts first (uncapped), then proposals by score; ties by
	// keys so the order is stable across runs.
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].Op != matched[j].Op {
			return matched[i].Op == PairOpAssert
		}
		if matched[i].Score != matched[j].Score {
			return matched[i].Score > matched[j].Score
		}
		if matched[i].Features.OlderKey != matched[j].Features.OlderKey {
			return matched[i].Features.OlderKey < matched[j].Features.OlderKey
		}
		return matched[i].Features.NewerKey < matched[j].Features.NewerKey
	})
	proposals := 0
	for _, fire := range matched {
		if fire.Op == PairOpPropose {
			proposals++
			if proposals > p.MaxProposals {
				result.Capped++
				continue
			}
		}
		if !p.DryRun {
			if fire.Op == PairOpAssert {
				older, newer := memByID(mems, fire.OlderID), memByID(mems, fire.NewerID)
				if _, err := s.CreateEdge(ctx, EdgeParams{FromNS: p.NS, FromKey: newer.key, ToNS: p.NS, ToKey: older.key, Rel: fire.Rel}); err == nil {
					fire.EdgeWritten = true
				}
			}
			id, err := s.insertRuleEvent(ctx, ruleEventSourcePair, fire)
			if err != nil {
				return nil, err
			}
			fire.EventID = id
		}
		result.Firings = append(result.Firings, fire)
	}
	return result, nil
}

func memByID(mems []*pairMem, id string) *pairMem {
	for _, m := range mems {
		if m.id == id {
			return m
		}
	}
	return &pairMem{}
}

func (m *pairMem) model() *model.Memory {
	return &model.Memory{ID: m.id, Key: m.key, Content: m.content, CreatedAt: m.created, SourceUser: m.sourceUser, SourceScope: m.sourceScope}
}

func (s *SQLiteStore) loadPairMemories(ctx context.Context, ns string, skip []string) ([]*pairMem, error) {
	now := s.now().UTC().Format(time.RFC3339)
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.key, m.content, m.created_at, COALESCE(m.source_user,''), COALESCE(m.source_scope,'')
		FROM memories m
		INNER JOIN (SELECT ns, key, MAX(version) AS mv FROM memories WHERE ns = ? AND deleted_at IS NULL GROUP BY ns, key) l
		  ON m.ns = l.ns AND m.key = l.key AND m.version = l.mv
		WHERE m.deleted_at IS NULL AND (m.expires_at IS NULL OR m.expires_at > ?)
		  AND COALESCE(m.tier,'stm') != 'dormant'
		ORDER BY m.created_at, m.key`, ns, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*pairMem
	for rows.Next() {
		var m pairMem
		var created string
		if err := rows.Scan(&m.id, &m.key, &m.content, &created, &m.sourceUser, &m.sourceScope); err != nil {
			return nil, err
		}
		if hasAnyPrefix(m.key, skip) {
			continue
		}
		m.created, _ = time.Parse(time.RFC3339, created)
		m.ents = pairEntities(m.content)
		out = append(out, &m)
	}
	return out, rows.Err()
}

// typedPairSet returns "from|to" keys of pairs that already carry a relation a
// pair rule could produce.
func (s *SQLiteStore) typedPairSet(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT from_id, to_id FROM memory_edges WHERE rel NOT IN ('relates_to','contains','merged_into')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := make(map[string]bool)
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		set[a+"|"+b] = true
	}
	return set, rows.Err()
}

// eventedPairSet returns "rule|older|newer" keys already recorded.
func (s *SQLiteStore) eventedPairSet(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT rule_id, from_id, to_id FROM rule_events WHERE source = ?`, ruleEventSourcePair)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	defer rows.Close()
	set := make(map[string]bool)
	for rows.Next() {
		var r, a, b string
		if err := rows.Scan(&r, &a, &b); err != nil {
			return nil, err
		}
		set[r+"|"+a+"|"+b] = true
	}
	return set, rows.Err()
}

func hasAnyPrefix(key string, prefixes []string) bool {
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// featuresJSON renders features for the event row; never fails the run.
func featuresJSON(f PairFeatures) string {
	b, err := json.Marshal(f)
	if err != nil {
		return "{}"
	}
	return string(b)
}

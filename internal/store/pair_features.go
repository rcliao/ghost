package store

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/rcliao/ghost/internal/entity"
	"github.com/rcliao/ghost/internal/model"
)

// Pair features are the deterministic ground pair rules stand on. PairFeatures
// is a pure function over two memories: no database, no model, no clock, so a
// rule's behaviour can be tested from a fixture and its firing recorded as the
// exact features it saw. See docs/research/pair-rules-design.md.
//
// Measured origin (2026-09-23, production snapshot, Jev as classifier):
// candidate pairs chosen by these features yielded 8–12 caused_by per 100
// with 5/7 graded correct, where cosine relates_to pairs yielded 0.

// PairFeatures describes the relationship-relevant shape of an ordered pair.
// Older is always the earlier memory; the pair is normalised on construction.
type PairFeatures struct {
	OlderKey string `json:"older_key"`
	NewerKey string `json:"newer_key"`

	// SharedEntities are entities present in both texts, with each entity's
	// document frequency in the namespace (how many memories mention it).
	// Low-frequency shared entities discriminate; "mami" in 4,000 memories
	// does not.
	SharedEntities []SharedEntity `json:"shared_entities"`
	MinSharedDF    int            `json:"min_shared_df"` // 0 when nothing is shared

	DaysApart float64 `json:"days_apart"`
	Jaccard   float64 `json:"jaccard"` // token overlap; high = restatement

	// NewerCue is the strongest cue class found in the NEWER memory:
	// "correction" (this replaces an earlier belief), "causal" (this happened
	// because of / led to), or "" for none.
	NewerCue string `json:"newer_cue"`

	SameUser       bool `json:"same_user"`        // both carry the same source_user
	SameScope      bool `json:"same_scope"`       // both carry the same source_scope
	KeyPrefixMatch bool `json:"key_prefix_match"` // share a key prefix up to the first date/number

	// CosineBand is filled by the caller when embeddings are at hand: "dup"
	// (>=0.92), "near" (>=0.80), "far" (<0.80), or "" when unknown. The pure
	// function never computes it.
	CosineBand string `json:"cosine_band,omitempty"`
}

// SharedEntity is one entity both memories mention.
type SharedEntity struct {
	Text string `json:"text"`
	DF   int    `json:"df"`
}

// Cue classes. Correction cues come from freshness.go's supersede detector;
// causal cues are the connectives a "because / led to" memory uses. Both are
// deliberately narrow: "because" alone matched 10k of 27k candidate pairs on
// the production snapshot and told the rule nothing.
var (
	correctionCueRe = regexp.MustCompile(`(?i)\b(switched (from|to)|no longer|instead of|replaced|superseded|corrected|correction|turns out|actually not|not .{1,20} but)\b|其實|而不是|搞錯|記錯|改成|換成|更正|已經不|錯了`)
	causalCueRe     = regexp.MustCompile(`(?i)\b(caused|led to|resulted in|because of|as a result|to prevent|to avoid|root cause|triggered by|in response to)\b|導致|因此|為了避免|為了防止|起因`)
)

var pairWordRe = regexp.MustCompile(`[\p{L}\p{N}]{3,}`)

// keyPrefixRe cuts a key at its first date-like or numeric segment, so
// "session-summary-2026-07-09-mami-meals" and "session-summary-2026-07-09"
// share the prefix "session-summary".
var keyPrefixRe = regexp.MustCompile(`^(.*?)-?(\d{4}-\d{2}|\d{6,}|gen\d+|v\d+)`)

// EntityDF counts, per entity text, how many of the given memories mention it.
// Computed once per run over a namespace and passed to PairFeatures.
func EntityDF(mems []*model.Memory) map[string]int {
	df := make(map[string]int)
	for _, m := range mems {
		for e := range pairEntities(m.Content) {
			df[e]++
		}
	}
	return df
}

// pairDateWords are capitalised in English and so look like entities to the
// extractor, but two memories sharing "Sat" or "March" share nothing. They are
// excluded HERE, not in the entity package: capture's salience scoring relies
// on months being entities ("My sister's birthday is March 3rd" is captured
// because of it), and its coverage eval regressed when they were removed there.
var pairDateWords = map[string]bool{
	"monday": true, "tuesday": true, "wednesday": true, "thursday": true, "friday": true, "saturday": true, "sunday": true,
	"mon": true, "tue": true, "tues": true, "wed": true, "thu": true, "thur": true, "thurs": true, "fri": true, "sat": true, "sun": true,
	"january": true, "february": true, "march": true, "april": true, "may": true, "june": true, "july": true, "august": true,
	"september": true, "october": true, "november": true, "december": true,
	"jan": true, "feb": true, "mar": true, "apr": true, "jun": true, "jul": true, "aug": true, "sep": true, "sept": true, "oct": true, "nov": true, "dec": true,
	"today": true, "tomorrow": true, "yesterday": true, "tonight": true, "am": true, "pm": true,
}

// pairEntities returns the entity texts of one memory as a set, minus date
// words and bare numbers.
func pairEntities(content string) map[string]bool {
	set := make(map[string]bool)
	for _, e := range entity.Extract(content) {
		if pairDateWords[e.Text] || isNumeric(e.Text) || !looksLikeEntity(e.Text) {
			continue
		}
		set[e.Text] = true
	}
	return set
}

// looksLikeEntity rejects extractor output that survives the frequency band
// but names nothing: fewer than three letters ("a/c", "ui"), or a token that
// is mostly punctuation and digits.
func looksLikeEntity(text string) bool {
	letters := 0
	for _, r := range text {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	return letters >= 3 && letters*2 >= len([]rune(text))
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// entityMatch reports whether two entity strings name the same thing: equal,
// or one contains the other at word boundaries ("port ellery" within "port
// ellery lantern festival"). The extractor groups capitalised runs greedily,
// so the same place surfaces with different neighbours in different memories.
// Returns the shorter form as the canonical shared text.
func entityMatch(a, b string) (string, bool) {
	if a == b {
		return a, true
	}
	short, long := a, b
	if len(short) > len(long) {
		short, long = long, short
	}
	if !strings.Contains(short, " ") && len(short) < 4 {
		return "", false // single short words match too loosely
	}
	if strings.HasPrefix(long, short+" ") || strings.HasSuffix(long, " "+short) || strings.Contains(long, " "+short+" ") {
		return short, true
	}
	return "", false
}

func pairTokens(s string) map[string]bool {
	m := make(map[string]bool)
	for _, w := range pairWordRe.FindAllString(strings.ToLower(s), -1) {
		m[w] = true
	}
	return m
}

func pairJaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

// cueClass returns the strongest cue class in text: correction beats causal.
func cueClass(text string) string {
	if correctionCueRe.MatchString(text) {
		return "correction"
	}
	if causalCueRe.MatchString(text) {
		return "causal"
	}
	return ""
}

// KeyPrefix returns the part of a key before its first date or number segment.
func KeyPrefix(key string) string {
	if m := keyPrefixRe.FindStringSubmatch(key); m != nil {
		return strings.TrimRight(m[1], "-")
	}
	return key
}

// NewPairFeatures computes the features of a pair. df is the namespace's
// entity document-frequency table from EntityDF; a nil df treats every shared
// entity as frequency 1. The result is normalised so Older precedes Newer.
func NewPairFeatures(a, b *model.Memory, df map[string]int) PairFeatures {
	older, newer := a, b
	if b.CreatedAt.Before(a.CreatedAt) {
		older, newer = b, a
	}
	f := PairFeatures{
		OlderKey:  older.Key,
		NewerKey:  newer.Key,
		DaysApart: math.Abs(newer.CreatedAt.Sub(older.CreatedAt).Hours()) / 24,
		Jaccard:   pairJaccard(pairTokens(older.Content), pairTokens(newer.Content)),
		NewerCue:  cueClass(newer.Content),
		SameUser:  older.SourceUser != "" && older.SourceUser == newer.SourceUser,
		SameScope: older.SourceScope != "" && older.SourceScope == newer.SourceScope,
	}
	op, np := KeyPrefix(older.Key), KeyPrefix(newer.Key)
	f.KeyPrefixMatch = op != "" && op == np && op != older.Key

	oe, ne := pairEntities(older.Content), pairEntities(newer.Content)
	sharedSet := make(map[string]bool)
	for e := range oe {
		for n := range ne {
			if text, ok := entityMatch(e, n); ok {
				sharedSet[text] = true
			}
		}
	}
	for text := range sharedSet {
		d := 1
		if df != nil {
			if v, ok := df[text]; ok {
				d = v
			}
		}
		f.SharedEntities = append(f.SharedEntities, SharedEntity{Text: text, DF: d})
	}
	// Collapse: a word indexed from a multi-word entity must not count again
	// when the whole entity is also shared ("tunnel view" + "tunnel" + "view"
	// is one shared thing, not three).
	f.SharedEntities = collapseParts(f.SharedEntities)
	sort.Slice(f.SharedEntities, func(i, j int) bool {
		if f.SharedEntities[i].DF != f.SharedEntities[j].DF {
			return f.SharedEntities[i].DF < f.SharedEntities[j].DF
		}
		return f.SharedEntities[i].Text < f.SharedEntities[j].Text
	})
	if len(f.SharedEntities) > 0 {
		f.MinSharedDF = f.SharedEntities[0].DF
	}
	return f
}

// collapseParts drops single-word shared entities that are words of another
// shared multi-word entity.
func collapseParts(in []SharedEntity) []SharedEntity {
	if len(in) < 2 {
		return in
	}
	covered := make(map[string]bool)
	for _, e := range in {
		if strings.Contains(e.Text, " ") {
			for _, w := range strings.Fields(e.Text) {
				covered[w] = true
			}
		}
	}
	out := in[:0]
	for _, e := range in {
		if !strings.Contains(e.Text, " ") && covered[e.Text] {
			continue
		}
		out = append(out, e)
	}
	return out
}

// ProposeScore ranks a proposal for review. Deterministic, from the features
// alone, so the same store always yields the same queue. Weights follow what
// separated true causal pairs from noise on the graded snapshot: a correction
// or causal cue on the newer side, more shared entities, and shared entities
// that are rare (low document frequency) in the namespace. Nearer in time
// breaks ties.
func (f PairFeatures) ProposeScore() float64 {
	score := 0.0
	switch f.NewerCue {
	case "correction":
		score += 2.0
	case "causal":
		score += 1.5
	}
	n := len(f.SharedEntities)
	if n > 3 {
		n = 3
	}
	score += float64(n)
	if f.MinSharedDF > 0 {
		score += 1.0 / float64(f.MinSharedDF)
	}
	if f.DaysApart > 0 {
		score += 0.5 / (1.0 + f.DaysApart/30.0)
	}
	return score
}

// SharedWithin counts shared entities whose document frequency is at most
// maxDF — the ones that discriminate. maxDF <= 0 counts all.
func (f PairFeatures) SharedWithin(maxDF int) int {
	if maxDF <= 0 {
		return len(f.SharedEntities)
	}
	n := 0
	for _, e := range f.SharedEntities {
		if e.DF <= maxDF {
			n++
		}
	}
	return n
}

// daysBetween is a small helper kept for tests and callers that build
// synthetic memories.
func daysBetween(a, b time.Time) float64 { return math.Abs(b.Sub(a).Hours()) / 24 }

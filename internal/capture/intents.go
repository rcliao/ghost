package capture

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rcliao/ghost/internal/entity"
)

// intent is one classifier: a name, the patterns that fire it, and how much it
// contributes to mechanical importance. The names double as the "cues" reported
// on each candidate for explainability, and drive kind inference.
type intent struct {
	name    string
	weight  float64
	pattern *regexp.Regexp
}

// Intent classifiers, ordered by how strongly they signal a durable, personal
// fact. Patterns are deliberately conservative — a missed capture is cheaper
// than a noisy one, and the entity gate in Extract catches salient facts that
// carry no explicit cue. Cue names are also used by inferKind.
var intents = []intent{
	// A user actively correcting the agent is the highest-value signal.
	// CJK alternations carry no \b anchors — Chinese has no word boundaries;
	// the coverage corpus (eval_coverage_test.go) grades every branch and the
	// chatter set holds false positives at zero.
	{"correction", 0.28, regexp.MustCompile(`(?i)\b(actually|no,|not\s+\w+\s+but|instead of|i\s+meant|correction|rather than|stop\s+\w+ing|don'?t\s+(?:do|use|say))\b|\bthat'?s\s+(?:not\s+right|wrong|incorrect)\b|不對|不是這樣|我(?:說|指)的是|其實(?:是|不是)|錯了|搞錯`)},
	// Stated preferences: how the user wants things done.
	{"preference", 0.24, regexp.MustCompile(`(?i)\b(i|we)\s+(prefer|like|love|hate|always|never|usually|tend to)\b|\bprefer(?:s|red)?\b|\buse\s+\S+\s+(?:not|instead of|over|rather than)\b|\bdon'?t\s+(?:use|want|like)\b|\bi'?d\s+rather\b|\bmy\s+preference\b|\bplease\s+always\b|\bgoing\s+forward\b|我(?:比較)?(?:喜歡|偏好|習慣)|比較喜歡|不喜歡|希望你|我(?:們)?(?:都)?不(?:喝|吃|碰|買|用)`)},
	// Decisions with rationale.
	{"decision", 0.22, regexp.MustCompile(`(?i)\b(we|i|let'?s)\s+(decided|chose|will use|are going to|should use|going with|settled on)\b|\blet'?s\s+(?:also\s+)?(?:go\s+with|store|start|keep|track|add|try|schedule)\b|\bthe plan is\b|\bdecision:\b|決定|就(?:選|用|訂)這|說好了`)},
	// Health events: symptoms, injuries, remedies — the premium episodic
	// class for a family agent (two live misses in two days before this cue).
	{"health", 0.26, regexp.MustCompile(`(?i)(?:allergic|nauseous|dizzy|fever|headache|injured|bleeding|rash|threw up)|拉肚子|不舒服|受傷|切到|流血|發燒|頭痛|想吐|過敏|起疹|紅疹|癢|(?:頭|肚子|喉嚨|牙|背|腰|胃)(?:有點|好|很|超|悶)?痛|中暑|吃了.{0,6}(?:藥|止痛)|藥膏|消了|痊癒|好多了|退燒`)},
	// Standing instructions / boundaries: rules about future conduct.
	{"boundary", 0.26, regexp.MustCompile(`(?i)\b(?:please\s+)?never\s+(?:share|send|post|tell|mention|give|use)\b|\bfrom\s+now\s+on\b|\bdon'?t\s+(?:send|share|text|message|call|post|tell|schedule|book)\b|\bmake\s+sure\s+(?:to|you|we)\b|\bbe\s+sure\s+to\b|以後|從現在開始|之後都|不要(?:再)?(?:傳|放|加|用|說|提)|別再|千萬不要|記得(?:要|以後)`)},
	// Gotchas / confirmed learnings worth not re-discovering.
	{"gotcha", 0.18, regexp.MustCompile(`(?i)\b(turns out|gotcha|the trick is|note that|remember to|important:|caveat|watch out|by default|the fix (?:is|was)|root cause)\b|看來(?:[^看]|$)|原來|居然`)},
	// Procedural / how-to: imperatives and command-shaped text. CLI verbs are
	// anchored to the segment start or a shell prompt so prose words like "go"
	// or "make" don't false-fire (e.g. "Go code", "make sense").
	{"procedural", 0.16, regexp.MustCompile(`(?i)^(?:to\s+\w+|first|then|next|step\s*\d|(?:npm|git|make|go|docker|kubectl|cargo|pip|yarn|pnpm)\s+\w)|\b(?:run|deploy|install|configure)\s+\S+|(?:^|\s)\$\s+\S+|` + "`[^`]+`")},
	// Identity / stable facts about the user or their world.
	{"fact", 0.12, regexp.MustCompile(`(?i)\b(my|our)\s+(?:\w+\s+){1,3}(?:is|are|=)\b|\bi'?m\s+(?:a|an|the)\b|\bi'?m\s+allergic\b|\bis\s+(?:located|based|set to)\b|\b\d+(?:\.\d+)?\s*(?:%|kg|lb|mb|gb|ms|hrs?|hours?|days?|px|x)\b|過敏|生日是|我住在`)},
}

// emphasisRe matches explicit emphasis markers that raise importance.
var emphasisRe = regexp.MustCompile(`(?i)\b(important|critical|must|always|never|essential|required|urgent)\b|!`)

// classify returns the names of every intent whose pattern fires on the segment.
func classify(seg string) []string {
	var cues []string
	for _, it := range intents {
		if it.pattern.MatchString(seg) {
			cues = append(cues, it.name)
		}
	}
	return cues
}

// importance computes a mechanical salience score in [0,1] from the strongest
// intent cue plus additive bonuses for emphasis and entity richness. Caller-set
// importance (via the LLM tier) always overrides this at commit time.
func importance(seg string, cues []string, ents []entity.Entity) float64 {
	score := 0.4 // base for anything that cleared the entity/cue gate

	// Strongest cue dominates; a second cue adds a small corroboration bonus.
	var top, second float64
	for _, c := range cues {
		w := cueWeight(c)
		if w > top {
			second = top
			top = w
		} else if w > second {
			second = w
		}
	}
	score += top + 0.25*second

	if emphasisRe.MatchString(seg) {
		score += 0.08
	}
	if hasAllCapsWord(seg) {
		score += 0.04
	}
	switch {
	case len(ents) >= 2:
		score += 0.10
	case len(ents) == 1:
		score += 0.05
	}

	if score > 1 {
		score = 1
	}
	return score
}

func cueWeight(name string) float64 {
	for _, it := range intents {
		if it.name == name {
			return it.weight
		}
	}
	return 0
}

// inferKind maps the fired cues to a Tulving memory kind. Procedural cues win
// (how-to knowledge), then semantic cues (facts/preferences/decisions), else
// the segment is treated as an episodic observation.
func inferKind(cues []string) string {
	has := func(name string) bool {
		for _, c := range cues {
			if c == name {
				return true
			}
		}
		return false
	}
	switch {
	case has("procedural"):
		return "procedural"
	case has("preference"), has("decision"), has("fact"), has("gotcha"), has("correction"), has("boundary"):
		return "semantic"
	case has("health"):
		return "episodic"
	default:
		return "episodic"
	}
}

func priorityFor(imp float64) string {
	switch {
	case imp >= 0.75:
		return "high"
	case imp >= 0.55:
		return "normal"
	default:
		return "low"
	}
}

// makeKey builds a stable, descriptive kebab-case key from the segment's named
// entities, falling back to its most distinctive content words.
func makeKey(seg string, ents []entity.Entity) string {
	var parts []string
	for _, e := range ents {
		parts = append(parts, e.Text)
		if len(parts) == 3 {
			break
		}
	}
	if len(parts) == 0 {
		parts = topWords(seg, 3)
	}
	key := kebab(strings.Join(parts, " "))
	if key == "" {
		key = "note"
	}
	if len(key) > maxKeyBytes {
		key = strings.Trim(truncateUTF8(key, maxKeyBytes), "-")
	}
	return key
}

// maxKeyBytes caps a derived key's length in BYTES (CJK runes are 3 bytes).
const maxKeyBytes = 60

// truncateUTF8 returns the longest prefix of s that is at most n bytes and
// ends on a rune boundary. A plain s[:n] splits multi-byte runes and yields
// invalid UTF-8 (69 such keys found live in one store, 2026-10).
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// DedupeKey returns the n-th (n >= 1) collision variant of key: key-a .. key-z
// for n <= 26, then key-27, key-28, ... so every n maps to a distinct key
// (the old fallback reused "-x" for every n > 26).
func DedupeKey(key string, n int) string {
	if n <= 26 {
		return key + "-" + string(rune('a'+n-1))
	}
	return key + "-" + strconv.Itoa(n)
}

// SameContent reports whether two memory contents are the same text up to
// case and whitespace — the rule capture uses to decide that a new value is a
// restatement of an existing head (version it) rather than a different fact
// that merely derived the same key (suffix it).
func SameContent(a, b string) bool {
	return normalizeContent(a) == normalizeContent(b)
}

// topWords returns up to n longer, non-common content words in order of
// appearance — a deterministic stand-in for TF-IDF when a segment has no
// entities and no corpus is available.
func topWords(seg string, n int) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(seg)) {
		w = strings.TrimFunc(w, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
		if len(w) < 4 || commonKeyWords[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
		if len(out) == n {
			break
		}
	}
	return out
}

func kebab(s string) string {
	var b strings.Builder
	lastDash := true // avoid leading dash
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		case !lastDash:
			b.WriteRune('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func hasAllCapsWord(s string) bool {
	for _, w := range strings.Fields(s) {
		if len(w) < 3 {
			continue
		}
		allCaps := true
		hasLetter := false
		for _, r := range w {
			if unicode.IsLetter(r) {
				hasLetter = true
				if !unicode.IsUpper(r) {
					allCaps = false
					break
				}
			}
		}
		if allCaps && hasLetter {
			return true
		}
	}
	return false
}

// commonKeyWords are filler words unhelpful in a key. Kept small and local to
// avoid coupling to entity's larger stopword set.
var commonKeyWords = map[string]bool{
	"this": true, "that": true, "with": true, "from": true, "have": true,
	"want": true, "need": true, "will": true, "should": true, "would": true,
	"about": true, "there": true, "which": true, "their": true, "these": true,
	"those": true, "your": true, "when": true, "what": true, "were": true,
	"been": true, "into": true, "over": true, "just": true, "like": true,
	"some": true, "them": true, "then": true, "than": true, "only": true,
}

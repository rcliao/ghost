// Package capture turns raw usage — conversation transcripts, exchanges, notes —
// into candidate memories using only deterministic heuristics. No LLM required.
//
// It is the LLM-free tier of ghost's write path: an agent (or the ghost CLI)
// can feed a transcript in and get back weighted, kind-inferred, keyed memory
// candidates ready to Put. An LLM may still be used out-of-band to produce
// higher-fidelity candidates, but capture never needs one.
//
// The extractor reuses internal/entity for salience (rule-based NER) and adds
// regex intent classifiers (preference / correction / decision / gotcha /
// procedural / fact) that both gate what is worth remembering and drive the
// kind and mechanical importance of each candidate.
package capture

import (
	"regexp"
	"sort"
	"strings"

	"github.com/rcliao/ghost/internal/entity"
)

// Candidate is a proposed memory extracted from raw text. Its JSON shape is
// stable so an LLM tier can emit the same structure and feed it through the
// identical commit path (see `ghost capture --json`).
type Candidate struct {
	Content    string   `json:"content"`
	Key        string   `json:"key"`
	Kind       string   `json:"kind"` // semantic | episodic | procedural
	Tags       []string `json:"tags,omitempty"`
	Importance float64  `json:"importance"`         // 0..1 mechanical salience
	Priority   string   `json:"priority"`           // low | normal | high | critical
	Cues       []string `json:"cues,omitempty"`     // intent signals that fired (explainability)
	Entities   []string `json:"entities,omitempty"` // named entities detected in the segment
}

// Options tune extraction. The zero value is usable; Extract fills sane defaults.
type Options struct {
	// MinSalience drops candidates whose mechanical importance is below this.
	// Default 0.35.
	MinSalience float64
	// MaxCandidates caps how many candidates are returned (highest importance
	// first). Default 12. Zero means default; negative means unlimited.
	MaxCandidates int
	// SpeakerFilter, when non-empty, keeps only lines spoken by this speaker
	// (case-insensitive match on a "Speaker: ..." prefix, e.g. "user"). Empty
	// captures from every line.
	SpeakerFilter string
	// Tags are added to every returned candidate (e.g. a session or date tag).
	Tags []string
}

const (
	defaultMinSalience   = 0.35
	defaultMaxCandidates = 12
	minSegmentLen        = 20
	minSegmentLenCJK     = 12
	maxSegmentLen        = 400
)

// Extract runs the deterministic pipeline over raw text and returns memory
// candidates ordered by descending mechanical importance. It never calls an LLM
// and never touches a database.
func Extract(text string, opts Options) []Candidate {
	if opts.MinSalience == 0 {
		opts.MinSalience = defaultMinSalience
	}
	if opts.MaxCandidates == 0 {
		opts.MaxCandidates = defaultMaxCandidates
	}
	filter := strings.ToLower(strings.TrimSpace(opts.SpeakerFilter))

	// A2A relay wrapper: a fellow agent's group message forwarded into this
	// agent's turn. The inner speech belongs to the PEER, not the user —
	// distilling it stores wrong-provenance facts (2 live FPs 2026-07-18).
	// Transport metadata, same class as media placeholders.
	if a2aRelayRe.MatchString(text) {
		return nil
	}

	var out []Candidate
	seenKeys := map[string]int{}
	seenContent := map[string]bool{}

	segs := segment(text, filter)
	// A turn that opens with a pure question is ASKING something; the lines
	// under it enumerate the question's context ("can he still eat ice
	// cream? he had A, B, C today"). Those lines elide their subject, so
	// standalone memories from them silently misattribute (2 live FPs
	// 2026-07-19). Cue-carrying or first-person lines still capture.
	leadIsQuestion := len(segs) > 0 && isPureQuestion(stripURLs(strings.TrimSpace(segs[0])))

	for i, seg := range segs {
		trimmed := strings.TrimSpace(seg)
		// Bridge-generated media placeholders ("(sticker) [emoji: ...]",
		// "(photo)") are transport metadata, not user prose — entity-bearing
		// set names were distilling as memories (found live 2026-07-14).
		if mediaPlaceholderRe.MatchString(trimmed) {
			continue
		}
		// CJK packs far more meaning per byte ("我們不吃牛" is a durable
		// family fact in 15 bytes), so CJK-bearing segments get a lower
		// floor; the cue/entity gate still drops short chatter.
		floor := minSegmentLen
		if containsCJKRune(trimmed) {
			floor = minSegmentLenCJK
		}
		if len(trimmed) < floor {
			continue
		}
		if len(trimmed) > maxSegmentLen {
			trimmed = truncateUTF8(trimmed, maxSegmentLen)
		}

		// Grade cues and gate entities on the PROSE only — URLs stay in the
		// stored content as referents, but their innards must not fire cues
		// (%E4… matches the numeric fact pattern) or mint entities.
		prose := stripURLs(trimmed)
		cues := classify(prose)
		// Bullet-line fragments ("-One Mighty的吐司+spread") are pieces of a
		// structured message, not standalone facts — without an intent cue
		// the line rides on its parent memo (live FP 2026-07-18).
		if bulletLineRe.MatchString(trimmed) &&
			(len(cues) == 0 || (len(cues) == 1 && cues[0] == "fact")) {
			// The numeric fact cue fires on spec rows ("商品重量：0.63kg") —
			// units in reference data are not personal facts.
			continue
		}
		// Entity extraction tokenizes on spaces, so Latin names embedded in a
		// CJK run ("看了Milo的燈") are invisible without padding the script
		// boundaries first. Stored content stays original.
		ents := entity.Extract(padScriptBoundaries(prose))

		// A segment is worth remembering only if it carries an intent cue or a
		// named entity. Pure chatter ("haha ok thanks") has neither and is
		// dropped. A name in VOCATIVE position ("週末我不想那麼早起欸Nova",
		// "Nova 我回來囉") is the addressee, not subject matter — it cannot be
		// the entity that qualifies the segment (2 live FPs 2026-07-18).
		if len(cues) == 0 && countNonVocative(prose, ents) == 0 {
			continue
		}
		// Entity-only pure questions are inquiries ABOUT the world, not facts
		// about the asker ("你們知道Paze嗎？" made Paze a "memory" — 4 live
		// FPs). A question still captures when an intent cue fired or when it
		// states the asker's own plan (first-person marker present).
		if len(questionSafeCues(prose, cues)) == 0 && isPureQuestion(prose) && !hasFirstPerson(prose) {
			continue
		}
		// Enumeration under a leading question (see leadIsQuestion above).
		if i > 0 && leadIsQuestion && len(cues) == 0 && !hasFirstPerson(prose) {
			continue
		}
		// Meta-conversation addressed at the agent ("Nova你給我的圖片是…") is
		// about this turn, not the world (live FP 2026-07-19).
		if len(cues) == 0 && addressedToAgent(prose) {
			continue
		}
		// Task requests aimed at the agent ("幫我找找…嗎") are commands to
		// execute, not facts to keep — the embedded 我 must not rescue them
		// through the first-person plan exception (2 live FPs 2026-07-19).
		if len(cues) == 0 && taskRequestRe.MatchString(prose) {
			continue
		}

		imp := importance(trimmed, cues, ents)
		if imp < opts.MinSalience {
			continue
		}

		// Collapse near-identical content within a single batch so the same fact
		// stated twice in one transcript does not yield two candidates.
		norm := normalizeContent(trimmed)
		if seenContent[norm] {
			continue
		}
		seenContent[norm] = true

		entTexts := make([]string, 0, len(ents))
		for _, e := range ents {
			entTexts = append(entTexts, e.Text)
		}

		key := makeKey(trimmed, ents)
		if n := seenKeys[key]; n > 0 {
			key = DedupeKey(key, n)
		}
		seenKeys[key]++

		out = append(out, Candidate{
			Content:    trimmed,
			Key:        key,
			Kind:       inferKind(cues),
			Tags:       append([]string(nil), opts.Tags...),
			Importance: imp,
			Priority:   priorityFor(imp),
			Cues:       cues,
			Entities:   entTexts,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Importance > out[j].Importance
	})
	if opts.MaxCandidates > 0 && len(out) > opts.MaxCandidates {
		out = out[:opts.MaxCandidates]
	}
	return out
}

// segment splits raw text into candidate units. It handles speaker-prefixed
// conversation lines ("User: ...", "Assistant: ..."), applies the speaker
// filter, and splits each line into sentences so a multi-fact utterance yields
// multiple candidates.
func segment(text string, speakerFilter string) []string {
	var segs []string
	prevKept := false
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			prevKept = false
			continue
		}

		speaker, body := splitSpeaker(line)
		if speakerFilter != "" && speaker != "" && strings.ToLower(speaker) != speakerFilter {
			prevKept = false
			continue
		}
		if body == "" {
			body = line
		}

		// A bare-URL line is a referent for the sentence above it, not a
		// sentence of its own ("不對喔，我們的是這個" + link — the deictic
		// correction is content-free without the URL). Attach it to the
		// previous segment when that segment came from the same kept speaker.
		if bareURLRe.MatchString(body) {
			if prevKept && len(segs) > 0 {
				segs[len(segs)-1] += " " + body
			}
			continue
		}
		prevKept = true

		for _, sent := range splitSentences(body) {
			sent = strings.TrimSpace(sent)
			if sent != "" {
				segs = append(segs, sent)
			}
		}
	}
	return segs
}

// splitSpeaker separates a short "Speaker: rest" prefix from the body. It only
// treats the prefix as a speaker when it is short and single-token, mirroring
// entity.Extract's heuristic so "http://..." style colons are not mistaken.
func splitSpeaker(line string) (speaker, body string) {
	idx := strings.Index(line, ": ")
	if idx <= 0 || idx >= 30 {
		return "", line
	}
	prefix := line[:idx]
	if len(prefix) >= 20 || strings.ContainsAny(prefix, " \t/") {
		return "", line
	}
	return prefix, strings.TrimSpace(line[idx+2:])
}

// splitSentences breaks a line on sentence terminators while keeping the
// terminator attached. It is intentionally simple (no abbreviation handling) —
// over-splitting is harmless because tiny fragments are dropped by minSegmentLen.
func splitSentences(s string) []string {
	var out []string
	var b strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		b.WriteRune(r)
		terminator := r == '!' || r == '\n' || r == '。' || r == '！' || r == '？'
		// '.' and '?' split only at a word boundary — mid-token they are
		// URL/decimal structure ("shop.example.com", "3.99"), and splitting
		// there shreds links into cue-firing fragments (live FP 2026-07-19).
		if (r == '.' || r == '?') && (i+1 == len(runes) || runes[i+1] == ' ' || runes[i+1] == '\t') {
			terminator = true
		}
		if terminator {
			out = append(out, b.String())
			b.Reset()
		}
	}
	if strings.TrimSpace(b.String()) != "" {
		out = append(out, b.String())
	}
	return out
}

// stripURLs removes http(s) links so cue and entity grading see only the
// user's prose — percent-encoded paths fire the numeric fact cue (%E4…)
// and link innards mint bogus entities. The URL itself stays in the stored
// content as the referent.
var urlTokenRe = regexp.MustCompile(`https?://\S+`)

func stripURLs(s string) string {
	return strings.TrimSpace(urlTokenRe.ReplaceAllString(s, " "))
}

func normalizeContent(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// padScriptBoundaries inserts spaces where Latin/digit runs abut CJK runs so
// space-tokenized consumers (entity extraction) see embedded names like
// "Milo" in 看了Milo的燈 as their own tokens.
func padScriptBoundaries(s string) string {
	isCJK := func(r rune) bool {
		// Includes CJK punctuation (、。「」…) and fullwidth forms (，！？) —
		// without them "OFF，再調回" tokenizes as one blob and the UI label
		// leaks through as an entity (live FP 2026-07-18).
		return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF) ||
			(r >= 0x3040 && r <= 0x30FF) || (r >= 0xF900 && r <= 0xFAFF) ||
			(r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF)
	}
	isLatin := func(r rune) bool {
		return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	var prev rune
	for i, r := range s {
		if i > 0 && ((isCJK(prev) && isLatin(r)) || (isLatin(prev) && isCJK(r))) {
			b.WriteRune(' ')
		}
		b.WriteRune(r)
		prev = r
	}
	return b.String()
}

// containsCJKRune reports whether the string carries any Han/kana rune.
func containsCJKRune(s string) bool {
	for _, r := range s {
		if (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF) ||
			(r >= 0x3040 && r <= 0x30FF) {
			return true
		}
	}
	return false
}

// mediaPlaceholderRe matches transport-layer media placeholder segments.
// mediaPlaceholderRe matches the bridge's media metadata lines. The media
// word may be followed by qualifiers before the closing paren — the bridge
// began emitting "(sticker from <Name> (nickname))" and the old form, which
// required ")" immediately after the word, let a previously-fixed FP class
// back in (live regression 2026-07-29). Anchored at the start and bounded to
// one line so it cannot swallow prose that merely opens with a paren.
var mediaPlaceholderRe = regexp.MustCompile(`^\((?:sticker|photo|video|voice(?:\s+message)?|file|document|audio)\b[^)\n]*\)?`)

// bareURLRe matches a line that is nothing but a single URL.
var bareURLRe = regexp.MustCompile(`^https?://\S+$`)

// a2aRelayRe matches the bridge's fellow-agent relay preamble at the start
// of a turn (optionally after a "User: " speaker prefix).
var a2aRelayRe = regexp.MustCompile(`^(?:\w+:\s*)?\[\S+ \(your fellow agent\) said this in the group`)

// bulletLineRe matches memo-style list-item lines, decorated headers
// (☆名稱：…), and label：value spec-sheet rows — pasted product listings
// are reference data, not user prose (3 live FPs 2026-07-19).
var bulletLineRe = regexp.MustCompile(`^[-–•▫️*☆★※◎]\s*\S|^[^：\s]{1,8}：|^[A-Za-z][A-Za-z ]{0,30}:\s`)

// countNonVocative returns how many entities are NOT in vocative position:
// at the segment end right after a CJK vocative particle, or leading the
// segment as a standalone address followed by CJK text (or a comma).
func countNonVocative(seg string, ents []entity.Entity) int {
	n := 0
	for _, e := range ents {
		if isVocative(seg, e.Text) {
			continue
		}
		// Short all-caps tokens (ON, OFF, PC, UV) are UI labels and unit
		// abbreviations, not named entities — they must not qualify a
		// cue-less segment (live FPs: ON/OFF troubleshooting steps).
		if isShortCapsToken(e.Text) {
			continue
		}
		n++
	}
	return n
}

// addressedRe matches a turn that OPENS by addressing someone by name:
// a capitalised name butted against a second-person pronoun ("Nova你給我的
// 圖片是…") or set off by a comma before CJK prose ("Nova，剛剛那張…"). The
// leading name is often invisible to the NER (sentence-initial capitals are
// discounted), so this is a pattern check rather than an entity check.
var addressedRe = regexp.MustCompile(`^[A-Z][A-Za-z]{1,15}(?:[你妳]|幫我|替我|[，,]\s*\p{Han})|^\p{Han}{2,4}[你妳]`)

// addressedToAgent reports meta-conversation aimed at the listener — about
// what the agent just did, not about the world. A real fact told to an agent
// carries an intent cue ("皮卡，我現在吃xyzal過敏藥" fires health), so the cue
// path keeps those.
func addressedToAgent(seg string) bool {
	return addressedRe.MatchString(strings.TrimSpace(seg))
}

func isShortCapsToken(s string) bool {
	if len(s) > 3 {
		return false
	}
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			// entity texts arrive lowercased; accept a-z as "caps" only by
			// length — a 2-3 letter bare token carries no identity either way
			if r < 'a' || r > 'z' {
				return false
			}
		}
	}
	return true
}

func isVocative(seg, name string) bool {
	if name == "" {
		return false
	}
	// Entity texts arrive lowercased; compare case-insensitively.
	low, lname := strings.ToLower(seg), strings.ToLower(name)
	// Trailing "…欸Nova" / "…啊Nova": name ends the segment and the rune
	// before it is a vocative particle.
	if strings.HasSuffix(low, lname) {
		head := []rune(seg[:len(seg)-len(name)])
		if len(head) > 0 {
			switch head[len(head)-1] {
			case '欸', '啊', '喔', '呀', '嘛', '，', ' ':
				return true
			}
		}
	}
	// Leading "Nova 我回來囉" / "Nova，…": standalone address followed by
	// CJK prose (English "Nova's…" or "Nova make sure…" stays a subject).
	if strings.HasPrefix(low, lname) {
		rest := []rune(seg[len(name):])
		if len(rest) >= 2 && (rest[0] == '，' || rest[0] == ',' || rest[0] == ' ') && containsCJKRune(string(rest[1:2])) {
			return true
		}
		// Unpunctuated address: a name butted straight against a
		// second-person pronoun ("Nova你給我的圖片是…") is who is being
		// spoken TO. Particles (Milo的燈) stay subject matter.
		if len(rest) >= 1 && (rest[0] == '你' || rest[0] == '妳') {
			return true
		}
	}
	return false
}

// isPureQuestion reports whether the segment reads as an interrogative:
// question-mark terminated, or carrying CJK question particles.
func isPureQuestion(s string) bool {
	t := strings.TrimSpace(s)
	if strings.HasSuffix(t, "?") || strings.HasSuffix(t, "？") {
		return true
	}
	// Question-final particles arrive without punctuation ("…平底鍋嗎") —
	// mami-style typing omits the ？ (live FPs 2026-07-19).
	if strings.HasSuffix(t, "嗎") || strings.HasSuffix(t, "呢") {
		return true
	}
	for _, p := range []string{"嗎？", "嗎?", "是不是", "為什麼", "怎麼會"} {
		if strings.Contains(t, p) {
			return true
		}
	}
	return false
}

// weakInQuestionRe matches triggers that carry intent in a STATEMENT but not
// in a question: bare "actually"/"really" are intensifiers ("does it actually
// work?"), and a leading discourse connective is not a procedural step ("then
// what really works?"). Both fired cues that rescued pure questions past the
// interrogative guard — 2 live FPs 2026-07-29.
var weakInQuestionRe = regexp.MustCompile(`(?i)\b(?:actually|really)\b|^\s*(?:then|first|next)\b`)

// questionSafeCues re-grades a segment with question-ambiguous triggers
// masked, so only cues with independent evidence can rescue a question.
// Statements are unaffected — this is consulted solely by the question guard.
func questionSafeCues(prose string, cues []string) []string {
	if len(cues) == 0 {
		return cues
	}
	masked := weakInQuestionRe.ReplaceAllString(prose, " ")
	if masked == prose {
		return cues
	}
	return classify(masked)
}

// taskRequestRe matches "do this for me" commands aimed at the agent
// (幫我找/查/看, 可以幫我…嗎). The agent executes the task; the request
// itself is not a durable fact — and its 我 must not trip the
// first-person plan exception.
var taskRequestRe = regexp.MustCompile(`^(?:還是|那|嗯)?(?:請|麻煩)?(?:你們?|妳們?)?(?:再|先|順便|也)?(?:可以|能不能|可不可以)?(?:幫|替)我|^(?:can|could|would)\s+you\b|^(?:please\s+)?help\s+me\b`)

// hasFirstPerson reports a first-person stake — the asker talking about their
// own plans or situation ("所以我們下個月到機場之後..." is a plan, not an inquiry).
func hasFirstPerson(s string) bool {
	if strings.Contains(s, "我") {
		return true
	}
	lower := " " + strings.ToLower(s) + " "
	for _, w := range []string{" i ", " my ", " we ", " our ", " i'm ", " i'll "} {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

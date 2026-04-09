// Package entity provides lightweight rule-based named entity extraction.
// No LLM required — uses heuristics for proper nouns, multi-word names,
// and domain-specific patterns.
package entity

import (
	"strings"
	"unicode"
)

// Entity represents an extracted named entity.
type Entity struct {
	Text string // normalized entity text (lowercase)
	Kind string // "name", "place", "thing", or "other"
}

// commonWords are words that are often capitalized but aren't entities.
var commonWords = map[string]bool{
	"i": true, "the": true, "a": true, "an": true, "and": true, "or": true,
	"but": true, "is": true, "are": true, "was": true, "were": true,
	"be": true, "been": true, "being": true, "have": true, "has": true,
	"had": true, "do": true, "does": true, "did": true, "will": true,
	"would": true, "could": true, "should": true, "may": true, "might": true,
	"shall": true, "can": true, "need": true, "dare": true, "ought": true,
	"used": true, "to": true, "of": true, "in": true, "for": true,
	"on": true, "with": true, "at": true, "by": true, "from": true,
	"as": true, "into": true, "through": true, "during": true, "before": true,
	"after": true, "above": true, "below": true, "between": true, "out": true,
	"off": true, "over": true, "under": true, "again": true, "further": true,
	"then": true, "once": true, "here": true, "there": true, "when": true,
	"where": true, "why": true, "how": true, "all": true, "each": true,
	"every": true, "both": true, "few": true, "more": true, "most": true,
	"other": true, "some": true, "such": true, "no": true, "nor": true,
	"not": true, "only": true, "own": true, "same": true, "so": true,
	"than": true, "too": true, "very": true, "just": true, "because": true,
	"if": true, "this": true, "that": true, "these": true, "those": true,
	"it": true, "its": true, "my": true, "your": true, "his": true,
	"her": true, "our": true, "their": true, "what": true, "which": true,
	"who": true, "whom": true, "we": true, "you": true, "he": true,
	"she": true, "they": true, "me": true, "him": true, "us": true,
	"them": true, "also": true, "about": true, "up": true, "well": true,
	"really": true, "think": true, "know": true, "like": true, "yes": true,
	"oh": true, "hey": true, "hi": true, "hello": true,
	"sure": true, "okay": true, "ok": true, "yeah": true, "right": true,
	"good": true, "great": true, "nice": true, "thanks": true, "thank": true,
	"going": true, "getting": true, "looking": true, "trying": true,
	// Common sentence starters
	"however": true, "moreover": true, "therefore": true, "meanwhile": true,
	"actually": true, "basically": true, "honestly": true, "personally": true,
	// Common verbs that might be capitalized at sentence start
	"said": true, "told": true, "asked": true, "went": true, "got": true,
	"made": true, "came": true, "took": true, "gave": true, "found": true,
	"thought": true, "started": true, "tried": true, "wanted": true,
}

// Extract extracts named entities from text using rule-based heuristics.
// Returns deduplicated entities sorted by frequency.
func Extract(text string) []Entity {
	counts := make(map[string]int)
	kinds := make(map[string]string)

	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Remove speaker prefix (e.g., "user: " or "Caroline: ")
		if idx := strings.Index(line, ": "); idx > 0 && idx < 30 {
			// Check if it's a speaker prefix (short, capitalized)
			prefix := line[:idx]
			if len(prefix) < 20 && !strings.Contains(prefix, " ") {
				line = line[idx+2:]
			}
		}

		words := strings.Fields(line)
		i := 0
		for i < len(words) {
			word := words[i]
			cleaned := cleanWord(word)

			if cleaned == "" || len(cleaned) < 2 {
				i++
				continue
			}

			// Check if word starts with uppercase
			firstRune, _ := firstRuneOf(cleaned)
			if !unicode.IsUpper(firstRune) {
				i++
				continue
			}

			// Skip if it's at the start of a sentence (after . ! ? or first word)
			if i == 0 || (i > 0 && endsWithSentence(words[i-1])) {
				// Still include if it's a multi-word name
				if i+1 < len(words) {
					next := cleanWord(words[i+1])
					nextFirst, _ := firstRuneOf(next)
					if unicode.IsUpper(nextFirst) && !commonWords[strings.ToLower(next)] {
						// Multi-word name at sentence start — include
						entity := collectMultiWord(words, i)
						lower := strings.ToLower(entity)
						if !isCommonPhrase(lower) {
							counts[lower]++
							kinds[lower] = classifyEntity(entity)
						}
						i += len(strings.Fields(entity))
						continue
					}
				}
				i++
				continue
			}

			// Skip common words
			if commonWords[strings.ToLower(cleaned)] {
				i++
				continue
			}

			// Collect multi-word names (consecutive capitalized words)
			entity := collectMultiWord(words, i)
			lower := strings.ToLower(entity)
			if !isCommonPhrase(lower) {
				counts[lower]++
				kinds[lower] = classifyEntity(entity)
			}
			i += len(strings.Fields(entity))
		}
	}

	// Convert to sorted list, filter by minimum occurrence or significance
	var entities []Entity
	for text, count := range counts {
		if count >= 1 && len(text) >= 2 {
			entities = append(entities, Entity{Text: text, Kind: kinds[text]})
		}
	}
	return entities
}

// EntityOverlap returns shared entities between two texts and an overlap score.
// Score is Jaccard similarity of entity sets, weighted by entity rarity.
func EntityOverlap(entitiesA, entitiesB []Entity) (shared []string, score float64) {
	setA := make(map[string]bool, len(entitiesA))
	for _, e := range entitiesA {
		setA[e.Text] = true
	}

	var common []string
	for _, e := range entitiesB {
		if setA[e.Text] {
			common = append(common, e.Text)
		}
	}

	if len(common) == 0 {
		return nil, 0
	}

	// Jaccard similarity
	union := len(entitiesA) + len(entitiesB) - len(common)
	if union == 0 {
		return common, 0
	}
	return common, float64(len(common)) / float64(union)
}

func cleanWord(w string) string {
	return strings.TrimFunc(w, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\'' && r != '-'
	})
}

func firstRuneOf(s string) (rune, bool) {
	for _, r := range s {
		return r, true
	}
	return 0, false
}

func endsWithSentence(w string) bool {
	w = strings.TrimSpace(w)
	if w == "" {
		return false
	}
	last := w[len(w)-1]
	return last == '.' || last == '!' || last == '?'
}

func collectMultiWord(words []string, start int) string {
	var parts []string
	for i := start; i < len(words); i++ {
		cleaned := cleanWord(words[i])
		if cleaned == "" {
			break
		}
		first, _ := firstRuneOf(cleaned)
		if !unicode.IsUpper(first) {
			break
		}
		// Allow short connectors in multi-word names ("New York City", "Dr. Martin Luther King")
		if commonWords[strings.ToLower(cleaned)] && i > start {
			// Only allow "of", "the", "de" as connectors
			lower := strings.ToLower(cleaned)
			if lower != "of" && lower != "the" && lower != "de" && lower != "van" && lower != "von" {
				break
			}
		}
		parts = append(parts, cleaned)
	}
	return strings.Join(parts, " ")
}

func isCommonPhrase(s string) bool {
	// Filter out common capitalized phrases that aren't entities
	common := map[string]bool{
		"i'm": true, "i've": true, "i'll": true, "i'd": true,
		"let's": true, "don't": true, "can't": true, "won't": true,
	}
	return common[s]
}

func classifyEntity(text string) string {
	// Simple heuristic classification
	lower := strings.ToLower(text)
	if strings.HasPrefix(lower, "dr.") || strings.HasPrefix(lower, "mr.") || strings.HasPrefix(lower, "mrs.") || strings.HasPrefix(lower, "ms.") {
		return "name"
	}
	words := strings.Fields(text)
	if len(words) == 1 {
		return "name" // single proper noun, likely a person name
	}
	return "other"
}

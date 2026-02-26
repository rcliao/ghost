// Package ingest provides parsers for importing external markdown files into agent-memory.
package ingest

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// Section represents a parsed section from a markdown file.
type Section struct {
	Heading    string // H2 heading text (empty for preamble or whole-file sections)
	Content    string // Body text below the heading
	SourceFile string // Original filename (basename)
	LineNumber int    // 1-based line number where this section starts
}

// ParseMarkdown splits markdown content by ## headings into sections.
// Each H2 heading becomes one section. Content before the first H2 goes into
// a section with an empty heading (the preamble). If no H2 headings exist,
// the whole file is returned as a single section.
func ParseMarkdown(content, sourceFile string) []Section {
	lines := strings.Split(content, "\n")
	var sections []Section
	var current *Section

	for i, line := range lines {
		if heading, ok := parseH2(line); ok {
			// Flush previous section
			if current != nil {
				current.Content = strings.TrimSpace(current.Content)
				if current.Heading != "" || current.Content != "" {
					sections = append(sections, *current)
				}
			}
			current = &Section{
				Heading:    heading,
				SourceFile: sourceFile,
				LineNumber: i + 1,
			}
		} else {
			if current == nil {
				// Preamble: content before first H2
				current = &Section{
					SourceFile: sourceFile,
					LineNumber: 1,
				}
			}
			current.Content += line + "\n"
		}
	}

	// Flush last section
	if current != nil {
		current.Content = strings.TrimSpace(current.Content)
		if current.Heading != "" || current.Content != "" {
			sections = append(sections, *current)
		}
	}

	return sections
}

// parseH2 checks if a line is an H2 heading and returns the heading text.
func parseH2(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "## ") {
		return strings.TrimSpace(trimmed[3:]), true
	}
	return "", false
}

var (
	emojiRe      = regexp.MustCompile(`[\x{1F000}-\x{1FFFF}]|[\x{2600}-\x{27BF}]|[\x{FE00}-\x{FE0F}]|[\x{200D}]|[\x{20E3}]|[\x{E0020}-\x{E007F}]`)
	nonAlphaNum  = regexp.MustCompile(`[^a-z0-9-]+`)
	multiDash    = regexp.MustCompile(`-{2,}`)
	dateFilename = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\.md$`)
)

// SlugifyHeading converts a heading string to a key-safe slug.
// Lowercase, hyphens for separators, emoji stripped.
func SlugifyHeading(heading string) string {
	s := heading
	// Strip emoji
	s = emojiRe.ReplaceAllString(s, "")
	// Lowercase
	s = strings.ToLower(s)
	// Replace non-alphanumeric with hyphens
	s = nonAlphaNum.ReplaceAllString(s, "-")
	// Collapse multiple hyphens
	s = multiDash.ReplaceAllString(s, "-")
	// Trim leading/trailing hyphens
	s = strings.Trim(s, "-")
	if s == "" {
		return "untitled"
	}
	return s
}

// SectionKey generates a unique key for a section based on its heading and source file.
// For daily log files (YYYY-MM-DD.md), the date is prefixed to the key.
func SectionKey(section Section, sourceFile string) string {
	basename := filepath.Base(sourceFile)
	datePrefix := ""
	if m := dateFilename.FindStringSubmatch(basename); m != nil {
		datePrefix = m[1] + ":"
	}

	if section.Heading != "" {
		return datePrefix + SlugifyHeading(section.Heading)
	}

	// No heading: use filename as key
	name := strings.TrimSuffix(basename, filepath.Ext(basename))
	slug := slugifyFilename(name)
	if slug == "" {
		return datePrefix + "_preamble"
	}
	return datePrefix + slug
}

// PreambleKey returns the key for preamble content.
func PreambleKey(sourceFile string) string {
	basename := filepath.Base(sourceFile)
	datePrefix := ""
	if m := dateFilename.FindStringSubmatch(basename); m != nil {
		datePrefix = m[1] + ":"
	}
	return datePrefix + "_preamble"
}

// FilenameDate extracts a date string from a daily log filename (YYYY-MM-DD.md).
// Returns empty string if the filename doesn't match the pattern.
func FilenameDate(filename string) string {
	basename := filepath.Base(filename)
	if m := dateFilename.FindStringSubmatch(basename); m != nil {
		return m[1]
	}
	return ""
}

func slugifyFilename(name string) string {
	s := strings.ToLower(name)
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' {
			return r
		}
		return '-'
	}, s)
	s = multiDash.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

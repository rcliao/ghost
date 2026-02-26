package ingest

import (
	"testing"
)

func TestParseMarkdownMultipleSections(t *testing.T) {
	content := `# Title

Some intro text.

## Section One

Content of section one.

## Section Two

Content of section two.
More content here.

## Section Three

Final section.
`
	sections := ParseMarkdown(content, "TEST.md")

	// Expect: preamble + 3 sections
	if len(sections) != 4 {
		t.Fatalf("want 4 sections, got %d", len(sections))
	}

	// Preamble
	if sections[0].Heading != "" {
		t.Errorf("preamble heading: want empty, got %q", sections[0].Heading)
	}
	if got := sections[0].Content; got != "# Title\n\nSome intro text." {
		t.Errorf("preamble content: got %q", got)
	}

	// Section One
	if sections[1].Heading != "Section One" {
		t.Errorf("section 1 heading: want %q, got %q", "Section One", sections[1].Heading)
	}
	if got := sections[1].Content; got != "Content of section one." {
		t.Errorf("section 1 content: got %q", got)
	}
	if sections[1].LineNumber != 5 {
		t.Errorf("section 1 line: want 5, got %d", sections[1].LineNumber)
	}

	// Section Two
	if sections[2].Heading != "Section Two" {
		t.Errorf("section 2 heading: want %q, got %q", "Section Two", sections[2].Heading)
	}

	// Section Three
	if sections[3].Heading != "Section Three" {
		t.Errorf("section 3 heading: want %q, got %q", "Section Three", sections[3].Heading)
	}
}

func TestParseMarkdownNoH2(t *testing.T) {
	content := `# Just a Title

Some content without any H2 headings.
More lines here.
`
	sections := ParseMarkdown(content, "notes.md")

	if len(sections) != 1 {
		t.Fatalf("want 1 section, got %d", len(sections))
	}
	if sections[0].Heading != "" {
		t.Errorf("heading: want empty, got %q", sections[0].Heading)
	}
	if sections[0].SourceFile != "notes.md" {
		t.Errorf("source file: want %q, got %q", "notes.md", sections[0].SourceFile)
	}
}

func TestParseMarkdownEmptyFile(t *testing.T) {
	sections := ParseMarkdown("", "empty.md")
	if len(sections) != 0 {
		t.Fatalf("want 0 sections, got %d", len(sections))
	}
}

func TestParseMarkdownOnlyWhitespace(t *testing.T) {
	sections := ParseMarkdown("   \n\n  \n", "blank.md")
	if len(sections) != 0 {
		t.Fatalf("want 0 sections, got %d", len(sections))
	}
}

func TestParseMarkdownNoPreamble(t *testing.T) {
	content := `## First

Hello.

## Second

World.
`
	sections := ParseMarkdown(content, "test.md")
	if len(sections) != 2 {
		t.Fatalf("want 2 sections, got %d", len(sections))
	}
	if sections[0].Heading != "First" {
		t.Errorf("heading 0: want %q, got %q", "First", sections[0].Heading)
	}
}

func TestSlugifyHeading(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"EV (My Human)", "ev-my-human"},
		{"Project Status & Goals", "project-status-goals"},
		{"Simple", "simple"},
		{"ALL CAPS HEADING", "all-caps-heading"},
		{"with---dashes", "with-dashes"},
		{"  leading spaces  ", "leading-spaces"},
		{"emoji 🚀 heading 🎯", "emoji-heading"},
		{"", "untitled"},
		{"🔥🔥🔥", "untitled"},
	}

	for _, tt := range tests {
		got := SlugifyHeading(tt.input)
		if got != tt.want {
			t.Errorf("SlugifyHeading(%q): want %q, got %q", tt.input, tt.want, got)
		}
	}
}

func TestSectionKeyWithHeading(t *testing.T) {
	s := Section{Heading: "My Great Section", SourceFile: "MEMORY.md"}
	got := SectionKey(s, "MEMORY.md")
	if got != "my-great-section" {
		t.Errorf("want %q, got %q", "my-great-section", got)
	}
}

func TestSectionKeyNoHeading(t *testing.T) {
	s := Section{Heading: "", SourceFile: "IDENTITY.md"}
	got := SectionKey(s, "IDENTITY.md")
	if got != "identity" {
		t.Errorf("want %q, got %q", "identity", got)
	}
}

func TestSectionKeyDailyLog(t *testing.T) {
	s := Section{Heading: "Morning Tasks", SourceFile: "2026-02-15.md"}
	got := SectionKey(s, "2026-02-15.md")
	if got != "2026-02-15:morning-tasks" {
		t.Errorf("want %q, got %q", "2026-02-15:morning-tasks", got)
	}
}

func TestSectionKeyDailyLogNoHeading(t *testing.T) {
	s := Section{Heading: "", SourceFile: "2026-02-15.md"}
	got := SectionKey(s, "2026-02-15.md")
	// No heading on a date file: uses the date digits as slug
	if got != "2026-02-15:2026-02-15" {
		t.Errorf("want %q, got %q", "2026-02-15:2026-02-15", got)
	}
}

func TestPreambleKey(t *testing.T) {
	got := PreambleKey("MEMORY.md")
	if got != "_preamble" {
		t.Errorf("want %q, got %q", "_preamble", got)
	}

	got = PreambleKey("2026-02-15.md")
	if got != "2026-02-15:_preamble" {
		t.Errorf("want %q, got %q", "2026-02-15:_preamble", got)
	}
}

func TestFilenameDate(t *testing.T) {
	tests := []struct {
		filename string
		want     string
	}{
		{"2026-02-15.md", "2026-02-15"},
		{"MEMORY.md", ""},
		{"notes.md", ""},
		{"2026-13-40.md", "2026-13-40"}, // regex doesn't validate date values
		{"/path/to/2026-02-15.md", "2026-02-15"},
	}
	for _, tt := range tests {
		got := FilenameDate(tt.filename)
		if got != tt.want {
			t.Errorf("FilenameDate(%q): want %q, got %q", tt.filename, tt.want, got)
		}
	}
}

func TestParseMarkdownEmojiHeading(t *testing.T) {
	content := `## 🧠 Brain Dump

Some thoughts here.

## 🎯 Goals & Plans

Goal content.
`
	sections := ParseMarkdown(content, "test.md")
	if len(sections) != 2 {
		t.Fatalf("want 2 sections, got %d", len(sections))
	}

	key0 := SectionKey(sections[0], "test.md")
	if key0 != "brain-dump" {
		t.Errorf("key 0: want %q, got %q", "brain-dump", key0)
	}

	key1 := SectionKey(sections[1], "test.md")
	if key1 != "goals-plans" {
		t.Errorf("key 1: want %q, got %q", "goals-plans", key1)
	}
}

func TestParseMarkdownEmptySection(t *testing.T) {
	content := `## Has Content

Real content.

## Empty Section

## Also Has Content

Yep.
`
	sections := ParseMarkdown(content, "test.md")
	// "Empty Section" has no content but does have a heading — should still appear
	if len(sections) != 3 {
		t.Fatalf("want 3 sections, got %d", len(sections))
	}
	if sections[1].Heading != "Empty Section" {
		t.Errorf("heading 1: want %q, got %q", "Empty Section", sections[1].Heading)
	}
	if sections[1].Content != "" {
		t.Errorf("content 1: want empty, got %q", sections[1].Content)
	}
}

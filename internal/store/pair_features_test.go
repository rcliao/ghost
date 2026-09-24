package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rcliao/ghost/internal/model"
)

// The fixture is synthetic: it mirrors the SHAPES of pairs graded on a private
// production snapshot (2026-09-23) without any of their content. Each case
// states the features a correct extractor must produce for that shape.
type pairFixture struct {
	Cases []struct {
		Name    string        `json:"name"`
		Pattern string        `json:"pattern"`
		Older   fixtureMem    `json:"older"`
		Newer   fixtureMem    `json:"newer"`
		Expect  fixtureExpect `json:"expect"`
	} `json:"cases"`
}

type fixtureMem struct {
	Key        string `json:"key"`
	CreatedAt  string `json:"created_at"`
	SourceUser string `json:"source_user"`
	Content    string `json:"content"`
}

type fixtureExpect struct {
	DaysApartMin          *float64 `json:"days_apart_min"`
	DaysApartMax          *float64 `json:"days_apart_max"`
	SharedEntitiesInclude []string `json:"shared_entities_include"`
	SharedEntitiesExclude []string `json:"shared_entities_exclude"`
	JaccardMin            *float64 `json:"jaccard_min"`
	JaccardMax            *float64 `json:"jaccard_max"`
	NewerCue              *string  `json:"newer_cue"`
	SameUser              *bool    `json:"same_user"`
	KeyPrefixMatch        *bool    `json:"key_prefix_match"`
}

func (m fixtureMem) memory(t *testing.T) *model.Memory {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, m.CreatedAt)
	if err != nil {
		t.Fatalf("%s: bad created_at: %v", m.Key, err)
	}
	return &model.Memory{Key: m.Key, Content: m.Content, CreatedAt: ts, SourceUser: m.SourceUser}
}

func loadPairFixture(t *testing.T) pairFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "pair_rules", "fixture.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fx pairFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(fx.Cases) == 0 {
		t.Fatal("fixture has no cases")
	}
	return fx
}

func TestPairFeaturesFixture(t *testing.T) {
	fx := loadPairFixture(t)
	// Document frequency over the whole fixture corpus, as a run would compute
	// it over a namespace.
	var corpus []*model.Memory
	for _, c := range fx.Cases {
		corpus = append(corpus, c.Older.memory(t), c.Newer.memory(t))
	}
	df := EntityDF(corpus)

	for _, c := range fx.Cases {
		t.Run(c.Name, func(t *testing.T) {
			// Pass them reversed on purpose: the result must be normalised.
			f := NewPairFeatures(c.Newer.memory(t), c.Older.memory(t), df)
			if f.OlderKey != c.Older.Key || f.NewerKey != c.Newer.Key {
				t.Errorf("order not normalised: older=%s newer=%s", f.OlderKey, f.NewerKey)
			}
			e := c.Expect
			if e.DaysApartMin != nil && f.DaysApart < *e.DaysApartMin {
				t.Errorf("days_apart %.2f < min %.2f", f.DaysApart, *e.DaysApartMin)
			}
			if e.DaysApartMax != nil && f.DaysApart > *e.DaysApartMax {
				t.Errorf("days_apart %.2f > max %.2f", f.DaysApart, *e.DaysApartMax)
			}
			shared := map[string]bool{}
			for _, s := range f.SharedEntities {
				shared[s.Text] = true
			}
			for _, want := range e.SharedEntitiesInclude {
				if !shared[want] {
					t.Errorf("shared entities %v missing %q", keys(shared), want)
				}
			}
			for _, bad := range e.SharedEntitiesExclude {
				if shared[bad] {
					t.Errorf("shared entities include date word %q", bad)
				}
			}
			if e.JaccardMin != nil && f.Jaccard < *e.JaccardMin {
				t.Errorf("jaccard %.2f < min %.2f", f.Jaccard, *e.JaccardMin)
			}
			if e.JaccardMax != nil && f.Jaccard > *e.JaccardMax {
				t.Errorf("jaccard %.2f > max %.2f", f.Jaccard, *e.JaccardMax)
			}
			if e.NewerCue != nil && f.NewerCue != *e.NewerCue {
				t.Errorf("newer_cue = %q, want %q", f.NewerCue, *e.NewerCue)
			}
			if e.SameUser != nil && f.SameUser != *e.SameUser {
				t.Errorf("same_user = %v, want %v", f.SameUser, *e.SameUser)
			}
			if e.KeyPrefixMatch != nil && f.KeyPrefixMatch != *e.KeyPrefixMatch {
				t.Errorf("key_prefix_match = %v, want %v (prefixes %q / %q)", f.KeyPrefixMatch, *e.KeyPrefixMatch, KeyPrefix(c.Older.Key), KeyPrefix(c.Newer.Key))
			}
		})
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestPairFeaturesIsPure(t *testing.T) {
	a := &model.Memory{Key: "a-2026-01", Content: "Rina confirmed the Harborline booking.", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	b := &model.Memory{Key: "a-2026-02", Content: "The Harborline stay was cancelled because of the storm.", CreatedAt: time.Date(2026, 1, 9, 0, 0, 0, 0, time.UTC)}
	f1 := NewPairFeatures(a, b, nil)
	f2 := NewPairFeatures(b, a, nil)
	j1, _ := json.Marshal(f1)
	j2, _ := json.Marshal(f2)
	if string(j1) != string(j2) {
		t.Errorf("features depend on argument order:\n%s\n%s", j1, j2)
	}
	if f1.DaysApart != 8 || f1.NewerCue != "causal" || !f1.KeyPrefixMatch {
		t.Errorf("unexpected features: %+v", f1)
	}
	if f1.MinSharedDF != 1 || f1.SharedWithin(1) != 1 {
		t.Errorf("nil df should count shared entities at frequency 1: %+v", f1.SharedEntities)
	}
}

func TestSharedEntitiesCollapseParts(t *testing.T) {
	a := &model.Memory{Key: "a", Content: "We stopped at Tunnel View on the way in.", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	b := &model.Memory{Key: "b", Content: "Planning notes: Tunnel View at sunset, then dinner.", CreatedAt: time.Date(2026, 1, 9, 0, 0, 0, 0, time.UTC)}
	f := NewPairFeatures(a, b, nil)
	if len(f.SharedEntities) != 1 || f.SharedEntities[0].Text != "tunnel view" {
		t.Errorf("expected one collapsed shared entity, got %+v", f.SharedEntities)
	}
}

func TestKeyPrefix(t *testing.T) {
	cases := map[string]string{
		"session-summary-2026-07-09-mami-meals": "session-summary",
		"session-summary-2026-07-09":            "session-summary",
		"media-note-1786233128296":              "media-note",
		"session-summary-gen61-2026-08-13":      "session-summary",
		"learning-group-chat-eavesdrop":         "learning-group-chat-eavesdrop",
	}
	for in, want := range cases {
		if got := KeyPrefix(in); got != want {
			t.Errorf("KeyPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDaysBetweenHelper(t *testing.T) {
	a := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if d := daysBetween(a, a.Add(36*time.Hour)); d != 1.5 {
		t.Errorf("daysBetween = %v", d)
	}
}

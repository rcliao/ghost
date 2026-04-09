package entity

import (
	"testing"
)

func TestExtract(t *testing.T) {
	text := `user: I took Luna to the vet yesterday. Dr. Smith said she needs more exercise.
assistant: That's good advice! How old is Luna now?
user: She's about 3 years old. We adopted her from the Portland Animal Shelter.`

	entities := Extract(text)
	found := make(map[string]bool)
	for _, e := range entities {
		found[e.Text] = true
		t.Logf("Entity: %q (%s)", e.Text, e.Kind)
	}

	// Should find named entities
	for _, want := range []string{"luna", "dr smith", "portland animal shelter"} {
		if !found[want] {
			t.Errorf("expected entity %q not found", want)
		}
	}
}

func TestEntityOverlap(t *testing.T) {
	a := []Entity{{Text: "luna"}, {Text: "dr. smith"}, {Text: "portland"}}
	b := []Entity{{Text: "luna"}, {Text: "central park"}, {Text: "portland"}}

	shared, score := EntityOverlap(a, b)
	t.Logf("Shared: %v, Score: %.3f", shared, score)

	if len(shared) != 2 {
		t.Errorf("expected 2 shared entities, got %d", len(shared))
	}
	if score < 0.3 || score > 0.6 {
		t.Errorf("expected Jaccard ~0.5, got %.3f", score)
	}
}

func TestExtractTopics(t *testing.T) {
	corpus := []string{
		"We went hiking in the mountains yesterday and saw beautiful wildflowers along the trail",
		"I'm cooking pasta for dinner tonight with homemade tomato sauce and fresh basil",
		"The hiking trip was amazing, we reached the summit and the views were incredible",
	}

	topics := ExtractTopics(corpus[0], corpus, 5)
	for _, tp := range topics {
		t.Logf("Topic: %q (score=%.4f)", tp.Term, tp.Score)
	}

	// "hiking" should appear but with lower IDF (appears in 2/3 docs)
	// "wildflowers" or "mountains" should have higher scores (appear in 1/3 docs)
	if len(topics) == 0 {
		t.Error("expected at least 1 topic")
	}
}

func TestTopicOverlap(t *testing.T) {
	a := []Topic{{Term: "hiking", Score: 0.5}, {Term: "mountains", Score: 0.3}}
	b := []Topic{{Term: "hiking", Score: 0.4}, {Term: "cooking", Score: 0.6}}

	shared, score := TopicOverlap(a, b)
	t.Logf("Shared: %v, Score: %.3f", shared, score)

	if len(shared) != 1 {
		t.Errorf("expected 1 shared topic, got %d", len(shared))
	}
}

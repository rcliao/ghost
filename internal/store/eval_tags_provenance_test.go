package store

import (
	"context"
	"testing"
	"time"
)

// Two owner-accepted fixes relayed from the shell session, 2026-09-26.
//
// 1. Context treated ContextParams.Tags as a hard AND filter, so a relevant
//    memory that lacked one of the tags never surfaced (agent proposal
//    pikamini #3, written from real recall misses). Tags now bias scoring;
//    the hard filter stays available as TagMode "filter".
// 2. Consolidate wrote the summary with an empty source_kind even when every
//    source carried provenance; 31 of one agent's 76 unset-source memories in
//    a week were these summaries, which muddies the stated/self/observed split
//    the autonomy check reads.

func seedTagCorpus(t *testing.T, s *SQLiteStore) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return base })
	put := func(key, content string, tags []string) {
		if _, err := s.Put(ctx, PutParams{NS: "agent:tags", Key: key, Content: content, Tags: tags, Tier: "ltm", Importance: 0.6}); err != nil {
			t.Fatal(err)
		}
	}
	// Relevant to the query, carries the chat tag.
	put("tagged-relevant", "Rina's kefir substitute is the Floravita capsule after lunch.", []string{"chat:100"})
	// Equally relevant, NO chat tag — the case the hard filter silently dropped.
	put("untagged-relevant", "Rina takes a Floravita probiotic capsule after lunch; kefir is not part of her routine.", nil)
	// Irrelevant, carries the tag.
	put("tagged-irrelevant", "The patio lights are on a dusk timer.", []string{"chat:100"})
	s.SetClock(func() time.Time { return base.AddDate(0, 0, 1) })
}

func ctxKeys(res *ContextResult) []string {
	out := make([]string, 0, len(res.Memories))
	for _, m := range res.Memories {
		out = append(out, m.Key)
	}
	return out
}

func ctxRank(res *ContextResult, key string) int {
	for i, m := range res.Memories {
		if m.Key == key {
			return i + 1
		}
	}
	return 0
}

func TestEvalTagsBoostNotFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedTagCorpus(t, s)
	q := "what does Rina take instead of kefir"

	// Default: tags bias, nothing is excluded.
	res, err := s.Context(ctx, ContextParams{NS: "agent:tags", Query: q, Budget: 2000, Tags: []string{"chat:100"}})
	if err != nil {
		t.Fatal(err)
	}
	if ctxRank(res, "untagged-relevant") == 0 {
		t.Errorf("default tag mode dropped a relevant memory for lacking the tag: %v", ctxKeys(res))
	}
	rt, ru := ctxRank(res, "tagged-relevant"), ctxRank(res, "untagged-relevant")
	if rt == 0 || (ru != 0 && rt > ru) {
		t.Errorf("the tagged relevant memory should rank at or above the untagged one: tagged=%d untagged=%d %v", rt, ru, ctxKeys(res))
	}

	// Opt-in hard filter keeps the old behaviour.
	res, err = s.Context(ctx, ContextParams{NS: "agent:tags", Query: q, Budget: 2000, Tags: []string{"chat:100"}, TagMode: TagModeFilter})
	if err != nil {
		t.Fatal(err)
	}
	if ctxRank(res, "untagged-relevant") != 0 {
		t.Errorf("TagMode filter must exclude untagged memories: %v", ctxKeys(res))
	}
	if ctxRank(res, "tagged-relevant") == 0 {
		t.Errorf("TagMode filter must keep tagged relevant memories: %v", ctxKeys(res))
	}

	// No tags: unchanged ordering baseline (both relevant memories present).
	res, err = s.Context(ctx, ContextParams{NS: "agent:tags", Query: q, Budget: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if ctxRank(res, "untagged-relevant") == 0 || ctxRank(res, "tagged-relevant") == 0 {
		t.Errorf("without tags both relevant memories must be present: %v", ctxKeys(res))
	}
}

func TestEvalConsolidateCarriesProvenance(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	put := func(key, content, user, kind, scope string) {
		if _, err := s.Put(ctx, PutParams{NS: "agent:prov", Key: key, Content: content, SourceUser: user, SourceKind: "stated", SourceScope: scope}); err != nil {
			t.Fatal(err)
		}
		_ = kind
	}
	put("a", "mami said she stopped kefir in June", "mami", "stated", "chat:100")
	put("b", "mami said the capsule replaces it", "mami", "stated", "chat:100")
	put("c", "papi asked about the dandruff shampoo", "papi", "stated", "chat:100")

	// One dominant speaker: the summary is an OBSERVATION about that person.
	res, err := s.Consolidate(ctx, ConsolidateParams{NS: "agent:prov", SummaryKey: "sum-mami", Content: "mami replaced kefir with a capsule", SourceKeys: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.SourceKind != "observed" || res.Summary.SourceUser != "mami" || res.Summary.SourceScope != "chat:100" {
		t.Errorf("single-speaker summary should be observed/mami/chat:100, got %q/%q/%q", res.Summary.SourceKind, res.Summary.SourceUser, res.Summary.SourceScope)
	}

	// Mixed speakers: the summary is the agent's own note.
	res, err = s.Consolidate(ctx, ConsolidateParams{NS: "agent:prov", SummaryKey: "sum-mixed", Content: "household health notes", SourceKeys: []string{"a", "c"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.SourceKind != "self" || res.Summary.SourceUser != "" {
		t.Errorf("mixed-speaker summary should be self with no user, got %q/%q", res.Summary.SourceKind, res.Summary.SourceUser)
	}

	// Explicit provenance wins over derivation.
	res, err = s.Consolidate(ctx, ConsolidateParams{NS: "agent:prov", SummaryKey: "sum-explicit", Content: "exchange summary", SourceKeys: []string{"a", "b"}, SourceKind: "self"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.SourceKind != "self" || res.Summary.SourceUser != "" {
		t.Errorf("explicit SourceKind must win: got %q/%q", res.Summary.SourceKind, res.Summary.SourceUser)
	}

	// Sources without provenance: nothing to derive, so the summary is self —
	// never an empty source_kind.
	if _, err := s.Put(ctx, PutParams{NS: "agent:prov", Key: "x", Content: "no provenance here"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, PutParams{NS: "agent:prov", Key: "y", Content: "none here either"}); err != nil {
		t.Fatal(err)
	}
	res, err = s.Consolidate(ctx, ConsolidateParams{NS: "agent:prov", SummaryKey: "sum-none", Content: "summary of unattributed notes", SourceKeys: []string{"x", "y"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.SourceKind != "self" {
		t.Errorf("summary of unattributed sources must be self, got %q", res.Summary.SourceKind)
	}
}

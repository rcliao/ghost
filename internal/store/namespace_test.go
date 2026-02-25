package store

import (
	"context"
	"testing"
)

func TestValidateNS(t *testing.T) {
	tests := []struct {
		ns      string
		wantErr bool
	}{
		// Valid namespaces
		{"proj", false},
		{"my-project", false},
		{"reflect:agent-memory", false},
		{"a:b:c", false},
		{"ns1", false},
		{"project_name", false},
		{"A:B", false},
		{"reflect:agent-memory:sub", false},
		{"x", false},
		{"a1-b2_c3", false},

		// Invalid namespaces
		{"", true},                                                              // empty
		{":leading", true},                                                      // leading colon
		{"trailing:", true},                                                     // trailing colon
		{"double::colon", true},                                                 // consecutive colons
		{"-starts-with-dash", true},                                             // segment starts with dash
		{"_starts-with-underscore", true},                                       // segment starts with underscore
		{"has space", true},                                                     // space
		{"has/slash", true},                                                     // slash
		{"has.dot", true},                                                       // dot
		{"a:", true},                                                            // trailing colon
		{":a", true},                                                            // leading colon
		{"a::b", true},                                                          // double colon
		{string(make([]byte, 129)), true},                                       // too long
		{"ok:" + string(make([]byte, 130)), true},                               // total too long
	}

	for _, tc := range tests {
		err := ValidateNS(tc.ns)
		if tc.wantErr && err == nil {
			t.Errorf("ValidateNS(%q): expected error, got nil", tc.ns)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("ValidateNS(%q): unexpected error: %v", tc.ns, err)
		}
	}
}

func TestParseNSFilter(t *testing.T) {
	tests := []struct {
		input    string
		pattern  string
		isPrefix bool
	}{
		{"", "", false},
		{"exact", "exact", false},
		{"reflect:*", "reflect:", true},
		{"reflect:agent:*", "reflect:agent:", true},
		{"all*", "all", true},
	}

	for _, tc := range tests {
		f := ParseNSFilter(tc.input)
		if f.Pattern != tc.pattern {
			t.Errorf("ParseNSFilter(%q).Pattern = %q, want %q", tc.input, f.Pattern, tc.pattern)
		}
		if f.IsPrefix != tc.isPrefix {
			t.Errorf("ParseNSFilter(%q).IsPrefix = %v, want %v", tc.input, f.IsPrefix, tc.isPrefix)
		}
	}
}

func TestNSFilterMatchNS(t *testing.T) {
	tests := []struct {
		filter string
		ns     string
		want   bool
	}{
		// Empty filter matches everything
		{"", "anything", true},
		{"", "", true},

		// Exact match
		{"proj", "proj", true},
		{"proj", "proj:sub", false},
		{"proj", "other", false},

		// Prefix match
		{"reflect:*", "reflect:agent-memory", true},
		{"reflect:*", "reflect:foo", true},
		{"reflect:*", "reflect:a:b:c", true},
		{"reflect:*", "reflect", false},   // "reflect:" doesn't match "reflect"
		{"reflect:*", "reflector", false}, // not a segment boundary match
	}

	for _, tc := range tests {
		f := ParseNSFilter(tc.filter)
		got := f.MatchNS(tc.ns)
		if got != tc.want {
			t.Errorf("ParseNSFilter(%q).MatchNS(%q) = %v, want %v", tc.filter, tc.ns, got, tc.want)
		}
	}
}

func TestNSFilterSQL(t *testing.T) {
	tests := []struct {
		filter    string
		wantClause string
		wantArgs   int
	}{
		{"", "", 0},
		{"exact", "m.ns = ?", 1},
		{"reflect:*", "m.ns LIKE ?", 1},
	}

	for _, tc := range tests {
		f := ParseNSFilter(tc.filter)
		clause, args := f.SQL("m.ns")
		if clause != tc.wantClause {
			t.Errorf("ParseNSFilter(%q).SQL() clause = %q, want %q", tc.filter, clause, tc.wantClause)
		}
		if len(args) != tc.wantArgs {
			t.Errorf("ParseNSFilter(%q).SQL() args count = %d, want %d", tc.filter, len(args), tc.wantArgs)
		}
	}
}

func TestNSSegments(t *testing.T) {
	tests := []struct {
		ns   string
		want []string
	}{
		{"proj", []string{"proj"}},
		{"reflect:agent-memory", []string{"reflect", "agent-memory"}},
		{"a:b:c", []string{"a", "b", "c"}},
	}

	for _, tc := range tests {
		got := NSSegments(tc.ns)
		if len(got) != len(tc.want) {
			t.Errorf("NSSegments(%q) = %v, want %v", tc.ns, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("NSSegments(%q)[%d] = %q, want %q", tc.ns, i, got[i], tc.want[i])
			}
		}
	}
}

func TestNSParent(t *testing.T) {
	tests := []struct {
		ns   string
		want string
	}{
		{"proj", ""},
		{"reflect:agent-memory", "reflect"},
		{"a:b:c", "a:b"},
	}

	for _, tc := range tests {
		got := NSParent(tc.ns)
		if got != tc.want {
			t.Errorf("NSParent(%q) = %q, want %q", tc.ns, got, tc.want)
		}
	}
}

func TestNSDepth(t *testing.T) {
	tests := []struct {
		ns   string
		want int
	}{
		{"proj", 1},
		{"reflect:agent-memory", 2},
		{"a:b:c", 3},
	}

	for _, tc := range tests {
		got := NSDepth(tc.ns)
		if got != tc.want {
			t.Errorf("NSDepth(%q) = %d, want %d", tc.ns, got, tc.want)
		}
	}
}

func TestMockStorePrefixList(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "reflect:agent-memory", Key: "k1", Content: "c1"})
	s.Put(ctx, PutParams{NS: "reflect:other", Key: "k2", Content: "c2"})
	s.Put(ctx, PutParams{NS: "project", Key: "k3", Content: "c3"})

	// Prefix match: should return 2 memories under "reflect:"
	results, err := s.List(ctx, ListParams{NS: "reflect:*"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("prefix list: want 2, got %d", len(results))
	}

	// Exact match: should return 1
	results, err = s.List(ctx, ListParams{NS: "project"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("exact list: want 1, got %d", len(results))
	}

	// No filter: should return 3
	results, err = s.List(ctx, ListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("no filter list: want 3, got %d", len(results))
	}
}

func TestMockStorePrefixSearch(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "reflect:agent-memory", Key: "k1", Content: "hello world"})
	s.Put(ctx, PutParams{NS: "reflect:other", Key: "k2", Content: "hello there"})
	s.Put(ctx, PutParams{NS: "project", Key: "k3", Content: "hello project"})

	// Prefix search for "hello" within reflect:*
	results, err := s.Search(ctx, SearchParams{NS: "reflect:*", Query: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("prefix search: want 2, got %d", len(results))
	}
}

func TestMockStoreRmNamespace(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "reflect:agent-memory", Key: "k1", Content: "c1"})
	s.Put(ctx, PutParams{NS: "reflect:other", Key: "k2", Content: "c2"})
	s.Put(ctx, PutParams{NS: "project", Key: "k3", Content: "c3"})

	// Soft delete by prefix
	count, err := s.RmNamespace(ctx, "reflect:*", false)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("RmNamespace prefix: want 2 deleted, got %d", count)
	}

	// Verify reflect:* memories are gone
	results, _ := s.List(ctx, ListParams{NS: "reflect:*"})
	if len(results) != 0 {
		t.Fatalf("expected 0 reflect:* after rm, got %d", len(results))
	}

	// Verify project is still there
	results, _ = s.List(ctx, ListParams{NS: "project"})
	if len(results) != 1 {
		t.Fatalf("expected project to survive, got %d", len(results))
	}
}

func TestMockStoreRmNamespaceExact(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "ns1", Key: "a", Content: "a"})
	s.Put(ctx, PutParams{NS: "ns1", Key: "b", Content: "b"})
	s.Put(ctx, PutParams{NS: "ns2", Key: "c", Content: "c"})

	count, err := s.RmNamespace(ctx, "ns1", false)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("RmNamespace exact: want 2 deleted, got %d", count)
	}

	remaining, _ := s.MemoryCount(ctx)
	if remaining != 1 {
		t.Fatalf("expected 1 remaining, got %d", remaining)
	}
}

func TestMockStoreRmNamespaceHard(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "ns1", Key: "a", Content: "a"})
	s.Put(ctx, PutParams{NS: "ns1", Key: "b", Content: "b"})

	count, err := s.RmNamespace(ctx, "ns1", true)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("RmNamespace hard: want 2, got %d", count)
	}
}

func TestMockStorePutValidatesNS(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	// Invalid namespace should fail
	_, err := s.Put(ctx, PutParams{NS: "bad ns", Key: "k", Content: "c"})
	if err == nil {
		t.Fatal("expected error for invalid namespace")
	}

	_, err = s.Put(ctx, PutParams{NS: ":leading", Key: "k", Content: "c"})
	if err == nil {
		t.Fatal("expected error for leading colon")
	}

	// Valid namespace should succeed
	_, err = s.Put(ctx, PutParams{NS: "valid-ns", Key: "k", Content: "c"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMockStorePrefixExport(t *testing.T) {
	s := NewMockStore()
	ctx := context.Background()

	s.Put(ctx, PutParams{NS: "reflect:a", Key: "k1", Content: "c1"})
	s.Put(ctx, PutParams{NS: "reflect:b", Key: "k2", Content: "c2"})
	s.Put(ctx, PutParams{NS: "project", Key: "k3", Content: "c3"})

	// Prefix export
	results, err := s.ExportAll(ctx, "reflect:*")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("prefix export: want 2, got %d", len(results))
	}
}

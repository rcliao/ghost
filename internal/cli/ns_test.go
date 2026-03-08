package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rcliao/ghost/internal/store"
)

func seedNSMock(t *testing.T) *store.MockStore {
	t.Helper()
	mock := store.NewMockStore()

	orig := OpenStoreFunc
	OpenStoreFunc = func() (store.Store, error) { return mock, nil }
	t.Cleanup(func() { OpenStoreFunc = orig })

	ctx := context.Background()
	mock.Put(ctx, store.PutParams{NS: "reflect:agent-memory", Key: "task1", Content: "did stuff"})
	mock.Put(ctx, store.PutParams{NS: "reflect:other", Key: "task2", Content: "did other stuff"})
	mock.Put(ctx, store.PutParams{NS: "project", Key: "readme", Content: "how to build"})
	mock.Put(ctx, store.PutParams{NS: "project:sub", Key: "detail", Content: "sub detail"})

	return mock
}

func TestNSList(t *testing.T) {
	seedNSMock(t)

	out, err := executeCmd(t, "ns", "list")
	if err != nil {
		t.Fatalf("ns list: %v", err)
	}

	var namespaces []store.NamespaceStats
	if err := json.Unmarshal([]byte(out), &namespaces); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	if len(namespaces) != 4 {
		t.Fatalf("expected 4 namespaces, got %d", len(namespaces))
	}
}

func TestNSListText(t *testing.T) {
	seedNSMock(t)

	out, err := executeCmd(t, "ns", "list", "-f", "text")
	if err != nil {
		t.Fatalf("ns list text: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines, got %d: %v", len(lines), lines)
	}
	for _, line := range lines {
		if !strings.Contains(line, "memories") {
			t.Errorf("expected 'memories' in text output, got %q", line)
		}
	}
}

func TestNSTree(t *testing.T) {
	seedNSMock(t)

	out, err := executeCmd(t, "ns", "tree", "-f", "text")
	if err != nil {
		t.Fatalf("ns tree: %v", err)
	}

	// Should contain tree characters and namespace segments
	if !strings.Contains(out, "reflect") {
		t.Errorf("expected 'reflect' in tree, got:\n%s", out)
	}
	if !strings.Contains(out, "project") {
		t.Errorf("expected 'project' in tree, got:\n%s", out)
	}
	// Should contain tree connectors
	if !strings.Contains(out, "├") && !strings.Contains(out, "└") {
		t.Errorf("expected tree connectors in output, got:\n%s", out)
	}
}

func TestNSTreeJSON(t *testing.T) {
	seedNSMock(t)

	out, err := executeCmd(t, "ns", "tree")
	if err != nil {
		t.Fatalf("ns tree json: %v", err)
	}

	var entries []struct {
		NS    string `json:"ns"`
		Depth int    `json:"depth"`
		Count int    `json:"count"`
		Keys  int    `json:"keys"`
	}
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(entries))
	}

	// Check depths
	depthMap := map[string]int{}
	for _, e := range entries {
		depthMap[e.NS] = e.Depth
	}
	if depthMap["project"] != 1 {
		t.Errorf("project depth: want 1, got %d", depthMap["project"])
	}
	if depthMap["reflect:agent-memory"] != 2 {
		t.Errorf("reflect:agent-memory depth: want 2, got %d", depthMap["reflect:agent-memory"])
	}
}

func TestNSRm(t *testing.T) {
	seedNSMock(t)

	out, err := executeCmd(t, "ns", "rm", "project")
	if err != nil {
		t.Fatalf("ns rm: %v", err)
	}

	var result struct {
		OK      bool   `json:"ok"`
		NS      string `json:"ns"`
		Action  string `json:"action"`
		Deleted int64  `json:"deleted"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	if !result.OK {
		t.Error("expected ok=true")
	}
	if result.Deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", result.Deleted)
	}
	if result.Action != "soft-deleted" {
		t.Errorf("expected action=soft-deleted, got %q", result.Action)
	}
}

func TestNSRmPrefix(t *testing.T) {
	seedNSMock(t)

	out, err := executeCmd(t, "ns", "rm", "reflect:*")
	if err != nil {
		t.Fatalf("ns rm prefix: %v", err)
	}

	var result struct {
		Deleted int64 `json:"deleted"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	if result.Deleted != 2 {
		t.Errorf("expected 2 deleted (both reflect: namespaces), got %d", result.Deleted)
	}
}

func TestListPrefixFilter(t *testing.T) {
	seedNSMock(t)

	out, err := executeCmd(t, "list", "-n", "reflect:*")
	if err != nil {
		t.Fatalf("list prefix: %v", err)
	}

	var memories []struct {
		NS string `json:"ns"`
	}
	if err := json.Unmarshal([]byte(out), &memories); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	if len(memories) != 2 {
		t.Fatalf("expected 2 memories under reflect:*, got %d", len(memories))
	}
	for _, m := range memories {
		if !strings.HasPrefix(m.NS, "reflect:") {
			t.Errorf("expected ns starting with 'reflect:', got %q", m.NS)
		}
	}
}

func TestListPrefixProjectStar(t *testing.T) {
	seedNSMock(t)

	out, err := executeCmd(t, "list", "-n", "project*")
	if err != nil {
		t.Fatalf("list project*: %v", err)
	}

	var memories []struct {
		NS string `json:"ns"`
	}
	if err := json.Unmarshal([]byte(out), &memories); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	// Should match both "project" and "project:sub"
	if len(memories) != 2 {
		t.Fatalf("expected 2 memories under project*, got %d", len(memories))
	}
}

package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rcliao/ghost/internal/model"
	"github.com/rcliao/ghost/internal/store"
)

// seedMock creates a MockStore, injects it via OpenStoreFunc, and seeds it with test data.
// Returns the mock and a cleanup function that restores the original OpenStoreFunc.
func seedMock(t *testing.T) *store.MockStore {
	t.Helper()
	mock := store.NewMockStore()

	orig := OpenStoreFunc
	OpenStoreFunc = func() (store.Store, error) { return mock, nil }
	t.Cleanup(func() { OpenStoreFunc = orig })

	ctx := context.Background()
	mock.Put(ctx, store.PutParams{NS: "proj", Key: "readme", Content: "how to build", Kind: "semantic", Tags: []string{"docs"}})
	mock.Put(ctx, store.PutParams{NS: "proj", Key: "changelog", Content: "v1.0 release", Kind: "semantic", Tags: []string{"docs", "release"}})
	mock.Put(ctx, store.PutParams{NS: "notes", Key: "todo", Content: "buy milk", Kind: "procedural"})

	return mock
}

func TestListMock_DefaultJSON(t *testing.T) {
	seedMock(t)

	out, err := executeCmd(t, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	var memories []model.Memory
	if err := json.Unmarshal([]byte(out), &memories); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	if len(memories) != 3 {
		t.Fatalf("count: want 3, got %d", len(memories))
	}
}

func TestListMock_FilterByNamespace(t *testing.T) {
	seedMock(t)

	out, err := executeCmd(t, "list", "-n", "proj")
	if err != nil {
		t.Fatalf("list -n proj: %v", err)
	}

	var memories []model.Memory
	if err := json.Unmarshal([]byte(out), &memories); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	if len(memories) != 2 {
		t.Fatalf("count: want 2, got %d", len(memories))
	}
	for _, m := range memories {
		if m.NS != "proj" {
			t.Errorf("expected ns=proj, got %q", m.NS)
		}
	}
}

func TestListMock_FilterByKind(t *testing.T) {
	seedMock(t)

	out, err := executeCmd(t, "list", "--kind", "procedural")
	if err != nil {
		t.Fatalf("list --kind procedural: %v", err)
	}

	var memories []model.Memory
	if err := json.Unmarshal([]byte(out), &memories); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	if len(memories) != 1 {
		t.Fatalf("count: want 1, got %d", len(memories))
	}
	if memories[0].Key != "todo" {
		t.Errorf("key: want %q, got %q", "todo", memories[0].Key)
	}
}

func TestListMock_FilterByTags(t *testing.T) {
	seedMock(t)

	out, err := executeCmd(t, "list", "-t", "release")
	if err != nil {
		t.Fatalf("list -t release: %v", err)
	}

	var memories []model.Memory
	if err := json.Unmarshal([]byte(out), &memories); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	if len(memories) != 1 {
		t.Fatalf("count: want 1, got %d", len(memories))
	}
	if memories[0].Key != "changelog" {
		t.Errorf("key: want %q, got %q", "changelog", memories[0].Key)
	}
}

func TestListMock_KeysOnly(t *testing.T) {
	seedMock(t)

	out, err := executeCmd(t, "list", "--keys-only")
	if err != nil {
		t.Fatalf("list --keys-only: %v", err)
	}

	var keys []struct {
		NS  string `json:"ns"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal([]byte(out), &keys); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	if len(keys) != 3 {
		t.Fatalf("count: want 3, got %d", len(keys))
	}
	// Each entry should have ns and key, no content field
	for _, k := range keys {
		if k.NS == "" || k.Key == "" {
			t.Errorf("expected non-empty ns/key, got ns=%q key=%q", k.NS, k.Key)
		}
	}
}

func TestListMock_KeysOnlyText(t *testing.T) {
	seedMock(t)

	out, err := executeCmd(t, "list", "--keys-only", "-f", "text")
	if err != nil {
		t.Fatalf("list --keys-only -f text: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines: want 3, got %d (%v)", len(lines), lines)
	}
	for _, line := range lines {
		if !strings.Contains(line, "/") {
			t.Errorf("expected ns/key format, got %q", line)
		}
	}
}

func TestListMock_TextFormat(t *testing.T) {
	seedMock(t)

	out, err := executeCmd(t, "list", "-n", "notes", "-f", "text")
	if err != nil {
		t.Fatalf("list -f text: %v", err)
	}

	if strings.TrimSpace(out) != "buy milk" {
		t.Errorf("text output: want %q, got %q", "buy milk", strings.TrimSpace(out))
	}
}

func TestListMock_Limit(t *testing.T) {
	seedMock(t)

	out, err := executeCmd(t, "list", "-l", "1")
	if err != nil {
		t.Fatalf("list -l 1: %v", err)
	}

	var memories []model.Memory
	if err := json.Unmarshal([]byte(out), &memories); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	if len(memories) != 1 {
		t.Fatalf("count: want 1, got %d", len(memories))
	}
}

func TestListMock_Compact(t *testing.T) {
	seedMock(t)

	out, err := executeCmd(t, "list", "--compact", "-n", "proj")
	if err != nil {
		t.Fatalf("list --compact: %v", err)
	}

	// Compact outputs one JSON object per line (JSONL)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("JSONL lines: want 2, got %d", len(lines))
	}
	for _, line := range lines {
		var m model.Memory
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("unmarshal JSONL line: %v\nline: %s", err, line)
		}
		if m.NS != "proj" {
			t.Errorf("ns: want %q, got %q", "proj", m.NS)
		}
	}
}

func TestListMock_EmptyResult(t *testing.T) {
	mock := store.NewMockStore()
	orig := OpenStoreFunc
	OpenStoreFunc = func() (store.Store, error) { return mock, nil }
	t.Cleanup(func() { OpenStoreFunc = orig })

	out, err := executeCmd(t, "list")
	if err != nil {
		t.Fatalf("list empty: %v", err)
	}

	var memories []model.Memory
	if err := json.Unmarshal([]byte(out), &memories); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	if len(memories) != 0 {
		t.Fatalf("count: want 0, got %d", len(memories))
	}
}

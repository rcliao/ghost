package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rcliao/ghost/internal/store"
)

func seedTagsMock(t *testing.T) *store.MockStore {
	t.Helper()
	mock := store.NewMockStore()

	orig := OpenStoreFunc
	OpenStoreFunc = func() (store.Store, error) { return mock, nil }
	t.Cleanup(func() { OpenStoreFunc = orig })

	ctx := context.Background()
	mock.Put(ctx, store.PutParams{NS: "proj", Key: "a", Content: "alpha", Tags: []string{"deploy", "infra"}})
	mock.Put(ctx, store.PutParams{NS: "proj", Key: "b", Content: "beta", Tags: []string{"deploy", "docs"}})
	mock.Put(ctx, store.PutParams{NS: "notes", Key: "c", Content: "gamma", Tags: []string{"personal"}})
	mock.Put(ctx, store.PutParams{NS: "notes", Key: "d", Content: "delta"}) // no tags

	return mock
}

func TestTagsList(t *testing.T) {
	seedTagsMock(t)

	out, err := executeCmd(t, "tags", "list")
	if err != nil {
		t.Fatalf("tags list: %v", err)
	}

	var tags []store.TagInfo
	if err := json.Unmarshal([]byte(out), &tags); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	tagMap := map[string]int{}
	for _, ti := range tags {
		tagMap[ti.Tag] = ti.Count
	}

	if tagMap["deploy"] != 2 {
		t.Errorf("expected deploy=2, got %d", tagMap["deploy"])
	}
	if tagMap["infra"] != 1 {
		t.Errorf("expected infra=1, got %d", tagMap["infra"])
	}
	if tagMap["personal"] != 1 {
		t.Errorf("expected personal=1, got %d", tagMap["personal"])
	}
}

func TestTagsListByNamespace(t *testing.T) {
	seedTagsMock(t)

	out, err := executeCmd(t, "tags", "list", "-n", "proj")
	if err != nil {
		t.Fatalf("tags list -n proj: %v", err)
	}

	var tags []store.TagInfo
	if err := json.Unmarshal([]byte(out), &tags); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	for _, ti := range tags {
		if ti.Tag == "personal" {
			t.Error("expected personal tag to NOT appear in proj namespace")
		}
	}
}

func TestTagsListText(t *testing.T) {
	seedTagsMock(t)

	out, err := executeCmd(t, "tags", "list", "-f", "text")
	if err != nil {
		t.Fatalf("tags list text: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines (4 unique tags), got %d: %v", len(lines), lines)
	}
	// First line should be deploy (count=2, sorted by count desc)
	if !strings.Contains(lines[0], "deploy") {
		t.Errorf("expected first line to contain 'deploy' (highest count), got %q", lines[0])
	}
	for _, line := range lines {
		if !strings.Contains(line, "memories") {
			t.Errorf("expected 'memories' in text output, got %q", line)
		}
	}
}

func TestTagsListEmpty(t *testing.T) {
	mock := store.NewMockStore()
	orig := OpenStoreFunc
	OpenStoreFunc = func() (store.Store, error) { return mock, nil }
	t.Cleanup(func() { OpenStoreFunc = orig })

	out, err := executeCmd(t, "tags", "list")
	if err != nil {
		t.Fatalf("tags list empty: %v", err)
	}

	var tags []store.TagInfo
	if err := json.Unmarshal([]byte(out), &tags); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}
	if len(tags) != 0 {
		t.Fatalf("expected 0 tags, got %d", len(tags))
	}
}

func TestTagsListEmptyText(t *testing.T) {
	mock := store.NewMockStore()
	orig := OpenStoreFunc
	OpenStoreFunc = func() (store.Store, error) { return mock, nil }
	t.Cleanup(func() { OpenStoreFunc = orig })

	out, err := executeCmd(t, "tags", "list", "-f", "text")
	if err != nil {
		t.Fatalf("tags list empty text: %v", err)
	}

	if !strings.Contains(out, "(no tags)") {
		t.Errorf("expected '(no tags)' message, got %q", out)
	}
}

func TestTagsRename(t *testing.T) {
	seedTagsMock(t)

	out, err := executeCmd(t, "tags", "rename", "deploy", "release")
	if err != nil {
		t.Fatalf("tags rename: %v", err)
	}

	var result struct {
		OK       bool   `json:"ok"`
		OldTag   string `json:"old_tag"`
		NewTag   string `json:"new_tag"`
		Affected int    `json:"affected"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	if !result.OK {
		t.Error("expected ok=true")
	}
	if result.Affected != 2 {
		t.Errorf("expected 2 affected, got %d", result.Affected)
	}
	if result.OldTag != "deploy" || result.NewTag != "release" {
		t.Errorf("unexpected tags: old=%q new=%q", result.OldTag, result.NewTag)
	}
}

func TestTagsRenameWithNamespace(t *testing.T) {
	seedTagsMock(t)

	out, err := executeCmd(t, "tags", "rename", "deploy", "release", "-n", "proj")
	if err != nil {
		t.Fatalf("tags rename -n proj: %v", err)
	}

	var result struct {
		Affected int `json:"affected"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	if result.Affected != 2 {
		t.Errorf("expected 2 affected in proj, got %d", result.Affected)
	}
}

func TestTagsRm(t *testing.T) {
	seedTagsMock(t)

	out, err := executeCmd(t, "tags", "rm", "deploy")
	if err != nil {
		t.Fatalf("tags rm: %v", err)
	}

	var result struct {
		OK       bool   `json:"ok"`
		Tag      string `json:"tag"`
		Affected int    `json:"affected"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	if !result.OK {
		t.Error("expected ok=true")
	}
	if result.Affected != 2 {
		t.Errorf("expected 2 affected, got %d", result.Affected)
	}
	if result.Tag != "deploy" {
		t.Errorf("expected tag=deploy, got %q", result.Tag)
	}
}

func TestTagsRmWithNamespace(t *testing.T) {
	seedTagsMock(t)

	out, err := executeCmd(t, "tags", "rm", "deploy", "-n", "proj")
	if err != nil {
		t.Fatalf("tags rm -n proj: %v", err)
	}

	var result struct {
		Affected int `json:"affected"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	if result.Affected != 2 {
		t.Errorf("expected 2 affected in proj, got %d", result.Affected)
	}
}

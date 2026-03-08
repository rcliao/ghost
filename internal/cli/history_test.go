package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rcliao/ghost/internal/model"
	"github.com/rcliao/ghost/internal/store"
)

// seedHistoryMock creates a MockStore with multiple versions of a key, including a deleted version.
func seedHistoryMock(t *testing.T) *store.MockStore {
	t.Helper()
	mock := store.NewMockStore()

	orig := OpenStoreFunc
	OpenStoreFunc = func() (store.Store, error) { return mock, nil }
	t.Cleanup(func() { OpenStoreFunc = orig })

	ctx := context.Background()
	mock.Put(ctx, store.PutParams{NS: "proj", Key: "config", Content: "v1 initial", Kind: "semantic", Tags: []string{"setup"}})
	mock.Put(ctx, store.PutParams{NS: "proj", Key: "config", Content: "v2 updated", Kind: "semantic", Tags: []string{"setup", "update"}})
	mock.Put(ctx, store.PutParams{NS: "proj", Key: "config", Content: "v3 final", Kind: "semantic", Priority: "high"})
	// Also add a different key to ensure filtering works
	mock.Put(ctx, store.PutParams{NS: "proj", Key: "readme", Content: "docs"})

	return mock
}

func TestHistory_JSON(t *testing.T) {
	seedHistoryMock(t)

	out, err := executeCmd(t, "history", "-n", "proj", "-k", "config")
	if err != nil {
		t.Fatalf("history: %v", err)
	}

	var memories []model.Memory
	if err := json.Unmarshal([]byte(out), &memories); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	if len(memories) != 3 {
		t.Fatalf("count: want 3, got %d", len(memories))
	}

	// Should be ordered by version ascending (chronological)
	for i, m := range memories {
		if m.Version != i+1 {
			t.Errorf("version[%d]: want %d, got %d", i, i+1, m.Version)
		}
		if m.NS != "proj" || m.Key != "config" {
			t.Errorf("unexpected ns/key: %s/%s", m.NS, m.Key)
		}
	}
}

func TestHistory_Text(t *testing.T) {
	seedHistoryMock(t)

	out, err := executeCmd(t, "history", "-n", "proj", "-k", "config", "-f", "text")
	if err != nil {
		t.Fatalf("history -f text: %v", err)
	}

	// Should contain header
	if !strings.Contains(out, "History: proj/config") {
		t.Errorf("missing header in text output")
	}
	if !strings.Contains(out, "3 versions") {
		t.Errorf("missing version count in header")
	}

	// Should contain version markers
	if !strings.Contains(out, "v1") {
		t.Errorf("missing v1")
	}
	if !strings.Contains(out, "v2") {
		t.Errorf("missing v2")
	}
	if !strings.Contains(out, "v3") {
		t.Errorf("missing v3")
	}

	// Should contain content previews
	if !strings.Contains(out, "v1 initial") {
		t.Errorf("missing v1 content")
	}
	if !strings.Contains(out, "v3 final") {
		t.Errorf("missing v3 content")
	}
}

func TestHistory_SingleVersion(t *testing.T) {
	mock := store.NewMockStore()
	orig := OpenStoreFunc
	OpenStoreFunc = func() (store.Store, error) { return mock, nil }
	t.Cleanup(func() { OpenStoreFunc = orig })

	ctx := context.Background()
	mock.Put(ctx, store.PutParams{NS: "test", Key: "single", Content: "only one"})

	out, err := executeCmd(t, "history", "-n", "test", "-k", "single", "-f", "text")
	if err != nil {
		t.Fatalf("history: %v", err)
	}

	// Singular "version" not "versions"
	if !strings.Contains(out, "1 version)") {
		t.Errorf("expected singular 'version', got: %s", out)
	}
}

func TestHistory_IncludesDeleted(t *testing.T) {
	mock := store.NewMockStore()
	orig := OpenStoreFunc
	OpenStoreFunc = func() (store.Store, error) { return mock, nil }
	t.Cleanup(func() { OpenStoreFunc = orig })

	ctx := context.Background()
	mock.Put(ctx, store.PutParams{NS: "test", Key: "k1", Content: "v1"})
	mock.Put(ctx, store.PutParams{NS: "test", Key: "k1", Content: "v2"})

	// Soft-delete latest version
	mock.Rm(ctx, store.RmParams{NS: "test", Key: "k1"})

	out, err := executeCmd(t, "history", "-n", "test", "-k", "k1")
	if err != nil {
		t.Fatalf("history: %v", err)
	}

	var memories []model.Memory
	if err := json.Unmarshal([]byte(out), &memories); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	// History should show both versions, including the deleted one
	if len(memories) != 2 {
		t.Fatalf("count: want 2 (including deleted), got %d", len(memories))
	}

	// The deleted version should have deleted_at set
	hasDeleted := false
	for _, m := range memories {
		if m.DeletedAt != nil {
			hasDeleted = true
		}
	}
	if !hasDeleted {
		t.Error("expected at least one deleted version in history")
	}
}

func TestHistory_DeletedTextFormat(t *testing.T) {
	mock := store.NewMockStore()
	orig := OpenStoreFunc
	OpenStoreFunc = func() (store.Store, error) { return mock, nil }
	t.Cleanup(func() { OpenStoreFunc = orig })

	ctx := context.Background()
	mock.Put(ctx, store.PutParams{NS: "test", Key: "k1", Content: "v1"})
	mock.Put(ctx, store.PutParams{NS: "test", Key: "k1", Content: "v2"})
	mock.Rm(ctx, store.RmParams{NS: "test", Key: "k1"})

	out, err := executeCmd(t, "history", "-n", "test", "-k", "k1", "-f", "text")
	if err != nil {
		t.Fatalf("history -f text: %v", err)
	}

	if !strings.Contains(out, "deleted") {
		t.Errorf("expected 'deleted' status in text output for soft-deleted version")
	}
}

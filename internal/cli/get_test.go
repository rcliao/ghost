package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rcliao/agent-memory/internal/model"
)

func TestGetBasic(t *testing.T) {
	db := tempDB(t)

	// Seed a memory.
	_, err := executeCmd(t, "put", "--db", db, "-n", "ns1", "-k", "key1", "hello world")
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	out, err := executeCmd(t, "get", "--db", db, "-n", "ns1", "-k", "key1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	var mem model.Memory
	if err := json.Unmarshal([]byte(out), &mem); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	if mem.NS != "ns1" {
		t.Errorf("ns: want %q, got %q", "ns1", mem.NS)
	}
	if mem.Key != "key1" {
		t.Errorf("key: want %q, got %q", "key1", mem.Key)
	}
	if mem.Content != "hello world" {
		t.Errorf("content: want %q, got %q", "hello world", mem.Content)
	}
	if mem.Version != 1 {
		t.Errorf("version: want 1, got %d", mem.Version)
	}
}

func TestGetTextFormat(t *testing.T) {
	db := tempDB(t)

	_, err := executeCmd(t, "put", "--db", db, "-n", "ns1", "-k", "key1", "hello world")
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	out, err := executeCmd(t, "get", "--db", db, "-n", "ns1", "-k", "key1", "-f", "text")
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if strings.TrimSpace(out) != "hello world" {
		t.Errorf("text output: want %q, got %q", "hello world", strings.TrimSpace(out))
	}
}

func TestGetHistory(t *testing.T) {
	db := tempDB(t)

	// Create two versions.
	_, err := executeCmd(t, "put", "--db", db, "-n", "ns1", "-k", "key1", "v1")
	if err != nil {
		t.Fatalf("put v1: %v", err)
	}
	_, err = executeCmd(t, "put", "--db", db, "-n", "ns1", "-k", "key1", "v2")
	if err != nil {
		t.Fatalf("put v2: %v", err)
	}

	out, err := executeCmd(t, "get", "--db", db, "-n", "ns1", "-k", "key1", "--history")
	if err != nil {
		t.Fatalf("get --history: %v", err)
	}

	var memories []model.Memory
	if err := json.Unmarshal([]byte(out), &memories); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	if len(memories) != 2 {
		t.Fatalf("history count: want 2, got %d", len(memories))
	}
	// Newest first.
	if memories[0].Content != "v2" {
		t.Errorf("history[0].content: want %q, got %q", "v2", memories[0].Content)
	}
	if memories[1].Content != "v1" {
		t.Errorf("history[1].content: want %q, got %q", "v1", memories[1].Content)
	}
}

func TestGetSpecificVersion(t *testing.T) {
	db := tempDB(t)

	_, err := executeCmd(t, "put", "--db", db, "-n", "ns1", "-k", "key1", "v1")
	if err != nil {
		t.Fatalf("put v1: %v", err)
	}
	_, err = executeCmd(t, "put", "--db", db, "-n", "ns1", "-k", "key1", "v2")
	if err != nil {
		t.Fatalf("put v2: %v", err)
	}

	out, err := executeCmd(t, "get", "--db", db, "-n", "ns1", "-k", "key1", "-v", "1")
	if err != nil {
		t.Fatalf("get -v 1: %v", err)
	}

	var mem model.Memory
	if err := json.Unmarshal([]byte(out), &mem); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	if mem.Version != 1 {
		t.Errorf("version: want 1, got %d", mem.Version)
	}
	if mem.Content != "v1" {
		t.Errorf("content: want %q, got %q", "v1", mem.Content)
	}
}

func TestGetHistoryTextFormat(t *testing.T) {
	db := tempDB(t)

	_, err := executeCmd(t, "put", "--db", db, "-n", "ns1", "-k", "key1", "v1")
	if err != nil {
		t.Fatalf("put v1: %v", err)
	}
	_, err = executeCmd(t, "put", "--db", db, "-n", "ns1", "-k", "key1", "v2")
	if err != nil {
		t.Fatalf("put v2: %v", err)
	}

	out, err := executeCmd(t, "get", "--db", db, "-n", "ns1", "-k", "key1", "--history", "-f", "text")
	if err != nil {
		t.Fatalf("get --history -f text: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("text lines: want 2, got %d (%v)", len(lines), lines)
	}
	if lines[0] != "v2" {
		t.Errorf("line[0]: want %q, got %q", "v2", lines[0])
	}
	if lines[1] != "v1" {
		t.Errorf("line[1]: want %q, got %q", "v1", lines[1])
	}
}

func TestGetLatestVersion(t *testing.T) {
	db := tempDB(t)

	_, err := executeCmd(t, "put", "--db", db, "-n", "ns1", "-k", "key1", "v1")
	if err != nil {
		t.Fatalf("put v1: %v", err)
	}
	_, err = executeCmd(t, "put", "--db", db, "-n", "ns1", "-k", "key1", "v2")
	if err != nil {
		t.Fatalf("put v2: %v", err)
	}

	// Default get (no --history, no -v) should return latest.
	out, err := executeCmd(t, "get", "--db", db, "-n", "ns1", "-k", "key1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	var mem model.Memory
	if err := json.Unmarshal([]byte(out), &mem); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	if mem.Version != 2 {
		t.Errorf("version: want 2, got %d", mem.Version)
	}
	if mem.Content != "v2" {
		t.Errorf("content: want %q, got %q", "v2", mem.Content)
	}
}

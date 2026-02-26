package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/rcliao/agent-memory/internal/model"
)

func TestIngestSingleFile(t *testing.T) {
	db := tempDB(t)
	dir := t.TempDir()
	mdFile := filepath.Join(dir, "MEMORY.md")
	os.WriteFile(mdFile, []byte(`## Facts

User likes Go.

## Preferences

Dark mode preferred.
`), 0644)

	out, err := executeCmd(t, "ingest", "--db", db, "-n", "test:ctx", mdFile)
	if err != nil {
		t.Fatalf("ingest failed: %v\nout: %s", err, out)
	}

	var result struct {
		OK       bool `json:"ok"`
		Ingested int  `json:"ingested"`
		Files    int  `json:"files"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}
	if !result.OK {
		t.Error("expected ok=true")
	}
	if result.Ingested != 2 {
		t.Errorf("ingested: want 2, got %d", result.Ingested)
	}
	if result.Files != 1 {
		t.Errorf("files: want 1, got %d", result.Files)
	}

	// Verify memories were stored.
	listOut, err := executeCmd(t, "list", "--db", db, "-n", "test:ctx", "-l", "50")
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	var memories []model.Memory
	if err := json.Unmarshal([]byte(listOut), &memories); err != nil {
		t.Fatalf("unmarshal list: %v\nraw: %s", err, listOut)
	}
	if len(memories) != 2 {
		t.Errorf("stored memories: want 2, got %d", len(memories))
	}

	// Check keys.
	keys := map[string]bool{}
	for _, m := range memories {
		keys[m.Key] = true
	}
	if !keys["facts"] {
		t.Error("expected key 'facts'")
	}
	if !keys["preferences"] {
		t.Error("expected key 'preferences'")
	}
}

func TestIngestDirectory(t *testing.T) {
	db := tempDB(t)
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "one.md"), []byte(`## Alpha

Content alpha.
`), 0644)
	os.WriteFile(filepath.Join(dir, "two.md"), []byte(`## Beta

Content beta.
`), 0644)
	// Non-md file should be ignored.
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("not markdown"), 0644)

	out, err := executeCmd(t, "ingest", "--db", db, "-n", "test:dir", dir)
	if err != nil {
		t.Fatalf("ingest dir failed: %v\nout: %s", err, out)
	}

	var result struct {
		OK       bool `json:"ok"`
		Ingested int  `json:"ingested"`
		Files    int  `json:"files"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}
	if result.Ingested != 2 {
		t.Errorf("ingested: want 2, got %d", result.Ingested)
	}
	if result.Files != 2 {
		t.Errorf("files: want 2, got %d", result.Files)
	}
}

func TestIngestDryRun(t *testing.T) {
	db := tempDB(t)
	dir := t.TempDir()
	mdFile := filepath.Join(dir, "SOUL.md")
	os.WriteFile(mdFile, []byte(`## Identity

I am helpful.

## Voice

Warm and direct.
`), 0644)

	out, err := executeCmd(t, "ingest", "--db", db, "-n", "test:soul", "--dry-run", mdFile)
	if err != nil {
		t.Fatalf("ingest dry-run failed: %v\nout: %s", err, out)
	}

	var memories []model.Memory
	if err := json.Unmarshal([]byte(out), &memories); err != nil {
		t.Fatalf("unmarshal dry-run: %v\nraw: %s", err, out)
	}
	if len(memories) != 2 {
		t.Fatalf("dry-run memories: want 2, got %d", len(memories))
	}
	if memories[0].NS != "test:soul" {
		t.Errorf("ns: want %q, got %q", "test:soul", memories[0].NS)
	}
	if memories[0].Kind != "semantic" {
		t.Errorf("kind: want %q, got %q", "semantic", memories[0].Kind)
	}

	// Verify nothing was stored.
	listOut, err := executeCmd(t, "list", "--db", db, "-n", "test:soul", "-l", "50")
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	var stored []model.Memory
	if err := json.Unmarshal([]byte(listOut), &stored); err != nil {
		t.Fatalf("unmarshal list: %v\nraw: %s", err, listOut)
	}
	if len(stored) != 0 {
		t.Errorf("dry-run should not store: got %d memories", len(stored))
	}
}

func TestIngestFlags(t *testing.T) {
	db := tempDB(t)
	dir := t.TempDir()
	mdFile := filepath.Join(dir, "test.md")
	os.WriteFile(mdFile, []byte(`## Section

Some content.
`), 0644)

	out, err := executeCmd(t, "ingest", "--db", db,
		"-n", "test:flags",
		"--kind", "procedural",
		"--tags", "personality,core",
		"--priority", "high",
		"--meta", `{"source":"openclaw"}`,
		"--dry-run",
		mdFile,
	)
	if err != nil {
		t.Fatalf("ingest failed: %v\nout: %s", err, out)
	}

	var memories []model.Memory
	if err := json.Unmarshal([]byte(out), &memories); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}
	if len(memories) != 1 {
		t.Fatalf("want 1 memory, got %d", len(memories))
	}

	m := memories[0]
	if m.Kind != "procedural" {
		t.Errorf("kind: want %q, got %q", "procedural", m.Kind)
	}
	if m.Priority != "high" {
		t.Errorf("priority: want %q, got %q", "high", m.Priority)
	}
	if m.Meta != `{"source":"openclaw"}` {
		t.Errorf("meta: want %q, got %q", `{"source":"openclaw"}`, m.Meta)
	}
	if len(m.Tags) != 2 || m.Tags[0] != "personality" || m.Tags[1] != "core" {
		t.Errorf("tags: want [personality core], got %v", m.Tags)
	}
}

func TestIngestDateNS(t *testing.T) {
	db := tempDB(t)
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "2026-02-15.md"), []byte(`## Morning

Did stuff.
`), 0644)
	os.WriteFile(filepath.Join(dir, "2026-02-16.md"), []byte(`## Evening

More stuff.
`), 0644)
	os.WriteFile(filepath.Join(dir, "notes.md"), []byte(`## General

Non-dated file.
`), 0644)

	out, err := executeCmd(t, "ingest", "--db", db,
		"-n", "test:log",
		"--kind", "episodic",
		"--date-ns",
		"--dry-run",
		dir,
	)
	if err != nil {
		t.Fatalf("ingest date-ns failed: %v\nout: %s", err, out)
	}

	var memories []model.Memory
	if err := json.Unmarshal([]byte(out), &memories); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}
	if len(memories) != 3 {
		t.Fatalf("want 3 memories, got %d", len(memories))
	}

	nsSet := map[string]bool{}
	for _, m := range memories {
		nsSet[m.NS] = true
	}
	if !nsSet["test:log:2026-02-15"] {
		t.Error("expected namespace test:log:2026-02-15")
	}
	if !nsSet["test:log:2026-02-16"] {
		t.Error("expected namespace test:log:2026-02-16")
	}
	if !nsSet["test:log"] {
		t.Error("expected namespace test:log (for non-dated file)")
	}
}

// Note: error paths (invalid path, empty file) call exitErr → os.Exit(1),
// which cannot be tested in-process. These are validated via manual testing.

func TestIngestPreamble(t *testing.T) {
	db := tempDB(t)
	dir := t.TempDir()
	mdFile := filepath.Join(dir, "IDENTITY.md")
	os.WriteFile(mdFile, []byte(`# Identity File

This is preamble content before any sections.

## Name

My name is Agent.
`), 0644)

	out, err := executeCmd(t, "ingest", "--db", db, "-n", "test:pre", "--dry-run", mdFile)
	if err != nil {
		t.Fatalf("ingest failed: %v\nout: %s", err, out)
	}

	var memories []model.Memory
	if err := json.Unmarshal([]byte(out), &memories); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}
	if len(memories) != 2 {
		t.Fatalf("want 2 memories (preamble + section), got %d", len(memories))
	}

	// Preamble should have _preamble key.
	if memories[0].Key != "_preamble" {
		t.Errorf("preamble key: want %q, got %q", "_preamble", memories[0].Key)
	}
	if memories[1].Key != "name" {
		t.Errorf("section key: want %q, got %q", "name", memories[1].Key)
	}
}

func TestIngestWholeFileNoH2(t *testing.T) {
	db := tempDB(t)
	dir := t.TempDir()
	mdFile := filepath.Join(dir, "SOUL.md")
	os.WriteFile(mdFile, []byte(`# Soul

I am a helpful assistant.
I care about accuracy.
`), 0644)

	out, err := executeCmd(t, "ingest", "--db", db, "-n", "test:soul", "--dry-run", mdFile)
	if err != nil {
		t.Fatalf("ingest failed: %v\nout: %s", err, out)
	}

	var memories []model.Memory
	if err := json.Unmarshal([]byte(out), &memories); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}
	if len(memories) != 1 {
		t.Fatalf("want 1 memory, got %d", len(memories))
	}
	// File with no H2 uses _preamble key.
	if memories[0].Key != "_preamble" {
		t.Errorf("key: want %q, got %q", "_preamble", memories[0].Key)
	}
}

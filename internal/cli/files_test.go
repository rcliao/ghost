package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rcliao/ghost/internal/model"
)

func TestPutWithFilesFlag(t *testing.T) {
	db := tempDB(t)
	out, err := executeCmd(t, "put", "--db", db,
		"-n", "ns", "-k", "task1",
		"--files", "src/main.go,src/util.go",
		"refactored code",
	)
	if err != nil {
		t.Fatalf("put with files: %v", err)
	}

	var mem model.Memory
	if err := json.Unmarshal([]byte(out), &mem); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}
	if len(mem.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(mem.Files))
	}
	if mem.Files[0].Path != "src/main.go" {
		t.Errorf("files[0].path: want %q, got %q", "src/main.go", mem.Files[0].Path)
	}
	if mem.Files[0].Rel != "modified" {
		t.Errorf("files[0].rel: want %q, got %q", "modified", mem.Files[0].Rel)
	}
}

func TestPutWithFilesAndRel(t *testing.T) {
	db := tempDB(t)
	out, err := executeCmd(t, "put", "--db", db,
		"-n", "ns", "-k", "task1",
		"--files", "new_file.go",
		"--file-rel", "created",
		"added new file",
	)
	if err != nil {
		t.Fatalf("put with files: %v", err)
	}

	var mem model.Memory
	if err := json.Unmarshal([]byte(out), &mem); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}
	if len(mem.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(mem.Files))
	}
	if mem.Files[0].Rel != "created" {
		t.Errorf("files[0].rel: want %q, got %q", "created", mem.Files[0].Rel)
	}
}

func TestGetShowsFiles(t *testing.T) {
	db := tempDB(t)

	// Put with files
	_, err := executeCmd(t, "put", "--db", db,
		"-n", "ns", "-k", "k",
		"--files", "a.go,b.go",
		"content",
	)
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// Get should include files
	out, err := executeCmd(t, "get", "--db", db, "-n", "ns", "-k", "k")
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	var mem model.Memory
	if err := json.Unmarshal([]byte(out), &mem); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}
	if len(mem.Files) != 2 {
		t.Fatalf("expected 2 files in get output, got %d", len(mem.Files))
	}
}

func TestFilesSubcommand(t *testing.T) {
	db := tempDB(t)

	// Seed two memories linking to the same file
	_, err := executeCmd(t, "put", "--db", db,
		"-n", "ns", "-k", "task1",
		"--files", "shared.go",
		"first change",
	)
	if err != nil {
		t.Fatalf("put task1: %v", err)
	}
	_, err = executeCmd(t, "put", "--db", db,
		"-n", "ns", "-k", "task2",
		"--files", "shared.go,other.go",
		"second change",
	)
	if err != nil {
		t.Fatalf("put task2: %v", err)
	}

	// Query by file
	out, err := executeCmd(t, "files", "--db", db, "shared.go")
	if err != nil {
		t.Fatalf("files: %v", err)
	}

	var memories []model.Memory
	if err := json.Unmarshal([]byte(out), &memories); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}
	if len(memories) != 2 {
		t.Fatalf("expected 2 memories for shared.go, got %d", len(memories))
	}
}

func TestFilesSubcommandTextFormat(t *testing.T) {
	db := tempDB(t)

	_, err := executeCmd(t, "put", "--db", db,
		"-n", "ns", "-k", "task1",
		"--files", "file.go",
		"did some work",
	)
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	out, err := executeCmd(t, "files", "--db", db, "-f", "text", "file.go")
	if err != nil {
		t.Fatalf("files: %v", err)
	}

	line := strings.TrimSpace(out)
	if line != "ns/task1: did some work" {
		t.Errorf("text output: want %q, got %q", "ns/task1: did some work", line)
	}
}

func TestFilesSubcommandNoResults(t *testing.T) {
	db := tempDB(t)

	out, err := executeCmd(t, "files", "--db", db, "nonexistent.go")
	if err != nil {
		t.Fatalf("files: %v", err)
	}

	if strings.TrimSpace(out) != "[]" {
		t.Errorf("expected empty array, got %q", strings.TrimSpace(out))
	}
}

func TestFilesSubcommandWithRelFilter(t *testing.T) {
	db := tempDB(t)

	_, err := executeCmd(t, "put", "--db", db,
		"-n", "ns", "-k", "task1",
		"--files", "file.go",
		"--file-rel", "modified",
		"modified it",
	)
	if err != nil {
		t.Fatalf("put task1: %v", err)
	}
	_, err = executeCmd(t, "put", "--db", db,
		"-n", "ns", "-k", "task2",
		"--files", "file.go",
		"--file-rel", "read",
		"just read it",
	)
	if err != nil {
		t.Fatalf("put task2: %v", err)
	}

	// Filter by rel=modified
	out, err := executeCmd(t, "files", "--db", db, "--rel", "modified", "file.go")
	if err != nil {
		t.Fatalf("files: %v", err)
	}

	var memories []model.Memory
	if err := json.Unmarshal([]byte(out), &memories); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}
	if len(memories) != 1 {
		t.Fatalf("expected 1 memory with rel=modified, got %d", len(memories))
	}
	if memories[0].Key != "task1" {
		t.Errorf("expected task1, got %q", memories[0].Key)
	}
}

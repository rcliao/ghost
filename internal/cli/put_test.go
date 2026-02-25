package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/rcliao/agent-memory/internal/model"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// resetFlags recursively resets all flags on a command and its subcommands.
func resetFlags(cmd *cobra.Command) {
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		f.Value.Set(f.DefValue)
		f.Changed = false
	})
	for _, sub := range cmd.Commands() {
		resetFlags(sub)
	}
}

// executeCmd resets Cobra flag state, runs RootCmd with the given args,
// and returns captured stdout plus any execution error.
func executeCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()

	// Reset flags on all subcommands (recursively) to avoid state leaking between tests.
	for _, c := range RootCmd.Commands() {
		resetFlags(c)
	}

	buf := new(bytes.Buffer)
	RootCmd.SetOut(buf)
	RootCmd.SetErr(buf)
	RootCmd.SetArgs(args)
	err := RootCmd.Execute()
	return buf.String(), err
}

func tempDB(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "test.db")
}

func TestPutBasic(t *testing.T) {
	db := tempDB(t)
	out, err := executeCmd(t, "put", "--db", db, "-n", "ns1", "-k", "key1", "hello world")
	if err != nil {
		t.Fatalf("put failed: %v", err)
	}

	var mem model.Memory
	if err := json.Unmarshal([]byte(out), &mem); err != nil {
		t.Fatalf("unmarshal output: %v\nraw: %s", err, out)
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
	if mem.Kind != "semantic" {
		t.Errorf("kind: want %q, got %q", "semantic", mem.Kind)
	}
	if mem.Priority != "normal" {
		t.Errorf("priority: want %q, got %q", "normal", mem.Priority)
	}
	if mem.ID == "" {
		t.Error("expected non-empty ID")
	}
}

func TestPutWithAllFlags(t *testing.T) {
	db := tempDB(t)
	out, err := executeCmd(t, "put", "--db", db,
		"-n", "myns", "-k", "mykey",
		"--kind", "procedural",
		"--tags", "go,testing,cli",
		"--priority", "high",
		"--meta", `{"author":"test"}`,
		"some content here",
	)
	if err != nil {
		t.Fatalf("put failed: %v", err)
	}

	var mem model.Memory
	if err := json.Unmarshal([]byte(out), &mem); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}

	if mem.Kind != "procedural" {
		t.Errorf("kind: want %q, got %q", "procedural", mem.Kind)
	}
	if mem.Priority != "high" {
		t.Errorf("priority: want %q, got %q", "high", mem.Priority)
	}
	if mem.Meta != `{"author":"test"}` {
		t.Errorf("meta: want %q, got %q", `{"author":"test"}`, mem.Meta)
	}
	wantTags := []string{"go", "testing", "cli"}
	if len(mem.Tags) != len(wantTags) {
		t.Fatalf("tags len: want %d, got %d", len(wantTags), len(mem.Tags))
	}
	for i, tag := range wantTags {
		if mem.Tags[i] != tag {
			t.Errorf("tag[%d]: want %q, got %q", i, tag, mem.Tags[i])
		}
	}
}

func TestPutJoinsMultipleArgs(t *testing.T) {
	db := tempDB(t)
	out, err := executeCmd(t, "put", "--db", db, "-n", "ns", "-k", "k", "hello", "world", "foo")
	if err != nil {
		t.Fatalf("put failed: %v", err)
	}

	var mem model.Memory
	if err := json.Unmarshal([]byte(out), &mem); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if mem.Content != "hello world foo" {
		t.Errorf("content: want %q, got %q", "hello world foo", mem.Content)
	}
}

func TestPutVersionIncrement(t *testing.T) {
	db := tempDB(t)

	out1, err := executeCmd(t, "put", "--db", db, "-n", "ns", "-k", "k", "v1")
	if err != nil {
		t.Fatalf("put v1: %v", err)
	}
	var m1 model.Memory
	if err := json.Unmarshal([]byte(out1), &m1); err != nil {
		t.Fatalf("unmarshal v1: %v", err)
	}
	if m1.Version != 1 {
		t.Errorf("v1 version: want 1, got %d", m1.Version)
	}

	out2, err := executeCmd(t, "put", "--db", db, "-n", "ns", "-k", "k", "v2")
	if err != nil {
		t.Fatalf("put v2: %v", err)
	}
	var m2 model.Memory
	if err := json.Unmarshal([]byte(out2), &m2); err != nil {
		t.Fatalf("unmarshal v2: %v", err)
	}
	if m2.Version != 2 {
		t.Errorf("v2 version: want 2, got %d", m2.Version)
	}
	if m2.Supersedes == "" {
		t.Error("expected supersedes to be set on v2")
	}
}

func TestPutWithTTL(t *testing.T) {
	db := tempDB(t)
	out, err := executeCmd(t, "put", "--db", db, "-n", "ns", "-k", "k", "--ttl", "24h", "expiring content")
	if err != nil {
		t.Fatalf("put with ttl: %v", err)
	}

	var mem model.Memory
	if err := json.Unmarshal([]byte(out), &mem); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if mem.ExpiresAt == nil {
		t.Error("expected expires_at to be set when TTL is provided")
	}
}

func TestPutTagsWithSpaces(t *testing.T) {
	db := tempDB(t)
	out, err := executeCmd(t, "put", "--db", db, "-n", "ns", "-k", "k", "--tags", " foo , bar , ", "content")
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	var mem model.Memory
	if err := json.Unmarshal([]byte(out), &mem); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Empty tags from trailing comma should be stripped
	if len(mem.Tags) != 2 {
		t.Fatalf("tags: want 2, got %d (%v)", len(mem.Tags), mem.Tags)
	}
	if mem.Tags[0] != "foo" || mem.Tags[1] != "bar" {
		t.Errorf("tags: want [foo bar], got %v", mem.Tags)
	}
}

func TestPutContentTrimmed(t *testing.T) {
	db := tempDB(t)
	out, err := executeCmd(t, "put", "--db", db, "-n", "ns", "-k", "k", "  padded content  ")
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	var mem model.Memory
	if err := json.Unmarshal([]byte(out), &mem); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if mem.Content != "padded content" {
		t.Errorf("content should be trimmed: want %q, got %q", "padded content", mem.Content)
	}
}

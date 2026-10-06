package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rcliao/ghost/internal/model"
)

func TestCurateUsed(t *testing.T) {
	db := tempDB(t)

	if _, err := executeCmd(t, "put", "--db", db, "-n", "ns1", "-k", "key1", "hello world"); err != nil {
		t.Fatalf("put: %v", err)
	}

	// An unused memory omits used_count: existing JSON output is unchanged.
	out, err := executeCmd(t, "get", "--db", db, "-n", "ns1", "-k", "key1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if strings.Contains(out, "used_count") {
		t.Errorf("unused memory should omit used_count, got: %s", out)
	}

	out, err = executeCmd(t, "curate", "--db", db, "-n", "ns1", "-k", "key1", "--op", "used")
	if err != nil {
		t.Fatalf("curate used: %v\n%s", err, out)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal curate output: %v\nraw: %s", err, out)
	}
	if res["op"] != "used" || res["ns"] != "ns1" || res["key"] != "key1" {
		t.Errorf("unexpected curate output: %v", res)
	}

	out, err = executeCmd(t, "get", "--db", db, "-n", "ns1", "-k", "key1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	var mem model.Memory
	if err := json.Unmarshal([]byte(out), &mem); err != nil {
		t.Fatalf("unmarshal get: %v\nraw: %s", err, out)
	}
	if mem.UsedCount != 1 {
		t.Errorf("used_count: want 1, got %d\nraw: %s", mem.UsedCount, out)
	}
	if mem.UtilityCount != 0 {
		t.Errorf("used must not touch utility_count: got %d", mem.UtilityCount)
	}
	if mem.Version != 1 {
		t.Errorf("version: want 1, got %d", mem.Version)
	}
}

func TestCurateUsedMissing(t *testing.T) {
	db := tempDB(t)
	if _, err := executeCmd(t, "curate", "--db", db, "-n", "ns1", "-k", "nope", "--op", "used"); err == nil {
		t.Error("expected error for missing memory")
	}
}

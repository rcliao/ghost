package cli

import (
	"strings"
	"testing"
)

func TestInferEdgesRequiresNS(t *testing.T) {
	db := tempDB(t)
	out, err := executeCmd(t, "infer-edges", "--db", db)
	if err == nil {
		t.Fatalf("expected error when --ns is missing; got output: %s", out)
	}
	if !strings.Contains(err.Error(), "ns") && !strings.Contains(out, "ns") {
		t.Fatalf("expected error to mention --ns; got: %v / %s", err, out)
	}
}

func TestInferEdgesRegistered(t *testing.T) {
	db := tempDB(t)
	out, err := executeCmd(t, "infer-edges", "--db", db, "--help")
	if err != nil {
		t.Fatalf("help should succeed: %v", err)
	}
	for _, want := range []string{"--ns", "--max-pairs", "--seed", "--dry-run", "--model"} {
		if !strings.Contains(out, want) {
			t.Errorf("help output missing flag %q", want)
		}
	}
	if !strings.Contains(out, "caused_by") && !strings.Contains(out, "reasoning") {
		t.Errorf("help should mention reasoning edges; got: %s", out)
	}
}

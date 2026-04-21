package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReportStructure(t *testing.T) {
	// Verify the report struct decodes a real checkpoint without loss.
	sample := report{
		Total:   100,
		Dataset: "locomo_plus.json",
		LLM:     "claude-cli:haiku",
		ByType: map[string]typeAgg{
			"causal": {
				Count: 25,
				Metrics: map[string]map[string]float64{
					"ghost-compress-wide": {"score": 17, "input_tokens": 9000, "output_tokens": 2700, "latency_sec": 600},
				},
			},
		},
		Overall: map[string]map[string]float64{
			"ghost-compress-wide": {"score": 67, "input_tokens": 35900, "output_tokens": 10700, "latency_sec": 2430},
			"oracle":              {"score": 88.5, "input_tokens": 20300, "output_tokens": 9200, "latency_sec": 808},
		},
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "ckpt.json")
	buf, err := json.Marshal(sample)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf, 0644); err != nil {
		t.Fatal(err)
	}

	readBuf, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got report
	if err := json.Unmarshal(readBuf, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Total != 100 {
		t.Errorf("Total: got %d, want 100", got.Total)
	}
	if len(got.Overall) != 2 {
		t.Errorf("Overall modes: got %d, want 2", len(got.Overall))
	}
	if got.Overall["oracle"]["score"] != 88.5 {
		t.Errorf("oracle score: got %v, want 88.5", got.Overall["oracle"]["score"])
	}
}

func TestPricingPresets(t *testing.T) {
	for _, name := range []string{"haiku", "sonnet", "opus"} {
		p, ok := presets[name]
		if !ok {
			t.Errorf("missing preset %q", name)
			continue
		}
		if p.inCost <= 0 || p.outCost <= 0 {
			t.Errorf("preset %q has non-positive pricing: %+v", name, p)
		}
		if p.outCost <= p.inCost {
			t.Errorf("preset %q has output cheaper than input: %+v", name, p)
		}
	}
}

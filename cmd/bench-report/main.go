// bench-report reads a LoCoMo-Plus E2E checkpoint JSON file and emits a
// markdown table suitable for pasting into docs/eval.md.
//
// Usage:
//
//	go run ./cmd/bench-report /tmp/ghost-lmplus-full.json
//	go run ./cmd/bench-report -input /tmp/foo.json -pricing haiku
//
// Pricing presets (per 1M tokens, input/output):
//
//	haiku: $1 / $5
//	sonnet: $3 / $15
//	opus: $15 / $75
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
)

type pricing struct {
	name    string
	inCost  float64 // $ per 1M tokens
	outCost float64
}

var presets = map[string]pricing{
	"haiku":  {"Haiku 4.5", 1, 5},
	"sonnet": {"Sonnet 4.6", 3, 15},
	"opus":   {"Opus 4.7", 15, 75},
}

type report struct {
	Total   int                           `json:"total"`
	Dataset string                        `json:"dataset"`
	LLM     string                        `json:"llm"`
	ByType  map[string]typeAgg            `json:"by_type"`
	Overall map[string]map[string]float64 `json:"overall"`
}

type typeAgg struct {
	Count   int                           `json:"count"`
	Metrics map[string]map[string]float64 `json:"metrics"`
}

func main() {
	var (
		inputFlag   = flag.String("input", "", "Checkpoint JSON path (or pass as first arg)")
		pricingFlag = flag.String("pricing", "haiku", "Pricing preset: haiku|sonnet|opus")
	)
	flag.Parse()

	path := *inputFlag
	if path == "" && flag.NArg() > 0 {
		path = flag.Arg(0)
	}
	if path == "" {
		fmt.Fprintln(os.Stderr, "usage: bench-report <checkpoint.json>")
		os.Exit(1)
	}

	buf, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read: %v\n", err)
		os.Exit(1)
	}
	var r report
	if err := json.Unmarshal(buf, &r); err != nil {
		fmt.Fprintf(os.Stderr, "parse: %v\n", err)
		os.Exit(1)
	}

	price, ok := presets[*pricingFlag]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown pricing preset %q\n", *pricingFlag)
		os.Exit(1)
	}

	modes := make([]string, 0, len(r.Overall))
	for m := range r.Overall {
		modes = append(modes, m)
	}
	sort.Strings(modes)

	fmt.Printf("**Dataset:** %s  •  **LLM:** %s  •  **n=%d questions**\n\n", r.Dataset, r.LLM, r.Total)

	fmt.Printf("| Mode | Score | In-Tok | Out-Tok | Latency | $/question |\n")
	fmt.Printf("|------|-------|--------|---------|---------|-----------|\n")
	for _, mode := range modes {
		m := r.Overall[mode]
		n := float64(r.Total)
		score := m["score"] / n
		inTok := m["input_tokens"] / n
		outTok := m["output_tokens"] / n
		latency := m["latency_sec"] / n
		cost := (inTok*price.inCost + outTok*price.outCost) / 1_000_000 * 1_000_000 // $/M tokens × per-q tokens = μ$
		fmt.Printf("| %s | %.3f | %.0f | %.0f | %.1fs | $%.0fμ |\n",
			mode, score, inTok, outTok, latency, cost)
	}

	// Per-type breakdown
	if len(r.ByType) > 0 {
		fmt.Println()
		fmt.Println("**Per-type score breakdown:**")
		fmt.Println()

		types := make([]string, 0, len(r.ByType))
		for t := range r.ByType {
			types = append(types, t)
		}
		sort.Strings(types)

		fmt.Printf("| Type |")
		for _, mode := range modes {
			fmt.Printf(" %s |", mode)
		}
		fmt.Println()
		fmt.Printf("|------|")
		for range modes {
			fmt.Printf("---|")
		}
		fmt.Println()
		for _, typ := range types {
			agg := r.ByType[typ]
			fmt.Printf("| %s (n=%d) |", typ, agg.Count)
			for _, mode := range modes {
				score := agg.Metrics[mode]["score"] / float64(agg.Count)
				fmt.Printf(" %.2f |", score)
			}
			fmt.Println()
		}
	}
}

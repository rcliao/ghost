package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/rcliao/ghost/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{
		Use:   "infer-edges",
		Short: "Use an LLM to infer reasoning edges (caused_by, prevents, implies) between related memories",
		Long: `Scans pairs of memories connected by relates_to edges and asks an LLM to classify
whether a reasoning relationship exists. Creates typed edges when confirmed.

LLM is called out-of-band — Ghost's hot path (Search, Context) remains LLM-free.
Uses 'claude -p' by default; set ANTHROPIC_API_KEY to use the API directly.

Examples:
  ghost infer-edges --ns agent:claude-code --max-pairs 50 --dry-run
  ghost infer-edges --ns agent:pikamini --seed "login-flow,auth-decision"`,
		RunE: runInferEdges,
	}

	cmd.Flags().StringP("ns", "n", "", "Namespace to scan (required)")
	cmd.Flags().Int("max-pairs", 100, "Max candidate pairs to examine")
	cmd.Flags().String("seed", "", "Optional comma-separated keys; only pairs touching these are examined")
	cmd.Flags().Bool("dry-run", false, "Classify but don't write edges")
	cmd.Flags().String("model", "", "LLM model (default: claude CLI default)")
	cmd.MarkFlagRequired("ns")

	RootCmd.AddCommand(cmd)
}

func runInferEdges(cmd *cobra.Command, args []string) error {
	ns, _ := cmd.Flags().GetString("ns")
	maxPairs, _ := cmd.Flags().GetInt("max-pairs")
	seedStr, _ := cmd.Flags().GetString("seed")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	model, _ := cmd.Flags().GetString("model")

	var seeds []string
	if seedStr != "" {
		for _, k := range strings.Split(seedStr, ",") {
			if k = strings.TrimSpace(k); k != "" {
				seeds = append(seeds, k)
			}
		}
	}

	var llm store.InferLLMClient
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		llm = store.NewAnthropicClient(model)
	} else {
		llm = store.NewClaudeCLIClient(model)
	}

	result, err := st.InferEdges(cmd.Context(), store.InferEdgesParams{
		NS:       ns,
		LLM:      llm,
		MaxPairs: maxPairs,
		Seed:     seeds,
		DryRun:   dryRun,
	})
	if err != nil {
		return fmt.Errorf("infer edges: %w", err)
	}

	if formatFlag == "text" {
		prefix := ""
		if dryRun {
			prefix = "(dry-run) "
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%sInferred edges for ns=%s:\n", prefix, ns)
		fmt.Fprintf(cmd.OutOrStdout(), "  Pairs examined: %d\n", result.PairsExamined)
		fmt.Fprintf(cmd.OutOrStdout(), "  Edges created:  %d\n", result.EdgesCreated)
		fmt.Fprintf(cmd.OutOrStdout(), "  Edges skipped:  %d (already exist)\n", result.EdgesSkipped)
		for _, inf := range result.Inferences {
			mark := " "
			if inf.Applied {
				mark = "+"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  %s %s --[%s]--> %s\n", mark, inf.FromKey, inf.Rel, inf.ToKey)
			if inf.Reason != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "      reason: %s\n", inf.Reason)
			}
		}
		return nil
	}
	outputJSON(cmd, result)
	return nil
}

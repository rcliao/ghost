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
Providers: 'claude' (claude -p, default), 'anthropic' (ANTHROPIC_API_KEY),
'jev' (TypeSafe decision API, TYPESAFE_API_KEY — one typed choice question per
pair, returns a probability that is kept in the edge reason for review).

Examples:
  ghost infer-edges --ns agent:claude-code --max-pairs 50 --dry-run
  ghost infer-edges --ns agent:pikamini --seed "login-flow,auth-decision"
  ghost infer-edges --ns agent:pikamini --provider jev --min-prob 0.7 --dry-run`,
		RunE: runInferEdges,
	}

	cmd.Flags().StringP("ns", "n", "", "Namespace to scan (required)")
	cmd.Flags().Int("max-pairs", 100, "Max candidate pairs to examine")
	cmd.Flags().String("seed", "", "Optional comma-separated keys; only pairs touching these are examined")
	cmd.Flags().Bool("dry-run", false, "Classify but don't write edges")
	cmd.Flags().String("model", "", "LLM model (default: claude CLI default)")
	cmd.Flags().String("provider", "", "claude | anthropic | jev (default: anthropic if ANTHROPIC_API_KEY is set, else claude)")
	cmd.Flags().Float64("min-prob", jevMinProbDefault, "jev only: minimum probability for a relation to be accepted")
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

	provider, _ := cmd.Flags().GetString("provider")
	minProb, _ := cmd.Flags().GetFloat64("min-prob")
	var llm store.InferLLMClient
	switch provider {
	case "jev":
		if os.Getenv(jevKeyEnv) == "" {
			return fmt.Errorf("--provider jev requires %s", jevKeyEnv)
		}
		llm = newJevClient(minProb)
	case "anthropic":
		llm = store.NewAnthropicClient(model)
	case "claude":
		llm = store.NewClaudeCLIClient(model)
	case "":
		if os.Getenv("ANTHROPIC_API_KEY") != "" {
			llm = store.NewAnthropicClient(model)
		} else {
			llm = store.NewClaudeCLIClient(model)
		}
	default:
		return fmt.Errorf("unknown --provider %q (claude | anthropic | jev)", provider)
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
	if jc, ok := llm.(*jevClient); ok {
		if sum := jc.Summary(); sum != "" {
			fmt.Fprintln(cmd.ErrOrStderr(), sum)
		}
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

package cli

import (
	"fmt"
	"strings"

	"github.com/rcliao/ghost/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{
		Use:   "context [description]",
		Short: "Assemble relevant memories for a task",
		Long:  "Search and score memories, then greedily pack them into a token budget.",
		Args:  cobra.MinimumNArgs(1),
		Run:   runContext,
	}

	cmd.Flags().StringP("ns", "n", "", "Filter by namespace (supports prefix: 'ns:*')")
	cmd.Flags().String("kind", "", "Filter by kind")
	cmd.Flags().StringSliceP("tags", "t", nil, "Filter by tags")
	cmd.Flags().IntP("budget", "b", 4000, "Max tokens in output")
	cmd.Flags().Float64("min-score", 0, "Drop candidates with score below this floor (0 = no filter). Useful at scale to suppress noisy low-confidence retrievals.")
	cmd.Flags().Float64("min-spread", 0, "If top-1 score minus top-5 score is less than this delta, collapse to top-1 only (flat-noise detection, 0 = no filter).")

	RootCmd.AddCommand(cmd)
}

func runContext(cmd *cobra.Command, args []string) {
	ns, _ := cmd.Flags().GetString("ns")
	kind, _ := cmd.Flags().GetString("kind")
	tags, _ := cmd.Flags().GetStringSlice("tags")
	budget, _ := cmd.Flags().GetInt("budget")
	minScore, _ := cmd.Flags().GetFloat64("min-score")
	minSpread, _ := cmd.Flags().GetFloat64("min-spread")
	query := strings.Join(args, " ")

	if err := validateKind(kind); err != nil {
		exitErr("context", err)
	}
	if budget <= 0 {
		exitErr("context", fmt.Errorf("--budget must be a positive number (got %d)", budget))
	}

	result, err := st.Context(cmd.Context(), store.ContextParams{
		NS:        ns,
		Query:     query,
		Kind:      kind,
		Tags:      tags,
		Budget:    budget,
		MinScore:  minScore,
		MinSpread: minSpread,
	})
	if err != nil {
		exitErr("context", err)
	}

	outputJSON(cmd, result)
}

package cli

import (
	"fmt"
	"strings"

	"github.com/rcliao/agent-memory/internal/store"
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

	RootCmd.AddCommand(cmd)
}

func runContext(cmd *cobra.Command, args []string) {
	ns, _ := cmd.Flags().GetString("ns")
	kind, _ := cmd.Flags().GetString("kind")
	tags, _ := cmd.Flags().GetStringSlice("tags")
	budget, _ := cmd.Flags().GetInt("budget")
	query := strings.Join(args, " ")

	if err := validateKind(kind); err != nil {
		exitErr("context", err)
	}
	if budget <= 0 {
		exitErr("context", fmt.Errorf("--budget must be a positive number (got %d)", budget))
	}

	result, err := st.Context(cmd.Context(), store.ContextParams{
		NS:     ns,
		Query:  query,
		Kind:   kind,
		Tags:   tags,
		Budget: budget,
	})
	if err != nil {
		exitErr("context", err)
	}

	outputJSON(cmd, result)
}

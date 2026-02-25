package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show database statistics",
		Run:   runStats,
	}

	RootCmd.AddCommand(cmd)
}

func runStats(cmd *cobra.Command, args []string) {
	stats, err := st.Stats(cmd.Context(), getDBPath())
	if err != nil {
		exitErr("stats", fmt.Errorf("failed to read database stats: %w", err))
	}

	outputJSON(cmd, stats)
}

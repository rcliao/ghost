package cli

import (
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
	s, err := openStore()
	if err != nil {
		exitErr("open store", err)
	}
	defer s.Close()

	stats, err := s.Stats(cmd.Context(), getDBPath())
	if err != nil {
		exitErr("stats", err)
	}

	outputJSON(cmd, stats)
}

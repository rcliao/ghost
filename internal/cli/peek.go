package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	RootCmd.AddCommand(peekCmd)
	peekCmd.Flags().String("ns", "", "Namespace filter")
}

var peekCmd = &cobra.Command{
	Use:   "peek",
	Short: "Show a lightweight index of memory state for lazy discovery",
	RunE: func(cmd *cobra.Command, args []string) error {
		ns, _ := cmd.Flags().GetString("ns")

		result, err := st.Peek(cmd.Context(), ns)
		if err != nil {
			return err
		}

		if formatFlag == "text" {
			if result.IdentitySummary != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Identity: %s\n", result.IdentitySummary)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Memories by tier:\n")
			for tier, count := range result.MemoryCounts {
				tokens := result.TotalEstTokens[tier]
				fmt.Fprintf(cmd.OutOrStdout(), "  %-10s %d memories, ~%d tokens\n", tier, count, tokens)
			}
			if len(result.RecentTopics) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "Recent topics: %v\n", result.RecentTopics)
			}
			if len(result.HighImportance) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "High importance:\n")
				for _, s := range result.HighImportance {
					fmt.Fprintf(cmd.OutOrStdout(), "  [%.1f] %s (%s/%s): %s\n",
						s.Importance, s.ID[:8], s.Tier, s.Kind, s.Summary)
				}
			}
			return nil
		}

		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	},
}

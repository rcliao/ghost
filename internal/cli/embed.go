package cli

import (
	"fmt"

	"github.com/rcliao/ghost/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	RootCmd.AddCommand(embedCmd)
}

var embedCmd = &cobra.Command{
	Use:   "embed",
	Short: "Manage vector embeddings",
}

func init() {
	backfillCmd := &cobra.Command{
		Use:   "backfill",
		Short: "Generate embeddings for all chunks that don't have one",
		Long:  "Generates vector embeddings for existing memory chunks that were stored before embeddings were enabled. This is a one-time operation.",
		RunE: func(cmd *cobra.Command, args []string) error {
			sqlStore, ok := st.(*store.SQLiteStore)
			if !ok {
				return fmt.Errorf("backfill requires SQLite store")
			}

			ctx := cmd.Context()
			fmt.Fprintln(cmd.OutOrStdout(), "Backfilling embeddings...")

			skipped := 0
			updated, err := sqlStore.BackfillEmbeddings(ctx, func(done, total, skip int) {
				skipped = skip
				if done%50 == 0 || done == total {
					fmt.Fprintf(cmd.OutOrStdout(), "  %d/%d chunks embedded", done, total)
					if skip > 0 {
						fmt.Fprintf(cmd.OutOrStdout(), " (%d skipped)", skip)
					}
					fmt.Fprintln(cmd.OutOrStdout())
				}
			})
			if err != nil {
				return fmt.Errorf("backfill: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Done. %d chunks embedded", updated)
			if skipped > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), ", %d skipped", skipped)
			}
			fmt.Fprintln(cmd.OutOrStdout(), ".")
			return nil
		},
	}

	embedCmd.AddCommand(backfillCmd)
}

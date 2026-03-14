package cli

import (
	"fmt"

	"github.com/rcliao/ghost/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{
		Use:   "curate",
		Short: "Apply a lifecycle action to a single memory",
		Long: `Curate applies a lifecycle operation to a specific memory identified by
namespace and key. Use this for targeted adjustments instead of running
a full reflect cycle.

Operations:
  promote   Tier up: dormant → stm → ltm
  demote    Tier down: ltm → stm → dormant
  boost     Importance +0.2 (caps at 1.0)
  diminish  Importance -0.2 (floors at 0.1)
  archive   Move to dormant tier
  delete    Soft-delete (recoverable)
  pin       Always loaded in context, exempt from decay
  unpin     Remove from always-on context`,
		RunE: runCurate,
	}

	cmd.Flags().StringP("ns", "n", "", "Namespace (required)")
	cmd.Flags().StringP("key", "k", "", "Memory key (required)")
	cmd.Flags().String("op", "", "Operation: promote, demote, boost, diminish, archive, delete, pin, unpin (required)")

	RootCmd.AddCommand(cmd)
}

func runCurate(cmd *cobra.Command, args []string) error {
	ns, _ := cmd.Flags().GetString("ns")
	key, _ := cmd.Flags().GetString("key")
	op, _ := cmd.Flags().GetString("op")

	if ns == "" || key == "" || op == "" {
		return fmt.Errorf("required flags: --ns, --key, --op")
	}

	result, err := st.Curate(cmd.Context(), store.CurateParams{
		NS:  ns,
		Key: key,
		Op:  op,
	})
	if err != nil {
		return fmt.Errorf("curate: %w", err)
	}

	outputJSON(cmd, result)
	return nil
}

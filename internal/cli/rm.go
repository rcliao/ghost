package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/rcliao/agent-memory/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{
		Use:   "rm",
		Short: "Delete a memory",
		Run:   runRm,
	}

	cmd.Flags().StringP("ns", "n", "", "Namespace (required)")
	cmd.Flags().StringP("key", "k", "", "Key (required)")
	cmd.Flags().Bool("all-versions", false, "Delete all versions")
	cmd.Flags().Bool("hard", false, "Permanent delete (irreversible)")

	cmd.MarkFlagRequired("ns")
	cmd.MarkFlagRequired("key")

	RootCmd.AddCommand(cmd)
}

func runRm(cmd *cobra.Command, args []string) {
	ns, _ := cmd.Flags().GetString("ns")
	key, _ := cmd.Flags().GetString("key")
	allVersions, _ := cmd.Flags().GetBool("all-versions")
	hard, _ := cmd.Flags().GetBool("hard")

	if hard {
		fmt.Fprintf(os.Stderr, "warning: --hard permanently deletes %s/%s (cannot be undone)\n", ns, key)
	}

	err := st.Rm(cmd.Context(), store.RmParams{
		NS:          ns,
		Key:         key,
		AllVersions: allVersions,
		Hard:        hard,
	})
	if err != nil {
		errStr := err.Error()
		switch {
		case strings.Contains(errStr, "not found"):
			exitErr("rm", fmt.Errorf("memory %s/%s not found — use 'list -n %s' to see existing keys", ns, key, ns))
		default:
			exitErr("rm", fmt.Errorf("failed to delete %s/%s: %w", ns, key, err))
		}
	}

	outputJSONCompact(cmd, struct {
		OK  bool   `json:"ok"`
		NS  string `json:"ns"`
		Key string `json:"key"`
	}{OK: true, NS: ns, Key: key})
}

package cli

import (
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

	s, err := openStore()
	if err != nil {
		exitErr("open store", err)
	}
	defer s.Close()

	err = s.Rm(cmd.Context(), store.RmParams{
		NS:          ns,
		Key:         key,
		AllVersions: allVersions,
		Hard:        hard,
	})
	if err != nil {
		exitErr("rm", err)
	}

	outputJSONCompact(cmd, struct {
		OK  bool   `json:"ok"`
		NS  string `json:"ns"`
		Key string `json:"key"`
	}{OK: true, NS: ns, Key: key})
}

package cli

import (
	"fmt"

	"github.com/rcliao/agent-memory/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Retrieve a memory",
		Run:   runGet,
	}

	cmd.Flags().StringP("ns", "n", "", "Namespace (required)")
	cmd.Flags().StringP("key", "k", "", "Key (required)")
	cmd.Flags().Bool("history", false, "Return all versions (newest first)")
	cmd.Flags().IntP("version", "v", 0, "Specific version number")

	cmd.MarkFlagRequired("ns")
	cmd.MarkFlagRequired("key")

	RootCmd.AddCommand(cmd)
}

func runGet(cmd *cobra.Command, args []string) {
	ns, _ := cmd.Flags().GetString("ns")
	key, _ := cmd.Flags().GetString("key")
	history, _ := cmd.Flags().GetBool("history")
	version, _ := cmd.Flags().GetInt("version")

	s, err := openStore()
	if err != nil {
		exitErr("open store", err)
	}
	defer s.Close()

	memories, err := s.Get(cmd.Context(), store.GetParams{
		NS:      ns,
		Key:     key,
		History: history,
		Version: version,
	})
	if err != nil {
		exitErr("get", err)
	}

	if formatFlag == "text" {
		w := writer(cmd)
		for _, m := range memories {
			fmt.Fprintln(w, m.Content)
		}
	} else if history || len(memories) > 1 {
		outputJSON(cmd, memories)
	} else {
		outputJSON(cmd, memories[0])
	}
}

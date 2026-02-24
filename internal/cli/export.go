package cli

import (
	"github.com/rcliao/agent-memory/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export memories as JSON",
		Long:  "Export memories as newline-delimited JSON. Filter by namespace with -n.",
		Run:   runExport,
	}

	cmd.Flags().StringP("ns", "n", "", "Filter by namespace")

	RootCmd.AddCommand(cmd)
}

func runExport(cmd *cobra.Command, args []string) {
	ns, _ := cmd.Flags().GetString("ns")

	s, err := openStore()
	if err != nil {
		exitErr("open store", err)
	}
	defer s.Close()

	memories, err := s.List(cmd.Context(), store.ListParams{
		NS:    ns,
		Limit: 100000, // effectively unlimited
	})
	if err != nil {
		exitErr("export", err)
	}

	// Also include historical versions via ExportAll
	allMemories, err := s.ExportAll(cmd.Context(), ns)
	if err != nil {
		exitErr("export", err)
	}
	_ = memories // ExportAll is more complete

	outputJSON(cmd, allMemories)
}

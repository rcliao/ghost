package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export memories as JSON",
		Long:  "Export memories as newline-delimited JSON. Filter by namespace with -n.",
		Run:   runExport,
	}

	cmd.Flags().StringP("ns", "n", "", "Filter by namespace (supports prefix: 'ns:*')")

	RootCmd.AddCommand(cmd)
}

func runExport(cmd *cobra.Command, args []string) {
	ns, _ := cmd.Flags().GetString("ns")

	allMemories, err := st.ExportAll(cmd.Context(), ns)
	if err != nil {
		exitErr("export", fmt.Errorf("failed to export memories: %w", err))
	}

	if len(allMemories) == 0 {
		if ns != "" {
			fmt.Fprintf(os.Stderr, "warning: no memories found in namespace %q\n", ns)
		} else {
			fmt.Fprintf(os.Stderr, "warning: no memories found — store is empty\n")
		}
	}

	outputJSON(cmd, allMemories)
}

package cli

import (
	"github.com/spf13/cobra"
)

func init() {
	nsCmd := &cobra.Command{
		Use:   "ns",
		Short: "Namespace management",
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all namespaces",
		Run:   runNSList,
	}

	nsCmd.AddCommand(listCmd)
	RootCmd.AddCommand(nsCmd)
}

func runNSList(cmd *cobra.Command, args []string) {
	s, err := openStore()
	if err != nil {
		exitErr("open store", err)
	}
	defer s.Close()

	rows, err := s.ListNamespaces(cmd.Context())
	if err != nil {
		exitErr("list namespaces", err)
	}

	outputJSON(cmd, rows)
}

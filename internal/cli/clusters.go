package cli

import (
	"fmt"

	"github.com/rcliao/ghost/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{
		Use:   "clusters",
		Short: "Show groups of similar memories connected by edges",
		Long: `Find clusters of memories connected by relates_to edges within a namespace.
Each cluster represents a group of similar memories that could be consolidated
into a summary via ghost consolidate.`,
		RunE: runClusters,
	}

	cmd.Flags().StringP("ns", "n", "", "Namespace (required)")
	cmd.MarkFlagRequired("ns")

	RootCmd.AddCommand(cmd)
}

func runClusters(cmd *cobra.Command, args []string) error {
	ns, _ := cmd.Flags().GetString("ns")

	clusters, err := st.GetSimilarClusters(cmd.Context(), ns)
	if err != nil {
		return fmt.Errorf("get clusters: %w", err)
	}

	if clusters == nil {
		clusters = []store.MemoryCluster{}
	}

	outputJSON(cmd, clusters)
	return nil
}

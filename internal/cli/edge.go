package cli

import (
	"fmt"
	"strings"

	"github.com/rcliao/ghost/internal/store"
	"github.com/spf13/cobra"
)

// validEdgeRels lists the accepted values for the --rel flag on edge.
var validEdgeRels = map[string]bool{
	"relates_to": true, "contradicts": true, "depends_on": true,
	"refines": true, "contains": true, "merged_into": true,
}

func init() {
	cmd := &cobra.Command{
		Use:   "edge",
		Short: "Create, remove, or list edges between memories",
		Long:  "Manage weighted edges (associations) between memories for DAG-based retrieval.",
		RunE:  runEdge,
	}

	cmd.Flags().StringP("ns", "n", "", "Namespace (used for both from and to if specific not set)")
	cmd.Flags().String("from-ns", "", "Source namespace (overrides --ns for source)")
	cmd.Flags().String("from-key", "", "Source key")
	cmd.Flags().String("to-ns", "", "Target namespace (overrides --ns for target)")
	cmd.Flags().String("to-key", "", "Target key")
	cmd.Flags().StringP("rel", "r", "", "Relation: relates_to, contradicts, depends_on, refines, contains, merged_into")
	cmd.Flags().Float64P("weight", "w", 0, "Edge weight (0.0-1.0, 0 means use default for rel type)")
	cmd.Flags().Bool("rm", false, "Remove the edge")
	cmd.Flags().Bool("list", false, "List all edges for a memory (requires --ns and --from-key)")

	RootCmd.AddCommand(cmd)
}

func runEdge(cmd *cobra.Command, args []string) error {
	ns, _ := cmd.Flags().GetString("ns")
	fromNS, _ := cmd.Flags().GetString("from-ns")
	fromKey, _ := cmd.Flags().GetString("from-key")
	toNS, _ := cmd.Flags().GetString("to-ns")
	toKey, _ := cmd.Flags().GetString("to-key")
	rel, _ := cmd.Flags().GetString("rel")
	weight, _ := cmd.Flags().GetFloat64("weight")
	rm, _ := cmd.Flags().GetBool("rm")
	list, _ := cmd.Flags().GetBool("list")

	// Resolve namespace defaults
	if fromNS == "" {
		fromNS = ns
	}
	if toNS == "" {
		toNS = ns
	}

	// List mode
	if list {
		if fromNS == "" || fromKey == "" {
			return fmt.Errorf("--list requires --ns (or --from-ns) and --from-key")
		}
		edges, err := st.GetEdgesByNSKey(cmd.Context(), fromNS, fromKey)
		if err != nil {
			errStr := err.Error()
			if strings.Contains(errStr, "not found") {
				return fmt.Errorf("memory not found: %s/%s", fromNS, fromKey)
			}
			return fmt.Errorf("get edges: %w", err)
		}
		if edges == nil {
			edges = []store.Edge{}
		}
		outputJSON(cmd, edges)
		return nil
	}

	// Create/remove mode — validate required flags
	if fromNS == "" || fromKey == "" || toNS == "" || toKey == "" || rel == "" {
		return fmt.Errorf("required flags: --from-key, --to-key, --rel (and --ns or --from-ns/--to-ns)")
	}

	if !validEdgeRels[rel] {
		return fmt.Errorf("invalid --rel %q — must be one of: relates_to, contradicts, depends_on, refines, contains, merged_into", rel)
	}

	if rm {
		err := st.DeleteEdge(cmd.Context(), store.EdgeParams{
			FromNS:  fromNS,
			FromKey: fromKey,
			ToNS:    toNS,
			ToKey:   toKey,
			Rel:     rel,
		})
		if err != nil {
			errStr := err.Error()
			if strings.Contains(errStr, "not found") {
				return fmt.Errorf("one or both memories not found: %s/%s or %s/%s", fromNS, fromKey, toNS, toKey)
			}
			return fmt.Errorf("delete edge: %w", err)
		}
		outputJSON(cmd, map[string]string{"status": "deleted", "rel": rel})
		return nil
	}

	edge, err := st.CreateEdge(cmd.Context(), store.EdgeParams{
		FromNS:  fromNS,
		FromKey: fromKey,
		ToNS:    toNS,
		ToKey:   toKey,
		Rel:     rel,
		Weight:  weight,
	})
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "not found") {
			return fmt.Errorf("one or both memories not found: %s/%s or %s/%s", fromNS, fromKey, toNS, toKey)
		}
		return fmt.Errorf("create edge: %w", err)
	}

	outputJSON(cmd, edge)
	return nil
}

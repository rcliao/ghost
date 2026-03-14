package cli

import (
	"fmt"
	"strings"

	"github.com/rcliao/ghost/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{
		Use:   "consolidate",
		Short: "Create a summary memory that contains multiple source memories",
		Long: `Consolidate creates a new summary memory and links it to source memories
via 'contains' edges. When the summary appears in context, its children are
suppressed to reduce redundancy.

The summary content must be provided by the caller (no LLM calls inside ghost).`,
		RunE: runConsolidate,
	}

	cmd.Flags().StringP("ns", "n", "", "Namespace")
	cmd.Flags().String("summary-key", "", "Key for the new summary memory")
	cmd.Flags().String("keys", "", "Comma-separated source memory keys to consolidate")
	cmd.Flags().String("content", "", "Summary content text")
	cmd.Flags().String("kind", "semantic", "Memory kind for the summary")
	cmd.Flags().Float64("importance", 0.7, "Importance for the summary (default: 0.7)")
	cmd.Flags().StringSlice("tags", nil, "Tags for the summary memory")

	cmd.MarkFlagRequired("ns")
	cmd.MarkFlagRequired("summary-key")
	cmd.MarkFlagRequired("keys")
	cmd.MarkFlagRequired("content")

	RootCmd.AddCommand(cmd)
}

func runConsolidate(cmd *cobra.Command, args []string) error {
	ns, _ := cmd.Flags().GetString("ns")
	summaryKey, _ := cmd.Flags().GetString("summary-key")
	keysStr, _ := cmd.Flags().GetString("keys")
	content, _ := cmd.Flags().GetString("content")
	kind, _ := cmd.Flags().GetString("kind")
	importance, _ := cmd.Flags().GetFloat64("importance")
	tags, _ := cmd.Flags().GetStringSlice("tags")

	sourceKeys := strings.Split(keysStr, ",")
	if len(sourceKeys) < 2 {
		return fmt.Errorf("--keys must contain at least 2 comma-separated keys")
	}

	// Trim whitespace from keys
	for i := range sourceKeys {
		sourceKeys[i] = strings.TrimSpace(sourceKeys[i])
		if sourceKeys[i] == "" {
			return fmt.Errorf("empty key in --keys at position %d", i)
		}
	}

	// Verify all source memories exist
	for _, key := range sourceKeys {
		_, err := st.Get(cmd.Context(), store.GetParams{NS: ns, Key: key})
		if err != nil {
			return fmt.Errorf("source memory not found: %s/%s", ns, key)
		}
	}

	// Create the summary memory
	mem, err := st.Put(cmd.Context(), store.PutParams{
		NS:         ns,
		Key:        summaryKey,
		Content:    content,
		Kind:       kind,
		Importance: importance,
		Tags:       tags,
	})
	if err != nil {
		return fmt.Errorf("create summary: %w", err)
	}

	// Create contains edges from summary → each source
	var edges []store.Edge
	for _, key := range sourceKeys {
		edge, err := st.CreateEdge(cmd.Context(), store.EdgeParams{
			FromNS: ns, FromKey: summaryKey,
			ToNS: ns, ToKey: key,
			Rel: "contains",
		})
		if err != nil {
			return fmt.Errorf("create contains edge to %s: %w", key, err)
		}
		edges = append(edges, *edge)
	}

	result := store.PutResult{
		Memory:     mem,
		AutoLinked: edges,
	}
	outputJSON(cmd, result)
	return nil
}

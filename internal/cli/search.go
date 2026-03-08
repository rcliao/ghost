package cli

import (
	"fmt"
	"strings"

	"github.com/rcliao/ghost/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Search memories by keyword",
		Long:  "Search memory content, keys, and chunks for matching text.",
		Args:  cobra.MinimumNArgs(1),
		Run:   runSearch,
	}

	cmd.Flags().StringP("ns", "n", "", "Filter by namespace (supports prefix: 'ns:*')")
	cmd.Flags().String("kind", "", "Filter by kind")
	cmd.Flags().IntP("limit", "l", 20, "Max results")

	RootCmd.AddCommand(cmd)
}

func runSearch(cmd *cobra.Command, args []string) {
	ns, _ := cmd.Flags().GetString("ns")
	kind, _ := cmd.Flags().GetString("kind")
	limit, _ := cmd.Flags().GetInt("limit")
	query := strings.Join(args, " ")

	if err := validateKind(kind); err != nil {
		exitErr("search", err)
	}
	if err := validateLimit(limit); err != nil {
		exitErr("search", err)
	}

	results, err := st.Search(cmd.Context(), store.SearchParams{
		NS:    ns,
		Query: query,
		Kind:  kind,
		Limit: limit,
	})
	if err != nil {
		exitErr("search", err)
	}

	if formatFlag == "text" {
		w := writer(cmd)
		for _, r := range results {
			fmt.Fprintln(w, r.Content)
		}
		return
	}

	if len(results) == 0 {
		outputText(cmd, "[]")
		return
	}

	outputJSON(cmd, results)
}

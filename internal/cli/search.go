package cli

import (
	"fmt"
	"strings"
	"time"

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
	cmd.Flags().String("after", "", "Only memories created after this date (YYYY-MM-DD or RFC3339)")
	cmd.Flags().String("before", "", "Only memories created before this date (YYYY-MM-DD or RFC3339)")

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

	sp := store.SearchParams{
		NS:    ns,
		Query: query,
		Kind:  kind,
		Limit: limit,
	}
	if afterStr, _ := cmd.Flags().GetString("after"); afterStr != "" {
		if t, err := parseFlexDate(afterStr); err == nil {
			sp.After = t
		}
	}
	if beforeStr, _ := cmd.Flags().GetString("before"); beforeStr != "" {
		if t, err := parseFlexDate(beforeStr); err == nil {
			sp.Before = t
		}
	}

	results, err := st.Search(cmd.Context(), sp)
	if err != nil {
		exitErr("search", err)
	}

	if len(results) == 0 && formatFlag == "text" {
		fmt.Fprintln(cmd.OutOrStdout(), "No results found.")
		return
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

// parseFlexDate parses a date string in various formats.
func parseFlexDate(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse date: %q (expected YYYY-MM-DD or RFC3339)", s)
}

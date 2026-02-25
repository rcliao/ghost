package cli

import (
	"fmt"

	"github.com/rcliao/agent-memory/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{
		Use:   "files [path]",
		Short: "Find memories linked to a file path",
		Long:  "Find all memories that reference the given file path.",
		Args:  cobra.ExactArgs(1),
		Run:   runFiles,
	}

	cmd.Flags().String("rel", "", "Filter by relationship (modified, created, deleted, read)")
	cmd.Flags().IntP("limit", "l", 20, "Max results")

	RootCmd.AddCommand(cmd)
}

func runFiles(cmd *cobra.Command, args []string) {
	path := args[0]
	rel, _ := cmd.Flags().GetString("rel")
	limit, _ := cmd.Flags().GetInt("limit")

	if rel != "" && !validFileRels[rel] {
		exitErr("files", fmt.Errorf("invalid --rel %q — must be one of: modified, created, deleted, read", rel))
	}
	if err := validateLimit(limit); err != nil {
		exitErr("files", err)
	}

	memories, err := st.FindByFile(cmd.Context(), store.FindByFileParams{
		Path:  path,
		Rel:   rel,
		Limit: limit,
	})
	if err != nil {
		exitErr("files", err)
	}

	if formatFlag == "text" {
		w := writer(cmd)
		for _, m := range memories {
			fmt.Fprintf(w, "%s/%s: %s\n", m.NS, m.Key, m.Content)
		}
		return
	}

	if len(memories) == 0 {
		outputText(cmd, "[]")
		return
	}

	outputJSON(cmd, memories)
}

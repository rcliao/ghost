package cli

import (
	"fmt"
	"strings"

	"github.com/rcliao/agent-memory/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Show full version history for a memory",
		Long:  "Shows the complete timeline of changes for a namespace/key pair, including deleted versions.",
		Run:   runHistory,
	}

	cmd.Flags().StringP("ns", "n", "", "Namespace (required)")
	cmd.Flags().StringP("key", "k", "", "Key (required)")

	cmd.MarkFlagRequired("ns")
	cmd.MarkFlagRequired("key")

	RootCmd.AddCommand(cmd)
}

func runHistory(cmd *cobra.Command, args []string) {
	ns, _ := cmd.Flags().GetString("ns")
	key, _ := cmd.Flags().GetString("key")

	memories, err := st.History(cmd.Context(), store.HistoryParams{
		NS:  ns,
		Key: key,
	})
	if err != nil {
		exitErr("history", fmt.Errorf("failed to retrieve history for %s/%s: %w", ns, key, err))
	}

	if formatFlag == "text" {
		w := writer(cmd)
		fmt.Fprintf(w, "History: %s/%s (%d version", ns, key, len(memories))
		if len(memories) != 1 {
			fmt.Fprint(w, "s")
		}
		fmt.Fprintln(w, ")")
		fmt.Fprintln(w, strings.Repeat("-", 60))

		for _, m := range memories {
			status := "active"
			if m.DeletedAt != nil {
				status = "deleted " + m.DeletedAt.Format("2006-01-02 15:04:05")
			}
			if m.ExpiresAt != nil {
				status += " (expires " + m.ExpiresAt.Format("2006-01-02 15:04:05") + ")"
			}

			fmt.Fprintf(w, "\nv%d  %s  [%s]\n", m.Version, m.CreatedAt.Format("2006-01-02 15:04:05"), status)
			if m.Supersedes != "" {
				fmt.Fprintf(w, "  supersedes: %s\n", m.Supersedes)
			}
			fmt.Fprintf(w, "  kind=%s  priority=%s", m.Kind, m.Priority)
			if len(m.Tags) > 0 {
				fmt.Fprintf(w, "  tags=%s", strings.Join(m.Tags, ","))
			}
			fmt.Fprintln(w)

			// Show content preview (first 200 chars)
			content := m.Content
			if len(content) > 200 {
				content = content[:200] + "..."
			}
			fmt.Fprintf(w, "  %s\n", content)
		}
		return
	}

	outputJSON(cmd, memories)
}

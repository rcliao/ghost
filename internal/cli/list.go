package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rcliao/agent-memory/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List memories",
		Run:   runList,
	}

	cmd.Flags().StringP("ns", "n", "", "Filter by namespace (supports prefix: 'ns:*')")
	cmd.Flags().String("kind", "", "Filter by kind")
	cmd.Flags().StringP("tags", "t", "", "Filter by tags (comma-separated)")
	cmd.Flags().IntP("limit", "l", 20, "Max results")
	cmd.Flags().Bool("keys-only", false, "Only output ns/key pairs")
	cmd.Flags().Bool("compact", false, "Output one JSON object per line (JSONL)")

	RootCmd.AddCommand(cmd)
}

func runList(cmd *cobra.Command, args []string) {
	ns, _ := cmd.Flags().GetString("ns")
	kind, _ := cmd.Flags().GetString("kind")
	tagsStr, _ := cmd.Flags().GetString("tags")
	limit, _ := cmd.Flags().GetInt("limit")
	keysOnly, _ := cmd.Flags().GetBool("keys-only")
	compact, _ := cmd.Flags().GetBool("compact")

	if err := validateKind(kind); err != nil {
		exitErr("list", err)
	}
	if err := validateLimit(limit); err != nil {
		exitErr("list", err)
	}

	var tags []string
	if tagsStr != "" {
		for _, t := range strings.Split(tagsStr, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				tags = append(tags, t)
			}
		}
	}

	memories, err := st.List(cmd.Context(), store.ListParams{
		NS:    ns,
		Kind:  kind,
		Tags:  tags,
		Limit: limit,
	})
	if err != nil {
		exitErr("list", err)
	}

	if compact {
		enc := json.NewEncoder(writer(cmd))
		for _, m := range memories {
			if keysOnly {
				enc.Encode(struct {
					NS  string `json:"ns"`
					Key string `json:"key"`
				}{NS: m.NS, Key: m.Key})
			} else {
				enc.Encode(m)
			}
		}
		return
	}

	if keysOnly {
		if formatFlag == "text" {
			w := writer(cmd)
			for _, m := range memories {
				fmt.Fprintf(w, "%s/%s\n", m.NS, m.Key)
			}
			return
		}
		type keyEntry struct {
			NS  string `json:"ns"`
			Key string `json:"key"`
		}
		keys := make([]keyEntry, len(memories))
		for i, m := range memories {
			keys[i] = keyEntry{NS: m.NS, Key: m.Key}
		}
		outputJSON(cmd, keys)
		return
	}

	if formatFlag == "text" {
		w := writer(cmd)
		for _, m := range memories {
			fmt.Fprintln(w, m.Content)
		}
		return
	}

	outputJSON(cmd, memories)
}

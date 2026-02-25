package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

func init() {
	tagsCmd := &cobra.Command{
		Use:   "tags",
		Short: "Tag management across memories",
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all tags with counts",
		Run:   runTagsList,
	}
	listCmd.Flags().StringP("ns", "n", "", "Filter by namespace (supports prefix with *)")

	renameCmd := &cobra.Command{
		Use:   "rename [old] [new]",
		Short: "Rename a tag across all memories",
		Args:  cobra.ExactArgs(2),
		Run:   runTagsRename,
	}
	renameCmd.Flags().StringP("ns", "n", "", "Limit to namespace (supports prefix with *)")

	rmCmd := &cobra.Command{
		Use:   "rm [tag]",
		Short: "Remove a tag from all memories",
		Args:  cobra.ExactArgs(1),
		Run:   runTagsRm,
	}
	rmCmd.Flags().StringP("ns", "n", "", "Limit to namespace (supports prefix with *)")

	tagsCmd.AddCommand(listCmd, renameCmd, rmCmd)
	RootCmd.AddCommand(tagsCmd)
}

func runTagsList(cmd *cobra.Command, args []string) {
	ns, _ := cmd.Flags().GetString("ns")

	tags, err := st.ListTags(cmd.Context(), ns)
	if err != nil {
		exitErr("tags list", fmt.Errorf("failed to list tags: %w", err))
	}

	// Sort by count descending, then by name
	sort.Slice(tags, func(i, j int) bool {
		if tags[i].Count != tags[j].Count {
			return tags[i].Count > tags[j].Count
		}
		return tags[i].Tag < tags[j].Tag
	})

	if formatFlag == "text" {
		if len(tags) == 0 {
			outputText(cmd, "(no tags)")
			return
		}
		w := writer(cmd)
		for _, t := range tags {
			fmt.Fprintf(w, "%-30s %d memories\n", t.Tag, t.Count)
		}
		return
	}

	outputJSON(cmd, tags)
}

func runTagsRename(cmd *cobra.Command, args []string) {
	oldTag := strings.TrimSpace(args[0])
	newTag := strings.TrimSpace(args[1])
	ns, _ := cmd.Flags().GetString("ns")

	if oldTag == "" {
		exitErr("tags rename", fmt.Errorf("old tag name cannot be empty"))
	}
	if newTag == "" {
		exitErr("tags rename", fmt.Errorf("new tag name cannot be empty"))
	}
	if oldTag == newTag {
		exitErr("tags rename", fmt.Errorf("old and new tag names are the same"))
	}

	count, err := st.RenameTag(cmd.Context(), oldTag, newTag, ns)
	if err != nil {
		exitErr("tags rename", fmt.Errorf("failed to rename tag %q to %q: %w", oldTag, newTag, err))
	}

	if count == 0 {
		fmt.Fprintf(os.Stderr, "warning: tag %q not found in any memories\n", oldTag)
	}

	outputJSONCompact(cmd, struct {
		OK       bool   `json:"ok"`
		OldTag   string `json:"old_tag"`
		NewTag   string `json:"new_tag"`
		Affected int    `json:"affected"`
	}{OK: true, OldTag: oldTag, NewTag: newTag, Affected: count})
}

func runTagsRm(cmd *cobra.Command, args []string) {
	tag := strings.TrimSpace(args[0])
	ns, _ := cmd.Flags().GetString("ns")

	if tag == "" {
		exitErr("tags rm", fmt.Errorf("tag name cannot be empty"))
	}

	count, err := st.RemoveTag(cmd.Context(), tag, ns)
	if err != nil {
		exitErr("tags rm", fmt.Errorf("failed to remove tag %q: %w", tag, err))
	}

	if count == 0 {
		fmt.Fprintf(os.Stderr, "warning: tag %q not found in any memories\n", tag)
	}

	outputJSONCompact(cmd, struct {
		OK       bool   `json:"ok"`
		Tag      string `json:"tag"`
		Affected int    `json:"affected"`
	}{OK: true, Tag: tag, Affected: count})
}

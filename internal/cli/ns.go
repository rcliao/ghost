package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/rcliao/ghost/internal/store"
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

	treeCmd := &cobra.Command{
		Use:   "tree",
		Short: "Show namespace hierarchy as a tree",
		Run:   runNSTree,
	}

	rmCmd := &cobra.Command{
		Use:   "rm [namespace]",
		Short: "Delete all memories in a namespace",
		Long:  "Delete all memories in a namespace. Supports prefix matching (e.g. 'reflect:*').",
		Args:  cobra.ExactArgs(1),
		Run:   runNSRm,
	}
	rmCmd.Flags().Bool("hard", false, "Permanently delete (irreversible)")

	nsCmd.AddCommand(listCmd, treeCmd, rmCmd)
	RootCmd.AddCommand(nsCmd)
}

func runNSList(cmd *cobra.Command, args []string) {
	rows, err := st.ListNamespaces(cmd.Context())
	if err != nil {
		exitErr("list namespaces", err)
	}

	if formatFlag == "text" {
		w := writer(cmd)
		for _, ns := range rows {
			fmt.Fprintf(w, "%-30s %d memories, %d keys\n", ns.NS, ns.Count, ns.Keys)
		}
		return
	}

	outputJSON(cmd, rows)
}

// nsNode represents a node in the namespace tree.
type nsNode struct {
	name     string
	children map[string]*nsNode
	stats    *store.NamespaceStats // non-nil for leaf namespaces with data
}

func runNSTree(cmd *cobra.Command, args []string) {
	rows, err := st.ListNamespaces(cmd.Context())
	if err != nil {
		exitErr("ns tree", err)
	}

	if len(rows) == 0 {
		if formatFlag == "text" {
			outputText(cmd, "(no namespaces)")
		} else {
			outputJSON(cmd, rows)
		}
		return
	}

	if formatFlag == "text" {
		// Build tree
		root := &nsNode{children: make(map[string]*nsNode)}
		for i := range rows {
			ns := &rows[i]
			segments := store.NSSegments(ns.NS)
			node := root
			for _, seg := range segments {
				child, ok := node.children[seg]
				if !ok {
					child = &nsNode{name: seg, children: make(map[string]*nsNode)}
					node.children[seg] = child
				}
				node = child
			}
			node.stats = ns
		}

		w := writer(cmd)
		printTreeChildren(w, root, "")
		return
	}

	// JSON output: return a flat list enhanced with depth info
	type nsTreeEntry struct {
		NS    string `json:"ns"`
		Depth int    `json:"depth"`
		Count int    `json:"count"`
		Keys  int    `json:"keys"`
	}
	var entries []nsTreeEntry
	for _, ns := range rows {
		entries = append(entries, nsTreeEntry{
			NS:    ns.NS,
			Depth: store.NSDepth(ns.NS),
			Count: ns.Count,
			Keys:  ns.Keys,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].NS < entries[j].NS
	})
	outputJSON(cmd, entries)
}

func printTreeChildren(w io.Writer, node *nsNode, prefix string) {
	// Collect and sort child keys
	keys := make([]string, 0, len(node.children))
	for k := range node.children {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for i, key := range keys {
		child := node.children[key]
		isLast := i == len(keys)-1

		connector := "├── "
		if isLast {
			connector = "└── "
		}

		line := prefix + connector + key
		if child.stats != nil {
			line += fmt.Sprintf("  (%d memories, %d keys)", child.stats.Count, child.stats.Keys)
		}
		fmt.Fprintln(w, line)

		childPrefix := prefix + "│   "
		if isLast {
			childPrefix = prefix + "    "
		}
		printTreeChildren(w, child, childPrefix)
	}
}

func runNSRm(cmd *cobra.Command, args []string) {
	ns := args[0]
	hard, _ := cmd.Flags().GetBool("hard")

	if hard {
		fmt.Fprintf(os.Stderr, "warning: --hard permanently deletes all memories in namespace %q (cannot be undone)\n", ns)
	}

	count, err := st.RmNamespace(cmd.Context(), ns, hard)
	if err != nil {
		errStr := err.Error()
		switch {
		case strings.Contains(errStr, "cannot be empty"):
			exitErr("ns rm", fmt.Errorf("namespace argument is required"))
		default:
			exitErr("ns rm", fmt.Errorf("failed to delete namespace %q: %w", ns, err))
		}
	}

	if count == 0 {
		fmt.Fprintf(os.Stderr, "warning: no memories found in namespace %q\n", ns)
	}

	action := "soft-deleted"
	if hard {
		action = "permanently deleted"
	}

	outputJSONCompact(cmd, struct {
		OK      bool   `json:"ok"`
		NS      string `json:"ns"`
		Action  string `json:"action"`
		Deleted int64  `json:"deleted"`
	}{OK: true, NS: ns, Action: action, Deleted: count})
}

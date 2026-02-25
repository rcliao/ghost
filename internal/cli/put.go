package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/rcliao/agent-memory/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{
		Use:   "put [content]",
		Short: "Store a memory",
		Long:  "Store a memory. Content can be a positional arg or piped via stdin.",
		Run:   runPut,
	}

	cmd.Flags().StringP("ns", "n", "", "Namespace (required)")
	cmd.Flags().StringP("key", "k", "", "Key (required)")
	cmd.Flags().String("kind", "semantic", "Kind: semantic, episodic, procedural")
	cmd.Flags().StringP("tags", "t", "", "Comma-separated tags")
	cmd.Flags().StringP("priority", "p", "normal", "Priority: low, normal, high, critical")
	cmd.Flags().String("meta", "", "JSON metadata")
	cmd.Flags().String("ttl", "", "Time-to-live (e.g. 7d, 24h, 30m)")
	cmd.Flags().String("files", "", "Comma-separated file paths to link")
	cmd.Flags().String("file-rel", "modified", "File relationship: modified, created, deleted, read")

	cmd.MarkFlagRequired("ns")
	cmd.MarkFlagRequired("key")

	RootCmd.AddCommand(cmd)
}

func runPut(cmd *cobra.Command, args []string) {
	ns, _ := cmd.Flags().GetString("ns")
	key, _ := cmd.Flags().GetString("key")
	kind, _ := cmd.Flags().GetString("kind")
	tagsStr, _ := cmd.Flags().GetString("tags")
	priority, _ := cmd.Flags().GetString("priority")
	meta, _ := cmd.Flags().GetString("meta")
	ttl, _ := cmd.Flags().GetString("ttl")
	filesStr, _ := cmd.Flags().GetString("files")
	fileRel, _ := cmd.Flags().GetString("file-rel")

	// Validate namespace.
	if err := store.ValidateNS(ns); err != nil {
		exitErr("put", fmt.Errorf("invalid namespace %q — use letters, digits, hyphens, and colons (e.g. 'project:logs')", ns))
	}

	// Validate enum flags.
	if err := validateKind(kind); err != nil {
		exitErr("put", err)
	}
	if err := validatePriority(priority); err != nil {
		exitErr("put", err)
	}
	if !validFileRels[fileRel] {
		exitErr("put", fmt.Errorf("invalid --file-rel %q — must be one of: modified, created, deleted, read", fileRel))
	}
	if meta != "" {
		if !json.Valid([]byte(meta)) {
			exitErr("put", fmt.Errorf("invalid --meta: not valid JSON (got %q)", meta))
		}
	}
	if ttl != "" {
		if _, err := store.ParseTTL(ttl); err != nil {
			exitErr("put", fmt.Errorf("invalid --ttl %q — use a duration like 7d, 24h, or 30m", ttl))
		}
	}

	// Get content: positional arg first, then check stdin
	var content string
	if len(args) > 0 {
		content = strings.Join(args, " ")
	} else {
		stat, _ := os.Stdin.Stat()
		if (stat.Mode() & os.ModeCharDevice) == 0 {
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				exitErr("read stdin", err)
			}
			content = string(b)
		}
	}

	if strings.TrimSpace(content) == "" {
		exitErr("put", fmt.Errorf("content is required (positional arg or stdin)"))
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

	var files []store.FileParam
	if filesStr != "" {
		for _, f := range strings.Split(filesStr, ",") {
			f = strings.TrimSpace(f)
			if f != "" {
				files = append(files, store.FileParam{Path: f, Rel: fileRel})
			}
		}
	}

	mem, err := st.Put(cmd.Context(), store.PutParams{
		NS:       ns,
		Key:      key,
		Content:  strings.TrimSpace(content),
		Kind:     kind,
		Tags:     tags,
		Priority: priority,
		Meta:     meta,
		TTL:      ttl,
		Files:    files,
	})
	if err != nil {
		exitErr("put", err)
	}

	outputJSONCompact(cmd, mem)
}

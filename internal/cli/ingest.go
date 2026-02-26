package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rcliao/agent-memory/internal/ingest"
	"github.com/rcliao/agent-memory/internal/model"
	"github.com/rcliao/agent-memory/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{
		Use:   "ingest [path]",
		Short: "Import markdown files into agent-memory",
		Long:  "Parse markdown files by ## headings and store each section as a memory. Accepts a file or directory.",
		Args:  cobra.ExactArgs(1),
		Run:   runIngest,
	}

	cmd.Flags().StringP("ns", "n", "", "Namespace for imported memories (required)")
	cmd.Flags().String("kind", "semantic", "Memory kind: semantic, episodic, procedural")
	cmd.Flags().StringP("tags", "t", "", "Comma-separated tags to apply to all memories")
	cmd.Flags().StringP("priority", "p", "normal", "Priority: low, normal, high, critical")
	cmd.Flags().Bool("dry-run", false, "Output JSON to stdout instead of storing")
	cmd.Flags().String("meta", "", "JSON metadata to attach to all memories")
	cmd.Flags().Bool("date-ns", false, "For directories, append filename date to namespace (e.g. ns + 2026-02-15.md -> ns:2026-02-15)")

	cmd.MarkFlagRequired("ns")

	RootCmd.AddCommand(cmd)
}

func runIngest(cmd *cobra.Command, args []string) {
	path := args[0]
	ns, _ := cmd.Flags().GetString("ns")
	kind, _ := cmd.Flags().GetString("kind")
	tagsStr, _ := cmd.Flags().GetString("tags")
	priority, _ := cmd.Flags().GetString("priority")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	meta, _ := cmd.Flags().GetString("meta")
	dateNS, _ := cmd.Flags().GetBool("date-ns")

	// Validate flags.
	if err := store.ValidateNS(ns); err != nil {
		exitErr("ingest", fmt.Errorf("invalid namespace %q — use letters, digits, hyphens, and colons (e.g. 'openclaw:context')", ns))
	}
	if err := validateKind(kind); err != nil {
		exitErr("ingest", err)
	}
	if err := validatePriority(priority); err != nil {
		exitErr("ingest", err)
	}
	if meta != "" && !json.Valid([]byte(meta)) {
		exitErr("ingest", fmt.Errorf("invalid --meta: not valid JSON (got %q)", meta))
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

	// Resolve files.
	files, err := resolveFiles(path)
	if err != nil {
		exitErr("ingest", err)
	}
	if len(files) == 0 {
		exitErr("ingest", fmt.Errorf("no .md files found at %q", path))
	}

	// Parse all files into sections.
	type memoryEntry struct {
		NS      string
		Key     string
		Content string
	}
	var entries []memoryEntry

	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			exitErr("ingest", fmt.Errorf("failed to read %s: %w", f, err))
		}

		content := string(data)
		if strings.TrimSpace(content) == "" {
			continue
		}

		basename := filepath.Base(f)
		sections := ingest.ParseMarkdown(content, basename)

		// Determine effective namespace for this file.
		effectiveNS := ns
		if dateNS {
			if date := ingest.FilenameDate(basename); date != "" {
				effectiveNS = ns + ":" + date
			}
		}

		for _, sec := range sections {
			key := ingest.SectionKey(sec, basename)
			// For preamble sections, use _preamble key.
			if sec.Heading == "" {
				key = ingest.PreambleKey(basename)
			}

			if strings.TrimSpace(sec.Content) == "" && sec.Heading == "" {
				continue
			}

			// Build content: include heading as context if present.
			body := sec.Content
			if sec.Heading != "" && body == "" {
				// Empty section with just a heading — skip.
				continue
			}

			entries = append(entries, memoryEntry{
				NS:      effectiveNS,
				Key:     key,
				Content: body,
			})
		}
	}

	if len(entries) == 0 {
		exitErr("ingest", fmt.Errorf("no content sections found in %q", path))
	}

	// Dry-run: output as JSON.
	if dryRun {
		var memories []model.Memory
		for _, e := range entries {
			memories = append(memories, model.Memory{
				NS:       e.NS,
				Key:      e.Key,
				Content:  e.Content,
				Kind:     kind,
				Tags:     tags,
				Priority: priority,
				Meta:     meta,
			})
		}
		outputJSON(cmd, memories)
		return
	}

	// Store each section.
	ingested := 0
	for _, e := range entries {
		// Validate derived namespace (may include date suffix).
		if err := store.ValidateNS(e.NS); err != nil {
			exitErr("ingest", fmt.Errorf("invalid derived namespace %q: %w", e.NS, err))
		}

		_, err := st.Put(cmd.Context(), store.PutParams{
			NS:       e.NS,
			Key:      e.Key,
			Content:  e.Content,
			Kind:     kind,
			Tags:     tags,
			Priority: priority,
			Meta:     meta,
		})
		if err != nil {
			exitErr("ingest", fmt.Errorf("failed to store %s/%s: %w", e.NS, e.Key, err))
		}
		ingested++
	}

	outputJSONCompact(cmd, struct {
		OK       bool `json:"ok"`
		Ingested int  `json:"ingested"`
		Files    int  `json:"files"`
	}{OK: true, Ingested: ingested, Files: len(files)})
}

// resolveFiles returns a list of .md file paths from a file or directory path.
func resolveFiles(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("cannot access %q: %w", path, err)
	}

	if !info.IsDir() {
		return []string{path}, nil
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read directory %q: %w", path, err)
	}

	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			files = append(files, filepath.Join(path, e.Name()))
		}
	}
	return files, nil
}

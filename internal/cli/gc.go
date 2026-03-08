package cli

import (
	"fmt"
	"os"

	"github.com/rcliao/ghost/internal/store"
	"github.com/spf13/cobra"
)

// gcOutput is the structured result of a gc run.
type gcOutput struct {
	Mode        string `json:"mode"`
	Deleted     int64  `json:"deleted,omitempty"`
	WouldDelete int64  `json:"would_delete,omitempty"`
	ChunksFreed int64  `json:"chunks_freed,omitempty"`
	Protected   int64  `json:"protected,omitempty"`
	Remaining   int64  `json:"remaining"`
	DBSizeBytes int64  `json:"db_size_bytes"`
}

func (o gcOutput) textSummary() string {
	switch o.Mode {
	case "expired":
		return fmt.Sprintf("gc: deleted %d expired memories, freed %d chunks, %d remaining (%s)",
			o.Deleted, o.ChunksFreed, o.Remaining, formatBytes(o.DBSizeBytes))
	case "expired_dry_run":
		return fmt.Sprintf("gc: would delete %d expired memories (%d chunks), %d remaining (%s)",
			o.WouldDelete, o.ChunksFreed, o.Remaining, formatBytes(o.DBSizeBytes))
	case "stale":
		return fmt.Sprintf("gc: deleted %d stale memories, %d protected (high/critical), %d remaining (%s)",
			o.Deleted, o.Protected, o.Remaining, formatBytes(o.DBSizeBytes))
	case "stale_dry_run":
		return fmt.Sprintf("gc: would delete %d stale memories, %d protected (high/critical), %d remaining (%s)",
			o.WouldDelete, o.Protected, o.Remaining, formatBytes(o.DBSizeBytes))
	default:
		return fmt.Sprintf("gc: %d remaining (%s)", o.Remaining, formatBytes(o.DBSizeBytes))
	}
}

func formatBytes(b int64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func init() {
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Delete expired memories",
		Run:   runGC,
	}

	cmd.Flags().Bool("dry-run", false, "Report what would be deleted without deleting")
	cmd.Flags().String("stale", "", "Soft-delete memories not accessed in duration (e.g. 30d)")
	RootCmd.AddCommand(cmd)
}

func runGC(cmd *cobra.Command, args []string) {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	stale, _ := cmd.Flags().GetString("stale")

	out := gcOutput{}

	if stale != "" {
		d, err := store.ParseTTL(stale)
		if err != nil {
			exitErr("gc", fmt.Errorf("invalid --stale %q — use a duration like 30d, 24h, or 60m", stale))
		}

		if dryRun {
			out.Mode = "stale_dry_run"
			result, err := st.GCStaleDryRun(cmd.Context(), d)
			if err != nil {
				exitErr("gc stale dry-run", err)
			}
			out.WouldDelete = result.MemoriesDeleted
			out.Protected = result.ProtectedCount
		} else {
			out.Mode = "stale"
			result, err := st.GCStale(cmd.Context(), d)
			if err != nil {
				exitErr("gc stale", err)
			}
			out.Deleted = result.MemoriesDeleted
			out.Protected = result.ProtectedCount
		}
	} else if dryRun {
		out.Mode = "expired_dry_run"
		result, err := st.GCDryRun(cmd.Context())
		if err != nil {
			exitErr("gc dry-run", err)
		}
		out.WouldDelete = result.MemoriesDeleted
		out.ChunksFreed = result.ChunksFreed
	} else {
		out.Mode = "expired"
		result, err := st.GC(cmd.Context())
		if err != nil {
			exitErr("gc", err)
		}
		out.Deleted = result.MemoriesDeleted
		out.ChunksFreed = result.ChunksFreed
	}

	out.Remaining, _ = st.MemoryCount(cmd.Context())

	if info, err := os.Stat(getDBPath()); err == nil {
		out.DBSizeBytes = info.Size()
	}

	if formatFlag == "text" {
		outputText(cmd, out.textSummary())
		return
	}

	outputJSONCompact(cmd, out)
}

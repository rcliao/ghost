package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/rcliao/agent-memory/internal/model"
	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import memories from JSON",
		Long:  "Import memories from JSON (stdin or file). Expects the format produced by export.",
		Run:   runImport,
	}

	RootCmd.AddCommand(cmd)
}

func runImport(cmd *cobra.Command, args []string) {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		exitErr("import", fmt.Errorf("failed to read stdin: %w", err))
	}

	if len(data) == 0 {
		exitErr("import", fmt.Errorf("no input received — pipe JSON via stdin, e.g.: agent-memory export | agent-memory import"))
	}

	var memories []model.Memory
	if err := json.Unmarshal(data, &memories); err != nil {
		exitErr("import", fmt.Errorf("invalid JSON input: %w — expected an array of memory objects (use 'export' to produce the correct format)", err))
	}

	if len(memories) == 0 {
		exitErr("import", fmt.Errorf("input contains an empty array — nothing to import"))
	}

	imported, err := st.Import(cmd.Context(), memories)
	if err != nil {
		exitErr("import", fmt.Errorf("failed to import %d memories: %w", len(memories), err))
	}

	outputJSONCompact(cmd, struct {
		OK       bool `json:"ok"`
		Imported int  `json:"imported"`
	}{OK: true, Imported: imported})
}

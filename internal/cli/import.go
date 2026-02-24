package cli

import (
	"encoding/json"
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
		exitErr("read stdin", err)
	}

	var memories []model.Memory
	if err := json.Unmarshal(data, &memories); err != nil {
		exitErr("parse json", err)
	}

	s, err := openStore()
	if err != nil {
		exitErr("open store", err)
	}
	defer s.Close()

	imported, err := s.Import(cmd.Context(), memories)
	if err != nil {
		exitErr("import", err)
	}

	outputJSONCompact(cmd, struct {
		OK       bool `json:"ok"`
		Imported int  `json:"imported"`
	}{OK: true, Imported: imported})
}

package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// outputJSON writes data as indented JSON to the command's output.
func outputJSON(cmd *cobra.Command, data any) {
	b, _ := json.MarshalIndent(data, "", "  ")
	fmt.Fprintln(cmd.OutOrStdout(), string(b))
}

// outputJSONCompact writes data as single-line JSON to the command's output.
func outputJSONCompact(cmd *cobra.Command, data any) {
	b, _ := json.Marshal(data)
	fmt.Fprintln(cmd.OutOrStdout(), string(b))
}

// outputText writes a single string line to the command's output.
func outputText(cmd *cobra.Command, line string) {
	fmt.Fprintln(cmd.OutOrStdout(), line)
}

// writer returns the command's output writer.
func writer(cmd *cobra.Command) io.Writer {
	return cmd.OutOrStdout()
}

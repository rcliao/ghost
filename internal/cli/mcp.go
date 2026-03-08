package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/rcliao/ghost/internal/mcpserver"
	"github.com/spf13/cobra"
)

func init() {
	RootCmd.AddCommand(mcpCmd)
}

var mcpCmd = &cobra.Command{
	Use:   "mcp-serve",
	Short: "Start MCP server over stdio",
	Long:  "Start a Model Context Protocol server over stdio transport. Exposes ghost memory operations as MCP tools for use with Claude Code, Claude Desktop, or any MCP client.",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		// Redirect any stray log output to stderr so stdout stays clean for MCP.
		fmt.Fprintln(os.Stderr, "ghost: MCP server starting...")

		return mcpserver.Serve(ctx, st)
	},
}

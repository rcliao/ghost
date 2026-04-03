package cli

import (
	"fmt"

	"github.com/rcliao/ghost/internal/dash"
	"github.com/spf13/cobra"
)

var dashPort int

func init() {
	dashCmd.Flags().IntVar(&dashPort, "port", 8675, "Port to listen on")
	RootCmd.AddCommand(dashCmd)
}

var dashCmd = &cobra.Command{
	Use:   "dash",
	Short: "Launch the memory dashboard in your browser",
	Long:  "Starts a local HTTP server serving an interactive memory dashboard for browsing, searching, and debugging memories.",
	RunE: func(cmd *cobra.Command, args []string) error {
		addr := fmt.Sprintf(":%d", dashPort)
		srv := dash.New(st, getDBPath())
		fmt.Fprintf(cmd.OutOrStdout(), "Ghost dashboard: http://localhost%s\n", addr)
		return srv.ListenAndServe(addr)
	},
}

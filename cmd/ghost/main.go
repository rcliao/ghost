package main

import (
	"os"

	"github.com/rcliao/ghost/internal/cli"
)

// Set via ldflags at build time.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	cli.SetVersionInfo(version, commit, date)
	if err := cli.RootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

package main

import (
	"os"

	"github.com/rcliao/ghost/internal/cli"
)

func main() {
	if err := cli.RootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

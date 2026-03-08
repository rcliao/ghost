// Package cli implements the ghost CLI commands.
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rcliao/ghost/internal/store"
	"github.com/spf13/cobra"
)

var (
	dbPath     string
	formatFlag string

	// st is the active store instance, opened by PersistentPreRunE.
	st store.Store
)

// OpenStoreFunc creates a store.Store. Override in tests to inject a mock.
var OpenStoreFunc = func() (store.Store, error) {
	return store.NewSQLiteStore(getDBPath())
}

// RootCmd is the top-level command.
var RootCmd = &cobra.Command{
	Use:   "ghost",
	Short: "Persistent memory for AI agents",
	Long:  "A tiny CLI for persistent agent memory. Text in, text out. SQLite-backed, single binary.",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if cmd.Run == nil && cmd.RunE == nil {
			return nil
		}
		var err error
		st, err = OpenStoreFunc()
		if err != nil {
			p := getDBPath()
			errStr := err.Error()
			switch {
			case strings.Contains(errStr, "database is locked"):
				return fmt.Errorf("cannot open store: database is locked — another process may be using %s", p)
			case strings.Contains(errStr, "permission denied"):
				return fmt.Errorf("cannot open store: permission denied for %s — check file/directory permissions", p)
			case strings.Contains(errStr, "no such file or directory"):
				return fmt.Errorf("cannot open store: directory does not exist for %s — check the --db path", p)
			default:
				return fmt.Errorf("cannot open store at %s: %w", p, err)
			}
		}
		return nil
	},
	PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
		if st != nil {
			err := st.Close()
			st = nil
			return err
		}
		return nil
	},
}

func init() {
	RootCmd.PersistentFlags().StringVarP(&dbPath, "db", "d", "", "Database path (default: $GHOST_DB or ~/.ghost/memory.db)")
	RootCmd.PersistentFlags().StringVarP(&formatFlag, "format", "f", "json", "Output format: json or text")
}

func getDBPath() string {
	if dbPath != "" {
		return dbPath
	}
	if env := os.Getenv("GHOST_DB"); env != "" {
		return env
	}
	// Fallback to legacy env var for backward compatibility.
	if env := os.Getenv("AGENT_MEMORY_DB"); env != "" {
		return env
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ghost", "memory.db")
}

func exitErr(msg string, err error) {
	errStr := err.Error()
	// Provide actionable hints for common errors.
	switch {
	case strings.Contains(errStr, "database is locked"):
		fmt.Fprintf(os.Stderr, "error: %s: database is locked — another process may be using it; try again\n", msg)
	case strings.Contains(errStr, "no such table"):
		fmt.Fprintf(os.Stderr, "error: %s: database appears corrupted or uninitialized — try removing and recreating the db file\n", msg)
	default:
		fmt.Fprintf(os.Stderr, "error: %s: %v\n", msg, err)
	}
	os.Exit(1)
}

// validKinds lists the accepted values for the --kind flag.
var validKinds = map[string]bool{"semantic": true, "episodic": true, "procedural": true}

// validPriorities lists the accepted values for the --priority flag.
var validPriorities = map[string]bool{"low": true, "normal": true, "high": true, "critical": true}

// validFileRels lists the accepted values for the --file-rel / --rel flag on files.
var validFileRels = map[string]bool{"modified": true, "created": true, "deleted": true, "read": true}

// validLinkRels lists the accepted values for the --rel flag on link.
var validLinkRels = map[string]bool{"relates_to": true, "contradicts": true, "depends_on": true, "refines": true}

func validateKind(kind string) error {
	if kind != "" && !validKinds[kind] {
		return fmt.Errorf("invalid kind %q — must be one of: semantic, episodic, procedural", kind)
	}
	return nil
}

func validatePriority(priority string) error {
	if priority != "" && !validPriorities[priority] {
		return fmt.Errorf("invalid priority %q — must be one of: low, normal, high, critical", priority)
	}
	return nil
}

func validateLimit(limit int) error {
	if limit <= 0 {
		return fmt.Errorf("--limit must be a positive number (got %d)", limit)
	}
	return nil
}

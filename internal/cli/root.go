// Package cli holds the cobra command tree. It consumes the persistence
// layer through the Store interface below and never touches SQL.
package cli

import (
	"context"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jwstover/td/internal/task"
)

// Store is the slice of the persistence layer the CLI needs.
type Store interface {
	AddTask(ctx context.Context, title string) (task.Task, error)
	ListLive(ctx context.Context) ([]task.Task, error)
	Close() error
}

// StoreFactory opens a Store at the given database path. main wires the
// concrete implementation in; commands call it lazily after flag parsing.
type StoreFactory func(ctx context.Context, dbPath string) (Store, error)

// Execute builds the command tree and runs it.
func Execute(open StoreFactory) error {
	return newRootCmd(open).Execute()
}

func newRootCmd(open StoreFactory) *cobra.Command {
	var dbPath string

	root := &cobra.Command{
		Use:           "td",
		Short:         "td is a terminal-native personal task tracker",
		SilenceUsage:  true,
		SilenceErrors: true,
		// TODO(owner): Gate 2 — running bare `td` launches the TUI.
		// Until then, show help.
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	root.PersistentFlags().StringVar(&dbPath, "db", "",
		"path to the SQLite database (default $TD_DB, then "+
			"$XDG_DATA_HOME/td/td.db)")

	openHere := func(ctx context.Context) (Store, error) {
		return open(ctx, resolveDBPath(dbPath))
	}
	root.AddCommand(newAddCmd(openHere))
	root.AddCommand(newLsCmd(openHere))
	return root
}

// resolveDBPath picks the database location: --db flag, then TD_DB, then
// the XDG data directory.
func resolveDBPath(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv("TD_DB"); env != "" {
		return env
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			// No home directory to anchor to; fall back to the
			// working directory rather than failing capture.
			return "td.db"
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "td", "td.db")
}

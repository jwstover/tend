// Package cli holds the cobra command tree. It consumes the persistence
// layer through the Store interface below and never touches SQL.
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/version"
)

// Store is the slice of the persistence layer the CLI needs.
type Store interface {
	AddTask(ctx context.Context, title string) (task.Task, error)
	ListLive(ctx context.Context) ([]task.Task, error)
	ListEvents(ctx context.Context, from, to time.Time) ([]task.Event, error)
	AddLogEntry(ctx context.Context, taskID *int64, body string) (task.LogEntry, error)
	ListLogEntries(ctx context.Context, from, to time.Time) ([]task.LogEntry, error)
	Close() error
}

// StoreFactory opens a Store at the given database path. main wires the
// concrete implementation in; commands call it lazily after flag parsing.
type StoreFactory func(ctx context.Context, dbPath string) (Store, error)

// TUIRunner launches the TUI against the database at dbPath and blocks
// until it exits. main wires it so cli never imports the tui package.
type TUIRunner func(ctx context.Context, dbPath string) error

// Execute builds the command tree and runs it.
func Execute(open StoreFactory, runTUI TUIRunner) error {
	return newRootCmd(open, runTUI).Execute()
}

func newRootCmd(open StoreFactory, runTUI TUIRunner) *cobra.Command {
	var dbPath string

	root := &cobra.Command{
		Use:           "tend",
		Short:         "tend is a terminal-native personal task tracker",
		Version:       version.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(cmd.Context(), resolveDBPath(dbPath))
		},
	}
	root.PersistentFlags().StringVar(&dbPath, "db", "",
		"path to the SQLite database (default $TEND_DB, then "+
			"$XDG_DATA_HOME/tend/tend.db)")

	openHere := func(ctx context.Context) (Store, error) {
		return open(ctx, resolveDBPath(dbPath))
	}
	root.AddCommand(newAddCmd(openHere))
	root.AddCommand(newLsCmd(openHere))
	root.AddCommand(newStandupCmd(openHere))
	root.AddCommand(newLogCmd(openHere))
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the tend version",
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version.String())
		},
	})
	return root
}

// resolveDBPath picks the database location: --db flag, then TEND_DB, then
// the XDG data directory.
func resolveDBPath(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv("TEND_DB"); env != "" {
		return env
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			// No home directory to anchor to; fall back to the
			// working directory rather than failing capture.
			return "tend.db"
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "tend", "tend.db")
}

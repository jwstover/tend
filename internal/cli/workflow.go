package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/runner"
)

// RunnerStoreFactory opens the runner.Store slice of the persistence
// layer at the given database path -- its own factory for the same
// reason MCPStoreFactory is: `tend workflow run` needs a different
// method set than the rest of the command tree.
type RunnerStoreFactory func(ctx context.Context, dbPath string) (runner.Store, error)

// newWorkflowCmd wires `tend workflow`: the runner behind agent
// workflows (tend task #171) and what is needed to re-enter one. `run`
// is hidden -- it is the process the TUI's `w` chord (and `tend workflow
// start`, task #185) hosts in tmux, not a user command. `resume` is the
// user's way back into a run whose runner died. The rest of the
// scriptable surface (ls, start, status, approve, cancel, logs) is #185.
func newWorkflowCmd(open func(ctx context.Context) (runner.Store, error), dbPath func() string) *cobra.Command {
	root := &cobra.Command{
		Use:     "workflow",
		Aliases: []string{"wf"},
		Short:   "Drive and resume agent workflow runs",
		Long: "Agent workflows are multi-step agent procedures a task can run headlessly. " +
			"A run is driven by its own runner process inside a tmux session on tend's " +
			"server (`tmux -L tend`); `resume` starts a new runner for a run whose runner died.",
	}
	root.AddCommand(newWorkflowRunCmd(open), newWorkflowResumeCmd(open, dbPath))
	return root
}

// newWorkflowRunCmd is the runner itself. It claims the run, drives it to
// a terminal state (or a pause), and exits; its progress goes to stdout
// -- the tmux pane -- and to runner.log in the run's log directory, so
// what it said survives the pane. SIGINT, SIGTERM and SIGHUP (what
// `tmux kill-session` delivers) cancel the runner's context, which
// SIGTERMs the step's claude and leaves the run for `resume`.
func newWorkflowRunCmd(open func(ctx context.Context) (runner.Store, error)) *cobra.Command {
	var takeover bool
	cmd := &cobra.Command{
		Use:    "run <run-id>",
		Short:  "Drive one workflow run (the runner; hosted in tmux by the TUI)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID, err := parseRunID(args[0])
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
			defer stop()

			s, err := open(ctx)
			if err != nil {
				return err
			}
			defer s.Close()

			log, closeLog := runnerLog(runID, cmd.OutOrStdout())
			defer closeLog()
			dbPath, _ := cmd.Flags().GetString("db")
			r := &runner.Runner{Store: s, Exec: runner.ClaudeExec{DBPath: dbPath}, Log: log}
			return r.Run(ctx, runID, takeover)
		},
	}
	cmd.Flags().BoolVar(&takeover, "takeover", false,
		"re-enter a run left running or waiting by a runner that died (set by `tend workflow resume`)")
	return cmd
}

// newWorkflowResumeCmd starts a fresh runner for a run whose runner is
// gone: paused by the user, or left running by a runner that died with
// its host or tmux server. A run that has ended, or whose runner is
// still alive, is refused with a message saying which.
func newWorkflowResumeCmd(open func(ctx context.Context) (runner.Store, error), dbPath func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "resume <run-id>",
		Short: "Re-enter a run whose runner died or was paused",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID, err := parseRunID(args[0])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			s, err := open(ctx)
			if err != nil {
				return err
			}
			defer s.Close()
			name, err := runner.Resume(ctx, s, runID, dbPath())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "resumed run %d in tmux session %s (tmux -L %s attach -t %s to watch)\n",
				runID, name, agent.SocketName, name)
			return nil
		},
	}
}

func parseRunID(s string) (int64, error) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("run id %q is not a positive integer", s)
	}
	return id, nil
}

// runnerLog tees the runner's progress to stdout and runner.log. A log
// file that cannot be opened just means stdout only; a stdout that is
// gone -- the tmux pane closed under a killed runner -- must not stop
// the file from being written, so write errors are ignored on both.
func runnerLog(runID int64, stdout io.Writer) (io.Writer, func()) {
	writers := []io.Writer{quietWriter{stdout}}
	closeLog := func() {}
	if path, err := agent.RunnerLogPath(runID); err == nil {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
				writers = append(writers, quietWriter{f})
				closeLog = func() { f.Close() }
			}
		}
	}
	return io.MultiWriter(writers...), closeLog
}

// quietWriter swallows write errors so one dead sink cannot silence the
// others in an io.MultiWriter.
type quietWriter struct{ w io.Writer }

func (q quietWriter) Write(p []byte) (int, error) {
	_, _ = q.w.Write(p)
	return len(p), nil
}

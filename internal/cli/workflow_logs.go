package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/workflow"
)

// logsPollInterval is how often `logs -f` re-reads the file and the run's
// rows. A step writes a line every few seconds at most, so half a second
// keeps the tail feeling live without hammering SQLite; a var so tests
// can shorten it.
var logsPollInterval = 500 * time.Millisecond

// logsOpts are the flags of `tend workflow logs`.
type logsOpts struct {
	step   int  // 1-based step run number as `status` prints it; 0 picks the default
	follow bool // keep printing until the step (or run) ends
	raw    bool // the stream-json lines themselves rather than the rendered view
	runner bool // the run's runner.log instead of a step log
}

// newWorkflowLogsCmd is the shell's window on a run's output: the current
// step's stream-json log rendered the way the TUI's run view shows it (the
// assistant's text and its tool calls), or raw, or the runner's own log;
// with -f it tails the file until the step hands off or the run ends, so
// `start` then `logs -f` reads like watching the step. It reads the same
// two things the TUI does -- the log file and the rows in SQLite -- and
// never talks to the runner.
func newWorkflowLogsCmd(open openWorkflowStore) *cobra.Command {
	var opts logsOpts
	cmd := &cobra.Command{
		Use:   "logs <run-id> [--step N] [-f]",
		Short: "Print a run's step log (rendered, raw, or the runner's own), optionally following it",
		Long: "Print the log of one of a run's steps: by default the step the run is on " +
			"(or the last one), or the step numbered N the way `status` lists them. The " +
			"stream-json is rendered to the assistant's text and tool calls; --raw prints " +
			"the lines as written. --runner prints the runner's own progress log instead. " +
			"With -f, keep printing as the file grows until the step finishes or the run ends.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID, err := parseRunID(args[0])
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("step") && opts.step < 1 {
				return fmt.Errorf("--step %d: step numbers start at 1", opts.step)
			}
			// Ctrl-C is how a follow ends; it must read as a clean exit,
			// not a stack trace, so the signal cancels the context and the
			// loop returns nil on it.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			cmd.SetContext(ctx)
			return withWorkflowStore(cmd, open, func(ctx context.Context, s WorkflowStore) error {
				return showLogs(ctx, cmd.OutOrStdout(), s, runID, opts)
			})
		},
	}
	cmd.Flags().IntVar(&opts.step, "step", 0, "which step run to show, numbered as `status` lists them (default: the current step)")
	cmd.Flags().BoolVarP(&opts.follow, "follow", "f", false, "keep printing new lines until the step finishes or the run ends")
	cmd.Flags().BoolVar(&opts.raw, "raw", false, "print the stream-json lines as written instead of rendering them")
	cmd.Flags().BoolVar(&opts.runner, "runner", false, "print the run's runner.log instead of a step log (ignores --step and --raw)")
	return cmd
}

// showLogs resolves which file to print and how, then prints it once or
// follows it. The run is read first so a bad id fails the same way every
// other subcommand does.
func showLogs(ctx context.Context, out io.Writer, s WorkflowStore, runID int64, opts logsOpts) error {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if opts.runner {
		return showRunnerLog(ctx, out, s, run, opts.follow)
	}

	stepRuns, err := s.ListStepRunsForRun(ctx, runID)
	if err != nil {
		return err
	}
	idx, err := pickStepRun(run, stepRuns, opts.step)
	if err != nil {
		return err
	}
	sr := stepRuns[idx]
	if sr.LogPath == "" {
		// A gate never has a log; an agent step without one has not
		// been handed to claude yet. Say which, since only the second
		// is worth waiting on.
		label := stepRunLabel(ctx, s, sr)
		if st, err := s.GetStep(ctx, sr.StepID); err == nil && st.Kind == workflow.StepGate {
			return fmt.Errorf("step %d (%s) is a gate; it has no log", idx+1, label)
		}
		return fmt.Errorf("no log yet for step %d (%s)", idx+1, label)
	}

	render := agent.RenderStreamLine
	if opts.raw {
		render = rawLine
	}
	t := &tailer{path: sr.LogPath}
	if !opts.follow {
		// A recorded path whose file is not there yet is a step that
		// just started: nothing to show, nothing wrong.
		return t.emit(out, render)
	}
	// The step handing off ends the follow; so does the run ending
	// under it (a cancel kills the step without finishing its row).
	return followLog(ctx, out, t, render, func(ctx context.Context) (bool, error) {
		cur, err := s.GetStepRun(ctx, sr.ID)
		if err != nil {
			return false, err
		}
		if cur.Finished() {
			return true, nil
		}
		return runEnded(ctx, s, runID)
	})
}

// showRunnerLog prints runner.log for run, raw (it is plain text, not
// stream-json), following until the run is terminal when asked. The file
// missing is an error naming the path: unlike a step log, there is no
// "just started" case worth staying quiet about -- the runner opens it
// first thing.
func showRunnerLog(ctx context.Context, out io.Writer, s WorkflowStore, run workflow.Run, follow bool) error {
	path, err := agent.RunnerLogPath(run.ID)
	if err != nil {
		return fmt.Errorf("locating runner log: %w", err)
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("run %d has no runner log at %s", run.ID, path)
		}
		return fmt.Errorf("reading runner log: %w", err)
	}
	t := &tailer{path: path}
	if !follow {
		return t.emit(out, rawLine)
	}
	return followLog(ctx, out, t, rawLine, func(ctx context.Context) (bool, error) {
		return runEnded(ctx, s, run.ID)
	})
}

// runEnded reports whether the run has reached a terminal state.
func runEnded(ctx context.Context, s WorkflowStore, runID int64) (bool, error) {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return false, err
	}
	return run.State.Terminal(), nil
}

// pickStepRun chooses which of a run's step runs to show and returns its
// index: --step N by the same 1-based numbering `status` prints (the
// order ListStepRunsForRun returns), else the run's current step run,
// else the last one. Out of range says how many there are.
func pickStepRun(run workflow.Run, stepRuns []workflow.StepRun, n int) (int, error) {
	if len(stepRuns) == 0 {
		return 0, fmt.Errorf("run %d has no step runs yet", run.ID)
	}
	if n != 0 {
		if n < 1 || n > len(stepRuns) {
			return 0, fmt.Errorf("run %d has %s; --step %d is out of range",
				run.ID, plural(int64(len(stepRuns)), "step run"), n)
		}
		return n - 1, nil
	}
	if run.CurrentStepRunID != nil {
		for i, sr := range stepRuns {
			if sr.ID == *run.CurrentStepRunID {
				return i, nil
			}
		}
	}
	return len(stepRuns) - 1, nil
}

// rawLine is the --raw renderer: the line as written.
func rawLine(line []byte) []string { return []string{string(line)} }

// followLog prints what t has, then polls: re-read, ask done, sleep. The
// read comes before the check so that once done reports true, one more
// drain is guaranteed to have seen everything written before the finish
// was recorded -- a step's result line lands just before its row does. A
// cancelled context (Ctrl-C) ends the follow quietly.
func followLog(ctx context.Context, out io.Writer, t *tailer, render func([]byte) []string,
	done func(context.Context) (bool, error)) error {
	tick := time.NewTicker(logsPollInterval)
	defer tick.Stop()
	for {
		if err := t.emit(out, render); err != nil {
			return err
		}
		finished, err := done(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if finished {
			if err := t.emit(out, render); err != nil {
				return err
			}
			// Nothing more is coming, so a trailing line without its
			// newline is the writer's last word, not a line in progress.
			return t.flush(out, render)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// tailer reads a log file incrementally. offset is the byte past the
// last read; pending holds the bytes after the last newline, a line the
// writer has not finished, kept until the rest of it lands so a line is
// never printed (or rendered) in two halves.
type tailer struct {
	path    string
	offset  int64
	pending []byte
}

// emit reads what was appended since the last call and writes each
// complete line's rendering to out. A file that does not exist yet
// yields nothing; a file shorter than offset was rewritten and is read
// again from the start.
func (t *tailer) emit(out io.Writer, render func([]byte) []string) error {
	lines, err := t.read()
	if err != nil {
		return err
	}
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		for _, s := range render(line) {
			if _, err := fmt.Fprintln(out, s); err != nil {
				return err
			}
		}
	}
	return nil
}

// flush writes the pending partial line, if any, as a line of its own.
func (t *tailer) flush(out io.Writer, render func([]byte) []string) error {
	if len(bytes.TrimSpace(t.pending)) == 0 {
		t.pending = nil
		return nil
	}
	line := t.pending
	t.pending = nil
	for _, s := range render(line) {
		if _, err := fmt.Fprintln(out, s); err != nil {
			return err
		}
	}
	return nil
}

// read returns the complete lines (without their newline) appended
// since the last read, carrying any partial trailing line over to the
// next call.
func (t *tailer) read() ([][]byte, error) {
	f, err := os.Open(t.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opening log: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("reading log: %w", err)
	}
	if fi.Size() < t.offset {
		t.offset = 0
		t.pending = nil
	}
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return nil, fmt.Errorf("reading log: %w", err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("reading log: %w", err)
	}
	t.offset += int64(len(data))

	buf := append(t.pending, data...)
	var lines [][]byte
	for {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			break
		}
		lines = append(lines, buf[:i])
		buf = buf[i+1:]
	}
	// Copy so a few pending bytes do not pin the whole read's buffer,
	// and the next append cannot write into memory the caller's lines
	// still point at.
	t.pending = bytes.Clone(buf)
	return lines, nil
}

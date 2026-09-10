package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/runner"
	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/workflow"
)

// WorkflowStore is the slice of the persistence layer `tend workflow`
// needs: the runner's own slice (the hidden `run` subcommand is the
// runner) plus what the scriptable commands read and write. Its own
// factory for the same reason MCPStoreFactory is: a different method set
// than the rest of the command tree.
type WorkflowStore interface {
	runner.Store
	ListWorkflows(ctx context.Context) ([]workflow.Workflow, error)
	WorkflowByName(ctx context.Context, name string) (workflow.Workflow, error)
	ListEdges(ctx context.Context, workflowID int64) ([]workflow.Edge, error)
	CreateRun(ctx context.Context, workflowID, taskID int64, cwd string) (workflow.Run, error)
	ListActiveRuns(ctx context.Context) ([]workflow.Run, error)
	ListSessionsForTask(ctx context.Context, taskID int64) ([]task.Session, error)
	GetProject(ctx context.Context, id int64) (task.Project, error)
}

// WorkflowStoreFactory opens a WorkflowStore at the given database path.
type WorkflowStoreFactory func(ctx context.Context, dbPath string) (WorkflowStore, error)

// openWorkflowStore opens a WorkflowStore for the current command's --db.
type openWorkflowStore func(ctx context.Context) (WorkflowStore, error)

// The process-control edges of `tend workflow start` and `status` behind
// seams, for the same reason the TUI keeps checkInstalled and runnerAlive
// as package-level vars: the tests drive these commands and must not
// depend on whether the host has claude and tmux, or reach for the real
// tmux server.
var (
	checkClaude  = agent.CheckInstalled
	tmuxPresent  = agent.TmuxInstalled
	launchRunner = func(ctx context.Context, s runner.LaunchStore, runID int64, dbPath string) (string, error) {
		return runner.Launch(ctx, s, runID, dbPath, false)
	}
	// runnerAlive asks tmux whether a run's runner session exists. known
	// is false when there is no way to ask (no tmux, no config), so the
	// caller says nothing rather than reporting a runner gone that may
	// well be there.
	runnerAlive = func(runID int64) (alive, known bool) {
		if !agent.TmuxInstalled() {
			return false, false
		}
		confPath, err := agent.WriteConfig()
		if err != nil {
			return false, false
		}
		return agent.HasSession(agent.RunnerSessionName(runID), confPath), true
	}
)

// newWorkflowCmd wires `tend workflow`: the scriptable surface over agent
// workflows (tend task #185), mirroring `tend projects`, plus the runner
// behind them. With no subcommand it lists the workflows. `run` is hidden
// -- it is the process `start` (and the TUI's `w` chord) hosts in tmux,
// not a user command. Every other subcommand reads or writes exactly what
// the TUI would: a run is created and its runner launched the same way, a
// gate decision is the same FinishStepRun the finish_step tool makes, and
// pause and cancel are the same SetRunState the runner polls for.
func newWorkflowCmd(open openWorkflowStore, dbPath func() string) *cobra.Command {
	root := &cobra.Command{
		Use:     "workflow",
		Aliases: []string{"wf", "workflows"},
		Short:   "List, start, watch and steer agent workflow runs",
		Long: "Agent workflows are multi-step agent procedures a task can run headlessly. " +
			"A run is driven by its own runner process inside a tmux session on tend's " +
			"server (`tmux -L tend`), one process per run. With no subcommand, lists the " +
			"workflows with their step counts. Workflows themselves are authored in the TUI (W).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return listWorkflows(cmd, open) },
	}
	root.AddCommand(
		&cobra.Command{
			Use:   "ls",
			Short: "List workflows with their step counts",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, _ []string) error { return listWorkflows(cmd, open) },
		},
		newWorkflowStartCmd(open, dbPath),
		newWorkflowStatusCmd(open),
		newWorkflowGateCmd(open, workflow.OutcomeApprove),
		newWorkflowGateCmd(open, workflow.OutcomeReject),
		newWorkflowPauseCmd(open),
		newWorkflowResumeCmd(open, dbPath),
		newWorkflowCancelCmd(open),
		newWorkflowLogsCmd(open),
		newWorkflowRunCmd(open),
	)
	return root
}

// withWorkflowStore is withStore for the workflow store: open, run fn,
// close.
func withWorkflowStore(cmd *cobra.Command, open openWorkflowStore,
	fn func(context.Context, WorkflowStore) error) error {
	ctx := cmd.Context()
	s, err := open(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	return fn(ctx, s)
}

// --- ls -------------------------------------------------------------------

func listWorkflows(cmd *cobra.Command, open openWorkflowStore) error {
	return withWorkflowStore(cmd, open, func(ctx context.Context, s WorkflowStore) error {
		wfs, err := s.ListWorkflows(ctx)
		if err != nil {
			return err
		}
		if len(wfs) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "no workflows yet — create one in the TUI with W")
			return nil
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
		for _, wf := range wfs {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", wf.ID, wf.Name, plural(wf.StepCount, "step"), wf.Description)
		}
		return w.Flush()
	})
}

// --- start ----------------------------------------------------------------

// newWorkflowStartCmd is the CLI counterpart of the TUI's `w` chord: the
// same checks (the workflow has steps, every prompt renders, claude and
// tmux are on $PATH), the same cwd default (the task's most recent
// session's directory, else its project's default, else the shell's), a
// pending run, and its runner launched in tmux. A runner that cannot be
// started fails the run on the spot so no pending run is left looking
// live, and the error is what the shell sees.
func newWorkflowStartCmd(open openWorkflowStore, dbPath func() string) *cobra.Command {
	var taskID int64
	var cwd string
	cmd := &cobra.Command{
		Use:   "start <workflow> --task <id> [--cwd <dir>]",
		Short: "Run a workflow on a task: create the run and launch its runner in tmux",
		Long: "Create a run of the named workflow on a task and start its runner in a " +
			"detached tmux session, then return at once. The runner drives every step " +
			"headlessly from there; `status` and `logs` follow it. The working directory " +
			"defaults to the task's most recent session's, then its project's default, " +
			"then the current directory. A workflow name resolves but is never created.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withWorkflowStore(cmd, open, func(ctx context.Context, s WorkflowStore) error {
				wf, err := resolveWorkflow(ctx, s, joinArgs(args))
				if err != nil {
					return err
				}
				t, err := s.GetTask(ctx, taskID)
				if err != nil {
					return err
				}
				if err := checkRunnable(ctx, s, wf); err != nil {
					return err
				}
				dir, err := runCwd(ctx, s, t, cwd)
				if err != nil {
					return err
				}
				run, err := s.CreateRun(ctx, wf.ID, t.ID, dir)
				if err != nil {
					return err
				}
				name, err := launchRunner(ctx, s, run.ID, dbPath())
				if err != nil {
					if ferr := s.FailRun(ctx, run.ID, err.Error()); ferr != nil {
						return fmt.Errorf("%w (and failing run %d: %v)", err, run.ID, ferr)
					}
					return fmt.Errorf("run %d failed to start: %w", run.ID, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(),
					"started run %d: %s on #%d in %s\n  tend workflow status %d to follow it; tmux -L %s attach -t %s for the runner's pane\n",
					run.ID, wf.Name, t.ID, dir, run.ID, agent.SocketName, name)
				return nil
			})
		},
	}
	cmd.Flags().Int64Var(&taskID, "task", 0, "id of the task to run the workflow on (required)")
	cmd.Flags().StringVar(&cwd, "cwd", "", "working directory for the run's agent sessions")
	_ = cmd.MarkFlagRequired("task")
	return cmd
}

// resolveWorkflow looks a workflow up by name, falling back to its id for
// a numeric argument (the id `ls` prints), and turns a miss into a pointed
// error. Names resolve, never create -- the projects convention: a typo
// must say so, not quietly spawn a workflow.
func resolveWorkflow(ctx context.Context, s WorkflowStore, name string) (workflow.Workflow, error) {
	wf, err := s.WorkflowByName(ctx, name)
	if err == nil {
		return wf, nil
	}
	if !errors.Is(err, workflow.ErrWorkflowNotFound) && !errors.Is(err, workflow.ErrEmptyName) {
		return workflow.Workflow{}, err
	}
	if id, perr := strconv.ParseInt(strings.TrimSpace(name), 10, 64); perr == nil && id > 0 {
		if wf, err := s.GetWorkflow(ctx, id); err == nil {
			return wf, nil
		} else if !errors.Is(err, workflow.ErrWorkflowNotFound) {
			return workflow.Workflow{}, err
		}
	}
	return workflow.Workflow{}, fmt.Errorf(
		"no workflow named %q (`tend workflow ls` lists them; create one in the TUI with W)", name)
}

// checkRunnable is the TUI's pre-flight before a run is written: at least
// one step, every prompt renders (the runner would fail the run on the
// same error, but here a typo reads as an authoring problem rather than a
// failed run), and claude and tmux both present, since the runner lives in
// tmux and runs claude. Anything missing is named.
func checkRunnable(ctx context.Context, s WorkflowStore, wf workflow.Workflow) error {
	steps, err := s.ListSteps(ctx, wf.ID)
	if err != nil {
		return err
	}
	if len(steps) == 0 {
		return fmt.Errorf("%s has no steps — add one in the TUI with W", wf.Name)
	}
	for _, st := range steps {
		if st.Kind == workflow.StepGate && st.PromptMD == "" {
			continue
		}
		if err := workflow.ValidatePrompt(st.PromptMD); err != nil {
			return fmt.Errorf("%s / %s: %w", wf.Name, st.Name, err)
		}
	}
	if err := checkClaude(); err != nil {
		return err
	}
	if !tmuxPresent() {
		return runner.ErrNoTmux
	}
	return nil
}

// runCwd resolves where the run's sessions start: the --cwd flag, made
// absolute against the shell's directory, or the same default the TUI
// offers -- the task's most recent session's directory (ListSessionsForTask
// is newest first), else its project's default, else the shell's own.
func runCwd(ctx context.Context, s WorkflowStore, t task.Task, flag string) (string, error) {
	if flag != "" {
		dir, err := filepath.Abs(task.NormalizeProjectCwd(flag))
		if err != nil {
			return "", fmt.Errorf("resolving %q: %w", flag, err)
		}
		return dir, nil
	}
	if sessions, err := s.ListSessionsForTask(ctx, t.ID); err == nil && len(sessions) > 0 && sessions[0].Cwd != "" {
		return sessions[0].Cwd, nil
	}
	if p, err := s.GetProject(ctx, t.ProjectID); err == nil && p.Cwd != "" {
		return p.Cwd, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("no cwd for the run: %w (pass --cwd)", err)
	}
	return dir, nil
}

// --- status ---------------------------------------------------------------

// newWorkflowStatusCmd prints the live runs -- id, workflow, task, state,
// current step, elapsed, and "runner gone" for a run whose state says a
// runner owns it while no tmux session hosts one -- or, given a run id,
// one run in full: its header, every step run in order with its outcome
// or live state, the error for a failed run, and the input a waiting gate
// is reviewing, so a gate can be decided from the shell with what the
// reviewer needs in front of them.
func newWorkflowStatusCmd(open openWorkflowStore) *cobra.Command {
	return &cobra.Command{
		Use:   "status [<run-id>]",
		Short: "Show live runs, or one run in full",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withWorkflowStore(cmd, open, func(ctx context.Context, s WorkflowStore) error {
				if len(args) == 0 {
					return listActiveRuns(ctx, cmd.OutOrStdout(), s)
				}
				runID, err := parseRunID(args[0])
				if err != nil {
					return err
				}
				return showRun(ctx, cmd.OutOrStdout(), s, runID)
			})
		},
	}
}

func listActiveRuns(ctx context.Context, out io.Writer, s WorkflowStore) error {
	runs, err := s.ListActiveRuns(ctx)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		fmt.Fprintln(out, "no live runs")
		return nil
	}
	now := time.Now()
	w := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
	for _, run := range runs {
		name := fmt.Sprintf("run %d", run.ID)
		if wf, err := s.GetWorkflow(ctx, run.WorkflowID); err == nil {
			name = wf.Name
		}
		step := ""
		if run.CurrentStepRunID != nil {
			if sr, err := s.GetStepRun(ctx, *run.CurrentStepRunID); err == nil {
				step = stepRunLabel(ctx, s, sr)
			}
		}
		fmt.Fprintf(w, "%d\t%s\t#%d\t%s\t%s\t%s%s\n", run.ID, name, run.TaskID, run.State, step,
			fmtDuration(runElapsed(run, now)), runnerNote(run))
	}
	return w.Flush()
}

func showRun(ctx context.Context, out io.Writer, s WorkflowStore, runID int64) error {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("workflow %d", run.WorkflowID)
	if wf, err := s.GetWorkflow(ctx, run.WorkflowID); err == nil {
		name = wf.Name
	}
	taskLine := fmt.Sprintf("#%d", run.TaskID)
	if t, err := s.GetTask(ctx, run.TaskID); err == nil {
		taskLine += " " + t.Title
	}
	fmt.Fprintf(out, "run %d: %s on %s\n", run.ID, name, taskLine)
	state := string(run.State)
	if run.State == workflow.RunFailed && run.Error != "" {
		state += ": " + run.Error
	}
	fmt.Fprintf(out, "state: %s%s\n", state, runnerNote(run))
	fmt.Fprintf(out, "cwd: %s\n", run.Cwd)
	fmt.Fprintf(out, "started: %s (%s)\n", run.StartedAt.Local().Format("2006-01-02 15:04"),
		fmtDuration(runElapsed(run, time.Now())))
	if run.TmuxSession != "" {
		fmt.Fprintf(out, "runner: tmux -L %s attach -t %s\n", agent.SocketName, run.TmuxSession)
	}

	stepRuns, err := s.ListStepRunsForRun(ctx, runID)
	if err != nil {
		return err
	}
	if len(stepRuns) == 0 {
		fmt.Fprintln(out, "steps: none started yet")
		return nil
	}
	fmt.Fprintln(out, "steps:")
	w := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
	var waiting *workflow.StepRun
	for i, sr := range stepRuns {
		current := run.CurrentStepRunID != nil && *run.CurrentStepRunID == sr.ID
		status := sr.Outcome
		if !sr.Finished() {
			status = stepRunLiveState(run, current)
			if current && run.State == workflow.RunWaitingReview {
				waiting = &stepRuns[i]
			}
		}
		marker := " "
		if current {
			marker = ">"
		}
		fmt.Fprintf(w, "%s %d\t%s\t%s\t%s\n", marker, i+1, stepRunLabel(ctx, s, sr), status,
			fmtDuration(stepRunElapsed(sr, time.Now())))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if waiting != nil {
		fmt.Fprintf(out, "\nwaiting for review: tend workflow approve %d | reject %d --feedback \"...\"\n", run.ID, run.ID)
		if waiting.Input != "" {
			fmt.Fprintf(out, "input under review:\n%s\n", indent(waiting.Input, "  "))
		}
	}
	return nil
}

// stepRunLabel is the step's name with its iteration when it has run
// more than once in this run (a loop-back), or the step id when the step
// can no longer be read.
func stepRunLabel(ctx context.Context, s WorkflowStore, sr workflow.StepRun) string {
	name := fmt.Sprintf("step %d", sr.StepID)
	if st, err := s.GetStep(ctx, sr.StepID); err == nil {
		name = st.Name
	}
	if sr.Iteration > 1 {
		name += fmt.Sprintf(" #%d", sr.Iteration)
	}
	return name
}

// stepRunLiveState is what an unfinished step run is doing, read from the
// run: the current step is running, waiting for review, or paused with
// the run; an unfinished step run that is not current was interrupted.
func stepRunLiveState(run workflow.Run, current bool) string {
	if !current {
		return "interrupted"
	}
	switch run.State {
	case workflow.RunWaitingReview:
		return "waiting review"
	case workflow.RunPaused:
		return "paused"
	case workflow.RunRunning:
		return "running"
	}
	return string(run.State)
}

// runnerNote is the " (runner gone)" suffix for a run whose state says a
// runner owns it while no tmux session hosts one; "" otherwise, including
// when there is no way to ask tmux.
func runnerNote(run workflow.Run) string {
	if run.State != workflow.RunRunning && run.State != workflow.RunWaitingReview {
		return ""
	}
	if alive, known := runnerAlive(run.ID); known && !alive {
		return " (runner gone — tend workflow resume " + strconv.FormatInt(run.ID, 10) + ")"
	}
	return ""
}

// --- approve / reject -----------------------------------------------------

// newWorkflowGateCmd builds `approve` and `reject`: the shell's way to
// decide the gate a run is waiting at. The write is the same one-shot
// FinishStepRun the TUI's `a`/`x` and the finish_step tool make; the
// runner polls the row and routes on the outcome. --feedback is the
// gate's deliverable, and runner.nextStep decides what that means: on a
// loop-back edge (the reject case) the target keeps its input and gets
// the text as {{.Feedback}}; on a forward edge (the usual approve) the
// text REPLACES the reviewed deliverable as the next step's {{.Input}}.
// So reject requires non-blank feedback, and approve leaves it out by
// default so the gate passes its input through unchanged, exactly as the
// TUI's `a` does. A gate whose edges route other outcomes is refused
// naming them, so a decision never silently ends the run for want of an
// edge.
func newWorkflowGateCmd(open openWorkflowStore, outcome string) *cobra.Command {
	var feedback string
	short := "Approve the gate a run is waiting at"
	usage := "optional; becomes the gate's deliverable: the next step's {{.Input}} on a forward edge (replacing the reviewed deliverable), " +
		"its {{.Feedback}} on a loop-back edge; omitted, the gate passes its input through unchanged as the TUI does"
	if outcome == workflow.OutcomeReject {
		short = "Reject the gate a run is waiting at, with feedback for the step it loops back to"
		usage = "the gate's deliverable, handed to the step the reject edge loops back to as its {{.Feedback}}; must not be blank"
	}
	cmd := &cobra.Command{
		Use:   outcome + " <run-id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID, err := parseRunID(args[0])
			if err != nil {
				return err
			}
			if outcome == workflow.OutcomeReject && strings.TrimSpace(feedback) == "" {
				return errors.New("reject needs --feedback with something in it: it is all the step it loops back to hears")
			}
			return withWorkflowStore(cmd, open, func(ctx context.Context, s WorkflowStore) error {
				name, err := decideGate(ctx, s, runID, outcome, feedback)
				if err != nil {
					return err
				}
				msg := fmt.Sprintf("run %d: %s %s", runID, name, outcome)
				if feedback != "" {
					msg += " with feedback"
				}
				fmt.Fprintln(cmd.OutOrStdout(), msg)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&feedback, "feedback", "", usage)
	if outcome == workflow.OutcomeReject {
		_ = cmd.MarkFlagRequired("feedback")
	}
	return cmd
}

// decideGate settles the gate runID is waiting at on outcome and returns
// the gate's name. It refuses, saying which: a run that has ended or is
// not waiting for review, a current step that is not a gate, a gate
// already decided, or an outcome the gate's edges do not route.
func decideGate(ctx context.Context, s WorkflowStore, runID int64, outcome, feedback string) (string, error) {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return "", err
	}
	if run.State.Terminal() {
		return "", fmt.Errorf("run %d has already %s", runID, run.State)
	}
	if run.State != workflow.RunWaitingReview || run.CurrentStepRunID == nil {
		return "", fmt.Errorf("run %d is %s, not waiting for review", runID, run.State)
	}
	sr, err := s.GetStepRun(ctx, *run.CurrentStepRunID)
	if err != nil {
		return "", err
	}
	st, err := s.GetStep(ctx, sr.StepID)
	if err != nil {
		return "", err
	}
	if st.Kind != workflow.StepGate {
		return "", fmt.Errorf("run %d: the current step %s is not a gate", runID, st.Name)
	}
	if sr.Finished() {
		return "", fmt.Errorf("run %d: %s already decided: %s", runID, st.Name, sr.Outcome)
	}
	edges, err := s.OutgoingEdges(ctx, st.ID)
	if err != nil {
		return "", err
	}
	if len(edges) > 0 {
		outcomes := make([]string, 0, len(edges))
		for _, e := range edges {
			outcomes = append(outcomes, e.Outcome)
		}
		if !slices.Contains(outcomes, outcome) {
			return "", fmt.Errorf("run %d: %s routes %s, not %s", runID, st.Name,
				strings.Join(outcomes, ", "), outcome)
		}
	}
	if err := s.FinishStepRun(ctx, sr.ID, outcome, feedback); err != nil {
		return "", err
	}
	return st.Name, nil
}

// --- pause / cancel -------------------------------------------------------

// newWorkflowPauseCmd writes paused on a live run. The runner polls for it,
// SIGTERMs the step (its session stays resumable) and exits; `resume`
// starts a fresh runner. A pending run has no runner to stop yet and an
// ended one nothing to pause; both are refused saying so.
func newWorkflowPauseCmd(open openWorkflowStore) *cobra.Command {
	return &cobra.Command{
		Use:   "pause <run-id>",
		Short: "Pause a live run; its runner stops the current step and exits",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID, err := parseRunID(args[0])
			if err != nil {
				return err
			}
			return withWorkflowStore(cmd, open, func(ctx context.Context, s WorkflowStore) error {
				run, err := s.GetRun(ctx, runID)
				if err != nil {
					return err
				}
				switch run.State {
				case workflow.RunRunning, workflow.RunWaitingReview:
					if err := s.SetRunState(ctx, runID, workflow.RunPaused); err != nil {
						return err
					}
					fmt.Fprintf(cmd.OutOrStdout(),
						"paused run %d; the runner stops the step on its next poll (tend workflow resume %d to continue)\n",
						runID, runID)
					return nil
				case workflow.RunPaused:
					fmt.Fprintf(cmd.OutOrStdout(), "run %d is already paused\n", runID)
					return nil
				case workflow.RunPending:
					return fmt.Errorf("run %d has not been claimed by a runner yet", runID)
				}
				return fmt.Errorf("run %d has already %s", runID, run.State)
			})
		},
	}
}

// newWorkflowCancelCmd writes cancelled. The runner polls for it and
// kills the step; a paused run, or one whose runner died, simply ends.
// Terminal is final: a run that has ended is refused.
func newWorkflowCancelCmd(open openWorkflowStore) *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <run-id>",
		Short: "Cancel a run; its runner kills the current step and exits",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID, err := parseRunID(args[0])
			if err != nil {
				return err
			}
			return withWorkflowStore(cmd, open, func(ctx context.Context, s WorkflowStore) error {
				run, err := s.GetRun(ctx, runID)
				if err != nil {
					return err
				}
				if run.State.Terminal() {
					return fmt.Errorf("run %d has already %s", runID, run.State)
				}
				if err := s.SetRunState(ctx, runID, workflow.RunCancelled); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "cancelled run %d\n", runID)
				return nil
			})
		},
	}
}

// --- run / resume ---------------------------------------------------------

// newWorkflowRunCmd is the runner itself. It claims the run, drives it to
// a terminal state (or a pause), and exits; its progress goes to stdout
// -- the tmux pane -- and to runner.log in the run's log directory, so
// what it said survives the pane. SIGINT, SIGTERM and SIGHUP (what
// `tmux kill-session` delivers) cancel the runner's context, which
// SIGTERMs the step's claude and leaves the run for `resume`.
func newWorkflowRunCmd(open openWorkflowStore) *cobra.Command {
	var takeover bool
	cmd := &cobra.Command{
		Use:    "run <run-id>",
		Short:  "Drive one workflow run (the runner; hosted in tmux by `start` and the TUI)",
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
func newWorkflowResumeCmd(open openWorkflowStore, dbPath func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "resume <run-id>",
		Short: "Re-enter a run whose runner died or was paused",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID, err := parseRunID(args[0])
			if err != nil {
				return err
			}
			return withWorkflowStore(cmd, open, func(ctx context.Context, s WorkflowStore) error {
				name, err := runner.Resume(ctx, s, runID, dbPath())
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "resumed run %d in tmux session %s (tmux -L %s attach -t %s to watch)\n",
					runID, name, agent.SocketName, name)
				return nil
			})
		},
	}
}

// --- helpers --------------------------------------------------------------

func parseRunID(s string) (int64, error) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("run id %q is not a positive integer", s)
	}
	return id, nil
}

// runElapsed is how long a run has been going, or took.
func runElapsed(run workflow.Run, now time.Time) time.Duration {
	if run.EndedAt != nil {
		return run.EndedAt.Sub(run.StartedAt)
	}
	return now.Sub(run.StartedAt)
}

// stepRunElapsed is how long a step run has been going, or took.
func stepRunElapsed(sr workflow.StepRun, now time.Time) time.Duration {
	if sr.EndedAt != nil {
		return sr.EndedAt.Sub(sr.StartedAt)
	}
	return now.Sub(sr.StartedAt)
}

// fmtDuration renders a duration the way a run's clock reads: seconds
// under a minute, minutes under an hour, hours and minutes past that.
func fmtDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func plural(n int64, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
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

package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/runner"
	"github.com/jwstover/tend/internal/workflow"
)

// ScheduleStore is the slice of the persistence layer `tend schedule`
// needs: the workflow store (names, steps, tasks, runs) plus the schedule
// CRUD. Its own interface so WorkflowStore, and its fake, stay as they are.
type ScheduleStore interface {
	WorkflowStore
	runner.ScheduleStore
	StartScheduleNow(ctx context.Context, id int64) (workflow.Run, error)
	CreateSchedule(ctx context.Context, sc workflow.Schedule, now time.Time) (workflow.Schedule, error)
	GetSchedule(ctx context.Context, id int64) (workflow.Schedule, error)
	ScheduleByName(ctx context.Context, name string) (workflow.Schedule, error)
	ListSchedules(ctx context.Context) ([]workflow.Schedule, error)
	SetScheduleEnabled(ctx context.Context, id int64, enabled bool, now time.Time) error
	DeleteSchedule(ctx context.Context, id int64) error
}

// ScheduleStoreFactory opens a ScheduleStore at the given database path.
type ScheduleStoreFactory func(ctx context.Context, dbPath string) (ScheduleStore, error)

// openScheduleStore opens a ScheduleStore for the current command's --db.
type openScheduleStore func(ctx context.Context) (ScheduleStore, error)

// scheduleTimeLayout is how a next run is shown: local, to the minute.
const scheduleTimeLayout = "Mon Jan 2 15:04"

// newScheduleCmd wires `tend schedule`: the scriptable surface over
// scheduled workflow runs (tend task #645). With no subcommand it lists
// the schedules.
func newScheduleCmd(open openScheduleStore, dbPath func() string) *cobra.Command {
	root := &cobra.Command{
		Use:     "schedule",
		Aliases: []string{"schedules", "sched"},
		Short:   "List, add, pause and remove scheduled workflow runs",
		Long: "A schedule runs a workflow on a task on a cron expression. With no " +
			"subcommand, lists the schedules with their next and last run.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return listSchedules(cmd, open) },
	}
	root.AddCommand(
		&cobra.Command{
			Use:   "ls",
			Short: "List schedules",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, _ []string) error { return listSchedules(cmd, open) },
		},
		newScheduleAddCmd(open),
		newScheduleRmCmd(open),
		newScheduleEnableCmd(open, false),
		newScheduleEnableCmd(open, true),
		newScheduleRunNowCmd(open, dbPath),
		newScheduleTickCmd(open, dbPath),
		newScheduleInstallCmd(dbPath),
		newScheduleUninstallCmd(),
	)
	return root
}

func withScheduleStore(cmd *cobra.Command, open openScheduleStore,
	fn func(context.Context, ScheduleStore) error) error {
	ctx := cmd.Context()
	s, err := open(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	return fn(ctx, s)
}

// resolveSchedule looks a schedule up by name, falling back to its id.
func resolveSchedule(ctx context.Context, s ScheduleStore, ref string) (workflow.Schedule, error) {
	ref = strings.TrimSpace(ref)
	sc, err := s.ScheduleByName(ctx, ref)
	if err == nil {
		return sc, nil
	}
	if !errors.Is(err, workflow.ErrScheduleNotFound) && !errors.Is(err, workflow.ErrEmptyName) {
		return workflow.Schedule{}, err
	}
	if id, perr := strconv.ParseInt(ref, 10, 64); perr == nil && id > 0 {
		if sc, err := s.GetSchedule(ctx, id); err == nil {
			return sc, nil
		} else if !errors.Is(err, workflow.ErrScheduleNotFound) {
			return workflow.Schedule{}, err
		}
	}
	return workflow.Schedule{}, fmt.Errorf("no schedule named %q (`tend schedule ls` lists them)", ref)
}

func listSchedules(cmd *cobra.Command, open openScheduleStore) error {
	return withScheduleStore(cmd, open, func(ctx context.Context, s ScheduleStore) error {
		scs, err := s.ListSchedules(ctx)
		if err != nil {
			return err
		}
		if len(scs) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "no schedules yet — `tend schedule add` creates one")
			return nil
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tWORKFLOW\tTASK\tCRON\tSTATE\tNEXT\tLAST")
		for _, sc := range scs {
			wfName := fmt.Sprintf("#%d", sc.WorkflowID)
			if wf, err := s.GetWorkflow(ctx, sc.WorkflowID); err == nil {
				wfName = wf.Name
			}
			taskCell := fmt.Sprintf("#%d", sc.TaskID)
			if t, err := s.GetTask(ctx, sc.TaskID); err == nil {
				taskCell = fmt.Sprintf("#%d %s", t.ID, t.Title)
			}
			state, next := "on", sc.NextRunAt.Local().Format(scheduleTimeLayout)
			if !sc.Enabled {
				state, next = "paused", "-"
			}
			last := "-"
			if sc.LastRunID != nil {
				last = fmt.Sprintf("#%d", *sc.LastRunID)
				if run, err := s.GetRun(ctx, *sc.LastRunID); err == nil {
					last = fmt.Sprintf("#%d %s", run.ID, run.State)
				}
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				sc.ID, sc.Name, wfName, taskCell, sc.CronExpr, state, next, last)
		}
		return w.Flush()
	})
}

func newScheduleAddCmd(open openScheduleStore) *cobra.Command {
	var wfRef, cron, cwd string
	var taskID int64
	cmd := &cobra.Command{
		Use:   "add <name> --workflow <name|id> --task <id> --cron \"<expr>\" [--cwd <dir>]",
		Short: "Schedule a workflow to run on a task",
		Long: "Create a schedule that runs a workflow on a task on a five-field cron " +
			"expression (local time). The working directory defaults as for " +
			"`tend workflow start`.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withScheduleStore(cmd, open, func(ctx context.Context, s ScheduleStore) error {
				wf, err := resolveWorkflow(ctx, s, wfRef)
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
				sc, err := s.CreateSchedule(ctx, workflow.Schedule{
					Name: joinArgs(args), WorkflowID: wf.ID, TaskID: t.ID, Cwd: dir, CronExpr: cron,
				}, time.Now())
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "added schedule %d %q: %s on #%d, next run %s\n",
					sc.ID, sc.Name, wf.Name, t.ID, sc.NextRunAt.Local().Format(scheduleTimeLayout))
				if steps, err := s.ListSteps(ctx, wf.ID); err == nil {
					for _, w := range workflow.ScheduleWarnings(steps) {
						fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
					}
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&wfRef, "workflow", "", "workflow to run, by name or id (required)")
	cmd.Flags().Int64Var(&taskID, "task", 0, "id of the task to run it on (required)")
	cmd.Flags().StringVar(&cron, "cron", "", "five-field cron expression (required)")
	cmd.Flags().StringVar(&cwd, "cwd", "", "working directory for the run's agent sessions")
	_ = cmd.MarkFlagRequired("workflow")
	_ = cmd.MarkFlagRequired("task")
	_ = cmd.MarkFlagRequired("cron")
	return cmd
}

func newScheduleRmCmd(open openScheduleStore) *cobra.Command {
	return &cobra.Command{
		Use:   "rm <schedule>",
		Short: "Remove a schedule (its past runs stay)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withScheduleStore(cmd, open, func(ctx context.Context, s ScheduleStore) error {
				sc, err := resolveSchedule(ctx, s, joinArgs(args))
				if err != nil {
					return err
				}
				if err := s.DeleteSchedule(ctx, sc.ID); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "removed schedule %d %q\n", sc.ID, sc.Name)
				return nil
			})
		},
	}
}

func newScheduleEnableCmd(open openScheduleStore, enable bool) *cobra.Command {
	use, short := "pause <schedule>", "Pause a schedule"
	if enable {
		use, short = "resume <schedule>", "Resume a paused schedule from now (no catch-up runs)"
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withScheduleStore(cmd, open, func(ctx context.Context, s ScheduleStore) error {
				sc, err := resolveSchedule(ctx, s, joinArgs(args))
				if err != nil {
					return err
				}
				if err := s.SetScheduleEnabled(ctx, sc.ID, enable, time.Now()); err != nil {
					return err
				}
				if !enable {
					fmt.Fprintf(cmd.OutOrStdout(), "paused schedule %d %q\n", sc.ID, sc.Name)
					return nil
				}
				got, err := s.GetSchedule(ctx, sc.ID)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "resumed schedule %d %q, next run %s\n",
					sc.ID, sc.Name, got.NextRunAt.Local().Format(scheduleTimeLayout))
				return nil
			})
		},
	}
}

func newScheduleRunNowCmd(open openScheduleStore, dbPath func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "run-now <schedule>",
		Short: "Start a run of a schedule immediately, paused or not",
		Long: "Start one run of the schedule's workflow on its task now, whether or " +
			"not the schedule is paused or due. Refused while its last run is live.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withScheduleStore(cmd, open, func(ctx context.Context, s ScheduleStore) error {
				sc, err := resolveSchedule(ctx, s, joinArgs(args))
				if err != nil {
					return err
				}
				wf, err := s.GetWorkflow(ctx, sc.WorkflowID)
				if err != nil {
					return err
				}
				if err := checkRunnable(ctx, s, wf); err != nil {
					return err
				}
				run, err := s.StartScheduleNow(ctx, sc.ID)
				if err != nil {
					if errors.Is(err, workflow.ErrScheduleBusy) {
						return fmt.Errorf("schedule %q: %w", sc.Name, err)
					}
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
					run.ID, wf.Name, sc.TaskID, run.Cwd, run.ID, agent.SocketName, name)
				return nil
			})
		},
	}
}

func newScheduleTickCmd(open openScheduleStore, dbPath func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "tick",
		Short: "Fire every due schedule once (run every minute by launchd; see `tend schedule install`)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withScheduleStore(cmd, open, func(ctx context.Context, s ScheduleStore) error {
				launch := func(ctx context.Context, id int64) (string, error) {
					return launchRunner(ctx, s, id, dbPath())
				}
				res, err := runner.Tick(ctx, s, time.Now(), launch, runner.QuotaGuard)
				if err != nil {
					return err
				}
				out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
				for _, st := range res.Started {
					fmt.Fprintf(out, "started run %d for schedule %q\n", st.RunID, st.Schedule.Name)
				}
				for _, sk := range res.Skipped {
					fmt.Fprintf(out, "skipped %q: %s\n", sk.Schedule.Name, sk.Reason)
				}
				for _, f := range res.Failed {
					fmt.Fprintf(errOut, "failed %q: %v\n", f.Schedule.Name, f.Err)
				}
				return nil
			})
		},
	}
}

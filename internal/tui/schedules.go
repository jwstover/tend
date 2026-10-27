package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jwstover/tend/internal/runner"
	"github.com/jwstover/tend/internal/workflow"
)

// The Schedules view (`X`): every schedule in one full-screen list, with
// the workflow it runs, the task it runs on, its cron, when it fires next
// and how its last run went. It is the place to see what is scheduled and
// to pause one or run it now; creating, editing and deleting schedules
// stay in `tend schedule add/rm` and over MCP. Like the agents view it
// reloads on dbChangedMsg, so the backup ticker's claims (and a launchd
// tick's) show up without a poll of its own.

// schedulesView is the state of modeSchedules.
type schedulesView struct {
	rows     []scheduleRow
	cursor   int
	selectID int64 // schedule id to land on after the next load; 0 = keep
}

// scheduleRow is a schedule with what the list shows beside it, resolved at
// load. Each lookup degrades: a missing workflow, task or run leaves its
// field blank and the row still shows.
type scheduleRow struct {
	sched     workflow.Schedule
	workflow  string
	taskTitle string
	lastRun   *workflow.Run
}

type schedulesLoadedMsg struct{ rows []scheduleRow }

// backupTickInterval is how often the TUI fires due schedules itself.
const backupTickInterval = time.Minute

func (a *app) startSchedules() {
	a.mode = modeSchedules
	a.deletePending = false
	a.resize()
}

func (a *app) leaveSchedules() tea.Cmd {
	a.mode = modeList
	a.deletePending = false
	a.resize()
	return a.loadTasks(modeList)
}

func (a app) selectedSchedule() (scheduleRow, bool) {
	if a.sv.cursor < 0 || a.sv.cursor >= len(a.sv.rows) {
		return scheduleRow{}, false
	}
	return a.sv.rows[a.sv.cursor], true
}

// loadSchedules reads every schedule and resolves its workflow, task and
// last run.
func (a app) loadSchedules() tea.Cmd {
	return func() tea.Msg {
		list, err := a.store.ListSchedules(a.ctx)
		if err != nil {
			return errMsg{err}
		}
		rows := make([]scheduleRow, 0, len(list))
		for _, sc := range list {
			row := scheduleRow{sched: sc}
			if wf, err := a.store.GetWorkflow(a.ctx, sc.WorkflowID); err == nil {
				row.workflow = wf.Name
			}
			if t, err := a.store.GetTask(a.ctx, sc.TaskID); err == nil {
				row.taskTitle = t.Title
			}
			if sc.LastRunID != nil {
				if run, err := a.store.GetRun(a.ctx, *sc.LastRunID); err == nil {
					row.lastRun = &run
				}
			}
			rows = append(rows, row)
		}
		return schedulesLoadedMsg{rows: rows}
	}
}

// applySchedules installs a load, keeping the cursor on the same schedule
// by id.
func (a *app) applySchedules(msg schedulesLoadedMsg) {
	want := a.sv.selectID
	a.sv.selectID = 0
	if want == 0 {
		if row, ok := a.selectedSchedule(); ok {
			want = row.sched.ID
		}
	}
	a.sv.rows = msg.rows
	if want != 0 {
		if i := slices.IndexFunc(a.sv.rows, func(r scheduleRow) bool { return r.sched.ID == want }); i >= 0 {
			a.sv.cursor = i
		}
	}
	a.sv.cursor = max(min(a.sv.cursor, len(a.sv.rows)-1), 0)
}

func (a app) handleSchedulesKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, a.keys.Quit) && msg.String() != "q":
		return a, tea.Quit
	case key.Matches(msg, a.keys.Quit), key.Matches(msg, a.keys.Back), key.Matches(msg, a.keys.Schedules):
		return a, a.leaveSchedules()
	case key.Matches(msg, a.keys.Help):
		a.helpOpen = true
		return a, nil
	case key.Matches(msg, a.keys.Palette):
		a.openPalette()
		return a, nil
	case key.Matches(msg, a.keys.ScrollDown):
		a.sv.cursor = max(min(a.sv.cursor+1, len(a.sv.rows)-1), 0)
		return a, nil
	case key.Matches(msg, a.keys.ScrollUp):
		a.sv.cursor = max(a.sv.cursor-1, 0)
		return a, nil
	}
	row, ok := a.selectedSchedule()
	if !ok {
		return a, nil
	}
	switch msg.String() {
	case "p":
		return a, a.toggleSchedule(row.sched)
	case "r":
		return a, a.runScheduleNow(row.sched)
	case "enter", "v":
		if row.lastRun == nil {
			a.status = flash{text: "this schedule has not run yet"}
			return a, nil
		}
		return a, a.openRunView(*row.lastRun)
	}
	return a, nil
}

// toggleSchedule pauses an enabled schedule or resumes a paused one.
func (a app) toggleSchedule(sc workflow.Schedule) tea.Cmd {
	return func() tea.Msg {
		if err := a.store.SetScheduleEnabled(a.ctx, sc.ID, !sc.Enabled, time.Now()); err != nil {
			return statusMsg{isErr: true, text: err.Error()}
		}
		verb := "paused"
		if !sc.Enabled {
			verb = "resumed"
		}
		return refreshMsg{status: flash{kind: flashDone, text: verb + " " + sc.Name}}
	}
}

// runScheduleNow starts a run of the schedule right away and launches its
// runner; a launch that fails fails the run so none is left looking live.
func (a app) runScheduleNow(sc workflow.Schedule) tea.Cmd {
	return func() tea.Msg {
		run, err := a.store.StartScheduleNow(a.ctx, sc.ID)
		if errors.Is(err, workflow.ErrScheduleBusy) {
			return statusMsg{text: sc.Name + ": the last run is still live"}
		}
		if err != nil {
			return statusMsg{isErr: true, text: err.Error()}
		}
		if _, err := runner.Launch(a.ctx, a.store, run.ID, a.dbPath, false); err != nil {
			_ = a.store.FailRun(a.ctx, run.ID, err.Error())
			return statusMsg{isErr: true, text: fmt.Sprintf("run %d could not start: %v", run.ID, err)}
		}
		return refreshMsg{status: flash{kind: flashDone, text: fmt.Sprintf("started run %d of %s", run.ID, sc.Name)}}
	}
}

// relFuture renders when t is, relative to now: "in 12m", "in 3h", "in 2d".
func relFuture(t, now time.Time) string {
	d := t.Sub(now)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("in %dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("in %dh", int(d.Hours()))
	default:
		return fmt.Sprintf("in %dd", int(d.Hours()/24))
	}
}

func (a app) schedulesBody() string {
	s, g := a.styles, a.styles.Glyphs
	h := max(a.bodyHeight, 1)
	lines := []string{"", "  " + s.SubHeader.Render("SCHEDULES")}
	if len(a.sv.rows) == 0 {
		lines = append(lines, "", "  "+s.Muted.Render("no schedules — add one with `tend schedule add`"))
		return strings.Join(fitPane(lines, a.width, h, 0), "\n")
	}
	now := time.Now()
	for i, r := range a.sv.rows {
		selected := i == a.sv.cursor
		gutter, gutterStyle := "  ", s.Normal
		if selected {
			gutter, gutterStyle = g.SelBar+" ", s.SelBar
		}
		mark, markStyle := "●", s.Normal
		next := relFuture(r.sched.NextRunAt, now)
		nameStyle := s.Title
		if !r.sched.Enabled {
			mark, markStyle, next, nameStyle = "○", s.Faint, "paused", s.Dimmed
		}
		last := "never run"
		if r.lastRun != nil {
			last = fmt.Sprintf("#%d %s", r.lastRun.ID, r.lastRun.State)
		}
		task := fmt.Sprintf("#%d %s", r.sched.TaskID, r.taskTitle)
		line := gutterStyle.Render(gutter) + markStyle.Render(mark) + " " +
			nameStyle.Render(padRight(truncTail(r.sched.Name, 20, g.Ellipsis), 20)) + " " +
			s.Muted.Render(padRight(truncTail(r.workflow, 18, g.Ellipsis), 18)) + " " +
			s.Muted.Render(padRight(truncTail(task, 28, g.Ellipsis), 28)) + " " +
			s.Faint.Render(padRight(r.sched.CronExpr, 14)) + " " +
			s.Normal.Render(padRight(next, 9)) + " " + s.Faint.Render(last)
		lines = append(lines, lipgloss.NewStyle().MaxWidth(a.width).Render(line))
	}
	off := 0
	if line := 2 + a.sv.cursor; line >= h {
		off = line - h + 1
	}
	return strings.Join(fitPane(lines, a.width, h, off), "\n")
}

func (a app) schedulesHints() [][2]string {
	return [][2]string{{"j/k", "move"}, {"p", "pause/resume"}, {"r", "run now"}, {"⏎/v", "last run"},
		{"esc/q/X", "back"}, {"?", "help"}}
}

// runScheduleTicker fires due schedules every interval until ctx is
// canceled: the TUI's backup for launchd, so schedules run while the TUI is
// open even before `tend schedule install`. It is a goroutine, not a
// tea.Tick, because tea.ExecProcess pauses the program loop while a session
// is attached. The first tick is one interval out. Errors and results are
// dropped on purpose: the DB writes reach the view through the change
// watcher, and a failed run is on its row.
func runScheduleTicker(ctx context.Context, s runner.ScheduleStore, launch runner.LaunchFunc, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			_, _ = runner.Tick(ctx, s, now, launch, runner.QuotaGuard)
		}
	}
}

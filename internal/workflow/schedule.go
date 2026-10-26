package workflow

import (
	"errors"
	"fmt"
	"time"
)

// OverlapPolicy says what a due schedule does while its previous run is
// still live.
type OverlapPolicy string

const (
	// OverlapSkip drops the tick: a schedule never has two live runs.
	OverlapSkip OverlapPolicy = "skip"
)

// ErrScheduleNotFound is returned when a schedule id or name matches no row.
var ErrScheduleNotFound = errors.New("schedule not found")

// Schedule is a timer on a workflow run: when NextRunAt passes, a pending
// run of WorkflowID is created against TaskID, its home. NextRunAt and the
// other times are UTC.
type Schedule struct {
	ID         int64
	Name       string
	WorkflowID int64
	TaskID     int64
	Cwd        string
	CronExpr   string
	Enabled    bool
	Overlap    OverlapPolicy
	LastRunID  *int64
	NextRunAt  time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ScheduleWarnings names what is risky about running steps unattended: one
// line per agent step that runs with bypassPermissions.
func ScheduleWarnings(steps []Step) []string {
	var out []string
	for _, st := range steps {
		if st.Kind == StepAgent && st.PermissionMode == "bypassPermissions" {
			out = append(out, fmt.Sprintf("step %q runs with bypassPermissions; a scheduled run is unattended — prefer acceptEdits/default with a read-only prompt", st.Name))
		}
	}
	return out
}

// ErrScheduleBusy is returned when a schedule is asked to start a run while
// its last run is still live.
var ErrScheduleBusy = errors.New("schedule's last run is still live")

// ScheduleClaim is the outcome of claiming a due schedule. The zero value
// means nothing was claimed (not due, disabled, or another tick won).
// Skipped, when set, says why a claimed tick started no run.
type ScheduleClaim struct {
	Claimed bool
	Run     *Run
	Skipped string
}

// ScheduleRunContext is what a scheduled run knows about its schedule and
// the run before it.
type ScheduleRunContext struct {
	Schedule            Schedule
	Previous            *Run
	PreviousDeliverable string
}

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

package runner

import (
	"context"

	"github.com/jwstover/tend/internal/workflow"
)

// scheduleContexter is the optional slice of the store that knows which
// schedule started a run. It is asserted rather than added to Store so
// stores without schedules keep working.
type scheduleContexter interface {
	ScheduleRunContext(ctx context.Context, runID int64) (workflow.ScheduleRunContext, bool, error)
}

// promptSchedule loads the {{.Schedule.*}} template data for a run. It
// degrades quietly: an unscheduled run, a store without schedule support
// or a failed lookup all yield the zero value, so a prompt renders without
// the schedule's memory rather than failing the step.
func (r *Runner) promptSchedule(ctx context.Context, runID int64) workflow.PromptSchedule {
	sc, ok := r.Store.(scheduleContexter)
	if !ok {
		return workflow.PromptSchedule{}
	}
	rc, found, err := sc.ScheduleRunContext(ctx, runID)
	if err != nil || !found {
		return workflow.PromptSchedule{}
	}
	return workflow.NewPromptSchedule(rc)
}

package runner

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/workflow"
)

const promptScheduleTpl = "name=[{{.Schedule.Name}}] at=[{{.Schedule.LastRunAt}}] state=[{{.Schedule.LastRunState}}] last=[{{.Schedule.LastDeliverable}}]"

// promptScheduleStep adds a single agent step carrying promptScheduleTpl.
func promptScheduleStep(f *fixture) {
	f.t.Helper()
	st := f.step("watch", workflow.StepAgent)
	if err := f.s.SetStepPrompt(f.ctx, st.ID, promptScheduleTpl); err != nil {
		f.t.Fatalf("SetStepPrompt: %v", err)
	}
}

func TestPromptScheduleSecondRunSeesFirst(t *testing.T) {
	f := newFixture(t)
	promptScheduleStep(f)
	f.exec.handle = func(_ context.Context, req StepExec) (agent.HeadlessResult, error) {
		return success("found 3 items"), nil
	}
	sc, err := f.s.CreateSchedule(f.ctx, workflow.Schedule{
		Name: "nightly", WorkflowID: f.wf.ID, TaskID: f.tk.ID, Cwd: "/tmp/repo", CronExpr: "0 3 * * *",
	}, time.Now())
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}

	r1, err := f.s.StartScheduleNow(f.ctx, sc.ID)
	if err != nil {
		t.Fatalf("StartScheduleNow: %v", err)
	}
	if err := f.runner().Run(f.ctx, r1.ID, false); err != nil {
		t.Fatalf("Run 1: %v\n%s", err, f.log)
	}
	r2, err := f.s.StartScheduleNow(f.ctx, sc.ID)
	if err != nil {
		t.Fatalf("StartScheduleNow 2: %v", err)
	}
	if err := f.runner().Run(f.ctx, r2.ID, false); err != nil {
		t.Fatalf("Run 2: %v\n%s", err, f.log)
	}

	reqs := f.exec.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	if want := "name=[nightly] at=[] state=[] last=[]"; reqs[0].Prompt != want {
		t.Errorf("first prompt = %q, want %q", reqs[0].Prompt, want)
	}
	started := f.getRun(r1.ID).StartedAt.In(time.Local).Format(time.RFC3339)
	want := "name=[nightly] at=[" + started + "] state=[done] last=[found 3 items]"
	if reqs[1].Prompt != want {
		t.Errorf("second prompt = %q, want %q", reqs[1].Prompt, want)
	}
}

func TestPromptScheduleUnscheduledRunIsEmpty(t *testing.T) {
	f := newFixture(t)
	promptScheduleStep(f)
	run := f.run()
	if err := f.runner().Run(f.ctx, run.ID, false); err != nil {
		t.Fatalf("Run: %v\n%s", err, f.log)
	}
	if got, want := f.exec.requests()[0].Prompt, "name=[] at=[] state=[] last=[]"; got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}
}

// bareStore hides ScheduleRunContext behind the embedded interface.
type promptScheduleBareStore struct{ Store }

func TestPromptScheduleStoreWithoutSupport(t *testing.T) {
	f := newFixture(t)
	r := f.runner()
	r.Store = promptScheduleBareStore{f.s}
	if got := r.promptSchedule(f.ctx, 1); got != (workflow.PromptSchedule{}) {
		t.Errorf("promptSchedule = %+v, want zero", got)
	}
	if strings.Contains(f.log.String(), "error") {
		t.Errorf("log = %s", f.log)
	}
}

package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/jwstover/tend/internal/store"
	"github.com/jwstover/tend/internal/workflow"
)

// scheduleHarness is a real store on a temp file with one workflow ("W",
// one agent step) and task #1, and the claude/tmux seams stubbed present.
type scheduleHarness struct {
	t    *testing.T
	s    *store.Store
	step workflow.Step
}

func newScheduleHarness(t *testing.T) *scheduleHarness {
	t.Helper()
	oldClaude, oldTmux := checkClaude, tmuxPresent
	checkClaude, tmuxPresent = func() error { return nil }, func() bool { return true }
	t.Cleanup(func() { checkClaude, tmuxPresent = oldClaude, oldTmux })

	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir()+"/tend.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf, err := s.CreateWorkflow(ctx, "W", "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.AddStep(ctx, wf.ID, "check", workflow.StepAgent)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetStepPrompt(ctx, st.ID, "look around"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStepPermissionMode(ctx, st.ID, "acceptEdits"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddTask(ctx, "the task"); err != nil {
		t.Fatal(err)
	}
	return &scheduleHarness{t: t, s: s, step: st}
}

// run executes `tend schedule <args>` against the harness store; Close on
// the shared store is a no-op for the test's purposes (reopened per call).
func (h *scheduleHarness) run(args ...string) (stdout, stderr string, err error) {
	cmd := newScheduleCmd(func(context.Context) (ScheduleStore, error) { return noClose{h.s}, nil }, func() string { return "" })
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(args)
	err = cmd.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

type noClose struct{ *store.Store }

func (noClose) Close() error { return nil }

func TestScheduleAddLsPauseResumeRm(t *testing.T) {
	h := newScheduleHarness(t)
	out, errOut, err := h.run("add", "w1", "--workflow", "W", "--task", "1", "--cron", "*/5 * * * *")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.Contains(out, `added schedule 1 "w1": W on #1, next run `) || errOut != "" {
		t.Errorf("add out = %q, err = %q", out, errOut)
	}

	out, _, err = h.run("ls")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID", "w1", "W", "#1 the task", "*/5 * * * *", "on"} {
		if !strings.Contains(out, want) {
			t.Errorf("ls missing %q:\n%s", want, out)
		}
	}

	// Pause by name, twice (idempotent), then by id.
	for _, ref := range []string{"w1", "w1", "1"} {
		if _, _, err := h.run("pause", ref); err != nil {
			t.Fatalf("pause %s: %v", ref, err)
		}
	}
	out, _, _ = h.run("ls")
	if !strings.Contains(out, "paused") {
		t.Errorf("ls after pause:\n%s", out)
	}

	out, _, err = h.run("resume", "w1")
	if err != nil || !strings.Contains(out, "next run ") {
		t.Errorf("resume out = %q, err = %v", out, err)
	}
	if sc, _ := h.s.GetSchedule(context.Background(), 1); !sc.Enabled {
		t.Error("schedule still paused after resume")
	}

	if _, _, err := h.run("rm", "1"); err != nil {
		t.Fatal(err)
	}
	if scs, _ := h.s.ListSchedules(context.Background()); len(scs) != 0 {
		t.Errorf("schedules left: %v", scs)
	}
	if _, _, err := h.run("rm", "w1"); err == nil || !strings.Contains(err.Error(), `no schedule named "w1"`) {
		t.Errorf("rm miss err = %v", err)
	}
}

func TestScheduleAddRefusals(t *testing.T) {
	h := newScheduleHarness(t)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"bad cron", []string{"add", "x", "--workflow", "W", "--task", "1", "--cron", "nope"}, workflow.ErrInvalidCron.Error()},
		{"unknown workflow", []string{"add", "x", "--workflow", "Z", "--task", "1", "--cron", "* * * * *"}, `no workflow named "Z"`},
		{"unknown task", []string{"add", "x", "--workflow", "W", "--task", "99", "--cron", "* * * * *"}, ""},
	}
	for _, tc := range cases {
		_, _, err := h.run(tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
	if scs, _ := h.s.ListSchedules(context.Background()); len(scs) != 0 {
		t.Errorf("a refused add wrote %v", scs)
	}
}

func TestScheduleAddWarnsOnBypassPermissions(t *testing.T) {
	h := newScheduleHarness(t)
	if err := h.s.SetStepPermissionMode(context.Background(), h.step.ID, "bypassPermissions"); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := h.run("add", "risky", "--workflow", "1", "--task", "1", "--cron", "0 9 * * 1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "added schedule") || !strings.Contains(errOut, "warning: step \"check\" runs with bypassPermissions") {
		t.Errorf("out = %q, stderr = %q", out, errOut)
	}
}
